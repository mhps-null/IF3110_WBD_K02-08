package lifecycle

import (
	"context"
	"database/sql"
	"errors"
)

// ExecutionSpec carries immutable SQL and database identity to the worker
// It deliberately excludes credentials: the worker gets those from its own configured target-connection provider. SQL is never taken from queue payloads.
type ExecutionSpec struct {
	Request Request `json:"request"`
	SQL     string  `json:"sql"`
}

// LoadApprovedExecution is the worker-side read for a queue-published request
// The worker MUST obtain its database lock before calling Revalidate, and call
// Revalidate again immediately before each new attempt under that lock
func (s *Store) LoadApprovedExecution(ctx context.Context, requestID int64) (ExecutionSpec, error) {
	q, err := s.Get(ctx, requestID)
	if err != nil {
		return ExecutionSpec{}, err
	}
	if q.Status != Pending && q.Status != Running {
		return ExecutionSpec{}, ErrConflict
	}

	var statement sql.NullString
	column := "sql_up"
	if q.Action == Rollback {
		column = "sql_down"
	}

	err = s.DB.QueryRowContext(ctx, `SELECT `+column+` FROM migration_revisions WHERE migration_id=$1 AND id=$2`, q.MigrationID, q.RevisionID).Scan(&statement)
	if errors.Is(err, sql.ErrNoRows) {
		return ExecutionSpec{}, ErrNotFound
	}
	if err != nil {
		return ExecutionSpec{}, err
	}
	if !statement.Valid || statement.String == "" {
		return ExecutionSpec{}, ErrConflict
	}
	return ExecutionSpec{Request: q, SQL: statement.String}, nil
}

// Revalidate checks current actor permission, approval stakeholder snapshot,
// and linear execution order. Worker must hold a cross-worker TARGET DB lock
// for the entire operation; metadata advisory lock alone is not sufficient.
func (s *Store) Revalidate(ctx context.Context, requestID int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer tx.Rollback()
	var q Request
	err = tx.QueryRowContext(ctx, `SELECT id,migration_id,revision_id,db_id,action,target_execution_id,requested_by,status FROM migration_requests WHERE id=$1 FOR UPDATE`, requestID).Scan(&q.ID, &q.MigrationID, &q.RevisionID, &q.DBID, &q.Action, &q.TargetExecutionID, &q.RequestedBy, &q.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if q.Status != Pending && q.Status != Running {
		return ErrConflict
	}
	if err = lockDB(ctx, tx, q.DBID); err != nil {
		return err
	}

	var allowed bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users u WHERE u.id=$1 AND u.is_active AND (EXISTS(SELECT 1 FROM group_members gm JOIN group_db_access ga ON ga.group_id=gm.group_id WHERE gm.user_id=u.id AND ga.db_id=$2 AND ga.level=2)))`, q.RequestedBy, q.DBID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	if err = validateApproverSnapshot(ctx, tx, q.ID, q.DBID); err != nil {
		return err
	}

	var outstanding int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM migration_approval_requirements WHERE request_id=$1 AND approved_by IS NULL`, q.ID).Scan(&outstanding)
	if err != nil {
		return err
	}
	if outstanding > 0 {
		return ErrConflict
	}
	if err = eligibleModeTx(ctx, tx, q, eligibilityClaim); err != nil {
		return err
	}
	return tx.Commit()
}

// The worker reports finalized target-transaction outcomes through RecordAttempt.
// For uncertain commits, it must reconcile against the TARGET database before
// attempting another execution; this metadata API cannot establish the target
// transaction outcome or offer exactly-once guarantees by itself.

// RevalidateApproval checks that the requester still has View + Edit on the target
// database and that the approval snapshot of the request is still complete and
// current. It does not check request status or linear order, and is used by the
// job engine before per-batch compensation of an already-approved entry: the
// compensation runs the approved immutable inverse SQL of the same revision, so a
// new approval cycle is not required, but stale approval/permission is rejected.
func (s *Store) RevalidateApproval(ctx context.Context, requestID int64) error {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var dbID, requester int64
	err = tx.QueryRowContext(ctx, `SELECT db_id,requested_by FROM migration_requests WHERE id=$1`, requestID).Scan(&dbID, &requester)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var allowed bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users u WHERE u.id=$1 AND u.is_active AND EXISTS(
		SELECT 1 FROM group_members gm JOIN group_db_access ga ON ga.group_id=gm.group_id
		WHERE gm.user_id=u.id AND ga.db_id=$2 AND ga.level=2))`, requester, dbID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	if err = validateApproverSnapshot(ctx, tx, requestID, dbID); err != nil {
		return err
	}
	var outstanding int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM migration_approval_requirements WHERE request_id=$1 AND approved_by IS NULL`, requestID).Scan(&outstanding)
	if err != nil {
		return err
	}
	if outstanding > 0 {
		return ErrConflict
	}
	return tx.Commit()
}

// ReleaseQueued undoes a queue transition that has not been claimed by a worker
// (status pending). A request without attempts returns to approved, so the owner
// can refresh the review or press Execute again; a retried request (it already has
// attempts) returns to failed, which is the state Retry came from. The job engine
// uses it when the user cancels a queued job or when worker-side revalidation
// rejects a job before claim. A running request is never touched.
func (s *Store) ReleaseQueued(ctx context.Context, requestID int64) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE migration_requests SET status=CASE
			WHEN EXISTS(SELECT 1 FROM migration_execution_attempts a WHERE a.request_id=$1) THEN 'failed'
			ELSE 'approved' END,
		updated_at=now()
		WHERE id=$1 AND status='pending'`, requestID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
