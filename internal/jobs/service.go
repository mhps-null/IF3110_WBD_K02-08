package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/lifecycle"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/observability"
)

// LifecycleAPI is the subset of lifecycle.Store the API-side service needs.
type LifecycleAPI interface {
	Get(ctx context.Context, id int64) (lifecycle.Request, error)
	Can(ctx context.Context, user, db int64, write bool) (bool, error)
	RevisionDetails(ctx context.Context, requestID int64) (lifecycle.DefinitionInput, error)
	QueueWith(ctx context.Context, id, actor int64, hook lifecycle.QueueHook) (int64, error)
	Retry(ctx context.Context, requestID int64) (int64, error)
	ReleaseQueued(ctx context.Context, requestID int64) error
	RevalidateApproval(ctx context.Context, requestID int64) error
}

// Service implements the job API. It publishes and controls jobs but never
// executes SQL: only workers do.
type Service struct {
	Store     *Store
	Lifecycle LifecycleAPI
	Targets   TargetOpener // optional: early batch cursor validation
}

// Detail is the job page payload.
type Detail struct {
	Job     Job     `json:"job"`
	Batches []Batch `json:"batches"`
	Events  []Event `json:"events"`
	Actions Actions `json:"actions"`
}

// Actions tells the SPA which controls the backend will accept for this user.
type Actions struct {
	Resume     bool `json:"resume"`
	Cancel     bool `json:"cancel"`
	Compensate bool `json:"compensate"`
}

func traceFrom(ctx context.Context) (string, string) {
	carrier := map[string]string{}
	observability.InjectTraceContext(ctx, carrier)
	return carrier[observability.TraceParentHeader], carrier[observability.CorrelationIDHeader]
}

func mapDomain(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, lifecycle.ErrForbidden):
		return ErrForbidden
	case errors.Is(err, lifecycle.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, lifecycle.ErrInvalid):
		return ErrInvalid
	case errors.Is(err, lifecycle.ErrConflict):
		return fmt.Errorf("request is not executable now (needs status approved, current approvals, linear order, and no other running/partial migration on this database): %w", ErrConflict)
	}
	return err
}

func (s *Service) require(ctx context.Context, actor, db int64, write bool) error {
	if actor <= 0 || db <= 0 {
		return ErrForbidden
	}
	ok, err := s.Lifecycle.Can(ctx, actor, db, write)
	if err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}

// Execute publishes the approved request as a job and returns immediately.
func (s *Service) Execute(ctx context.Context, actor, dbID, requestID int64, in ExecuteOptions) (Job, error) {
	if err := s.require(ctx, actor, dbID, true); err != nil {
		return Job{}, err
	}
	req, err := s.Lifecycle.Get(ctx, requestID)
	if err != nil {
		return Job{}, mapDomain(err)
	}
	if req.DBID != dbID {
		return Job{}, ErrNotFound
	}
	revisionChanged := fmt.Errorf("revisi migration sudah berubah. Muat ulang dan periksa versi terbaru: %w", ErrConflict)
	if in.RevisionID != 0 && req.RevisionID != in.RevisionID {
		return Job{}, revisionChanged
	}
	if req.Status != lifecycle.Approved {
		return Job{}, fmt.Errorf("only an approved request can be executed (current: %s): %w", req.Status, ErrConflict)
	}
	explicitMode := strings.TrimSpace(in.Mode) != ""
	opts, err := in.Normalize()
	if err != nil {
		return Job{}, fmt.Errorf("invalid execution options: mode must be normal or batch; batch needs a table, an integer unique cursor column and batch size 1..%d: %w", MaxBatchSize, ErrInvalid)
	}
	var sourceJob *int64
	switch req.Action {
	case lifecycle.Rollback:
		// Rollback mode is derived: a batch execution is undone batch by batch
		// over its committed ranges (reverse order); otherwise one transaction.
		opts.Mode, opts.BatchTable, opts.BatchColumn, opts.BatchSize = ModeNormal, "", "", 0
		if req.TargetExecutionID != nil {
			src, err := s.Store.ForExecution(ctx, *req.TargetExecutionID)
			if err == nil && src.Mode == ModeBatch {
				id := src.ID
				sourceJob = &id
				opts.Mode, opts.BatchSize = ModeBatch, src.BatchSize
				if opts.BatchSize == 0 {
					opts.BatchSize = DefaultBatchSize
				}
			} else if err != nil && !errors.Is(err, ErrNotFound) {
				return Job{}, err
			}
		}
	case lifecycle.Forward:
		if !explicitMode {
			if src, err := s.Store.LatestBatchJobForMigration(ctx, req.MigrationID); err == nil {
				opts.Mode, opts.BatchTable, opts.BatchColumn, opts.BatchSize = ModeBatch, src.BatchTable, src.BatchColumn, src.BatchSize
			} else if !errors.Is(err, ErrNotFound) {
				return Job{}, err
			}
		}
	}

	def, err := s.Lifecycle.RevisionDetails(ctx, req.ID)
	if err != nil {
		return Job{}, mapDomain(err)
	}
	statement := def.SQLUp
	if req.Action == lifecycle.Rollback {
		statement = def.SQLDown
	}
	if err := CheckSQLForMode(opts.Mode, statement); err != nil {
		if opts.Mode == ModeBatch {
			return Job{}, fmt.Errorf("batch mode requires SQL that uses $1 (exclusive lower cursor) and $2 (inclusive upper cursor), e.g. UPDATE t SET ... WHERE id > $1 AND id <= $2: %w", ErrInvalid)
		}
		return Job{}, fmt.Errorf("this SQL uses $1/$2 placeholders and must be executed in batch mode: %w", ErrInvalid)
	}
	if opts.Mode == ModeBatch && sourceJob == nil && s.Targets != nil {
		target, engine, err := s.Targets.Open(ctx, dbID)
		if err != nil || engine != contract.EnginePostgres {
			return Job{}, fmt.Errorf("target database is unavailable for batch validation: %w", ErrConflict)
		}
		if _, err := ValidateCursor(ctx, target, opts.BatchTable, opts.BatchColumn); err != nil {
			if errors.Is(err, ErrInvalid) {
				return Job{}, err
			}
			return Job{}, fmt.Errorf("cannot validate batch cursor: %w", ErrConflict)
		}
	}

	trace, corr := traceFrom(ctx)
	jobID, err := s.Lifecycle.QueueWith(ctx, req.ID, actor, func(ctx context.Context, tx *sql.Tx, q lifecycle.Request, jobID int64) error {
		// QueueWith holds the request row lock and rolls back the enqueue if the hook fails.
		if q.RevisionID != req.RevisionID {
			return revisionChanged
		}
		return UpsertQueuedTx(ctx, tx, q, jobID, NewJob{Options: opts, SourceJobID: sourceJob, TraceParent: trace, CorrelationID: corr, EnqueuedBy: actor})
	})
	if err != nil {
		if errors.Is(err, revisionChanged) {
			return Job{}, err
		}
		if errors.Is(err, ErrConflict) {
			return Job{}, fmt.Errorf("a job for this request already ran; use resume instead: %w", ErrConflict)
		}
		return Job{}, mapDomain(err)
	}
	return s.Store.Get(ctx, jobID)
}

// List returns jobs on databases the actor can view.
func (s *Service) List(ctx context.Context, actor int64, f ListFilter) ([]Job, error) {
	if actor <= 0 {
		return nil, ErrForbidden
	}
	if f.Status != "" && allowedStatus(f.Status) == "other" {
		return nil, ErrInvalid
	}
	if f.DBID > 0 {
		if err := s.require(ctx, actor, f.DBID, false); err != nil {
			return nil, err
		}
	}
	f.UserID = actor
	return s.Store.List(ctx, f)
}

func (s *Service) load(ctx context.Context, actor, jobID int64, write bool) (Job, error) {
	if jobID <= 0 {
		return Job{}, ErrInvalid
	}
	j, err := s.Store.Get(ctx, jobID)
	if err != nil {
		return j, err
	}
	if err := s.require(ctx, actor, j.DBID, write); err != nil {
		// Hide existence of jobs on databases the user cannot view.
		if !write {
			return Job{}, ErrNotFound
		}
		if ok, _ := s.Lifecycle.Can(ctx, actor, j.DBID, false); !ok {
			return Job{}, ErrNotFound
		}
		return Job{}, err
	}
	return j, nil
}

// Get returns a job with batches, events and allowed actions.
func (s *Service) Get(ctx context.Context, actor, jobID int64) (Detail, error) {
	j, err := s.load(ctx, actor, jobID, false)
	if err != nil {
		return Detail{}, err
	}
	batches, err := s.Store.Batches(ctx, j.ID, 200)
	if err != nil {
		return Detail{}, err
	}
	events, err := s.Store.Events(ctx, j.ID, 100)
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Job: j, Batches: batches, Events: events}
	if canEdit, _ := s.Lifecycle.Can(ctx, actor, j.DBID, true); canEdit {
		d.Actions = allowedActions(j)
	}
	return d, nil
}

func allowedActions(j Job) Actions {
	a := Actions{}
	switch j.Status {
	case StatusFailed, StatusPartial, StatusPartiallyReverted, StatusReverted:
		a.Resume = j.RequestStatus == lifecycle.Failed
	case StatusCancelled, StatusRejected:
		a.Resume = j.RequestStatus == lifecycle.Approved
	case StatusUncertain:
		a.Resume = j.LeaseOwner == ""
	}
	switch j.Status {
	case StatusQueued, StatusRetrying:
		a.Cancel = j.LeaseOwner == "" && j.Op == OpApply
	case StatusRunning:
		a.Cancel = j.Mode == ModeBatch && !j.CancelRequested
	}
	a.Compensate = j.Mode == ModeBatch && (j.Status == StatusPartial || j.Status == StatusPartiallyReverted) &&
		j.BatchesDone > j.BatchesReverted && j.RequestStatus == lifecycle.Failed
	return a
}

// Resume continues a failed/partial job from its checkpoint (same approved
// entry, so no new approval cycle), re-publishes a cancelled/rejected one, or
// asks for immediate re-verification of an uncertain one.
func (s *Service) Resume(ctx context.Context, actor, jobID int64) (Job, error) {
	j, err := s.load(ctx, actor, jobID, true)
	if err != nil {
		return j, err
	}
	if !allowedActions(j).Resume {
		return j, fmt.Errorf("job cannot be resumed in status %s: %w", j.Status, ErrConflict)
	}
	trace, corr := traceFrom(ctx)
	switch j.Status {
	case StatusUncertain:
		if _, err := s.Store.DB.ExecContext(ctx, `UPDATE migration_jobs SET next_run_at=now(), updated_at=now()
			WHERE id=$1 AND status='uncertain' AND lease_owner IS NULL`, j.ID); err != nil {
			return j, err
		}
		_ = s.Store.AddEvent(ctx, j.ID, "job.verify_requested", StatusUncertain, fmt.Sprintf("immediate verification requested by user:%d", actor), "", nil, nil)
	case StatusCancelled, StatusRejected:
		opts := ExecuteOptions{Mode: j.Mode, BatchTable: j.BatchTable, BatchColumn: j.BatchColumn, BatchSize: j.BatchSize, MaxAttempts: j.MaxAttempts}
		if _, err := s.Lifecycle.QueueWith(ctx, j.RequestID, actor, func(ctx context.Context, tx *sql.Tx, q lifecycle.Request, id int64) error {
			return UpsertQueuedTx(ctx, tx, q, id, NewJob{Options: opts, SourceJobID: j.SourceJobID, TraceParent: trace, CorrelationID: corr, EnqueuedBy: actor})
		}); err != nil {
			return j, mapDomain(err)
		}
	default:
		if held, err := s.Store.DBHeld(ctx, j.DBID, j.RequestID); err != nil {
			return j, err
		} else if held {
			return j, fmt.Errorf("another job holds this database: %w", ErrConflict)
		}
		if _, err := s.Lifecycle.Retry(ctx, j.RequestID); err != nil {
			return j, mapDomain(err)
		}
		if err := s.Store.Rearm(ctx, j.ID, trace, corr); err != nil && !errors.Is(err, ErrConflict) {
			return j, err
		}
	}
	return s.Store.Get(ctx, j.ID)
}

// Cancel removes a queued job from the queue (request back to approved/failed),
// or asks a running batch job to stop after its current batch. A running
// single-transaction job cannot be interrupted safely mid-statement.
func (s *Service) Cancel(ctx context.Context, actor, jobID int64) (Job, error) {
	j, err := s.load(ctx, actor, jobID, true)
	if err != nil {
		return j, err
	}
	switch {
	case (j.Status == StatusQueued || j.Status == StatusRetrying) && j.LeaseOwner == "" && j.Op == OpApply:
		tx, err := s.Store.DB.BeginTx(ctx, nil)
		if err != nil {
			return j, err
		}
		defer tx.Rollback()
		if err := CancelQueuedTx(ctx, tx, j.ID); err != nil {
			return j, fmt.Errorf("job was claimed by a worker meanwhile: %w", ErrConflict)
		}
		if err := tx.Commit(); err != nil {
			return j, err
		}
		if err := s.Lifecycle.ReleaseQueued(ctx, j.RequestID); err != nil && !errors.Is(err, lifecycle.ErrConflict) {
			return j, err
		}
	case j.Status == StatusRunning && j.Mode == ModeBatch:
		if err := s.Store.RequestCancel(ctx, j.ID); err != nil {
			return j, fmt.Errorf("job is no longer running: %w", err)
		}
	default:
		return j, fmt.Errorf("job cannot be cancelled in status %s (a single-transaction job cannot be stopped mid-statement): %w", j.Status, ErrConflict)
	}
	return s.Store.Get(ctx, j.ID)
}

// Compensate schedules per-batch compensation of committed batches.
func (s *Service) Compensate(ctx context.Context, actor, jobID int64) (Job, error) {
	j, err := s.load(ctx, actor, jobID, true)
	if err != nil {
		return j, err
	}
	if !allowedActions(j).Compensate {
		return j, fmt.Errorf("only a partial/partially reverted batch job with committed batches can be compensated: %w", ErrConflict)
	}
	if err := s.Lifecycle.RevalidateApproval(ctx, j.RequestID); err != nil {
		return j, fmt.Errorf("compensation requires a still-valid approval and View + Edit permission of the requester: %w", mapDomainPlain(err))
	}
	def, err := s.Lifecycle.RevisionDetails(ctx, j.RequestID)
	if err != nil {
		return j, mapDomain(err)
	}
	inverse := def.SQLDown
	if j.Action == lifecycle.Rollback {
		inverse = def.SQLUp
	}
	if strings.TrimSpace(inverse) == "" || CheckSQLForMode(ModeBatch, inverse) != nil {
		return j, fmt.Errorf("compensation is unsafe: the approved inverse SQL is missing (irreversible) or not batch-compatible; create a corrective migration: %w", ErrConflict)
	}
	if err := s.Store.QueueCompensation(ctx, j.ID, actor); err != nil {
		return j, err
	}
	return s.Store.Get(ctx, j.ID)
}

func mapDomainPlain(err error) error {
	switch {
	case errors.Is(err, lifecycle.ErrForbidden):
		return ErrForbidden
	case errors.Is(err, lifecycle.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, lifecycle.ErrConflict):
		return ErrConflict
	}
	return err
}
