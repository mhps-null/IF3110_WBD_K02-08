package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/lifecycle"
)

func TestNormalizeOptions(t *testing.T) {
	o, err := ExecuteOptions{}.Normalize()
	if err != nil || o.Mode != ModeNormal || o.MaxAttempts != DefaultMaxAttempts {
		t.Fatalf("defaults: %+v %v", o, err)
	}
	o, err = ExecuteOptions{Mode: "BATCH", BatchTable: "public.big", BatchColumn: "id"}.Normalize()
	if err != nil || o.Mode != ModeBatch || o.BatchSize != DefaultBatchSize {
		t.Fatalf("batch defaults: %+v %v", o, err)
	}
	o, _ = ExecuteOptions{Mode: "normal", BatchTable: "x", BatchColumn: "y", BatchSize: 5}.Normalize()
	if o.BatchTable != "" || o.BatchSize != 0 {
		t.Fatalf("normal mode must drop batch fields: %+v", o)
	}
	bad := []ExecuteOptions{
		{Mode: "fast"},
		{Mode: ModeBatch, BatchTable: "big"},
		{Mode: ModeBatch, BatchTable: `big"; DROP TABLE x; --`, BatchColumn: "id"},
		{Mode: ModeBatch, BatchTable: "a.b.c", BatchColumn: "id"},
		{Mode: ModeBatch, BatchTable: "big", BatchColumn: "id desc"},
		{Mode: ModeBatch, BatchTable: "big", BatchColumn: "id", BatchSize: -1},
		{Mode: ModeBatch, BatchTable: "big", BatchColumn: "id", BatchSize: MaxBatchSize + 1},
		{MaxAttempts: 11},
	}
	for i, b := range bad {
		if _, err := b.Normalize(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("case %d should be invalid: %+v", i, b)
		}
	}
}

func TestQuoteIdentifiers(t *testing.T) {
	q, err := quoteQualified("public.big_table")
	if err != nil || q != `"public"."big_table"` {
		t.Fatalf("%q %v", q, err)
	}
	if _, err := quoteQualified("x;y"); err == nil {
		t.Fatal("must reject unsafe identifiers")
	}
	p := CursorPlan{Table: `"big"`, Column: `"id"`}
	if !strings.Contains(p.nextBoundQuery(), `"id" > $1 AND "id" <= $2 ORDER BY "id" LIMIT $3`) {
		t.Fatalf("cursor query: %s", p.nextBoundQuery())
	}
}

func TestCheckSQLForMode(t *testing.T) {
	ok := []struct{ mode, sql string }{
		{ModeBatch, "UPDATE t SET v=1 WHERE id > $1 AND id <= $2"},
		{ModeNormal, "ALTER TABLE t ADD COLUMN x INT"},
		{ModeNormal, "CREATE FUNCTION f() RETURNS int AS $$ SELECT 1 $$ LANGUAGE sql"},
	}
	for _, c := range ok {
		if err := CheckSQLForMode(c.mode, c.sql); err != nil {
			t.Fatalf("%s %q: %v", c.mode, c.sql, err)
		}
	}
	bad := []struct{ mode, sql string }{
		{ModeBatch, "UPDATE t SET v=1"},
		{ModeBatch, "UPDATE t SET v=1 WHERE id > $1"},
		{ModeBatch, "UPDATE t SET v=$3 WHERE id > $1 AND id <= $2"},
		{ModeNormal, "UPDATE t SET v=1 WHERE id > $1 AND id <= $2"},
		{ModeNormal, "  "},
		{"other", "SELECT 1"},
	}
	for _, c := range bad {
		if err := CheckSQLForMode(c.mode, c.sql); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s %q should be invalid", c.mode, c.sql)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		err  error
		want Outcome
	}{
		{&pgconn.PgError{Code: "42601"}, OutcomePermanent},              // syntax
		{&pgconn.PgError{Code: "23505"}, OutcomePermanent},              // unique
		{&pgconn.PgError{Code: "40001"}, OutcomeTransient},              // serialization
		{&pgconn.PgError{Code: "40P01"}, OutcomeTransient},              // deadlock
		{&pgconn.PgError{Code: "55P03"}, OutcomeTransient},              // lock timeout
		{&pgconn.PgError{Code: "57P01"}, OutcomeUncertain},              // admin shutdown
		{fmt.Errorf("wrap: %w", io.ErrUnexpectedEOF), OutcomeUncertain}, // lost connection
		{context.Canceled, OutcomeUncertain},
	}
	for _, c := range cases {
		if got := Classify(c.err); got != c.want {
			t.Fatalf("%v: got %s want %s", c.err, got, c.want)
		}
	}
	if ClassifyBeforeWork(errors.New("dial tcp: refused")) != OutcomeTransient {
		t.Fatal("connection failure before work is transient")
	}
	if ClassifyBeforeWork(&pgconn.PgError{Code: "28P01"}) != OutcomePermanent {
		t.Fatal("auth failure before work is permanent")
	}
}

func TestBackoffBounded(t *testing.T) {
	base, max := time.Second, 10*time.Second
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second}
	for i, w := range want {
		if got := Backoff(i+1, base, max); got != w {
			t.Fatalf("retry %d: got %s want %s", i+1, got, w)
		}
	}
	if Backoff(0, 0, time.Minute) != time.Second {
		t.Fatal("defaults")
	}
}

func TestStatusHelpers(t *testing.T) {
	for _, s := range []string{StatusPartial, StatusPartiallyReverted, StatusCompensating, StatusUncertain} {
		if HoldReason(s) == "" {
			t.Fatalf("%s must hold the database", s)
		}
	}
	for _, s := range []string{StatusQueued, StatusSuccess, StatusFailed, StatusReverted} {
		if HoldReason(s) != "" {
			t.Fatalf("%s must not hold", s)
		}
	}
	if Terminal(StatusRunning) || Terminal(StatusUncertain) || Terminal(StatusRetrying) || !Terminal(StatusPartial) {
		t.Fatal("terminal classification")
	}
}

func TestAllowedActions(t *testing.T) {
	partial := Job{Status: StatusPartial, Mode: ModeBatch, BatchesDone: 3, RequestStatus: lifecycle.Failed, Op: OpApply}
	if a := allowedActions(partial); !a.Resume || !a.Compensate || a.Cancel {
		t.Fatalf("partial: %+v", a)
	}
	normalFailed := Job{Status: StatusFailed, Mode: ModeNormal, RequestStatus: lifecycle.Failed}
	if a := allowedActions(normalFailed); !a.Resume || a.Compensate {
		t.Fatalf("normal failed: %+v", a)
	}
	runningNormal := Job{Status: StatusRunning, Mode: ModeNormal, LeaseOwner: "w"}
	if a := allowedActions(runningNormal); a.Cancel {
		t.Fatal("single-transaction job cannot be cancelled mid-statement")
	}
	runningBatch := Job{Status: StatusRunning, Mode: ModeBatch, LeaseOwner: "w"}
	if a := allowedActions(runningBatch); !a.Cancel {
		t.Fatal("running batch job can be cancelled between batches")
	}
	queued := Job{Status: StatusQueued, Op: OpApply}
	if a := allowedActions(queued); !a.Cancel || a.Resume {
		t.Fatalf("queued: %+v", a)
	}
	success := Job{Status: StatusSuccess, RequestStatus: lifecycle.Success}
	if a := allowedActions(success); a.Resume || a.Cancel || a.Compensate {
		t.Fatalf("success has no actions: %+v", a)
	}
}

func TestPromMetricsExposition(t *testing.T) {
	m := NewPromMetrics()
	m.SetJobStatusCount(StatusRunning, 2)
	m.SetJobStatusCount("job-123", 9) // unbounded value collapses into "other"
	m.IncJobStatus(StatusSuccess, 1)
	m.IncJobRetry()
	m.ObserveQueueWait(1500 * time.Millisecond)
	m.ObserveLockWait(20 * time.Millisecond)
	m.ObserveMigrationDuration(StatusSuccess, 3*time.Second)
	m.AddProcessedBatches(4)
	m.AddProcessedRows(400)
	m.SyncPools([]contract.PoolStat{{DBName: "target", InUse: 1, Idle: 3, WaitCount: 2, WaitDuration: time.Second}})
	var buf bytes.Buffer
	if _, err := m.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		`dbplatform_migration_jobs{status="running"} 2`,
		`dbplatform_migration_jobs{status="other"} 9`,
		`dbplatform_migration_job_transitions_total{status="success"} 1`,
		`dbplatform_migration_job_retries_total 1`,
		`dbplatform_migration_queue_wait_seconds_bucket{le="2.5"} 1`,
		`dbplatform_migration_queue_wait_seconds_bucket{le="1"} 0`,
		`dbplatform_migration_queue_wait_seconds_count 1`,
		`dbplatform_migration_lock_wait_seconds_count 1`,
		`dbplatform_migration_duration_seconds_count{status="success"} 1`,
		`dbplatform_migration_batches_processed_total 4`,
		`dbplatform_migration_rows_processed_total 400`,
		`dbplatform_db_pool_connections{pool="target",state="active"} 1`,
		`dbplatform_db_pool_connections{pool="target",state="idle"} 3`,
		`dbplatform_db_pool_wait_total{pool="target"} 2`,
		"# TYPE dbplatform_migration_queue_wait_seconds histogram",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "job-123") {
		t.Fatal("high-cardinality values must never become labels")
	}
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain; version=0.0.4") {
		t.Fatal("prometheus content type")
	}
}

// fakeLifecycle denies everything; used to test HTTP authorization paths.
type fakeLifecycle struct{ can bool }

func (f fakeLifecycle) Get(context.Context, int64) (lifecycle.Request, error) {
	return lifecycle.Request{}, lifecycle.ErrNotFound
}
func (f fakeLifecycle) Can(context.Context, int64, int64, bool) (bool, error) { return f.can, nil }
func (f fakeLifecycle) RevisionDetails(context.Context, int64) (lifecycle.DefinitionInput, error) {
	return lifecycle.DefinitionInput{}, lifecycle.ErrNotFound
}
func (f fakeLifecycle) QueueWith(context.Context, int64, int64, lifecycle.QueueHook) (int64, error) {
	return 0, lifecycle.ErrConflict
}
func (f fakeLifecycle) Retry(context.Context, int64) (int64, error)     { return 0, lifecycle.ErrConflict }
func (f fakeLifecycle) ReleaseQueued(context.Context, int64) error      { return lifecycle.ErrConflict }
func (f fakeLifecycle) RevalidateApproval(context.Context, int64) error { return lifecycle.ErrConflict }

func serve(h Handler, method, path, body string, user int64) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if user > 0 {
				r = r.WithContext(contract.WithPrincipal(r.Context(), contract.Principal{UserID: user, Username: "u"}))
			}
			next.ServeHTTP(w, r)
		})
	}
	h.Register(mux, auth)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHTTPAuthorizationAndValidation(t *testing.T) {
	deny := Handler{Service: &Service{Lifecycle: fakeLifecycle{can: false}}}
	cases := []struct {
		name, method, path, body string
		user                     int64
		want                     int
	}{
		{"execute without permission", http.MethodPost, "/api/v2/databases/1/requests/2/execute", `{}`, 7, http.StatusForbidden},
		{"execute anonymous", http.MethodPost, "/api/v2/databases/1/requests/2/execute", `{}`, 0, http.StatusForbidden},
		{"execute bad path", http.MethodPost, "/api/v2/databases/x/requests/2/execute", `{}`, 7, http.StatusBadRequest},
		{"execute malformed body", http.MethodPost, "/api/v2/databases/1/requests/2/execute", `{"mode":`, 7, http.StatusBadRequest},
		{"execute unknown field", http.MethodPost, "/api/v2/databases/1/requests/2/execute", `{"sql":"DROP TABLE x"}`, 7, http.StatusBadRequest},
		{"list anonymous", http.MethodGet, "/api/v2/jobs", "", 0, http.StatusForbidden},
		{"list bad db filter", http.MethodGet, "/api/v2/jobs?db_id=-1", "", 7, http.StatusBadRequest},
		{"list bad status", http.MethodGet, "/api/v2/jobs?status=drop", "", 7, http.StatusBadRequest},
		{"list db without access", http.MethodGet, "/api/v2/jobs?db_id=3", "", 7, http.StatusForbidden},
		{"detail bad id", http.MethodGet, "/api/v2/jobs/abc", "", 7, http.StatusBadRequest},
		{"resume bad id", http.MethodPost, "/api/v2/jobs/0/resume", "", 7, http.StatusBadRequest},
	}
	for _, c := range cases {
		rec := serve(deny, c.method, c.path, c.body, c.user)
		if rec.Code != c.want {
			t.Fatalf("%s: got %d want %d (%s)", c.name, rec.Code, c.want, rec.Body.String())
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("%s: JSON error expected", c.name)
		}
	}
	// Execute by an editor for a missing request -> 404 (no SQL ever runs in the API).
	allow := Handler{Service: &Service{Lifecycle: fakeLifecycle{can: true}}}
	if rec := serve(allow, http.MethodPost, "/api/v2/databases/1/requests/99/execute", ``, 7); rec.Code != http.StatusNotFound {
		t.Fatalf("missing request: %d %s", rec.Code, rec.Body.String())
	}
}

func TestUserMessageStripsSentinel(t *testing.T) {
	err := fmt.Errorf("batch mode requires $1/$2: %w", ErrInvalid)
	if got := userMessage(err, "x"); got != "batch mode requires $1/$2" {
		t.Fatalf("%q", got)
	}
	if got := userMessage(ErrConflict, "conflict"); got != "conflict" {
		t.Fatalf("%q", got)
	}
}
