package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Store struct{ DB *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{DB: db} }
func (s *Store) Can(ctx context.Context, user, db int64, write bool) (bool, error) {
	if user <= 0 || db <= 0 {
		return false, nil
	}
	var permitted bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users u WHERE u.id=$1 AND u.is_active AND
 (EXISTS(SELECT 1 FROM group_members gm JOIN group_db_access ga ON ga.group_id=gm.group_id WHERE gm.user_id=u.id AND ga.db_id=$2 AND (NOT $3 OR ga.level=2))))`, user, db, write).Scan(&permitted)
	return permitted, err
}

func (s *Store) Create(ctx context.Context, actor int64, d DefinitionInput) (Request, error) {
	var out Request
	if err := ValidateDefinition(d); err != nil {
		return out, err
	}

	ok, err := s.Can(ctx, actor, d.DBID, true)
	if err != nil {
		return out, err
	}
	if !ok {
		return out, ErrForbidden
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}

	defer tx.Rollback()
	var id, rev int64
	err = tx.QueryRowContext(ctx, `INSERT INTO migration_definitions(db_id,title,author_id) VALUES($1,$2,$3) RETURNING id`, d.DBID, strings.TrimSpace(d.Title), actor).Scan(&id)
	if err != nil {
		return out, err
	}

	var down, reason any
	if strings.TrimSpace(d.SQLDown) != "" {
		down = d.SQLDown
	} else {
		reason = d.IrreversibleReason
	}

	err = tx.QueryRowContext(ctx, `INSERT INTO migration_revisions(migration_id,revision,sql_up,sql_down,irreversible_reason,checksum) VALUES($1,1,$2,$3,$4,$5) RETURNING id`, id, d.SQLUp, down, reason, Checksum(d.SQLUp, d.SQLDown, d.IrreversibleReason)).Scan(&rev)
	if err != nil {
		return out, err
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO migration_requests(migration_id,revision_id,db_id,action,requested_by) VALUES($1,$2,$3,'up',$4) RETURNING id`, id, rev, d.DBID, actor).Scan(&out.ID)
	if err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}

	out.MigrationID = id
	out.RevisionID = rev
	out.DBID = d.DBID
	out.RequestedBy = actor
	out.Action = Up
	out.Status = Draft
	return out, nil
}

// Revise creates a new immutable revision, never mutates SQL on an existing revision
// Allowed only before the request enters the queue or any worker has run it
func (s *Store) Revise(ctx context.Context, requestID, actor int64, d DefinitionInput) (Request, error) {
	var out Request
	if err := ValidateDefinition(d); err != nil {
		return out, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}

	defer tx.Rollback()
	var mid, db, owner int64
	var status, action string
	err = tx.QueryRowContext(ctx, `SELECT migration_id,db_id,requested_by,status,action FROM migration_requests WHERE id=$1 FOR UPDATE`, requestID).Scan(&mid, &db, &owner, &status, &action)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if db != d.DBID || owner != actor || action != Up || (status != Draft && status != PendingReview && status != Approved && status != Rejected) {
		return out, ErrConflict
	}

	var permitted bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users u WHERE u.id=$1 AND u.is_active AND (EXISTS(SELECT 1 FROM group_members gm JOIN group_db_access ga ON ga.group_id=gm.group_id WHERE gm.user_id=u.id AND ga.db_id=$2 AND ga.level=2)))`, actor, db).Scan(&permitted)
	if err != nil {
		return out, err
	}
	if !permitted {
		return out, ErrForbidden
	}

	var attempted bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM migration_execution_attempts a JOIN migration_requests r ON r.id=a.request_id WHERE r.migration_id=$1)`, mid).Scan(&attempted)
	if err != nil {
		return out, err
	}
	if attempted {
		return out, ErrConflict
	}

	var rev, version int64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0)+1 FROM migration_revisions WHERE migration_id=$1`, mid).Scan(&version)
	if err != nil {
		return out, err
	}

	var down, reason any
	if strings.TrimSpace(d.SQLDown) != "" {
		down = d.SQLDown
	} else {
		reason = d.IrreversibleReason
	}

	err = tx.QueryRowContext(ctx, `INSERT INTO migration_revisions(migration_id,revision,sql_up,sql_down,irreversible_reason,checksum) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, mid, version, d.SQLUp, down, reason, Checksum(d.SQLUp, d.SQLDown, d.IrreversibleReason)).Scan(&rev)
	if err != nil {
		return out, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE migration_definitions SET title=$2 WHERE id=$1`, mid, d.Title)
	if err != nil {
		return out, err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM migration_approval_requirements WHERE request_id=$1`, requestID)
	if err != nil {
		return out, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE migration_requests SET revision_id=$2,status='draft',updated_at=now() WHERE id=$1`, requestID, rev)
	if err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}

	out = Request{ID: requestID, MigrationID: mid, RevisionID: rev, DBID: db, Action: Up, RequestedBy: actor, Status: Draft}
	return out, nil
}

func (s *Store) Get(ctx context.Context, id int64) (Request, error) {
	var q Request
	err := s.DB.QueryRowContext(ctx, `SELECT id,migration_id,revision_id,db_id,action,target_execution_id,requested_by,status FROM migration_requests WHERE id=$1`, id).Scan(&q.ID, &q.MigrationID, &q.RevisionID, &q.DBID, &q.Action, &q.TargetExecutionID, &q.RequestedBy, &q.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return q, ErrNotFound
	}
	return q, err
}

func lockDB(ctx context.Context, tx *sql.Tx, db int64) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, db+2300000000)
	return err
}

func (s *Store) Submit(ctx context.Context, requestID, actor int64) (Request, error) {
	return s.submit(ctx, requestID, actor, 0)
}

// submit compares the validated revision under the same lock used by Revise.
func (s *Store) submit(ctx context.Context, requestID, actor, validatedRevisionID int64) (Request, error) {
	var out Request
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}

	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT id,migration_id,revision_id,db_id,action,target_execution_id,requested_by,status FROM migration_requests WHERE id=$1 FOR UPDATE`, requestID).Scan(&out.ID, &out.MigrationID, &out.RevisionID, &out.DBID, &out.Action, &out.TargetExecutionID, &out.RequestedBy, &out.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if out.RequestedBy != actor || out.Status != Draft {
		return out, ErrConflict
	}
	if validatedRevisionID != 0 && out.RevisionID != validatedRevisionID {
		return out, errRevisionChanged
	}

	var permitted bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users u WHERE u.id=$1 AND u.is_active AND (EXISTS(SELECT 1 FROM group_members gm JOIN group_db_access ga ON ga.group_id=gm.group_id WHERE gm.user_id=u.id AND ga.db_id=$2 AND ga.level=2)))`, actor, out.DBID).Scan(&permitted)
	if err != nil {
		return out, err
	}
	if !permitted {
		return out, ErrForbidden
	}
	if err = lockDB(ctx, tx, out.DBID); err != nil {
		return out, err
	}
	if err = eligibleTx(ctx, tx, out); err != nil {
		return out, err
	}
	if err = refreshReviewTx(ctx, tx, requestID, out.DBID); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}

	out.Status = PendingReview
	return out, nil
}

// RefreshReview snapshots the current access groups and leaders without changing
// the immutable migration revision. Existing decisions are reset for safety.
func (s *Store) RefreshReview(ctx context.Context, requestID, actor int64) (Request, error) {
	var out Request
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()

	err = tx.QueryRowContext(ctx, `SELECT id,migration_id,revision_id,db_id,action,target_execution_id,requested_by,status
		FROM migration_requests WHERE id=$1 FOR UPDATE`, requestID).
		Scan(&out.ID, &out.MigrationID, &out.RevisionID, &out.DBID, &out.Action, &out.TargetExecutionID, &out.RequestedBy, &out.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if out.RequestedBy != actor || (out.Status != PendingReview && out.Status != Approved) {
		return out, ErrConflict
	}

	var permitted bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users u WHERE u.id=$1 AND u.is_active AND
		EXISTS(SELECT 1 FROM group_members gm JOIN group_db_access ga ON ga.group_id=gm.group_id
		WHERE gm.user_id=u.id AND ga.db_id=$2 AND ga.level=2))`, actor, out.DBID).Scan(&permitted)
	if err != nil {
		return out, err
	}
	if !permitted {
		return out, ErrForbidden
	}
	if err = lockDB(ctx, tx, out.DBID); err != nil {
		return out, err
	}
	if err = eligibleTx(ctx, tx, out); err != nil {
		return out, err
	}
	if err = refreshReviewTx(ctx, tx, requestID, out.DBID); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	out.Status = PendingReview
	return out, nil
}

func refreshReviewTx(ctx context.Context, tx *sql.Tx, requestID, dbID int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM migration_approval_requirements WHERE request_id=$1`, requestID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO migration_approval_requirements(request_id,group_id,leader_id)
		SELECT $1,ga.group_id,g.leader_user_id
		FROM group_db_access ga JOIN groups g ON g.id=ga.group_id
		WHERE ga.db_id=$2`, requestID, dbID); err != nil {
		return err
	}

	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM migration_approval_requirements WHERE request_id=$1`, requestID).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrConflict
	}
	_, err := tx.ExecContext(ctx, `UPDATE migration_requests SET status='pending_review',updated_at=now() WHERE id=$1`, requestID)
	return err
}

func (s *Store) Approve(ctx context.Context, id, actor int64, approve bool) (Request, error) {
	return s.approve(ctx, id, actor, approve, 0)
}

func (s *Store) approve(ctx context.Context, id, actor int64, approve bool, reviewedRevisionID int64) (Request, error) {
	var out Request
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}

	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT id,migration_id,revision_id,db_id,action,target_execution_id,requested_by,status FROM migration_requests WHERE id=$1 FOR UPDATE`, id).Scan(&out.ID, &out.MigrationID, &out.RevisionID, &out.DBID, &out.Action, &out.TargetExecutionID, &out.RequestedBy, &out.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if out.Status != PendingReview {
		return out, ErrConflict
	}
	if reviewedRevisionID != 0 && out.RevisionID != reviewedRevisionID {
		return out, errRevisionChanged
	}

	// Snapshot group leader at submit time. A user may approve for multiple groups.
	decision := "rejected"
	if approve {
		decision = "approved"
	}
	rows, err := tx.QueryContext(ctx, `UPDATE migration_approval_requirements ar
		SET decision=$3,decided_by=$2,decided_at=now(),
			approved_by=CASE WHEN $3='approved' THEN $2 ELSE NULL END,
			approved_at=CASE WHEN $3='approved' THEN now() ELSE NULL END
		WHERE ar.request_id=$1 AND ar.leader_id=$2 AND ar.decision IS NULL
		AND EXISTS(SELECT 1 FROM users u WHERE u.id=$2 AND u.is_active)
		AND EXISTS(SELECT 1 FROM group_members gm JOIN groups g ON g.id=gm.group_id
			WHERE gm.group_id=ar.group_id AND gm.user_id=$2 AND g.leader_user_id=$2)
		RETURNING ar.group_id`, id, actor, decision)
	if err != nil {
		return out, err
	}
	groupIDs := []int64{}
	for rows.Next() {
		var groupID int64
		if err = rows.Scan(&groupID); err != nil {
			rows.Close()
			return out, err
		}
		groupIDs = append(groupIDs, groupID)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	if err = rows.Close(); err != nil {
		return out, err
	}
	if len(groupIDs) == 0 {
		return out, ErrForbidden
	}
	for _, groupID := range groupIDs {
		if _, err = tx.ExecContext(ctx, `INSERT INTO migration_approval_decisions
			(request_id,revision_id,group_id,leader_id,actor_id,decision)
			SELECT mr.id,mr.revision_id,ar.group_id,ar.leader_id,$3,$4
			FROM migration_requests mr
			JOIN migration_approval_requirements ar ON ar.request_id=mr.id
			WHERE mr.id=$1 AND ar.group_id=$2`,
			id, groupID, actor, decision); err != nil {
			return out, err
		}
	}
	if !approve {
		_, err = tx.ExecContext(ctx, `UPDATE migration_requests SET status='rejected',updated_at=now() WHERE id=$1`, id)
		out.Status = Rejected
	} else {
		var missing int
		err = tx.QueryRowContext(ctx, `SELECT count(*) FROM migration_approval_requirements WHERE request_id=$1 AND approved_by IS NULL`, id).Scan(&missing)
		if err == nil && missing == 0 {
			_, err = tx.ExecContext(ctx, `UPDATE migration_requests SET status='approved',updated_at=now() WHERE id=$1`, id)
			out.Status = Approved
		}
	}

	if err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}

	return out, nil
}

func (s *Store) Approvals(ctx context.Context, id int64) ([]Approval, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT group_id,leader_id,approved_by,decision,decided_by,decided_at
		FROM migration_approval_requirements WHERE request_id=$1 ORDER BY group_id`, id)
	if err != nil {
		return nil, err
	}

	defer rows.Close()
	a := []Approval{}
	for rows.Next() {
		var v Approval
		var decision sql.NullString
		var decidedBy sql.NullInt64
		var decidedAt sql.NullTime
		if err = rows.Scan(&v.GroupID, &v.LeaderID, &v.ApprovedBy, &decision, &decidedBy, &decidedAt); err != nil {
			return nil, err
		}
		if decision.Valid {
			v.Decision = &decision.String
		}
		if decidedBy.Valid {
			v.DecidedBy = &decidedBy.Int64
		}
		if decidedAt.Valid {
			v.DecidedAt = &decidedAt.Time
		}
		a = append(a, v)
	}

	return a, rows.Err()
}

func (s *Store) ApprovalHistory(ctx context.Context, id int64) ([]ApprovalDecision, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,revision_id,group_id,leader_id,actor_id,decision,decided_at
		FROM migration_approval_decisions WHERE request_id=$1 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	decisions := []ApprovalDecision{}
	for rows.Next() {
		var decision ApprovalDecision
		if err = rows.Scan(&decision.ID, &decision.RevisionID, &decision.GroupID, &decision.LeaderID,
			&decision.ActorID, &decision.Decision, &decision.DecidedAt); err != nil {
			return nil, err
		}
		decisions = append(decisions, decision)
	}
	return decisions, rows.Err()
}

// Queue atomically persists an outbox event and switches request to pending
// Orang 4 publishes/consumes the outbox; this method NEVER executes SQL synchronously
func (s *Store) Queue(ctx context.Context, id, actor int64) (int64, error) {
	return s.QueueWith(ctx, id, actor, nil)
}

func (s *Store) queueRevision(ctx context.Context, id, actor, revisionID int64) (int64, error) {
	return s.QueueWith(ctx, id, actor, func(_ context.Context, _ *sql.Tx, q Request, _ int64) error {
		if q.RevisionID != revisionID {
			return errRevisionChanged
		}
		return nil
	})
}

// QueueHook runs inside the Queue transaction after the outbox row exists, so the
// worker-side job record (mode, batch plan, trace context) commits atomically with
// the pending transition. A hook error aborts the whole enqueue.
type QueueHook func(ctx context.Context, tx *sql.Tx, req Request, jobID int64) error

// QueueWith is Queue with an optional transactional hook used by the job engine.
func (s *Store) QueueWith(ctx context.Context, id, actor int64, hook QueueHook) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}

	defer tx.Rollback()
	var q Request
	err = tx.QueryRowContext(ctx, `SELECT id,migration_id,revision_id,db_id,action,target_execution_id,requested_by,status FROM migration_requests WHERE id=$1 FOR UPDATE`, id).Scan(&q.ID, &q.MigrationID, &q.RevisionID, &q.DBID, &q.Action, &q.TargetExecutionID, &q.RequestedBy, &q.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if q.Status != Approved {
		return 0, ErrConflict
	}

	var allowed bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users u WHERE u.id=$1 AND u.is_active AND (EXISTS(SELECT 1 FROM group_members gm JOIN group_db_access ga ON ga.group_id=gm.group_id WHERE gm.user_id=u.id AND ga.db_id=$2 AND ga.level=2)))`, actor, q.DBID).Scan(&allowed)
	if err != nil {
		return 0, err
	}
	if !allowed {
		return 0, ErrForbidden
	}
	if err = lockDB(ctx, tx, q.DBID); err != nil {
		return 0, err
	}
	if err = eligibleTx(ctx, tx, q); err != nil {
		return 0, err
	}
	if err = validateApproverSnapshot(ctx, tx, id, q.DBID); err != nil {
		return 0, err
	}

	var missing int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM migration_approval_requirements WHERE request_id=$1 AND approved_by IS NULL`, id).Scan(&missing)
	if err != nil {
		return 0, err
	}
	if missing != 0 {
		return 0, ErrConflict
	}

	var jobID int64
	// One outbox row per request keeps the job ID stable when a request is
	// re-queued after the worker released it (e.g. stale approval or cancel).
	err = tx.QueryRowContext(ctx, `INSERT INTO migration_job_outbox(request_id) VALUES($1)
		ON CONFLICT (request_id) DO UPDATE SET dispatched_at=NULL RETURNING id`, id).Scan(&jobID)
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE migration_requests SET status='pending',updated_at=now() WHERE id=$1`, id)
	if err != nil {
		return 0, err
	}
	if hook != nil {
		q.Status = Pending
		if err = hook(ctx, tx, q, jobID); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}

	return jobID, nil
}

func (s *Store) ListHistory(ctx context.Context, dbID int64, hide bool) ([]Execution, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT r.id,a.id,r.request_id,r.migration_id,r.revision_id,a.actor_id,
		r.action,a.status,r.sql_snapshot,r.rollback_of_execution_id,a.attempt_number,a.created_at,a.error_message,
		v.sql_down IS NOT NULL
		FROM migration_execution_runs r
		JOIN migration_execution_attempts a ON a.run_id=r.id
		JOIN migration_revisions v ON v.id=r.revision_id
		WHERE r.db_id=$1 ORDER BY a.id`, dbID)
	if err != nil {
		return nil, err
	}

	defer rows.Close()
	out := []Execution{}
	var completed []committed
	for rows.Next() {
		var e Execution
		var at time.Time
		var reversible bool
		if err = rows.Scan(&e.ID, &e.AttemptID, &e.RequestID, &e.MigrationID, &e.RevisionID, &e.ActorID,
			&e.Action, &e.Status, &e.SQLSnapshot, &e.RollbackOf, &e.Attempt, &at, &e.ErrorMessage, &reversible); err != nil {
			return nil, err
		}
		e.ExecutedAt = at.UTC().Format(time.RFC3339Nano)
		out = append(out, e)
		if e.Status == Success {
			completed = append(completed, committed{id: e.ID, migration: e.MigrationID, revision: e.RevisionID,
				action: e.Action, target: e.RollbackOf, reversible: reversible})
		}
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}
	// Preserve readable history even when old data cannot establish a valid action.
	if history, err := replayHistory(completed); err == nil {
		rollbackID := history.source(Rollback).id
		forwardID := history.source(Forward).id
		for i := range out {
			if out[i].Status != Success {
				continue
			}
			switch out[i].ID {
			case rollbackID:
				out[i].AvailableAction = Rollback
			case forwardID:
				out[i].AvailableAction = Forward
			}
		}
	}
	if hide {
		return HideRollback(out), nil
	}
	return out, nil
}

// RecordAttempt must be called by the worker after it knows whether the target transaction committed
// Failed attempts are persisted too per latest spec revision and QnA2. Never overwrite history
func (s *Store) RecordAttempt(ctx context.Context, requestID, actor int64, status string, attempt int, errText string) (int64, error) {
	if status != Success && status != Failed || attempt < 1 || actor <= 0 {
		return 0, ErrInvalid
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}

	defer tx.Rollback()
	var q Request
	err = tx.QueryRowContext(ctx, `SELECT id,migration_id,revision_id,db_id,action,target_execution_id,requested_by,status FROM migration_requests WHERE id=$1 FOR UPDATE`, requestID).Scan(&q.ID, &q.MigrationID, &q.RevisionID, &q.DBID, &q.Action, &q.TargetExecutionID, &q.RequestedBy, &q.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if q.Status != Running {
		return 0, ErrConflict
	}

	var nextAttempt int
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_number),0)+1
		FROM migration_execution_attempts WHERE request_id=$1`, requestID).Scan(&nextAttempt)
	if err != nil {
		return 0, err
	}
	if attempt != nextAttempt {
		return 0, ErrConflict
	}

	var sqlText string
	field := "sql_up"
	if q.Action == Rollback {
		field = "sql_down"
	}

	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT %s FROM migration_revisions WHERE id=$1 AND migration_id=$2`, field), q.RevisionID, q.MigrationID).Scan(&sqlText)
	if err != nil {
		return 0, err
	}

	var execID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO migration_execution_runs
		(request_id,db_id,migration_id,revision_id,action,actor_id,sql_snapshot,rollback_of_execution_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (request_id) DO NOTHING
		RETURNING id`, requestID, q.DBID, q.MigrationID, q.RevisionID, q.Action, q.RequestedBy, sqlText, q.TargetExecutionID).Scan(&execID)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT id FROM migration_execution_runs WHERE request_id=$1`, requestID).Scan(&execID)
	}
	if err != nil {
		return 0, err
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO migration_execution_attempts
		(request_id,run_id,attempt_number,actor_id,status,error_message)
		VALUES($1,$2,$3,$4,$5,$6)`, requestID, execID, attempt, actor, status,
		sql.NullString{String: errText, Valid: errText != ""})
	if err != nil {
		return 0, err
	}

	_, err = tx.ExecContext(ctx, `UPDATE migration_requests SET status=$2,updated_at=now() WHERE id=$1`, requestID, status)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}

	return execID, nil
}

// RevisionDetails returns a frozen SQL definition. A worker must execute this exact snapshot, rather than accept arbitrary SQL from the job payload
func (s *Store) RevisionDetails(ctx context.Context, requestID int64) (DefinitionInput, error) {
	d, _, err := s.revisionDetails(ctx, requestID)
	return d, err
}

func (s *Store) revisionDetails(ctx context.Context, requestID int64) (DefinitionInput, int64, error) {
	var d DefinitionInput
	var revisionID int64
	var down, reason sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT r.db_id,d.title,v.id,v.sql_up,v.sql_down,v.irreversible_reason FROM migration_requests r JOIN migration_definitions d ON d.id=r.migration_id JOIN migration_revisions v ON v.id=r.revision_id WHERE r.id=$1`, requestID).Scan(&d.DBID, &d.Title, &revisionID, &d.SQLUp, &down, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return d, 0, ErrNotFound
	}
	if err != nil {
		return d, 0, err
	}

	d.SQLDown = down.String
	d.IrreversibleReason = reason.String
	return d, revisionID, nil
}

// Claim is the worker-side, atomic state transition; queue deliveries are at-least-once
// Only one claimant may move pending to running. Caller must additionally take a target-DB lock and revalidate before executing the target query.
func (s *Store) Claim(ctx context.Context, requestID int64) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}

	defer tx.Rollback()
	var q Request
	err = tx.QueryRowContext(ctx, `SELECT id,migration_id,revision_id,db_id,action,target_execution_id,requested_by,status FROM migration_requests WHERE id=$1 FOR UPDATE`, requestID).Scan(&q.ID, &q.MigrationID, &q.RevisionID, &q.DBID, &q.Action, &q.TargetExecutionID, &q.RequestedBy, &q.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	if q.Status != Pending {
		return false, nil
	}
	if err = lockDB(ctx, tx, q.DBID); err != nil {
		return false, err
	}
	if err = eligibleTx(ctx, tx, q); err != nil {
		return false, err
	}
	if err = validateApproverSnapshot(ctx, tx, requestID, q.DBID); err != nil {
		return false, err
	}

	var missing int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM migration_approval_requirements WHERE request_id=$1 AND approved_by IS NULL`, requestID).Scan(&missing)
	if err != nil {
		return false, err
	}
	if missing != 0 {
		return false, ErrConflict
	}

	result, err := tx.ExecContext(ctx, `UPDATE migration_requests SET status='running',updated_at=now() WHERE id=$1 AND status='pending'`, requestID)
	if err != nil {
		return false, err
	}

	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if n != 1 {
		return false, ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}

	return true, nil
}

// Retry re-enqueues a failed request with the same approved immutable revision
// The worker is responsible for classifying transient errors and bounding retries
func (s *Store) Retry(ctx context.Context, requestID int64) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}

	defer tx.Rollback()
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM migration_requests WHERE id=$1 FOR UPDATE`, requestID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if status != Failed {
		return 0, ErrConflict
	}

	var jobID int64
	// Original outbox row remains for provenance; a delivery counter belongs to Orang 4
	err = tx.QueryRowContext(ctx, `SELECT id FROM migration_job_outbox WHERE request_id=$1`, requestID).Scan(&jobID)
	if err != nil {
		return 0, err
	}

	_, err = tx.ExecContext(ctx, `UPDATE migration_requests SET status='pending',updated_at=now() WHERE id=$1`, requestID)
	if err != nil {
		return 0, err
	}

	_, err = tx.ExecContext(ctx, `UPDATE migration_job_outbox SET dispatched_at=NULL WHERE request_id=$1`, requestID)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}

	return jobID, nil
}

// ListRequests supports the SPA and always scopes by the authorized database
func (s *Store) ListRequests(ctx context.Context, db int64) ([]Request, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,migration_id,revision_id,db_id,action,target_execution_id,requested_by,status FROM migration_requests WHERE db_id=$1 ORDER BY id DESC LIMIT 200`, db)
	if err != nil {
		return nil, err
	}

	defer rows.Close()
	out := []Request{}
	for rows.Next() {
		var q Request
		if err = rows.Scan(&q.ID, &q.MigrationID, &q.RevisionID, &q.DBID, &q.Action, &q.TargetExecutionID, &q.RequestedBy, &q.Status); err != nil {
			return nil, err
		}
		out = append(out, q)
	}

	return out, rows.Err()
}

// validateApproverSnapshot prevents using approval from changed group membership or leader
// All groups (including view-only) must be represented; missing leaders stay pending
func validateApproverSnapshot(ctx context.Context, tx *sql.Tx, requestID, dbID int64) error {
	var mismatch bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT group_id, leader_id FROM migration_approval_requirements WHERE request_id=$1
 EXCEPT
 SELECT ga.group_id, CASE WHEN u.is_active AND gm.user_id IS NOT NULL THEN g.leader_user_id ELSE NULL END
 FROM group_db_access ga JOIN groups g ON g.id=ga.group_id
 LEFT JOIN users u ON u.id=g.leader_user_id
 LEFT JOIN group_members gm ON gm.group_id=g.id AND gm.user_id=g.leader_user_id
 WHERE ga.db_id=$2
 ) OR EXISTS(
 SELECT ga.group_id, CASE WHEN u.is_active AND gm.user_id IS NOT NULL THEN g.leader_user_id ELSE NULL END
 FROM group_db_access ga JOIN groups g ON g.id=ga.group_id
 LEFT JOIN users u ON u.id=g.leader_user_id
 LEFT JOIN group_members gm ON gm.group_id=g.id AND gm.user_id=g.leader_user_id
 WHERE ga.db_id=$2
 EXCEPT
 SELECT group_id,leader_id FROM migration_approval_requirements WHERE request_id=$1
 )`, requestID, dbID).Scan(&mismatch)
	if err != nil {
		return err
	}
	if mismatch {
		return ErrConflict
	}
	return nil
}
