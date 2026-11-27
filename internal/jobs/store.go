package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/lifecycle"
)

// Store is the metadata-database side of the queue.
type Store struct{ DB *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{DB: db} }

// NewJob describes the job row written in the same transaction as the lifecycle
// pending transition (see lifecycle.Store.QueueWith).
type NewJob struct {
	Options       ExecuteOptions
	SourceJobID   *int64
	TraceParent   string
	CorrelationID string
	EnqueuedBy    int64
}

// UpsertQueuedTx creates the job (or re-arms the same job ID when a released
// request is executed again) and marks the outbox row dispatched.
func UpsertQueuedTx(ctx context.Context, tx *sql.Tx, req lifecycle.Request, jobID int64, nj NewJob) error {
	o := nj.Options
	var table, column, corr, trace any
	var size any
	if o.Mode == ModeBatch {
		size = o.BatchSize
		if o.BatchTable != "" {
			table, column = o.BatchTable, o.BatchColumn
		}
	}
	if nj.TraceParent != "" {
		trace = nj.TraceParent
	}
	if nj.CorrelationID != "" {
		corr = nj.CorrelationID
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO migration_jobs
		(id,request_id,db_id,mode,op,status,batch_table,batch_column,batch_size,source_job_id,
		 max_attempts,trace_parent,correlation_id,enqueued_by,idempotency_key,next_run_at,queued_at)
		VALUES ($1,$2,$3,$4,'apply','queued',$5,$6,$7,$8,$9,$10,$11,$12,$13,now(),now())
		ON CONFLICT (id) DO UPDATE SET
			mode=EXCLUDED.mode, op='apply', status='queued', batch_table=EXCLUDED.batch_table,
			batch_column=EXCLUDED.batch_column, batch_size=EXCLUDED.batch_size,
			source_job_id=EXCLUDED.source_job_id, max_attempts=EXCLUDED.max_attempts,
			scope_from=NULL, scope_to=NULL, total_rows=NULL, last_cursor=NULL,
			retry_count=0, cancel_requested=false, last_error=NULL,
			trace_parent=EXCLUDED.trace_parent, correlation_id=EXCLUDED.correlation_id,
			enqueued_by=EXCLUDED.enqueued_by, next_run_at=now(), queued_at=now(),
			lease_owner=NULL, lease_expires_at=NULL, finished_at=NULL, updated_at=now()
		WHERE migration_jobs.status IN ('rejected','cancelled')`,
		jobID, req.ID, req.DBID, o.Mode, table, column, size, nj.SourceJobID, o.MaxAttempts,
		trace, corr, nj.EnqueuedBy, IdempotencyKey(jobID))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		// The job exists in a state that must not be overwritten (e.g. it already
		// ran); the lifecycle state machine should have prevented this.
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE migration_job_outbox SET dispatched_at=now() WHERE id=$1`, jobID); err != nil {
		return err
	}
	return addEventTx(ctx, tx, jobID, "job.queued", StatusQueued, "Execute pressed; job published to durable queue", "", nil, nil)
}

// Dispatch converts undispatched outbox rows into jobs. Outbox rows come from the
// lifecycle /queue endpoint (normal mode) or from lifecycle Retry (re-arm).
func (s *Store) Dispatch(ctx context.Context) (int, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT o.id,o.request_id,r.db_id,r.status
		FROM migration_job_outbox o JOIN migration_requests r ON r.id=o.request_id
		WHERE o.dispatched_at IS NULL ORDER BY o.id LIMIT 100 FOR UPDATE OF o SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	type item struct {
		id, req, db int64
		status      string
	}
	items := []item{}
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.req, &it.db, &it.status); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, it := range items {
		if it.status == lifecycle.Pending {
			// New job -> queued. Existing job: only re-arm when no worker will
			// otherwise pick it up; a job in retrying/running is left untouched.
			res, err := tx.ExecContext(ctx, `INSERT INTO migration_jobs (id,request_id,db_id,idempotency_key)
				VALUES ($1,$2,$3,$4)
				ON CONFLICT (id) DO UPDATE SET status='queued', op='apply', next_run_at=now(),
					queued_at=now(), cancel_requested=false, lease_owner=NULL, lease_expires_at=NULL,
					finished_at=NULL, updated_at=now()
				WHERE migration_jobs.status IN ('failed','partial','partially_reverted','reverted','cancelled','rejected')`,
				it.id, it.req, it.db, IdempotencyKey(it.id))
			if err != nil {
				return 0, err
			}
			if n, _ := res.RowsAffected(); n == 1 {
				if err := syncHoldTx(ctx, tx, it.id); err != nil {
					return 0, err
				}
				if err := addEventTx(ctx, tx, it.id, "job.dispatched", StatusQueued, "outbox event dispatched to queue", "", nil, nil); err != nil {
					return 0, err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE migration_job_outbox SET dispatched_at=now() WHERE id=$1`, it.id); err != nil {
			return 0, err
		}
	}
	return len(items), tx.Commit()
}

const jobColumns = `j.id,j.request_id,j.db_id,COALESCE(c.name,''),r.migration_id,r.revision_id,COALESCE(d.title,''),
	r.action,r.status,j.mode,j.op,j.status,COALESCE(j.batch_table,''),COALESCE(j.batch_column,''),
	COALESCE(j.batch_size,0),j.source_job_id,j.scope_from,j.scope_to,j.total_rows,j.rows_done,j.batches_done,
	j.rows_reverted,j.batches_reverted,j.last_cursor,j.max_attempts,j.retry_count,
	(SELECT count(*) FROM migration_execution_attempts a WHERE a.request_id=j.request_id),
	j.deliveries,j.recoveries,j.next_run_at,COALESCE(j.lease_owner,''),j.lease_expires_at,j.heartbeat_at,
	j.cancel_requested,COALESCE(j.last_error,''),EXISTS(SELECT 1 FROM migration_db_holds h WHERE h.job_id=j.id),
	COALESCE(j.trace_parent,''),COALESCE(j.correlation_id,''),j.enqueued_by,j.queued_at,j.created_at,
	j.started_at,j.finished_at,j.updated_at`

const jobFrom = ` FROM migration_jobs j
	JOIN migration_requests r ON r.id=j.request_id
	LEFT JOIN migration_definitions d ON d.id=r.migration_id
	LEFT JOIN db_connections c ON c.id=j.db_id`

type scanner interface{ Scan(dest ...any) error }

func scanJob(row scanner) (Job, error) {
	var j Job
	var lease, hb, started, finished sql.NullTime
	err := row.Scan(&j.ID, &j.RequestID, &j.DBID, &j.DBName, &j.MigrationID, &j.RevisionID, &j.Title,
		&j.Action, &j.RequestStatus, &j.Mode, &j.Op, &j.Status, &j.BatchTable, &j.BatchColumn,
		&j.BatchSize, &j.SourceJobID, &j.ScopeFrom, &j.ScopeTo, &j.TotalRows, &j.RowsDone, &j.BatchesDone,
		&j.RowsReverted, &j.BatchesReverted, &j.LastCursor, &j.MaxAttempts, &j.RetryCount,
		&j.Attempts, &j.Deliveries, &j.Recoveries, &j.NextRunAt, &j.LeaseOwner, &lease, &hb,
		&j.CancelRequested, &j.LastError, &j.Held, &j.TraceParent, &j.CorrelationID, &j.EnqueuedBy,
		&j.QueuedAt, &j.CreatedAt, &started, &finished, &j.UpdatedAt)
	if err != nil {
		return j, err
	}
	if lease.Valid {
		j.LeaseExpiresAt = &lease.Time
	}
	if hb.Valid {
		j.HeartbeatAt = &hb.Time
	}
	if started.Valid {
		j.StartedAt = &started.Time
	}
	if finished.Valid {
		j.FinishedAt = &finished.Time
	}
	return j, nil
}

// Get loads one job with its request/migration context.
func (s *Store) Get(ctx context.Context, id int64) (Job, error) {
	j, err := scanJob(s.DB.QueryRowContext(ctx, `SELECT `+jobColumns+jobFrom+` WHERE j.id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	return j, err
}

// ListFilter scopes the job list. UserID is mandatory: only jobs on databases
// the user can view (through group access) are returned.
type ListFilter struct {
	UserID int64
	DBID   int64
	Status string
	Limit  int
}

func (s *Store) List(ctx context.Context, f ListFilter) ([]Job, error) {
	if f.UserID <= 0 {
		return nil, ErrForbidden
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 100
	}
	var status any
	if f.Status != "" {
		status = f.Status
	}
	var db any
	if f.DBID > 0 {
		db = f.DBID
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+jobColumns+jobFrom+`
		WHERE j.db_id IN (SELECT ga.db_id FROM group_members gm JOIN group_db_access ga ON ga.group_id=gm.group_id
		                   JOIN users u ON u.id=gm.user_id WHERE gm.user_id=$1 AND u.is_active)
		  AND ($2::bigint IS NULL OR j.db_id=$2)
		  AND ($3::text IS NULL OR j.status=$3)
		ORDER BY j.id DESC LIMIT $4`, f.UserID, db, status, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ForRequest returns the job attached to a lifecycle request, if any.
func (s *Store) ForRequest(ctx context.Context, requestID int64) (Job, error) {
	j, err := scanJob(s.DB.QueryRowContext(ctx, `SELECT `+jobColumns+jobFrom+` WHERE j.request_id=$1`, requestID))
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	return j, err
}

// ForExecution returns the job that produced a lifecycle execution run.
func (s *Store) ForExecution(ctx context.Context, executionID int64) (Job, error) {
	j, err := scanJob(s.DB.QueryRowContext(ctx, `SELECT `+jobColumns+jobFrom+`
		WHERE j.request_id=(SELECT request_id FROM migration_execution_runs WHERE id=$1)`, executionID))
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	return j, err
}

// LatestBatchJobForMigration returns the newest batch-mode up/forward job of a
// migration, used to inherit the batch plan for a forward execution.
func (s *Store) LatestBatchJobForMigration(ctx context.Context, migrationID int64) (Job, error) {
	j, err := scanJob(s.DB.QueryRowContext(ctx, `SELECT `+jobColumns+jobFrom+`
		WHERE r.migration_id=$1 AND r.action IN ('up','forward') AND j.mode='batch' AND j.batch_table IS NOT NULL
		ORDER BY j.id DESC LIMIT 1`, migrationID))
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	return j, err
}

// ClaimNext leases the next due job. FOR UPDATE SKIP LOCKED makes concurrent
// workers claim different rows; a job whose database already has a live leased
// job is skipped (the advisory lock remains the actual guarantee).
func (s *Store) ClaimNext(ctx context.Context, workerID string, lease time.Duration) (Job, bool, error) {
	var id int64
	var from string
	err := s.DB.QueryRowContext(ctx, `WITH c AS (
			SELECT j.id, j.status FROM migration_jobs j
			WHERE j.status IN ('queued','retrying','uncertain') AND j.next_run_at <= now()
			  AND NOT EXISTS (SELECT 1 FROM migration_jobs o WHERE o.db_id=j.db_id AND o.id<>j.id
			                  AND o.status IN ('running','compensating') AND o.lease_expires_at > now())
			  AND NOT EXISTS (SELECT 1 FROM migration_db_holds h WHERE h.db_id=j.db_id AND h.job_id<>j.id)
			ORDER BY j.next_run_at, j.id
			FOR UPDATE SKIP LOCKED LIMIT 1)
		UPDATE migration_jobs j SET
			status=CASE WHEN j.op='compensate' THEN 'compensating' ELSE 'running' END,
			lease_owner=$1, lease_expires_at=now()+$2::interval, heartbeat_at=now(),
			deliveries=j.deliveries+1, started_at=COALESCE(j.started_at,now()), updated_at=now()
		FROM c WHERE j.id=c.id RETURNING j.id, c.status`, workerID, intervalString(lease)).Scan(&id, &from)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	j, err := s.Get(ctx, id)
	j.ClaimedFrom = from
	return j, err == nil, err
}

// Heartbeat extends the lease. ok=false means the lease was lost (another
// worker took over after expiry) and the caller must stop immediately.
func (s *Store) Heartbeat(ctx context.Context, jobID int64, workerID string, lease time.Duration) (ok, cancel bool, err error) {
	err = s.DB.QueryRowContext(ctx, `UPDATE migration_jobs SET lease_expires_at=now()+$3::interval, heartbeat_at=now()
		WHERE id=$1 AND lease_owner=$2 AND status IN ('running','compensating') RETURNING cancel_requested`,
		jobID, workerID, intervalString(lease)).Scan(&cancel)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	return err == nil, cancel, err
}

// RecoverExpired releases jobs whose worker stopped heart-beating. An apply job
// becomes "uncertain": the next worker must verify the target ledger before doing
// anything. A compensation job is re-queued (compensation is ledger-idempotent).
func (s *Store) RecoverExpired(ctx context.Context) ([]int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `UPDATE migration_jobs SET
			status=CASE WHEN op='compensate' THEN 'queued' ELSE 'uncertain' END,
			recoveries=recoveries+1, lease_owner=NULL, lease_expires_at=NULL, next_run_at=now(), updated_at=now(),
			last_error='worker lease expired; outcome will be verified from the target ledger'
		WHERE id IN (SELECT id FROM migration_jobs WHERE status IN ('running','compensating')
		             AND lease_expires_at < now() FOR UPDATE SKIP LOCKED)
		RETURNING id`)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := syncHoldTx(ctx, tx, id); err != nil {
			return nil, err
		}
		if err := addEventTx(ctx, tx, id, "job.lease_expired", StatusUncertain, "worker stopped heart-beating; lock released by PostgreSQL session end; job recovered", "", nil, nil); err != nil {
			return nil, err
		}
	}
	return ids, tx.Commit()
}

// Release ends this worker's lease and moves the job to a new status. A delay
// > 0 schedules the next run (backoff); terminal statuses set finished_at.
func (s *Store) Release(ctx context.Context, jobID int64, workerID, status string, delay time.Duration, errText string, retried bool) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var lastErr any
	if errText != "" {
		lastErr = errText
	}
	res, err := tx.ExecContext(ctx, `UPDATE migration_jobs SET status=$3, lease_owner=NULL, lease_expires_at=NULL,
			next_run_at=now()+$4::interval, last_error=COALESCE($5,last_error),
			retry_count=retry_count+CASE WHEN $6 THEN 1 ELSE 0 END,
			cancel_requested=CASE WHEN $7 THEN false ELSE cancel_requested END,
			finished_at=CASE WHEN $7 THEN now() ELSE NULL END,
			op=CASE WHEN $7 THEN 'apply' ELSE op END,
			updated_at=now()
		WHERE id=$1 AND (lease_owner=$2 OR $2='')`,
		jobID, workerID, status, intervalString(delay), lastErr, retried, Terminal(status))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrConflict
	}
	if status == StatusSuccess {
		if _, err := tx.ExecContext(ctx, `UPDATE migration_jobs SET last_error=NULL WHERE id=$1`, jobID); err != nil {
			return err
		}
	}
	if err := syncHoldTx(ctx, tx, jobID); err != nil {
		return err
	}
	return tx.Commit()
}

// syncHoldTx keeps migration_db_holds consistent with the job state: a database
// stays held while the job is partial / partially reverted / compensating /
// uncertain, or while a retry of a job that already committed batches is pending.
func syncHoldTx(ctx context.Context, tx *sql.Tx, jobID int64) error {
	var dbID, requestID int64
	var status string
	var net int
	err := tx.QueryRowContext(ctx, `SELECT db_id,request_id,status,batches_done-batches_reverted FROM migration_jobs WHERE id=$1`, jobID).
		Scan(&dbID, &requestID, &status, &net)
	if err != nil {
		return err
	}
	reason := HoldReason(status)
	if reason == "" && net > 0 && (status == StatusQueued || status == StatusRetrying || status == StatusRunning) {
		reason = StatusPartial
	}
	if reason == "" {
		_, err = tx.ExecContext(ctx, `DELETE FROM migration_db_holds WHERE job_id=$1`, jobID)
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO migration_db_holds (job_id,db_id,request_id,reason) VALUES ($1,$2,$3,$4)
		ON CONFLICT (job_id) DO UPDATE SET reason=EXCLUDED.reason, updated_at=now()`, jobID, dbID, requestID, reason)
	return err
}

// SetScope stores the target cursor range fixed at the start of a batch job.
func (s *Store) SetScope(ctx context.Context, jobID int64, from, to, total int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE migration_jobs SET scope_from=$2, scope_to=$3, total_rows=$4, updated_at=now()
		WHERE id=$1`, jobID, from, to, total)
	return err
}

// SyncBatches mirrors the target ledger (source of truth) into metadata and
// recomputes the progress counters, e.g. after a crash between commit and mirror.
func (s *Store) SyncBatches(ctx context.Context, jobID int64, ranges []LedgerRange) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var rows, revRows, last int64
	var done, rev int
	hasLast := false
	for _, r := range ranges {
		status := "committed"
		if r.Compensated {
			status = "reverted"
			rev++
			revRows += r.RevertedRows
		} else if !hasLast || r.To > last {
			last, hasLast = r.To, true
		}
		done++
		rows += r.Rows
		if _, err := tx.ExecContext(ctx, `INSERT INTO migration_job_batches (job_id,batch_seq,cursor_from,cursor_to,rows_affected,status,reverted_at)
			VALUES ($1,$2,$3,$4,$5,$6,CASE WHEN $6='reverted' THEN now() END)
			ON CONFLICT (job_id,batch_seq) DO UPDATE SET status=EXCLUDED.status,
				reverted_at=COALESCE(migration_job_batches.reverted_at,EXCLUDED.reverted_at)`,
			jobID, r.Seq, r.From, r.To, r.Rows, status); err != nil {
			return err
		}
	}
	var lastCursor any
	if hasLast {
		lastCursor = last
	}
	if _, err := tx.ExecContext(ctx, `UPDATE migration_jobs SET rows_done=$2, batches_done=$3, rows_reverted=$4,
		batches_reverted=$5, last_cursor=$6, updated_at=now() WHERE id=$1`, jobID, rows, done, revRows, rev, lastCursor); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordBatch mirrors one committed batch and advances progress.
func (s *Store) RecordBatch(ctx context.Context, jobID int64, seq int, from, to, rows int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO migration_job_batches (job_id,batch_seq,cursor_from,cursor_to,rows_affected,status)
		VALUES ($1,$2,$3,$4,$5,'committed') ON CONFLICT (job_id,batch_seq) DO NOTHING`, jobID, seq, from, to, rows)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		if _, err := tx.ExecContext(ctx, `UPDATE migration_jobs SET rows_done=rows_done+$2, batches_done=batches_done+1,
			last_cursor=$3, updated_at=now() WHERE id=$1`, jobID, rows, to); err != nil {
			return err
		}
	}
	if err := syncHoldTx(ctx, tx, jobID); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordRevert mirrors one compensated batch.
func (s *Store) RecordRevert(ctx context.Context, jobID int64, seq int, rows int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE migration_job_batches SET status='reverted', reverted_at=now()
		WHERE job_id=$1 AND batch_seq=$2 AND status='committed'`, jobID, seq)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		if _, err := tx.ExecContext(ctx, `UPDATE migration_jobs SET rows_reverted=rows_reverted+$2,
			batches_reverted=batches_reverted+1, updated_at=now() WHERE id=$1`, jobID, rows); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Batches lists the mirrored batches of a job (newest first, bounded).
func (s *Store) Batches(ctx context.Context, jobID int64, limit int) ([]Batch, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT batch_seq,cursor_from,cursor_to,rows_affected,status,committed_at,reverted_at
		FROM migration_job_batches WHERE job_id=$1 ORDER BY batch_seq DESC LIMIT $2`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Batch{}
	for rows.Next() {
		var b Batch
		var rev sql.NullTime
		if err := rows.Scan(&b.Seq, &b.CursorFrom, &b.CursorTo, &b.RowsAffected, &b.Status, &b.CommittedAt, &rev); err != nil {
			return nil, err
		}
		if rev.Valid {
			b.RevertedAt = &rev.Time
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// AddEvent appends a timeline event.
func (s *Store) AddEvent(ctx context.Context, jobID int64, event, status, message, workerID string, attempt, batch *int) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := addEventTx(ctx, tx, jobID, event, status, message, workerID, attempt, batch); err != nil {
		return err
	}
	return tx.Commit()
}

func addEventTx(ctx context.Context, tx *sql.Tx, jobID int64, event, status, message, workerID string, attempt, batch *int) error {
	var msg, worker any
	if message != "" {
		msg = truncate(message, 500)
	}
	if workerID != "" {
		worker = workerID
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO migration_job_events (job_id,event,status,message,worker_id,attempt,batch_seq)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, jobID, event, status, msg, worker, attempt, batch)
	return err
}

// Events lists the job timeline (newest first, bounded).
func (s *Store) Events(ctx context.Context, jobID int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,event,status,COALESCE(message,''),COALESCE(worker_id,''),attempt,batch_seq,created_at
		FROM migration_job_events WHERE job_id=$1 ORDER BY id DESC LIMIT $2`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var attempt, batch sql.NullInt64
		if err := rows.Scan(&e.ID, &e.Event, &e.Status, &e.Message, &e.WorkerID, &attempt, &batch, &e.CreatedAt); err != nil {
			return nil, err
		}
		if attempt.Valid {
			v := int(attempt.Int64)
			e.Attempt = &v
		}
		if batch.Valid {
			v := int(batch.Int64)
			e.BatchSeq = &v
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// NextAttempt returns the attempt number the lifecycle history expects next.
func (s *Store) NextAttempt(ctx context.Context, requestID int64) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_number),0)+1 FROM migration_execution_attempts WHERE request_id=$1`, requestID).Scan(&n)
	return n, err
}

// RequestCancel flags a running batch job to stop after the current batch.
func (s *Store) RequestCancel(ctx context.Context, jobID int64) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE migration_jobs SET cancel_requested=true, updated_at=now()
		WHERE id=$1 AND status IN ('running','compensating') AND mode='batch'`, jobID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrConflict
	}
	return s.AddEvent(ctx, jobID, "job.cancel_requested", StatusRunning, "cancel requested; worker stops after the current batch commits", "", nil, nil)
}

// CancelQueuedTx cancels a job that no worker has claimed yet.
func CancelQueuedTx(ctx context.Context, tx *sql.Tx, jobID int64) error {
	res, err := tx.ExecContext(ctx, `UPDATE migration_jobs SET status='cancelled', finished_at=now(), updated_at=now(),
		last_error='cancelled by user before execution'
		WHERE id=$1 AND status IN ('queued','retrying') AND lease_owner IS NULL`, jobID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrConflict
	}
	if err := syncHoldTx(ctx, tx, jobID); err != nil {
		return err
	}
	return addEventTx(ctx, tx, jobID, "job.cancelled", StatusCancelled, "cancelled by user before a worker claimed it", "", nil, nil)
}

// QueueCompensation schedules per-batch compensation of committed batches.
func (s *Store) QueueCompensation(ctx context.Context, jobID int64, actor int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE migration_jobs SET op='compensate', status='queued', next_run_at=now(),
			cancel_requested=false, finished_at=NULL, updated_at=now()
		WHERE id=$1 AND mode='batch' AND status IN ('partial','partially_reverted') AND batches_done > batches_reverted`, jobID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrConflict
	}
	// Keep the database held while compensation is queued.
	if _, err := tx.ExecContext(ctx, `INSERT INTO migration_db_holds (job_id,db_id,request_id,reason)
		SELECT id,db_id,request_id,'compensating' FROM migration_jobs WHERE id=$1
		ON CONFLICT (job_id) DO UPDATE SET reason='compensating', updated_at=now()`, jobID); err != nil {
		return err
	}
	if err := addEventTx(ctx, tx, jobID, "job.compensation_queued", StatusQueued,
		fmt.Sprintf("compensation of committed batches requested by user:%d", actor), "", nil, nil); err != nil {
		return err
	}
	return tx.Commit()
}

// Rearm puts a job back in the queue after lifecycle Retry (manual resume).
func (s *Store) Rearm(ctx context.Context, jobID int64, trace, corr string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var tp, cid any
	if trace != "" {
		tp = trace
	}
	if corr != "" {
		cid = corr
	}
	res, err := tx.ExecContext(ctx, `UPDATE migration_jobs SET status='queued', op='apply', next_run_at=now(),
			retry_count=0, cancel_requested=false, finished_at=NULL, lease_owner=NULL, lease_expires_at=NULL,
			trace_parent=COALESCE($2,trace_parent), correlation_id=COALESCE($3,correlation_id), updated_at=now()
		WHERE id=$1 AND status IN ('failed','partial','partially_reverted','reverted','cancelled')`, jobID, tp, cid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE migration_job_outbox SET dispatched_at=now() WHERE id=$1`, jobID); err != nil {
		return err
	}
	if err := syncHoldTx(ctx, tx, jobID); err != nil {
		return err
	}
	if err := addEventTx(ctx, tx, jobID, "job.resumed", StatusQueued, "resume requested; worker continues from the last committed checkpoint", "", nil, nil); err != nil {
		return err
	}
	return tx.Commit()
}

// StatusCounts returns the number of jobs per status (for gauges).
func (s *Store) StatusCounts(ctx context.Context) (map[string]int64, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT status,count(*) FROM migration_jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for _, st := range AllStatuses {
		out[st] = 0
	}
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// DBHeld reports whether another request's job holds the database.
func (s *Store) DBHeld(ctx context.Context, dbID, exceptRequest int64) (bool, error) {
	var held bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM migration_db_holds WHERE db_id=$1 AND request_id<>$2)`, dbID, exceptRequest).Scan(&held)
	return held, err
}

// IdempotencyKey is the stable per-job key; the same key is the primary key
// prefix of the target ledger, so a duplicate delivery cannot commit twice.
func IdempotencyKey(jobID int64) string { return fmt.Sprintf("job:%d", jobID) }

func intervalString(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%d milliseconds", d.Milliseconds())
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
