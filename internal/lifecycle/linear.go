package lifecycle

import (
	"context"
	"database/sql"
	"errors"
)

type committed struct {
	id, migration, revision int64
	action                  string
	target                  *int64
	reversible              bool
}

func committedTx(ctx context.Context, tx *sql.Tx, db int64) ([]committed, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT r.id,r.migration_id,r.revision_id,r.action,r.rollback_of_execution_id,v.sql_down IS NOT NULL
		FROM migration_execution_runs r
		JOIN migration_revisions v ON v.id=r.revision_id
		JOIN LATERAL (
			SELECT a.status,a.id
			FROM migration_execution_attempts a
			WHERE a.run_id=r.id
			ORDER BY a.attempt_number DESC,a.id DESC
			LIMIT 1
		) latest ON latest.status='success'
		WHERE r.db_id=$1
		ORDER BY latest.id`, db)
	if err != nil {
		return nil, err
	}

	defer rows.Close()
	list := []committed{}
	for rows.Next() {
		var v committed
		if err = rows.Scan(&v.id, &v.migration, &v.revision, &v.action, &v.target, &v.reversible); err != nil {
			return nil, err
		}
		list = append(list, v)
	}

	return list, rows.Err()
}

type linearHistory struct {
	active     []committed
	rolledBack []committed
}

func lastExecution(stack []committed) committed {
	if len(stack) == 0 {
		return committed{}
	}
	return stack[len(stack)-1]
}

func (h linearHistory) source(action string) committed {
	switch action {
	case Rollback:
		if e := lastExecution(h.active); e.reversible {
			return e
		}
	case Forward:
		return lastExecution(h.rolledBack)
	}
	return committed{}
}

func replayHistory(items []committed) (linearHistory, error) {
	var history linearHistory
	for _, e := range items {
		switch e.action {
		case Up:
			history.active = append(history.active, e)
			// A new migration replaces the abandoned branch.
			history.rolledBack = nil
		case Rollback:
			top := lastExecution(history.active)
			if top.id == 0 || e.target == nil || top.id != *e.target {
				return linearHistory{}, ErrConflict
			}
			history.active = history.active[:len(history.active)-1]
			history.rolledBack = append(history.rolledBack, e)
		case Forward:
			next := lastExecution(history.rolledBack)
			if next.id == 0 || e.target == nil || next.id != *e.target {
				return linearHistory{}, ErrConflict
			}
			history.rolledBack = history.rolledBack[:len(history.rolledBack)-1]
			history.active = append(history.active, e)
		default:
			return linearHistory{}, ErrConflict
		}
	}
	return history, nil
}

type eligibilityMode int

const (
	eligibilityReview  eligibilityMode = iota // Submit, RefreshReview, CreateAction
	eligibilityEnqueue                        // QueueWith (Execute)
	eligibilityClaim                          // Worker Claim & Revalidate
)

func eligibleTx(ctx context.Context, tx *sql.Tx, q Request) error {
	return eligibleModeTx(ctx, tx, q, eligibilityReview)
}

func eligibleModeTx(ctx context.Context, tx *sql.Tx, q Request, mode eligibilityMode) error {
	items, err := committedTx(ctx, tx, q.DBID)
	if err != nil {
		return err
	}
	history, err := replayHistory(items)
	if err != nil {
		return err
	}

	// A database held by a partial/partially-reverted/compensating/uncertain job
	// blocks other migrations until resolved (Spec: "Tahan migration berikutnya...").
	var held bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM migration_db_holds WHERE db_id=$1 AND request_id<>$2)`, q.DBID, q.ID).Scan(&held)
	if err != nil {
		return err
	}
	if held {
		return ErrConflict
	}

	// At worker claim/revalidation time, at most one request may be running on this DB.
	if mode == eligibilityClaim {
		var runningOther bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM migration_requests WHERE db_id=$1 AND id<>$2 AND status='running')`, q.DBID, q.ID).Scan(&runningOther)
		if err != nil {
			return err
		}
		if runningOther {
			return ErrConflict
		}
	}

	switch q.Action {
	case Up:
		for _, e := range items {
			if e.migration == q.MigrationID {
				return ErrConflict
			}
		}
		if mode == eligibilityEnqueue {
			var sameMigrationActive bool
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM migration_requests WHERE db_id=$1 AND id<>$2 AND migration_id=$3 AND status IN ('pending','running'))`,
				q.DBID, q.ID, q.MigrationID).Scan(&sameMigrationActive)
			if err != nil {
				return err
			}
			if sameMigrationActive {
				return ErrConflict
			}
		}

	case Rollback, Forward:
		if mode != eligibilityClaim {
			var inFlight bool
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM migration_requests WHERE db_id=$1 AND id<>$2 AND status IN ('pending','running'))`,
				q.DBID, q.ID).Scan(&inFlight)
			if err != nil {
				return err
			}
			if inFlight {
				return ErrConflict
			}
		}
		source := history.source(q.Action)
		if source.id == 0 || q.TargetExecutionID == nil || source.id != *q.TargetExecutionID ||
			source.migration != q.MigrationID || source.revision != q.RevisionID {
			return ErrConflict
		}

	default:
		return ErrInvalid
	}

	return nil
}

// CreateAction binds rollback/forward to an immutable, committed revision
// Rollback refers to the active up/forward execution; forward refers to the successful rollback
func (s *Store) CreateAction(ctx context.Context, actor, db, migration int64, action string) (Request, error) {
	return s.createAction(ctx, actor, db, migration, action, 0)
}

func (s *Store) createAction(ctx context.Context, actor, db, migration int64, action string, executionID int64) (Request, error) {
	var q Request
	if action != Rollback && action != Forward {
		return q, ErrInvalid
	}

	allowed, err := s.Can(ctx, actor, db, true)
	if err != nil {
		return q, err
	}
	if !allowed {
		return q, ErrForbidden
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return q, err
	}

	defer tx.Rollback()

	if err = lockDB(ctx, tx, db); err != nil {
		return q, err
	}

	var actual int64
	err = tx.QueryRowContext(ctx, `SELECT db_id FROM migration_definitions WHERE id=$1`, migration).Scan(&actual)
	if errors.Is(err, sql.ErrNoRows) {
		return q, ErrNotFound
	}
	if err != nil {
		return q, err
	}
	if actual != db {
		return q, ErrNotFound
	}

	items, err := committedTx(ctx, tx, db)
	if err != nil {
		return q, err
	}
	history, err := replayHistory(items)
	if err != nil {
		return q, err
	}

	source := history.source(action)
	if source.id == 0 || source.migration != migration || (executionID != 0 && source.id != executionID) {
		return q, ErrConflict
	}

	q = Request{DBID: db, MigrationID: migration, RevisionID: source.revision, Action: action, TargetExecutionID: &source.id, RequestedBy: actor, Status: Draft}
	if err = eligibleTx(ctx, tx, q); err != nil {
		return q, err
	}

	err = tx.QueryRowContext(ctx, `INSERT INTO migration_requests(migration_id,revision_id,db_id,action,target_execution_id,requested_by) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, migration, source.revision, db, action, source.id, actor).Scan(&q.ID)
	if err != nil {
		return q, err
	}
	if err = tx.Commit(); err != nil {
		return q, err
	}
	return q, nil
}

func (s *Store) Inbox(ctx context.Context, actor int64) ([]Request, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT r.id,r.migration_id,r.revision_id,r.db_id,r.action,r.target_execution_id,r.requested_by,r.status FROM migration_requests r JOIN migration_approval_requirements a ON a.request_id=r.id WHERE r.status='pending_review' AND a.leader_id=$1 AND a.approved_by IS NULL ORDER BY r.id DESC`, actor)
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
