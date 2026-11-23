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
}

func committedTx(ctx context.Context, tx *sql.Tx, db int64) ([]committed, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT r.id,r.migration_id,r.revision_id,r.action,r.rollback_of_execution_id
		FROM migration_execution_runs r
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
		if err = rows.Scan(&v.id, &v.migration, &v.revision, &v.action, &v.target); err != nil {
			return nil, err
		}
		list = append(list, v)
	}

	return list, rows.Err()
}

func activeStack(items []committed) ([]committed, error) {
	stack := []committed{}
	for _, e := range items {
		switch e.action {
		case Up, Forward:
			stack = append(stack, e)
		case Rollback:
			if e.target == nil || len(stack) == 0 || stack[len(stack)-1].id != *e.target {
				return nil, ErrConflict
			}
			stack = stack[:len(stack)-1]
		default:
			return nil, ErrConflict
		}
	}

	return stack, nil
}

func eligibleTx(ctx context.Context, tx *sql.Tx, q Request) error {
	items, err := committedTx(ctx, tx, q.DBID)
	if err != nil {
		return err
	}
	stack, err := activeStack(items)
	if err != nil {
		return err
	}

	var busy bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM migration_requests WHERE db_id=$1 AND id<>$2 AND status IN ('pending','running'))`, q.DBID, q.ID).Scan(&busy)
	if err != nil {
		return err
	}
	if busy {
		return ErrConflict
	}

	switch q.Action {
	case Up:
		for _, e := range items {
			if e.migration == q.MigrationID {
				return ErrConflict
			}
		}

	case Rollback:
		if len(stack) == 0 || q.TargetExecutionID == nil || stack[len(stack)-1].id != *q.TargetExecutionID {
			return ErrConflict
		}
		if stack[len(stack)-1].migration != q.MigrationID || stack[len(stack)-1].revision != q.RevisionID {
			return ErrConflict
		}
		var hasDown bool
		err = tx.QueryRowContext(ctx, `SELECT sql_down IS NOT NULL FROM migration_revisions WHERE id=$1`, q.RevisionID).Scan(&hasDown)
		if err != nil {
			return err
		}
		if !hasDown {
			return ErrConflict
		}

	case Forward:
		if len(items) == 0 || items[len(items)-1].action != Rollback || items[len(items)-1].migration != q.MigrationID || items[len(items)-1].revision != q.RevisionID {
			return ErrConflict
		}
		if q.TargetExecutionID == nil || items[len(items)-1].id != *q.TargetExecutionID {
			return ErrConflict
		}
		for _, e := range stack {
			if e.migration == q.MigrationID {
				return ErrConflict
			}
		}

	default:
		return ErrInvalid
	}

	return nil
}

// CreateAction binds rollback/forward to an immutable, committed revision
// Rollback refers to the active up/forward execution; forward refers to the successful rollback
func (s *Store) CreateAction(ctx context.Context, actor, db, migration int64, action string) (Request, error) {
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
	stack, err := activeStack(items)
	if err != nil {
		return q, err
	}

	var source committed
	if action == Rollback {
		if len(stack) == 0 || stack[len(stack)-1].migration != migration {
			return q, ErrConflict
		}
		source = stack[len(stack)-1]
	} else {
		if len(items) == 0 || items[len(items)-1].action != Rollback || items[len(items)-1].migration != migration {
			return q, ErrConflict
		}
		source = items[len(items)-1]
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
