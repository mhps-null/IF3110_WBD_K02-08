//go:build integration

package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/lifecycle"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/observability"
)

// Run with: TEST_METADATA_DSN=postgres://... go test -tags=integration ./internal/jobs
// The DSN user must be allowed to CREATE/DROP DATABASE.

type fakeTargets struct{ dbs map[int64]*sql.DB }

type lifecycleBeforeQueue struct {
	LifecycleAPI
	before func()
}

func (l lifecycleBeforeQueue) QueueWith(ctx context.Context, id, actor int64, hook lifecycle.QueueHook) (int64, error) {
	l.before()
	return l.LifecycleAPI.QueueWith(ctx, id, actor, hook)
}

func (f fakeTargets) Open(_ context.Context, id int64) (*sql.DB, contract.Engine, error) {
	db, ok := f.dbs[id]
	if !ok {
		return nil, "", errors.New("unknown target")
	}
	return db, contract.EnginePostgres, nil
}

type captureSpans struct {
	mu   sync.Mutex
	recs []observability.SpanRecord
}

func (c *captureSpans) ExportSpan(r observability.SpanRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recs = append(c.recs, r)
}

type env struct {
	t       *testing.T
	meta    *sql.DB
	life    *lifecycle.Store
	svc     *Service
	targets fakeTargets
	author  int64
	viewer  int64
	outside int64
	group   int64
	db1     int64
	db2     int64
	spans   *captureSpans
}

func createDB(t *testing.T, admin *sql.DB, cfg *pgx.ConnConfig, prefix string) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	if _, err := admin.Exec(fmt.Sprintf(`CREATE DATABASE "%s"`, name)); err != nil {
		t.Fatalf("create database: %v", err)
	}
	c := *cfg
	c.Database = name
	db := stdlib.OpenDB(c)
	db.SetMaxOpenConns(20)
	t.Cleanup(func() {
		_ = db.Close()
		_, _ = admin.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS "%s" WITH (FORCE)`, name))
	})
	return db
}

func setup(t *testing.T) *env {
	t.Helper()
	dsn := os.Getenv("TEST_METADATA_DSN")
	if dsn == "" {
		t.Skip("TEST_METADATA_DSN is not set")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = admin.Close() })
	if err := admin.Ping(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	meta := createDB(t, admin, cfg, "jobs_meta")
	meta.SetMaxOpenConns(40)
	for _, name := range []string{"001_initial_schema.sql", "002_session_privilege_version.sql", "003_audit_logs.sql",
		"004_group_leader.sql", "005_migration_lifecycle.sql", "006_lifecycle_execution_runs.sql", "007_job_queue_worker.sql"} {
		script, err := os.ReadFile(filepath.Join("..", "..", "migrations_meta", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := meta.Exec(string(script)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	e := &env{t: t, meta: meta, life: lifecycle.NewStore(meta), targets: fakeTargets{dbs: map[int64]*sql.DB{}}, spans: &captureSpans{}}
	e.author = e.user("author")
	e.viewer = e.user("viewer")
	e.outside = e.user("outsider")
	e.db1 = e.target(createDB(t, admin, cfg, "jobs_target_a"), "target-a")
	e.db2 = e.target(createDB(t, admin, cfg, "jobs_target_b"), "target-b")
	if err := meta.QueryRow(`INSERT INTO groups(name) VALUES('backend') RETURNING id`).Scan(&e.group); err != nil {
		t.Fatal(err)
	}
	var viewGroup int64
	if err := meta.QueryRow(`INSERT INTO groups(name) VALUES('readers') RETURNING id`).Scan(&viewGroup); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO group_members(group_id,user_id) VALUES($1,$2)`, e.group, e.author)
	e.exec(`INSERT INTO group_members(group_id,user_id) VALUES($1,$2)`, viewGroup, e.viewer)
	e.exec(`UPDATE groups SET leader_user_id=$2 WHERE id=$1`, e.group, e.author)
	e.exec(`UPDATE groups SET leader_user_id=$2 WHERE id=$1`, viewGroup, e.viewer)
	for _, db := range []int64{e.db1, e.db2} {
		e.exec(`INSERT INTO group_db_access(group_id,db_id,level) VALUES($1,$2,2)`, e.group, db)
		e.exec(`INSERT INTO group_db_access(group_id,db_id,level) VALUES($1,$2,1)`, viewGroup, db)
	}
	e.svc = &Service{Store: NewStore(meta), Lifecycle: e.life, Targets: e.targets}
	return e
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.meta.Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) user(name string) int64 {
	var id int64
	if err := e.meta.QueryRow(`INSERT INTO users(username,password_hash) VALUES($1,'x') RETURNING id`, name).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) target(db *sql.DB, name string) int64 {
	var id int64
	if err := e.meta.QueryRow(`INSERT INTO db_connections(name,engine,host,port,database_name,username,password_enc)
		VALUES($1,'postgres','localhost',5432,$1,'u','enc') RETURNING id`, name).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	e.targets.dbs[id] = db
	return id
}

func (e *env) tdb(id int64) *sql.DB { return e.targets.dbs[id] }

func (e *env) tq(id int64, q string) {
	e.t.Helper()
	if _, err := e.tdb(id).Exec(q); err != nil {
		e.t.Fatalf("target %s: %v", q, err)
	}
}

func (e *env) scalar(db *sql.DB, q string, args ...any) int64 {
	e.t.Helper()
	var n sql.NullInt64
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return n.Int64
}

// approved creates, submits and approves (by both leaders) an up migration.
func (e *env) approved(db int64, title, up, down string) lifecycle.Request {
	e.t.Helper()
	ctx := context.Background()
	req, err := e.life.Create(ctx, e.author, lifecycle.DefinitionInput{DBID: db, Title: title, SQLUp: up, SQLDown: down,
		IrreversibleReason: map[bool]string{true: "irreversible test", false: ""}[down == ""]})
	if err != nil {
		e.t.Fatalf("create: %v", err)
	}
	return e.approve(req)
}

func (e *env) approve(req lifecycle.Request) lifecycle.Request {
	e.t.Helper()
	ctx := context.Background()
	if _, err := e.life.Submit(ctx, req.ID, e.author); err != nil {
		e.t.Fatalf("submit: %v", err)
	}
	if _, err := e.life.Approve(ctx, req.ID, e.author, true); err != nil {
		e.t.Fatalf("approve author: %v", err)
	}
	out, err := e.life.Approve(ctx, req.ID, e.viewer, true)
	if err != nil || out.Status != lifecycle.Approved {
		e.t.Fatalf("approve viewer: %+v %v", out, err)
	}
	return out
}

func (e *env) worker(id string) *Worker {
	w := NewWorker(Config{WorkerID: id, Lease: 2 * time.Second, LockWait: 200 * time.Millisecond,
		RetryBase: 10 * time.Millisecond, RetryMax: 40 * time.Millisecond, VerifyInterval: 5 * time.Millisecond,
		UncertainRecheck: 10 * time.Millisecond, PollInterval: 20 * time.Millisecond, ShutdownGrace: 5 * time.Second},
		e.meta, e.life, e.targets, observability.NewLogger(os.Stdout, slog.LevelWarn),
		observability.NewTracer("worker-test", e.spans), NewPromMetrics())
	return w
}

// drive runs the worker until the job reaches a terminal status.
func (e *env) drive(w *Worker, jobID int64) Job {
	e.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := w.RunOnce(context.Background()); err != nil {
			e.t.Fatalf("run once: %v", err)
		}
		j, err := e.svc.Store.Get(context.Background(), jobID)
		if err != nil {
			e.t.Fatal(err)
		}
		if Terminal(j.Status) {
			return j
		}
		time.Sleep(15 * time.Millisecond)
	}
	j, _ := e.svc.Store.Get(context.Background(), jobID)
	e.t.Fatalf("job %d did not finish, status %s (%s)", jobID, j.Status, j.LastError)
	return j
}

func (e *env) execute(req lifecycle.Request, opts ExecuteOptions) Job {
	e.t.Helper()
	j, err := e.svc.Execute(context.Background(), e.author, req.DBID, req.ID, opts)
	if err != nil {
		e.t.Fatalf("execute: %v", err)
	}
	return j
}

func (e *env) attempts(reqID int64) []string {
	e.t.Helper()
	rows, err := e.meta.Query(`SELECT status FROM migration_execution_attempts WHERE request_id=$1 ORDER BY attempt_number`, reqID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func (e *env) requestStatus(id int64) string {
	r, err := e.life.Get(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return r.Status
}

func (e *env) seedBig(db int64, n int) {
	e.tq(db, `CREATE TABLE big (id BIGSERIAL PRIMARY KEY, v INTEGER NOT NULL DEFAULT 0)`)
	e.tq(db, fmt.Sprintf(`INSERT INTO big (v) SELECT 0 FROM generate_series(1,%d)`, n))
}

const (
	batchUp   = `UPDATE big SET v = v + 1 WHERE id > $1 AND id <= $2`
	batchDown = `UPDATE big SET v = v - 1 WHERE id > $1 AND id <= $2`
)

func batchOpts(size int) ExecuteOptions {
	return ExecuteOptions{Mode: ModeBatch, BatchTable: "big", BatchColumn: "id", BatchSize: size}
}

var permanentErr = &pgconn.PgError{Code: "23514", Message: "injected check violation"}
var transientErr = &pgconn.PgError{Code: "40001", Message: "injected serialization failure"}

// --- tests -------------------------------------------------------------------

func TestPublishReturnsJobImmediatelyAndWorkerExecutesAsync(t *testing.T) {
	e := setup(t)
	req := e.approved(e.db1, "create table", `CREATE TABLE async_t (id INT PRIMARY KEY)`, `DROP TABLE async_t`)
	j := e.execute(req, ExecuteOptions{})
	if j.ID == 0 || j.Status != StatusQueued || e.requestStatus(req.ID) != lifecycle.Pending {
		t.Fatalf("publish must only queue: %+v", j)
	}
	if e.scalar(e.tdb(e.db1), `SELECT count(*) FROM pg_tables WHERE tablename='async_t'`) != 0 {
		t.Fatal("SQL must not run before a worker processes the job")
	}
	done := e.drive(e.worker("w1"), j.ID)
	if done.Status != StatusSuccess || e.requestStatus(req.ID) != lifecycle.Success {
		t.Fatalf("expected success, got %+v", done)
	}
	if e.scalar(e.tdb(e.db1), `SELECT count(*) FROM pg_tables WHERE tablename='async_t'`) != 1 {
		t.Fatal("migration not applied")
	}
	if e.scalar(e.tdb(e.db1), `SELECT count(*) FROM _dbplatform.migration_ledger WHERE job_id=$1`, j.ID) != 1 {
		t.Fatal("ledger evidence missing")
	}
	if got := e.attempts(req.ID); len(got) != 1 || got[0] != "success" {
		t.Fatalf("history attempts: %v", got)
	}
}

func TestDuplicateDeliveryDoesNotDoubleCommit(t *testing.T) {
	e := setup(t)
	e.tq(e.db1, `CREATE TABLE counter (n INT)`)
	req := e.approved(e.db1, "insert once", `INSERT INTO counter VALUES (1)`, `DELETE FROM counter`)
	j := e.execute(req, ExecuteOptions{})
	w := e.worker("w1")
	e.drive(w, j.ID)
	// Re-deliver the same message: job back in the queue although the request finished.
	e.exec(`UPDATE migration_jobs SET status='queued', finished_at=NULL, next_run_at=now() WHERE id=$1`, j.ID)
	if done := e.drive(w, j.ID); done.Status != StatusSuccess {
		t.Fatalf("duplicate should be acknowledged as success: %+v", done)
	}
	// Re-deliver while the request still looks running (worst case): ledger blocks re-execution.
	e.exec(`UPDATE migration_requests SET status='running' WHERE id=$1`, req.ID)
	e.exec(`UPDATE migration_jobs SET status='uncertain', finished_at=NULL, next_run_at=now() WHERE id=$1`, j.ID)
	e.drive(w, j.ID)
	if n := e.scalar(e.tdb(e.db1), `SELECT count(*) FROM counter`); n != 1 {
		t.Fatalf("double commit: %d rows", n)
	}
}

func TestUncertainCommitIsVerifiedNotBlindlyRetried(t *testing.T) {
	e := setup(t)
	e.tq(e.db1, `CREATE TABLE counter (n INT)`)
	req := e.approved(e.db1, "lost ack", `INSERT INTO counter VALUES (1)`, `DELETE FROM counter`)
	j := e.execute(req, ExecuteOptions{})
	w := e.worker("w1")
	w.SetFaults(Faults{AfterCommit: func(Job, string, int) error { return errors.New("connection reset by peer") }})
	done := e.drive(w, j.ID)
	if done.Status != StatusSuccess || e.scalar(e.tdb(e.db1), `SELECT count(*) FROM counter`) != 1 {
		t.Fatalf("committed-but-unacknowledged must resolve to success once: %+v", done)
	}
	if got := e.attempts(req.ID); len(got) != 1 || got[0] != "success" {
		t.Fatalf("no extra attempt expected: %v", got)
	}

	// Lost connection BEFORE commit: ledger proves nothing committed, then a bounded retry.
	req2 := e.approved(e.db2, "lost before commit", `CREATE TABLE once_t (id INT)`, `DROP TABLE once_t`)
	j2 := e.execute(req2, ExecuteOptions{})
	calls := 0
	w.SetFaults(Faults{BeforeCommit: func(Job, string, int) error {
		calls++
		if calls == 1 {
			return errors.New("unexpected EOF")
		}
		return nil
	}})
	done = e.drive(w, j2.ID)
	if done.Status != StatusSuccess || done.RetryCount != 1 {
		t.Fatalf("expected verified retry: %+v", done)
	}
	if got := e.attempts(req2.ID); strings.Join(got, ",") != "failed,success" {
		t.Fatalf("failed attempt must be recorded before retry: %v", got)
	}
}

func TestUncertainWithUnreachableTargetHoldsDatabase(t *testing.T) {
	e := setup(t)
	req := e.approved(e.db1, "uncertain hold", `CREATE TABLE hold_t (id INT)`, `DROP TABLE hold_t`)
	j := e.execute(req, ExecuteOptions{})
	w := e.worker("w1")
	broken := false
	w.SetFaults(Faults{AfterCommit: func(Job, string, int) error {
		broken = true
		return errors.New("connection reset by peer")
	}})
	if _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !broken {
		t.Fatal("fault not triggered")
	}
	jb, _ := e.svc.Store.Get(context.Background(), j.ID)
	// With a reachable target the ledger resolves it immediately to success.
	if jb.Status != StatusSuccess {
		t.Fatalf("expected verified success, got %s", jb.Status)
	}
	// Simulate an unverifiable outcome: request running + job uncertain, ledger
	// table missing -> must stay uncertain and hold the database, never re-run.
	req2 := e.approved(e.db2, "unverifiable", `CREATE TABLE unv_t (id INT)`, `DROP TABLE unv_t`)
	j2 := e.execute(req2, ExecuteOptions{})
	e.exec(`UPDATE migration_requests SET status='running' WHERE id=$1`, req2.ID)
	e.exec(`UPDATE migration_jobs SET status='uncertain' WHERE id=$1`, j2.ID)
	w2 := e.worker("w2")
	w2.targets = fakeTargets{dbs: map[int64]*sql.DB{}} // target unreachable
	if _, err := w2.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	jb2, _ := e.svc.Store.Get(context.Background(), j2.ID)
	if jb2.Status != StatusUncertain || !jb2.Held {
		t.Fatalf("unverifiable outcome must stay uncertain and held: %+v", jb2)
	}
	other := e.approved(e.db1, "other db ok", `CREATE TABLE other_ok (id INT)`, `DROP TABLE other_ok`)
	if _, err := e.svc.Execute(context.Background(), e.author, e.db1, other.ID, ExecuteOptions{}); err != nil {
		t.Fatalf("a different database must not be blocked: %v", err)
	}
	if e.scalar(e.tdb(e.db2), `SELECT count(*) FROM pg_tables WHERE tablename='unv_t'`) != 0 {
		t.Fatal("uncertain job must not be blindly re-executed")
	}
}

func TestPermanentFailureIsRecordedWithoutRetry(t *testing.T) {
	e := setup(t)
	req := e.approved(e.db1, "bad sql", `ALTER TABLE does_not_exist ADD COLUMN x INT`, `SELECT 1`)
	j := e.execute(req, ExecuteOptions{})
	done := e.drive(e.worker("w1"), j.ID)
	if done.Status != StatusFailed || done.RetryCount != 0 || !strings.Contains(done.LastError, "does_not_exist") {
		t.Fatalf("permanent failure: %+v", done)
	}
	if got := e.attempts(req.ID); strings.Join(got, ",") != "failed" {
		t.Fatalf("failed attempt must be recorded: %v", got)
	}
	if e.requestStatus(req.ID) != lifecycle.Failed {
		t.Fatal("request must be failed")
	}
}

func TestTransientRetryIsBoundedWithBackoff(t *testing.T) {
	e := setup(t)
	req := e.approved(e.db1, "always transient", `CREATE TABLE tr_t (id INT)`, `DROP TABLE tr_t`)
	j := e.execute(req, ExecuteOptions{MaxAttempts: 3})
	w := e.worker("w1")
	w.SetFaults(Faults{BeforeCommit: func(Job, string, int) error { return transientErr }})
	if _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	mid, _ := e.svc.Store.Get(context.Background(), j.ID)
	if mid.Status != StatusRetrying || !mid.NextRunAt.After(time.Now().Add(-time.Second)) {
		t.Fatalf("expected retrying with backoff: %+v", mid)
	}
	done := e.drive(w, j.ID)
	if done.Status != StatusFailed || done.RetryCount != 2 {
		t.Fatalf("retry must stop at max attempts: %+v", done)
	}
	if got := e.attempts(req.ID); strings.Join(got, ",") != "failed,failed,failed" {
		t.Fatalf("every attempt recorded: %v", got)
	}
}

func TestBatchSuccessCheckpointsEveryBatch(t *testing.T) {
	e := setup(t)
	e.seedBig(e.db1, 1050)
	req := e.approved(e.db1, "batch", batchUp, batchDown)
	j := e.execute(req, batchOpts(100))
	done := e.drive(e.worker("w1"), j.ID)
	if done.Status != StatusSuccess || done.BatchesDone != 11 || done.RowsDone != 1050 || done.TotalRows == nil || *done.TotalRows != 1050 {
		t.Fatalf("batch progress: %+v", done)
	}
	if e.scalar(e.tdb(e.db1), `SELECT count(*) FROM big WHERE v <> 1`) != 0 {
		t.Fatal("every row must be updated exactly once")
	}
	if e.scalar(e.tdb(e.db1), `SELECT count(*) FROM _dbplatform.migration_ledger WHERE job_id=$1`, j.ID) != 11 {
		t.Fatal("one checkpoint per batch expected")
	}
}

func TestBatchFailurePartialHoldResumeWithoutRepeating(t *testing.T) {
	e := setup(t)
	e.seedBig(e.db1, 1000)
	req := e.approved(e.db1, "batch partial", batchUp, batchDown)
	j := e.execute(req, batchOpts(100))
	w := e.worker("w1")
	fail := true
	w.SetFaults(Faults{BeforeCommit: func(_ Job, _ string, seq int) error {
		if seq == 4 && fail {
			return permanentErr
		}
		return nil
	}})
	done := e.drive(w, j.ID)
	if done.Status != StatusPartial || done.BatchesDone != 3 || !done.Held {
		t.Fatalf("expected partial with 3 committed batches and a hold: %+v", done)
	}
	if e.scalar(e.tdb(e.db1), `SELECT count(*) FROM big WHERE v = 1`) != 300 {
		t.Fatal("only committed batches may persist")
	}
	// Next migration on the same database is held.
	ctx := context.Background()
	blocked, err := e.life.Create(ctx, e.author, lifecycle.DefinitionInput{DBID: e.db1, Title: "blocked", SQLUp: "SELECT 1", SQLDown: "SELECT 1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.life.Submit(ctx, blocked.ID, e.author); !errors.Is(err, lifecycle.ErrConflict) {
		t.Fatalf("held database must block other migrations, got %v", err)
	}
	// Resume continues from the checkpoint without repeating committed batches.
	fail = false
	if _, err := e.svc.Resume(ctx, e.author, j.ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	done = e.drive(w, j.ID)
	if done.Status != StatusSuccess || done.BatchesDone != 10 || done.Held {
		t.Fatalf("resume: %+v", done)
	}
	if e.scalar(e.tdb(e.db1), `SELECT count(*) FROM big WHERE v <> 1`) != 0 {
		t.Fatal("resume repeated or skipped a batch")
	}
	if got := e.attempts(req.ID); strings.Join(got, ",") != "failed,success" {
		t.Fatalf("attempts: %v", got)
	}
}

func TestCompensationRevertsInReverseAndSupportsPartiallyReverted(t *testing.T) {
	e := setup(t)
	e.seedBig(e.db1, 500)
	req := e.approved(e.db1, "batch comp", batchUp, batchDown)
	j := e.execute(req, batchOpts(100))
	w := e.worker("w1")
	w.SetFaults(Faults{BeforeCommit: func(_ Job, dir string, seq int) error {
		if dir == DirectionApply && seq == 4 {
			return permanentErr
		}
		if dir == DirectionCompensate && seq == 2 {
			return permanentErr
		}
		return nil
	}})
	if done := e.drive(w, j.ID); done.Status != StatusPartial || done.BatchesDone != 3 {
		t.Fatalf("setup partial: %+v", done)
	}
	ctx := context.Background()
	if _, err := e.svc.Compensate(ctx, e.viewer, j.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("view-only user must not compensate: %v", err)
	}
	if _, err := e.svc.Compensate(ctx, e.author, j.ID); err != nil {
		t.Fatalf("compensate: %v", err)
	}
	done := e.drive(w, j.ID)
	if done.Status != StatusPartiallyReverted || done.BatchesReverted != 1 || !done.Held {
		t.Fatalf("expected partially reverted (batch 3 reverted, batch 2 failed): %+v", done)
	}
	// Reverse order: batch 3 reverted first, batch 1 and 2 still applied.
	if e.scalar(e.tdb(e.db1), `SELECT count(*) FROM big WHERE v = 1`) != 200 ||
		e.scalar(e.tdb(e.db1), `SELECT count(*) FROM big WHERE id > 200 AND id <= 300 AND v = 0`) != 100 {
		t.Fatal("compensation must run in reverse batch order")
	}
	w.SetFaults(Faults{})
	if _, err := e.svc.Compensate(ctx, e.author, j.ID); err != nil {
		t.Fatalf("continue compensation: %v", err)
	}
	done = e.drive(w, j.ID)
	if done.Status != StatusReverted || done.BatchesReverted != 3 || done.Held {
		t.Fatalf("expected fully reverted: %+v", done)
	}
	if e.scalar(e.tdb(e.db1), `SELECT count(*) FROM big WHERE v <> 0`) != 0 {
		t.Fatal("all committed batches must be compensated exactly once")
	}
}

func TestCompensationRejectedForIrreversibleMigration(t *testing.T) {
	e := setup(t)
	e.seedBig(e.db1, 300)
	req := e.approved(e.db1, "irreversible batch", batchUp, "")
	j := e.execute(req, batchOpts(100))
	w := e.worker("w1")
	w.SetFaults(Faults{BeforeCommit: func(_ Job, _ string, seq int) error {
		if seq == 2 {
			return permanentErr
		}
		return nil
	}})
	e.drive(w, j.ID)
	if _, err := e.svc.Compensate(context.Background(), e.author, j.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("irreversible migration must not be compensated: %v", err)
	}
}

func TestWorkerCrashRecoveryResumesFromCheckpoint(t *testing.T) {
	e := setup(t)
	e.seedBig(e.db1, 600)
	req := e.approved(e.db1, "crash", batchUp, batchDown)
	j := e.execute(req, batchOpts(100))
	w1 := e.worker("w1")
	w1.SetFaults(Faults{AfterBatch: func(_ Job, seq int) error {
		if seq == 3 {
			return errCrash
		}
		return nil
	}})
	if _, err := w1.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	crashed, _ := e.svc.Store.Get(context.Background(), j.ID)
	if crashed.Status != StatusRunning || crashed.LeaseOwner != "w1" {
		t.Fatalf("crashed worker leaves job leased: %+v", crashed)
	}
	// The process is gone; its lease expires.
	e.exec(`UPDATE migration_jobs SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, j.ID)
	done := e.drive(e.worker("w2"), j.ID)
	if done.Status != StatusSuccess || done.Recoveries != 1 {
		t.Fatalf("recovery: %+v", done)
	}
	if e.scalar(e.tdb(e.db1), `SELECT count(*) FROM big WHERE v <> 1`) != 0 {
		t.Fatal("recovery repeated or skipped batches")
	}
	if got := e.attempts(req.ID); strings.Join(got, ",") != "failed,success" {
		t.Fatalf("interrupted attempt must be recorded: %v", got)
	}
}

func TestCrossWorkerLockSameAndDifferentDatabases(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	l1, err := AcquireDBLock(ctx, e.meta, e.tdb(e.db1), e.db1, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireDBLock(ctx, e.meta, e.tdb(e.db1), e.db1, 100*time.Millisecond); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("second lock on the same database must fail, got %v", err)
	}
	l2, err := AcquireDBLock(ctx, e.meta, e.tdb(e.db2), e.db2, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("different database must lock in parallel: %v", err)
	}
	l2.Release()

	// A worker meeting a held lock re-queues the job instead of running it.
	req := e.approved(e.db1, "lock wait", `CREATE TABLE lock_t (id INT)`, `DROP TABLE lock_t`)
	j := e.execute(req, ExecuteOptions{})
	w := e.worker("w1")
	if _, err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	mid, _ := e.svc.Store.Get(ctx, j.ID)
	if mid.Status != StatusQueued || e.requestStatus(req.ID) != lifecycle.Pending {
		t.Fatalf("job must wait for the lock: %+v", mid)
	}
	l1.Release()
	if done := e.drive(w, j.ID); done.Status != StatusSuccess {
		t.Fatalf("after release: %+v", done)
	}
	// Lock released automatically when the holder's session dies.
	l3, err := AcquireDBLock(ctx, e.meta, e.tdb(e.db1), e.db1, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	_ = l3.meta.Raw(func(dc any) error { return dc.(interface{ Conn() *pgx.Conn }).Conn().Close(ctx) })
	_ = l3.target.Raw(func(dc any) error { return dc.(interface{ Conn() *pgx.Conn }).Conn().Close(ctx) })
	l4, err := AcquireDBLock(ctx, e.meta, e.tdb(e.db1), e.db1, time.Second)
	if err != nil {
		t.Fatalf("lock must be recovered after holder session ends: %v", err)
	}
	l4.Release()
}

func TestTwoWorkersRunDifferentDatabasesInParallel(t *testing.T) {
	e := setup(t)
	r1 := e.approved(e.db1, "slow a", `SELECT pg_sleep(1)`, `SELECT 1`)
	r2 := e.approved(e.db2, "slow b", `SELECT pg_sleep(1)`, `SELECT 1`)
	j1 := e.execute(r1, ExecuteOptions{})
	j2 := e.execute(r2, ExecuteOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, id := range []string{"wa", "wb"} {
		w := e.worker(id)
		w.cfg.Concurrency = 1
		wg.Add(1)
		go func() { defer wg.Done(); _ = w.Run(ctx) }()
	}
	start := time.Now()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		a, _ := e.svc.Store.Get(context.Background(), j1.ID)
		b, _ := e.svc.Store.Get(context.Background(), j2.ID)
		if a.Status == StatusSuccess && b.Status == StatusSuccess {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	elapsed := time.Since(start)
	cancel()
	wg.Wait()
	a, _ := e.svc.Store.Get(context.Background(), j1.ID)
	b, _ := e.svc.Store.Get(context.Background(), j2.ID)
	if a.Status != StatusSuccess || b.Status != StatusSuccess {
		t.Fatalf("both jobs must succeed: %s %s", a.Status, b.Status)
	}
	if elapsed > 1900*time.Millisecond {
		t.Fatalf("different databases must run in parallel; took %s", elapsed)
	}
	if a.LeaseOwner != "" || b.LeaseOwner != "" {
		t.Fatal("lease must be released")
	}
}

func TestRevalidationRejectsStaleApprovalBeforeExecution(t *testing.T) {
	e := setup(t)
	req := e.approved(e.db1, "stale", `CREATE TABLE stale_t (id INT)`, `DROP TABLE stale_t`)
	j := e.execute(req, ExecuteOptions{})
	// Leader changes after approval and before the worker runs.
	newLeader := e.user("new-leader")
	e.exec(`INSERT INTO group_members(group_id,user_id) VALUES($1,$2)`, e.group, newLeader)
	e.exec(`UPDATE groups SET leader_user_id=$2 WHERE id=$1`, e.group, newLeader)
	done := e.drive(e.worker("w1"), j.ID)
	if done.Status != StatusRejected || e.requestStatus(req.ID) != lifecycle.Approved {
		t.Fatalf("stale approval must be rejected and released: %+v / %s", done, e.requestStatus(req.ID))
	}
	if e.scalar(e.tdb(e.db1), `SELECT count(*) FROM pg_tables WHERE tablename='stale_t'`) != 0 {
		t.Fatal("rejected job must not execute")
	}
	if len(e.attempts(req.ID)) != 0 {
		t.Fatal("no attempt should exist")
	}
}

func TestCancelQueuedAndAuthorization(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	req := e.approved(e.db1, "cancel", `CREATE TABLE c_t (id INT)`, `DROP TABLE c_t`)
	if _, err := e.svc.Execute(ctx, e.viewer, e.db1, req.ID, ExecuteOptions{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("view-only user must not execute: %v", err)
	}
	if _, err := e.svc.Execute(ctx, e.outside, e.db1, req.ID, ExecuteOptions{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("outsider must not execute: %v", err)
	}
	if _, err := e.svc.Execute(ctx, e.author, e.db2, req.ID, ExecuteOptions{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("request must be scoped to its database: %v", err)
	}
	j := e.execute(req, ExecuteOptions{})
	if _, err := e.svc.Execute(ctx, e.author, e.db1, req.ID, ExecuteOptions{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("double execute must conflict: %v", err)
	}
	if items, _ := e.svc.List(ctx, e.outside, ListFilter{}); len(items) != 0 {
		t.Fatalf("outsider must not list jobs: %+v", items)
	}
	if items, _ := e.svc.List(ctx, e.viewer, ListFilter{}); len(items) != 1 {
		t.Fatalf("viewer can monitor jobs of databases they can view: %+v", items)
	}
	if _, err := e.svc.Get(ctx, e.outside, j.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outsider must not see job: %v", err)
	}
	if d, err := e.svc.Get(ctx, e.viewer, j.ID); err != nil || d.Actions.Cancel {
		t.Fatalf("viewer sees job without control actions: %+v %v", d.Actions, err)
	}
	if _, err := e.svc.Cancel(ctx, e.viewer, j.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("view-only cancel: %v", err)
	}
	c, err := e.svc.Cancel(ctx, e.author, j.ID)
	if err != nil || c.Status != StatusCancelled || e.requestStatus(req.ID) != lifecycle.Approved {
		t.Fatalf("cancel queued: %+v %v", c, err)
	}
	// Re-execute after cancel keeps the same job ID.
	again, err := e.svc.Execute(ctx, e.author, e.db1, req.ID, ExecuteOptions{})
	if err != nil || again.ID != j.ID || again.Status != StatusQueued {
		t.Fatalf("re-execute: %+v %v", again, err)
	}
	if done := e.drive(e.worker("w1"), j.ID); done.Status != StatusSuccess {
		t.Fatalf("%+v", done)
	}
}

func TestExecuteValidatesBatchContract(t *testing.T) {
	e := setup(t)
	e.seedBig(e.db1, 10)
	e.tq(e.db1, `CREATE TABLE nouniq (k INT, v INT)`)
	ctx := context.Background()
	req := e.approved(e.db1, "batch sql", batchUp, batchDown)
	cases := []ExecuteOptions{
		{},                                   // batch SQL in normal mode
		{Mode: ModeBatch, BatchTable: "big"}, // missing column
		{Mode: ModeBatch, BatchTable: "big;drop", BatchColumn: "id"},
		{Mode: ModeBatch, BatchTable: "nouniq", BatchColumn: "k"}, // not unique
		{Mode: ModeBatch, BatchTable: "big", BatchColumn: "id", BatchSize: MaxBatchSize + 1},
		{Mode: "turbo"},
	}
	for i, c := range cases {
		if _, err := e.svc.Execute(ctx, e.author, e.db1, req.ID, c); !errors.Is(err, ErrInvalid) {
			t.Fatalf("case %d must be invalid, got %v", i, err)
		}
	}
	if e.requestStatus(req.ID) != lifecycle.Approved {
		t.Fatal("invalid execute must not change the request")
	}
}

func TestBatchRollbackAndForwardThroughQueue(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.seedBig(e.db1, 450)
	req := e.approved(e.db1, "batch rb", batchUp, batchDown)
	j := e.execute(req, batchOpts(100))
	w := e.worker("w1")
	e.drive(w, j.ID)

	rb, err := e.life.CreateAction(ctx, e.author, e.db1, req.MigrationID, lifecycle.Rollback)
	if err != nil {
		t.Fatalf("create rollback: %v", err)
	}
	rb = e.approve(rb)
	rj := e.execute(rb, ExecuteOptions{Mode: ModeNormal}) // mode is derived from the source job
	if rj.Mode != ModeBatch || rj.SourceJobID == nil || *rj.SourceJobID != j.ID {
		t.Fatalf("rollback must replay source batches: %+v", rj)
	}
	if done := e.drive(w, rj.ID); done.Status != StatusSuccess || done.BatchesDone != 5 {
		t.Fatalf("rollback: %+v", done)
	}
	if e.scalar(e.tdb(e.db1), `SELECT count(*) FROM big WHERE v <> 0`) != 0 {
		t.Fatal("rollback must revert every committed batch")
	}

	fw, err := e.life.CreateAction(ctx, e.author, e.db1, req.MigrationID, lifecycle.Forward)
	if err != nil {
		t.Fatalf("create forward: %v", err)
	}
	fw = e.approve(fw)
	fj := e.execute(fw, ExecuteOptions{}) // inherits batch plan
	if fj.Mode != ModeBatch || fj.BatchTable != "big" {
		t.Fatalf("forward should inherit batch plan: %+v", fj)
	}
	if done := e.drive(w, fj.ID); done.Status != StatusSuccess {
		t.Fatalf("forward: %+v", done)
	}
	if e.scalar(e.tdb(e.db1), `SELECT count(*) FROM big WHERE v <> 1`) != 0 {
		t.Fatal("forward must re-apply")
	}
	hist, err := e.life.ListHistory(ctx, e.db1, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 || hist[0].Action != lifecycle.Forward {
		t.Fatalf("hide rollback must leave only the forward execution: %+v", hist)
	}
}

func TestTraceContextPropagatesThroughQueue(t *testing.T) {
	e := setup(t)
	tracer := observability.NewTracer("api", e.spans)
	ctx, span := tracer.StartSpan(context.Background(), "POST execute")
	traceID := span.Context().TraceID
	req := e.approved(e.db1, "traced", `CREATE TABLE traced_t (id INT)`, `DROP TABLE traced_t`)
	j, err := e.svc.Execute(ctx, e.author, e.db1, req.ID, ExecuteOptions{})
	span.End()
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := e.svc.Store.Get(context.Background(), j.ID)
	if !strings.Contains(stored.TraceParent, traceID) {
		t.Fatalf("job payload must carry traceparent: %q", stored.TraceParent)
	}
	e.drive(e.worker("w1"), j.ID)
	names := map[string]bool{}
	e.spans.mu.Lock()
	for _, r := range e.spans.recs {
		if r.TraceID == traceID {
			names[r.Name] = true
		}
	}
	e.spans.mu.Unlock()
	for _, want := range []string{"POST execute", "job.process", "job.lock.acquire", "job.revalidate", "migration.execute", "sql.exec", "sql.commit"} {
		if !names[want] {
			t.Fatalf("span %q missing from trace %s: %v", want, traceID, names)
		}
	}
}

func TestDispatchPicksUpLifecycleQueueEndpoint(t *testing.T) {
	e := setup(t)
	req := e.approved(e.db1, "legacy queue", `CREATE TABLE lq_t (id INT)`, `DROP TABLE lq_t`)
	jobID, err := e.life.Queue(context.Background(), req.ID, e.author)
	if err != nil {
		t.Fatal(err)
	}
	if done := e.drive(e.worker("w1"), jobID); done.Status != StatusSuccess || done.Mode != ModeNormal {
		t.Fatalf("outbox dispatch: %+v", done)
	}
}

func TestExecuteRejectsRevisionChangedBeforeEnqueue(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	req := e.approved(e.db1, "bound execution", "SELECT 1", "SELECT 1")
	var approved lifecycle.Request
	e.svc.Lifecycle = lifecycleBeforeQueue{LifecycleAPI: e.life, before: func() {
		revised, err := e.life.Revise(ctx, req.ID, e.author, lifecycle.DefinitionInput{
			DBID: e.db1, Title: "revised", SQLUp: "SELECT 2", SQLDown: "SELECT 1",
		})
		if err != nil {
			t.Fatal(err)
		}
		approved = e.approve(revised)
	}}

	h := Handler{Service: e.svc}
	path := fmt.Sprintf("/api/v2/databases/%d/requests/%d/execute", e.db1, req.ID)
	oldBody := fmt.Sprintf(`{"revision_id":%d}`, req.RevisionID)
	for _, stage := range []string{"during enqueue", "before enqueue"} {
		rec := serve(h, http.MethodPost, path, oldBody, e.author)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "revisi") {
			t.Fatalf("%s: expected stale revision conflict, got %d %s", stage, rec.Code, rec.Body.String())
		}
		e.svc.Lifecycle = e.life
	}
	var outbox int
	if err := e.meta.QueryRow(`SELECT count(*) FROM migration_job_outbox WHERE request_id=$1`, req.ID).Scan(&outbox); err != nil || outbox != 0 {
		t.Fatalf("stale requests must leave the outbox empty: %d, %v", outbox, err)
	}
	newBody := fmt.Sprintf(`{"revision_id":%d}`, approved.RevisionID)
	if rec := serve(h, http.MethodPost, path, newBody, e.author); rec.Code != http.StatusAccepted {
		t.Fatalf("current revision should enqueue: %d %s", rec.Code, rec.Body.String())
	}
}
