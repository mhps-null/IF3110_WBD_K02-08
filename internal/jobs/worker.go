package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/lifecycle"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/migration"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/observability"
)

// Domain is the lifecycle contract (Orang 3) the worker depends on. The worker
// never changes migration definitions; it only claims, revalidates, loads the
// immutable approved SQL, and reports attempts. *lifecycle.Store implements it.
type Domain interface {
	Get(ctx context.Context, id int64) (lifecycle.Request, error)
	Claim(ctx context.Context, requestID int64) (bool, error)
	Revalidate(ctx context.Context, requestID int64) error
	RevalidateApproval(ctx context.Context, requestID int64) error
	LoadApprovedExecution(ctx context.Context, requestID int64) (lifecycle.ExecutionSpec, error)
	RevisionDetails(ctx context.Context, requestID int64) (lifecycle.DefinitionInput, error)
	RecordAttempt(ctx context.Context, requestID, actor int64, status string, attempt int, errText string) (int64, error)
	Retry(ctx context.Context, requestID int64) (int64, error)
	ReleaseQueued(ctx context.Context, requestID int64) error
}

// TargetOpener opens target database pools (connections.PoolManager).
type TargetOpener interface {
	Open(ctx context.Context, dbID int64) (*sql.DB, contract.Engine, error)
}

// Config tunes the worker. Zero values are replaced by safe defaults.
type Config struct {
	WorkerID         string
	Concurrency      int           // jobs processed in parallel (different databases)
	PollInterval     time.Duration // queue polling interval
	Lease            time.Duration // job lease; renewed by heartbeat every Lease/3
	LockWait         time.Duration // how long to wait for a busy database lock
	RetryBase        time.Duration // first retry backoff
	RetryMax         time.Duration // backoff cap
	LockTimeout      time.Duration // target SET LOCAL lock_timeout
	MaxDeliveries    int           // bound for "target unreachable before start" redeliveries
	ShutdownGrace    time.Duration // time in-flight jobs get after shutdown signal
	VerifyAttempts   int           // ledger verification tries for an uncertain commit
	VerifyInterval   time.Duration
	UncertainRecheck time.Duration // delay before re-verifying when the target is unreachable
}

func (c Config) withDefaults() Config {
	if c.WorkerID == "" {
		c.WorkerID = "worker"
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 4
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 500 * time.Millisecond
	}
	if c.Lease <= 0 {
		c.Lease = 30 * time.Second
	}
	if c.LockWait <= 0 {
		c.LockWait = 3 * time.Second
	}
	if c.RetryBase <= 0 {
		c.RetryBase = 2 * time.Second
	}
	if c.RetryMax <= 0 {
		c.RetryMax = time.Minute
	}
	if c.LockTimeout <= 0 {
		c.LockTimeout = 30 * time.Second
	}
	if c.MaxDeliveries <= 0 {
		c.MaxDeliveries = 20
	}
	if c.ShutdownGrace <= 0 {
		c.ShutdownGrace = 20 * time.Second
	}
	if c.VerifyAttempts <= 0 {
		c.VerifyAttempts = 3
	}
	if c.VerifyInterval <= 0 {
		c.VerifyInterval = 500 * time.Millisecond
	}
	if c.UncertainRecheck <= 0 {
		c.UncertainRecheck = 15 * time.Second
	}
	return c
}

// Faults are failure-injection hooks used only by tests.
type Faults struct {
	// BeforeCommit returns an error to make a batch/normal statement fail
	// (the transaction is rolled back). seq is 0 for normal mode.
	BeforeCommit func(job Job, direction string, seq int) error
	// AfterCommit returns an error to simulate a lost COMMIT acknowledgement
	// AFTER the transaction committed (uncertain outcome).
	AfterCommit func(job Job, direction string, seq int) error
	// AfterBatch runs after a batch is committed and mirrored; returning an
	// error simulates a worker crash (the job is abandoned with its lease).
	AfterBatch func(job Job, seq int) error
}

// errCrash simulates a worker process dying: the job is left leased.
var errCrash = errors.New("simulated worker crash")

// Worker consumes the durable queue.
type Worker struct {
	cfg     Config
	meta    *sql.DB
	store   *Store
	domain  Domain
	targets TargetOpener
	logger  *observability.Logger
	tracer  *observability.Tracer
	metrics observability.MetricsRecorder
	faults  Faults
}

func NewWorker(cfg Config, meta *sql.DB, domain Domain, targets TargetOpener, logger *observability.Logger, tracer *observability.Tracer, metrics observability.MetricsRecorder) *Worker {
	if logger == nil {
		logger = observability.NewLogger(nil, slog.LevelInfo)
	}
	if tracer == nil {
		tracer = observability.NewTracer("worker", nil)
	}
	if metrics == nil {
		metrics = observability.NoopMetrics{}
	}
	return &Worker{cfg: cfg.withDefaults(), meta: meta, store: NewStore(meta), domain: domain, targets: targets,
		logger: logger, tracer: tracer, metrics: metrics}
}

// SetFaults installs failure-injection hooks (tests only).
func (w *Worker) SetFaults(f Faults) { w.faults = f }

// ID returns the worker identity used for leases.
func (w *Worker) ID() string { return w.cfg.WorkerID }

// Run polls until ctx is cancelled, then waits up to ShutdownGrace for
// in-flight jobs. Jobs still running after the grace period are interrupted and
// become "uncertain" so another worker verifies and resumes them.
func (w *Worker) Run(ctx context.Context) error {
	w.logger.LogActivity(ctx, "worker.start", "system", "", "started",
		slog.String("worker_id", w.cfg.WorkerID), slog.Int("concurrency", w.cfg.Concurrency))
	jobCtx, cancelJobs := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelJobs()
	sem := make(chan struct{}, w.cfg.Concurrency)
	var wg sync.WaitGroup
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()
	for {
		w.tick(ctx, jobCtx, sem, &wg)
		select {
		case <-ctx.Done():
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(w.cfg.ShutdownGrace):
				cancelJobs()
				<-done
			}
			w.logger.LogActivity(context.Background(), "worker.stop", "system", "", "stopped", slog.String("worker_id", w.cfg.WorkerID))
			return nil
		case <-ticker.C:
		}
	}
}

func (w *Worker) tick(ctx, jobCtx context.Context, sem chan struct{}, wg *sync.WaitGroup) {
	if ctx.Err() != nil {
		return
	}
	if _, err := w.store.Dispatch(ctx); err != nil && ctx.Err() == nil {
		w.logger.LogActivity(ctx, "queue.dispatch", "system", "", "error", slog.String("error", err.Error()))
	}
	if ids, err := w.store.RecoverExpired(ctx); err != nil && ctx.Err() == nil {
		w.logger.LogActivity(ctx, "queue.recover", "system", "", "error", slog.String("error", err.Error()))
	} else {
		for _, id := range ids {
			w.metrics.IncJobStatus(StatusUncertain, 1)
			w.logger.LogActivity(ctx, "job.recovered", "system", "", StatusUncertain, slog.Int64("job_id", id),
				slog.String("reason", "lease expired"))
		}
	}
	for {
		select {
		case sem <- struct{}{}:
		default:
			return
		}
		job, ok, err := w.store.ClaimNext(ctx, w.cfg.WorkerID, w.cfg.Lease)
		if err != nil || !ok {
			<-sem
			if err != nil && ctx.Err() == nil {
				w.logger.LogActivity(ctx, "queue.claim", "system", "", "error", slog.String("error", err.Error()))
			}
			return
		}
		wg.Add(1)
		go func(j Job) {
			defer wg.Done()
			defer func() { <-sem }()
			w.Process(jobCtx, j)
		}(job)
	}
}

// RunOnce claims and processes at most one job synchronously (tests, tooling).
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	if _, err := w.store.Dispatch(ctx); err != nil {
		return false, err
	}
	if _, err := w.store.RecoverExpired(ctx); err != nil {
		return false, err
	}
	job, ok, err := w.store.ClaimNext(ctx, w.cfg.WorkerID, w.cfg.Lease)
	if err != nil || !ok {
		return false, err
	}
	w.Process(ctx, job)
	return true, nil
}

// run carries per-job state through processing.
type run struct {
	job    Job
	ctx    context.Context // cancelled on lease loss or shutdown
	book   context.Context // bookkeeping: survives job ctx cancellation
	target *sql.DB
	lock   *DBLock
	cancel func() bool // reports a user cancel request (batch mode)
	start  time.Time
}

// Process executes one claimed job end to end. It never panics on job errors;
// every outcome is persisted as a job status, lifecycle attempt, event and log.
func (w *Worker) Process(parent context.Context, job Job) {
	carrier := map[string]string{}
	if job.TraceParent != "" {
		carrier[observability.TraceParentHeader] = job.TraceParent
	}
	if job.CorrelationID != "" {
		carrier[observability.CorrelationIDHeader] = job.CorrelationID
	}
	ctx := observability.ExtractTraceContext(parent, carrier)
	ctx, span := w.tracer.StartSpan(ctx, "job.process",
		slog.Int64("job_id", job.ID), slog.Int64("request_id", job.RequestID), slog.Int64("db_id", job.DBID),
		slog.String("mode", job.Mode), slog.String("op", job.Op), slog.String("action", job.Action),
		slog.Int("delivery", job.Deliveries))
	defer span.End()

	if job.Deliveries == 1 && job.Op == OpApply {
		w.metrics.ObserveQueueWait(time.Since(job.QueuedAt))
	}
	w.metrics.IncJobStatus(job.Status, 1)
	w.event(ctx, job, "job.claimed", job.Status, fmt.Sprintf("claimed by %s (delivery %d)", w.cfg.WorkerID, job.Deliveries), nil, nil)

	jobCtx, cancelJob := context.WithCancel(ctx)
	defer cancelJob()
	r := &run{job: job, ctx: jobCtx, book: context.WithoutCancel(ctx), start: time.Now()}
	var cancelFlag sync.Map
	r.cancel = func() bool { _, ok := cancelFlag.Load("cancel"); return ok || job.CancelRequested }

	// Heartbeat: renews the lease, propagates cancel requests, and fences the job
	// (cancels its context) if the lease or lock session is lost.
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		t := time.NewTicker(w.cfg.Lease / 3)
		defer t.Stop()
		for {
			select {
			case <-jobCtx.Done():
				return
			case <-t.C:
				ok, cancel, err := w.store.Heartbeat(r.book, job.ID, w.cfg.WorkerID, w.cfg.Lease)
				if err == nil && !ok {
					w.logger.LogActivity(ctx, "job.lease_lost", "system", dbLabel(job), "error", slog.Int64("job_id", job.ID))
					cancelJob()
					return
				}
				if cancel {
					cancelFlag.Store("cancel", true)
				}
				if r.lock != nil {
					pctx, pc := context.WithTimeout(r.book, 2*time.Second)
					if err := r.lock.Alive(pctx); err != nil {
						w.logger.LogActivity(ctx, "job.lock_lost", "system", dbLabel(job), "error", slog.Int64("job_id", job.ID))
						pc()
						cancelJob()
						return
					}
					pc()
				}
			}
		}
	}()
	defer func() { cancelJob(); <-hbDone }()

	if err := w.acquire(r); err != nil {
		span.RecordError(err)
		return
	}
	defer r.lock.Release()

	var err error
	if job.Op == OpCompensate {
		err = w.compensate(r)
	} else {
		err = w.apply(r)
	}
	if err != nil {
		span.RecordError(err)
	}
}

// acquire opens the target and takes the cross-worker lock. On failure the job
// is released back to the queue (nothing has executed yet).
func (w *Worker) acquire(r *run) error {
	ctx, span := w.tracer.StartSpan(r.ctx, "job.lock.acquire", slog.Int64("db_id", r.job.DBID))
	defer span.End()
	target, engine, err := w.targets.Open(ctx, r.job.DBID)
	if err == nil && engine != contract.EnginePostgres {
		err = fmt.Errorf("target engine %q is not supported for migrations: %w", engine, ErrInvalid)
	}
	if err != nil {
		span.RecordError(err)
		return w.notStarted(r, "target database unavailable: "+safeErr(err))
	}
	r.target = target
	lockStart := time.Now()
	lock, err := AcquireDBLock(ctx, w.meta, target, r.job.DBID, w.cfg.LockWait)
	w.metrics.ObserveLockWait(time.Since(lockStart))
	if err != nil {
		span.RecordError(err)
		if errors.Is(err, ErrLockBusy) {
			w.event(ctx, r.job, "job.lock_busy", r.job.Status, "database lock held by another worker; re-queued", nil, nil)
			return w.requeue(r, r.restoreStatus(), 2*time.Second, "")
		}
		return w.notStarted(r, "cannot acquire database lock: "+safeErr(err))
	}
	r.lock = lock
	w.logger.LogActivity(ctx, "job.lock.acquired", "system", dbLabel(r.job), "success",
		slog.Int64("job_id", r.job.ID), slog.Int64("wait_ms", time.Since(lockStart).Milliseconds()))
	if err := EnsureLedger(ctx, target); err != nil {
		span.RecordError(err)
		r.lock.Release()
		r.lock = nil
		return w.notStarted(r, "cannot prepare target ledger: "+safeErr(err))
	}
	return nil
}

// notStarted handles failures before any target statement ran.
func (w *Worker) notStarted(r *run, msg string) error {
	status := r.restoreStatus()
	// An uncertain job keeps waiting for verification; others are bounded.
	if status != StatusUncertain && r.job.Deliveries >= w.cfg.MaxDeliveries {
		req, err := w.domain.Get(r.book, r.job.RequestID)
		if err == nil && req.Status == lifecycle.Pending {
			_ = w.domain.ReleaseQueued(r.book, req.ID)
		}
		final := StatusFailed
		if r.job.BatchesDone > r.job.BatchesReverted {
			final = StatusPartial
		}
		return w.finish(r, final, msg+" (delivery limit reached)")
	}
	delay := Backoff(r.job.Deliveries, w.cfg.RetryBase, w.cfg.RetryMax)
	if status == StatusUncertain {
		delay = w.cfg.UncertainRecheck
	}
	return w.requeue(r, status, delay, msg)
}

func (w *Worker) requeue(r *run, status string, delay time.Duration, msg string) error {
	if err := w.store.Release(r.book, r.job.ID, w.cfg.WorkerID, status, delay, msg, false); err != nil {
		return err
	}
	if msg != "" {
		w.event(r.book, r.job, "job.requeued", status, msg, nil, nil)
	}
	w.metrics.IncJobStatus(status, 1)
	return errors.New(nonEmpty(msg, "requeued"))
}

func (w *Worker) finish(r *run, status, msg string) error {
	if err := w.store.Release(r.book, r.job.ID, w.cfg.WorkerID, status, 0, msg, false); err != nil {
		w.logger.LogActivity(r.book, "job.finish", "system", dbLabel(r.job), "error",
			slog.Int64("job_id", r.job.ID), slog.String("error", err.Error()))
		return err
	}
	w.metrics.IncJobStatus(status, 1)
	w.metrics.ObserveMigrationDuration(status, time.Since(r.start))
	w.event(r.book, r.job, "job."+status, status, msg, nil, nil)
	w.logger.LogActivity(r.book, "job.finished", "system", dbLabel(r.job), status,
		slog.Int64("job_id", r.job.ID), slog.Int64("request_id", r.job.RequestID),
		slog.String("mode", r.job.Mode), slog.Int64("duration_ms", time.Since(r.start).Milliseconds()))
	if status == StatusSuccess || status == StatusReverted {
		return nil
	}
	return errors.New(nonEmpty(msg, status))
}

// restoreStatus is the queue status to return a job to when nothing ran in
// this delivery: an uncertain job stays uncertain (it still needs verification);
// anything else becomes claimable again.
func (r *run) restoreStatus() string {
	switch r.job.ClaimedFrom {
	case StatusUncertain, StatusRetrying:
		return r.job.ClaimedFrom
	}
	return StatusQueued
}

// actor is the user recorded on attempts: who pressed Execute, else the requester.
func (r *run) actor(req lifecycle.Request) int64 {
	if r.job.EnqueuedBy != nil && *r.job.EnqueuedBy > 0 {
		return *r.job.EnqueuedBy
	}
	return req.RequestedBy
}

// apply runs an up / rollback / forward execution.
func (w *Worker) apply(r *run) error {
	req, err := w.domain.Get(r.book, r.job.RequestID)
	if err != nil {
		return w.notStarted(r, "cannot load migration request: "+safeErr(err))
	}
	recovering := false
	switch req.Status {
	case lifecycle.Pending:
		ctx, span := w.tracer.StartSpan(r.ctx, "job.claim_request", slog.Int64("request_id", req.ID))
		claimed, err := w.domain.Claim(ctx, req.ID)
		span.RecordError(err)
		span.End()
		if errors.Is(err, lifecycle.ErrConflict) || errors.Is(err, lifecycle.ErrForbidden) {
			// Revalidation at claim rejected the job (stale approval, lost
			// permission, linear order changed, database busy). Nothing ran.
			_ = w.domain.ReleaseQueued(r.book, req.ID)
			return w.finish(r, StatusRejected, "revalidation rejected execution before start: "+domainReason(err))
		}
		if err != nil {
			return w.notStarted(r, "cannot claim request: "+safeErr(err))
		}
		if !claimed {
			return w.syncFinished(r)
		}
	case lifecycle.Running:
		// A previous delivery started this request (crash, lease expiry, or
		// uncertain commit). Never re-execute blindly: verify the ledger first.
		recovering = true
	default:
		// Duplicate delivery of an already finished request: no execution.
		return w.syncFinished(r)
	}

	attempt, err := w.store.NextAttempt(r.book, req.ID)
	if err != nil {
		return w.requeue(r, StatusUncertain, w.cfg.UncertainRecheck, "cannot read attempt counter: "+safeErr(err))
	}
	spec, err := w.domain.LoadApprovedExecution(r.book, req.ID)
	if err != nil {
		return w.failAttempt(r, req, attempt, OutcomePermanent, "cannot load approved immutable SQL: "+domainReason(err))
	}
	plan, err := w.plan(r, req, spec)
	if err != nil {
		if recovering {
			return w.requeue(r, StatusUncertain, w.cfg.UncertainRecheck, "cannot rebuild batch plan for verification: "+safeErr(err))
		}
		return w.failAttempt(r, req, attempt, OutcomePermanent, safeErr(err))
	}

	if recovering {
		done, err := w.recover(r, req, attempt, plan)
		if done || err != nil {
			return err
		}
	}

	ctx, span := w.tracer.StartSpan(r.ctx, "job.revalidate", slog.Int64("request_id", req.ID))
	err = w.domain.Revalidate(ctx, req.ID)
	span.RecordError(err)
	span.End()
	if err != nil {
		return w.failAttempt(r, req, attempt, OutcomePermanent, "revalidation failed: "+domainReason(err))
	}

	if plan.mode == ModeNormal {
		return w.runNormal(r, req, spec, attempt)
	}
	return w.runBatch(r, req, spec, attempt, plan)
}

// syncFinished handles a delivery whose request is no longer pending/running.
func (w *Worker) syncFinished(r *run) error {
	req, err := w.domain.Get(r.book, r.job.RequestID)
	if err != nil {
		return w.notStarted(r, "cannot load migration request: "+safeErr(err))
	}
	w.event(r.book, r.job, "job.duplicate", req.Status, "duplicate delivery ignored; request already "+req.Status, nil, nil)
	switch req.Status {
	case lifecycle.Success:
		return w.finish(r, StatusSuccess, "")
	case lifecycle.Failed:
		final := StatusFailed
		if r.job.BatchesDone > r.job.BatchesReverted {
			final = StatusPartial
		}
		return w.finish(r, final, "")
	default:
		return w.finish(r, StatusRejected, "request is "+req.Status+"; nothing to execute")
	}
}

type batchPlan struct {
	mode    string
	cursor  CursorPlan
	size    int
	ranges  []LedgerRange // rollback over the source job's committed batches (reverse order)
	reverse bool
}

// plan resolves the execution mode. A rollback of a batch execution replays the
// source job's committed ranges in reverse; everything else uses the job config.
func (w *Worker) plan(r *run, req lifecycle.Request, spec lifecycle.ExecutionSpec) (batchPlan, error) {
	p := batchPlan{mode: r.job.Mode, size: r.job.BatchSize}
	if req.Action == lifecycle.Rollback && r.job.SourceJobID != nil {
		ranges, err := LedgerRanges(r.ctx, r.target, *r.job.SourceJobID)
		if err != nil {
			return p, err
		}
		active := []LedgerRange{}
		for i := len(ranges) - 1; i >= 0; i-- {
			if !ranges[i].Compensated {
				active = append(active, ranges[i])
			}
		}
		p.mode, p.ranges, p.reverse = ModeBatch, active, true
	} else if p.mode == ModeBatch {
		cp, err := ValidateCursor(r.ctx, r.target, r.job.BatchTable, r.job.BatchColumn)
		if err != nil {
			return p, err
		}
		p.cursor = cp
	}
	if err := CheckSQLForMode(p.mode, spec.SQL); err != nil {
		if p.mode == ModeBatch {
			return p, fmt.Errorf("batch SQL must use $1 (exclusive lower cursor) and $2 (inclusive upper cursor) and no other parameters: %w", ErrInvalid)
		}
		return p, fmt.Errorf("normal-mode SQL must not contain positional parameters; execute it in batch mode: %w", ErrInvalid)
	}
	return p, nil
}

// recover resolves an interrupted attempt from target evidence. done=true means
// the job reached a final state.
func (w *Worker) recover(r *run, req lifecycle.Request, attempt int, p batchPlan) (bool, error) {
	ctx, span := w.tracer.StartSpan(r.ctx, "job.recover", slog.Int64("job_id", r.job.ID), slog.String("mode", p.mode))
	defer span.End()
	w.logger.LogActivity(ctx, "job.recover", "system", dbLabel(r.job), "started",
		slog.Int64("job_id", r.job.ID), slog.Int64("request_id", req.ID), slog.Int("attempt", attempt))
	if p.mode == ModeNormal {
		committed, err := LedgerHas(ctx, r.target, r.job.ID, DirectionApply, 0)
		if err != nil {
			span.RecordError(err)
			return true, w.requeue(r, StatusUncertain, w.cfg.UncertainRecheck, "cannot verify commit outcome: "+safeErr(err))
		}
		if committed {
			w.event(ctx, r.job, "job.verified_committed", StatusRunning, "target ledger proves the previous attempt committed; no re-execution", &attempt, nil)
			return true, w.succeed(r, req, attempt, "")
		}
		w.event(ctx, r.job, "job.verified_not_committed", StatusRunning, "target ledger shows the previous attempt did not commit", &attempt, nil)
		return true, w.failAttempt(r, req, attempt, OutcomeTransient, "previous attempt interrupted; verified not committed")
	}
	ranges, err := LedgerRanges(ctx, r.target, r.job.ID)
	if err != nil {
		span.RecordError(err)
		return true, w.requeue(r, StatusUncertain, w.cfg.UncertainRecheck, "cannot read batch checkpoints: "+safeErr(err))
	}
	if err := w.store.SyncBatches(r.book, r.job.ID, ranges); err != nil {
		return true, w.requeue(r, StatusUncertain, w.cfg.UncertainRecheck, "cannot mirror batch checkpoints: "+safeErr(err))
	}
	r.job.BatchesDone = len(ranges)
	w.event(ctx, r.job, "job.verified_checkpoint", StatusRunning,
		fmt.Sprintf("previous attempt interrupted after %d committed batch(es); resuming from checkpoint", len(ranges)), &attempt, nil)
	return true, w.failAttempt(r, req, attempt, OutcomeTransient,
		fmt.Sprintf("previous attempt interrupted after %d committed batch(es)", len(ranges)))
}

// runNormal executes the whole statement in one target transaction together with
// its ledger row.
func (w *Worker) runNormal(r *run, req lifecycle.Request, spec lifecycle.ExecutionSpec, attempt int) error {
	ctx, span := w.tracer.StartSpan(r.ctx, "migration.execute", slog.String("mode", ModeNormal),
		slog.Int64("job_id", r.job.ID), slog.Int("attempt", attempt), slog.String("action", req.Action))
	defer span.End()
	ev := w.sqlEvent(r, req, spec.SQL, attempt, 0)
	committed, rows, outcome, err := w.execUnit(ctx, r, req, attempt, DirectionApply, 0, spec.SQL, nil, nil, ev)
	if err != nil {
		span.RecordError(err)
	}
	switch {
	case committed:
		w.metrics.AddProcessedRows(rows)
		return w.succeed(r, req, attempt, "")
	case outcome == OutcomeUncertain:
		return w.requeue(r, StatusUncertain, w.cfg.UncertainRecheck, "commit outcome uncertain and target unreachable for verification: "+safeErr(err))
	default:
		return w.failAttempt(r, req, attempt, outcome, safeErr(err))
	}
}

// execUnit runs one statement (whole migration or one batch) with its ledger row
// in a single target transaction. It returns committed=true when the change is
// known to be committed (including a duplicate whose ledger row already exists).
// On an uncertain error it verifies the ledger before returning.
func (w *Worker) execUnit(ctx context.Context, r *run, req lifecycle.Request, attempt int, direction string, seq int,
	sqlText string, from, to *int64, ev observability.SQLEvent) (committed bool, rows int64, outcome Outcome, err error) {
	if err := r.lock.Alive(ctx); err != nil {
		return false, 0, OutcomeTransient, fmt.Errorf("database lock lost before execution: %w", err)
	}
	tx, err := r.target.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, ClassifyBeforeWork(err), err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL lock_timeout = '%dms'", w.cfg.LockTimeout.Milliseconds())); err != nil {
		return false, 0, ClassifyBeforeWork(err), err
	}

	args := []any{}
	if from != nil && to != nil {
		args = append(args, *from, *to)
		ev.Params = args
	}
	ev.Query = sqlText
	w.logger.LogSQLStart(ctx, ev)
	start := time.Now()
	sqlCtx, sqlSpan := w.tracer.StartSpan(ctx, "sql.exec", slog.String("direction", direction), slog.Int("batch", seq))
	res, err := tx.ExecContext(sqlCtx, sqlText, args...)
	ev.Duration = time.Since(start)
	if err != nil {
		sqlSpan.RecordError(err)
		sqlSpan.End()
		ev.Err = err
		w.logger.LogSQLResult(ctx, ev)
		_ = tx.Rollback()
		ev.Err, ev.Status = nil, "rolled_back"
		w.logger.LogSQLRollback(ctx, ev)
		outcome = Classify(err)
		if outcome == OutcomeUncertain {
			return w.verify(ctx, r, direction, seq, err)
		}
		return false, 0, outcome, err
	}
	if n, e := res.RowsAffected(); e == nil {
		rows = n
	}
	sqlSpan.SetAttr("rows", rows)
	sqlSpan.End()
	ev.RowsAffected = &rows
	w.logger.LogSQLResult(ctx, ev)

	inserted, err := insertLedger(ctx, tx, LedgerEntry{JobID: r.job.ID, Direction: direction, BatchSeq: seq,
		CursorFrom: from, CursorTo: to, RowsAffected: rows, RequestID: req.ID, Attempt: attempt, WorkerID: w.cfg.WorkerID})
	if err != nil {
		_ = tx.Rollback()
		outcome = Classify(err)
		if outcome == OutcomeUncertain {
			return w.verify(ctx, r, direction, seq, err)
		}
		return false, 0, outcome, err
	}
	if !inserted {
		// Duplicate delivery: this unit already committed earlier. Roll back the
		// re-execution so the change is never applied twice.
		_ = tx.Rollback()
		ev.Status = "rolled_back"
		w.logger.LogSQLRollback(ctx, ev)
		w.event(ctx, r.job, "job.duplicate_unit", r.job.Status, "unit already committed according to target ledger; duplicate rolled back", &attempt, intPtr(seq))
		return true, 0, OutcomePermanent, nil
	}
	if f := w.faults.BeforeCommit; f != nil {
		if ferr := f(r.job, direction, seq); ferr != nil {
			_ = tx.Rollback()
			ev.Err, ev.Status = ferr, "rolled_back"
			w.logger.LogSQLRollback(ctx, ev)
			return false, 0, Classify(ferr), ferr
		}
	}
	commitCtx, commitSpan := w.tracer.StartSpan(ctx, "sql.commit", slog.Int("batch", seq))
	err = tx.Commit()
	if err == nil {
		if f := w.faults.AfterCommit; f != nil {
			err = f(r.job, direction, seq)
		}
	}
	if err != nil {
		commitSpan.RecordError(err)
		commitSpan.End()
		outcome = Classify(err)
		ev.Err = err
		w.logger.LogSQLCommit(commitCtx, ev)
		if outcome == OutcomeUncertain {
			return w.verify(ctx, r, direction, seq, err)
		}
		return false, 0, outcome, err
	}
	commitSpan.End()
	ev.Err, ev.Status = nil, "committed"
	w.logger.LogSQLCommit(ctx, ev)
	return true, rows, OutcomePermanent, nil
}

// verify resolves an uncertain commit from the target ledger. It never retries
// the statement itself.
func (w *Worker) verify(ctx context.Context, r *run, direction string, seq int, cause error) (bool, int64, Outcome, error) {
	vctx, span := w.tracer.StartSpan(r.book, "job.verify_commit", slog.String("direction", direction), slog.Int("batch", seq))
	defer span.End()
	w.event(vctx, r.job, "job.commit_uncertain", StatusUncertain, "commit outcome unknown ("+safeErr(cause)+"); verifying target ledger", nil, intPtr(seq))
	var lastErr error
	for i := 0; i < w.cfg.VerifyAttempts; i++ {
		qctx, cancel := context.WithTimeout(vctx, 5*time.Second)
		ok, err := LedgerHas(qctx, r.target, r.job.ID, direction, seq)
		cancel()
		if err == nil {
			if ok {
				w.event(vctx, r.job, "job.verified_committed", r.job.Status, "target ledger confirms the commit", nil, intPtr(seq))
				return true, 0, OutcomePermanent, nil
			}
			w.event(vctx, r.job, "job.verified_not_committed", r.job.Status, "target ledger confirms the transaction did not commit", nil, intPtr(seq))
			return false, 0, OutcomeTransient, fmt.Errorf("commit not applied (verified): %s", safeErr(cause))
		}
		lastErr = err
		time.Sleep(w.cfg.VerifyInterval)
	}
	span.RecordError(lastErr)
	return false, 0, OutcomeUncertain, fmt.Errorf("%s; verification failed: %s", safeErr(cause), safeErr(lastErr))
}

// runBatch processes the target in cursor batches, each in its own transaction
// committed together with its ledger checkpoint.
func (w *Worker) runBatch(r *run, req lifecycle.Request, spec lifecycle.ExecutionSpec, attempt int, p batchPlan) error {
	ctx, span := w.tracer.StartSpan(r.ctx, "migration.execute", slog.String("mode", ModeBatch),
		slog.Int64("job_id", r.job.ID), slog.Int("attempt", attempt), slog.String("action", req.Action), slog.Int("batch_size", p.size))
	defer span.End()

	done, err := LedgerRanges(ctx, r.target, r.job.ID)
	if err != nil {
		return w.requeue(r, StatusUncertain, w.cfg.UncertainRecheck, "cannot read batch checkpoints: "+safeErr(err))
	}
	if err := w.store.SyncBatches(r.book, r.job.ID, done); err != nil {
		return w.requeue(r, StatusUncertain, w.cfg.UncertainRecheck, "cannot mirror batch checkpoints: "+safeErr(err))
	}
	committedNet := 0
	nextSeq := 1
	for _, d := range done {
		if !d.Compensated {
			committedNet++
		}
		if d.Seq >= nextSeq {
			nextSeq = d.Seq + 1
		}
	}

	// Units still to process: either the source ranges (rollback) or cursor
	// ranges computed lazily from the scope fixed at the first run.
	if p.reverse {
		total := int64(0)
		for _, rg := range p.ranges {
			total += rg.Rows
		}
		if r.job.ScopeFrom == nil {
			from, to := int64(0), int64(0)
			if len(p.ranges) > 0 {
				from, to = p.ranges[len(p.ranges)-1].From, p.ranges[0].To
			}
			_ = w.store.SetScope(r.book, r.job.ID, from, to, total)
		}
		doneSeq := map[int]bool{}
		for _, d := range done {
			doneSeq[d.Seq] = true
		}
		for i, rg := range p.ranges {
			seq := i + 1
			if doneSeq[seq] {
				continue
			}
			if stop, err := w.batchGate(r, req, attempt, committedNet); stop {
				return err
			}
			from, to := rg.From, rg.To
			if err := w.oneBatch(ctx, r, req, spec.SQL, attempt, seq, from, to); err != nil {
				return w.batchFailed(r, req, attempt, seq, committedNet, err)
			}
			committedNet++
		}
		return w.succeed(r, req, attempt, "")
	}

	scopeFrom, scopeTo := int64(0), int64(0)
	if r.job.ScopeFrom != nil && r.job.ScopeTo != nil {
		scopeFrom, scopeTo = *r.job.ScopeFrom, *r.job.ScopeTo
	} else {
		var minV, maxV sql.NullInt64
		var count int64
		if err := r.target.QueryRowContext(ctx, p.cursor.scopeQuery()).Scan(&minV, &maxV, &count); err != nil {
			return w.failAttempt(r, req, attempt, Classify(err), "cannot determine batch scope: "+safeErr(err))
		}
		if minV.Valid && maxV.Valid {
			scopeFrom, scopeTo = minV.Int64-1, maxV.Int64
		}
		if err := w.store.SetScope(r.book, r.job.ID, scopeFrom, scopeTo, count); err != nil {
			return w.requeue(r, StatusUncertain, w.cfg.UncertainRecheck, "cannot persist batch scope: "+safeErr(err))
		}
		r.job.ScopeFrom, r.job.ScopeTo = &scopeFrom, &scopeTo
		w.logger.LogActivity(ctx, "batch.scope", "system", dbLabel(r.job), "success", slog.Int64("job_id", r.job.ID),
			slog.Int64("scope_from", scopeFrom), slog.Int64("scope_to", scopeTo), slog.Int64("total_rows", count))
	}
	// Resume point: end of the committed, non-compensated prefix.
	cursor := scopeFrom
	for _, d := range done {
		if !d.Compensated && d.To > cursor {
			cursor = d.To
		}
	}
	for cursor < scopeTo {
		if stop, err := w.batchGate(r, req, attempt, committedNet); stop {
			return err
		}
		var hi sql.NullInt64
		if err := r.target.QueryRowContext(ctx, p.cursor.nextBoundQuery(), cursor, scopeTo, p.size).Scan(&hi); err != nil {
			return w.batchFailed(r, req, attempt, nextSeq, committedNet, err)
		}
		if !hi.Valid {
			break
		}
		if err := w.oneBatch(ctx, r, req, spec.SQL, attempt, nextSeq, cursor, hi.Int64); err != nil {
			return w.batchFailed(r, req, attempt, nextSeq, committedNet, err)
		}
		committedNet++
		cursor = hi.Int64
		nextSeq++
	}
	return w.succeed(r, req, attempt, "")
}

// batchGate stops between batches on cancel, shutdown/lease loss.
func (w *Worker) batchGate(r *run, req lifecycle.Request, attempt, committedNet int) (bool, error) {
	if r.ctx.Err() != nil {
		// Interrupted between batches: committed batches are safe in the ledger.
		return true, w.requeue(r, StatusUncertain, 0, "worker interrupted between batches; another delivery resumes from checkpoint")
	}
	if r.cancel() {
		msg := fmt.Sprintf("cancelled by user after %d committed batch(es); committed batches are kept", committedNet)
		execID, err := w.domain.RecordAttempt(r.book, req.ID, r.actor(req), lifecycle.Failed, attempt, msg)
		if err != nil {
			return true, w.requeue(r, StatusUncertain, w.cfg.UncertainRecheck, "cannot record cancelled attempt: "+safeErr(err))
		}
		w.logAttempt(r, req, execID, attempt, lifecycle.Failed, msg)
		status := StatusCancelled
		if committedNet > 0 {
			status = StatusPartial
		}
		return true, w.finish(r, status, msg)
	}
	return false, nil
}

// oneBatch executes and mirrors a single batch.
func (w *Worker) oneBatch(ctx context.Context, r *run, req lifecycle.Request, sqlText string, attempt, seq int, from, to int64) error {
	bctx, span := w.tracer.StartSpan(ctx, "migration.batch", slog.Int("batch", seq),
		slog.Int64("cursor_from", from), slog.Int64("cursor_to", to))
	defer span.End()
	ev := w.sqlEvent(r, req, sqlText, attempt, seq)
	committed, rows, outcome, err := w.execUnit(bctx, r, req, attempt, DirectionApply, seq, sqlText, &from, &to, ev)
	if !committed {
		span.RecordError(err)
		return &unitError{outcome: outcome, err: err}
	}
	if err := w.store.RecordBatch(r.book, r.job.ID, seq, from, to, rows); err != nil {
		// The batch is committed (ledger); the mirror is re-synced on next run.
		w.logger.LogActivity(bctx, "batch.mirror", "system", dbLabel(r.job), "error", slog.Int64("job_id", r.job.ID), slog.Int("batch", seq))
	}
	w.metrics.AddProcessedBatches(1)
	w.metrics.AddProcessedRows(rows)
	w.logger.LogActivity(bctx, "batch.committed", "system", dbLabel(r.job), "committed",
		slog.Int64("job_id", r.job.ID), slog.Int("batch", seq), slog.Int("attempt", attempt),
		slog.Int64("cursor_from", from), slog.Int64("cursor_to", to), slog.Int64("rows", rows))
	if f := w.faults.AfterBatch; f != nil {
		if err := f(r.job, seq); err != nil {
			return &unitError{outcome: OutcomeUncertain, err: errCrash}
		}
	}
	return nil
}

type unitError struct {
	outcome Outcome
	err     error
}

func (e *unitError) Error() string { return safeErr(e.err) }
func (e *unitError) Unwrap() error { return e.err }

func (w *Worker) batchFailed(r *run, req lifecycle.Request, attempt, seq, committedNet int, err error) error {
	var ue *unitError
	outcome := Classify(err)
	if errors.As(err, &ue) {
		outcome = ue.outcome
		if errors.Is(ue.err, errCrash) {
			// Simulated crash: abandon the job with its lease (no bookkeeping).
			return errCrash
		}
	}
	if outcome == OutcomeUncertain {
		return w.requeue(r, StatusUncertain, w.cfg.UncertainRecheck,
			fmt.Sprintf("batch %d commit outcome uncertain; target unreachable for verification", seq))
	}
	return w.failAttemptBatch(r, req, attempt, outcome, fmt.Sprintf("batch %d failed and was rolled back: %s", seq, safeErr(err)), committedNet)
}

func (w *Worker) succeed(r *run, req lifecycle.Request, attempt int, msg string) error {
	execID, err := w.domain.RecordAttempt(r.book, req.ID, r.actor(req), lifecycle.Success, attempt, "")
	if err != nil {
		// The target committed but metadata is unreachable: keep the job
		// uncertain; the next delivery proves the commit via the ledger.
		return w.requeue(r, StatusUncertain, w.cfg.UncertainRecheck, "committed, but recording the attempt failed: "+safeErr(err))
	}
	w.logAttempt(r, req, execID, attempt, lifecycle.Success, "")
	return w.finish(r, StatusSuccess, msg)
}

func (w *Worker) failAttempt(r *run, req lifecycle.Request, attempt int, outcome Outcome, msg string) error {
	net := r.job.BatchesDone - r.job.BatchesReverted
	return w.failAttemptBatch(r, req, attempt, outcome, msg, net)
}

// failAttemptBatch records a failed attempt (append-only history) and either
// schedules a bounded retry with backoff or finalizes failed / partial.
func (w *Worker) failAttemptBatch(r *run, req lifecycle.Request, attempt int, outcome Outcome, msg string, committedNet int) error {
	msg = migration.SanitizeErrorText(msg)
	execID, err := w.domain.RecordAttempt(r.book, req.ID, r.actor(req), lifecycle.Failed, attempt, msg)
	if err != nil {
		return w.requeue(r, StatusUncertain, w.cfg.UncertainRecheck, "cannot record failed attempt: "+safeErr(err))
	}
	w.logAttempt(r, req, execID, attempt, lifecycle.Failed, msg)
	final := StatusFailed
	if committedNet > 0 {
		final = StatusPartial
	}
	if outcome != OutcomeTransient || attempt >= r.job.MaxAttempts {
		if outcome == OutcomeTransient {
			msg += fmt.Sprintf(" (retry limit %d reached)", r.job.MaxAttempts)
		}
		return w.finish(r, final, msg)
	}
	// Same entry/revision: lifecycle Retry keeps the approval (QnA); the worker
	// still revalidates approval and permission before the next attempt.
	if _, err := w.domain.Retry(r.book, req.ID); err != nil {
		return w.finish(r, final, msg+"; automatic retry not possible: "+domainReason(err))
	}
	delay := Backoff(attempt, w.cfg.RetryBase, w.cfg.RetryMax)
	if err := w.store.Release(r.book, r.job.ID, w.cfg.WorkerID, StatusRetrying, delay, msg, true); err != nil {
		return err
	}
	w.metrics.IncJobRetry()
	w.metrics.IncJobStatus(StatusRetrying, 1)
	_, span := w.tracer.StartSpan(r.ctx, "job.retry", slog.Int("attempt", attempt), slog.Int64("backoff_ms", delay.Milliseconds()))
	span.End()
	w.event(r.book, r.job, "job.retry_scheduled", StatusRetrying,
		fmt.Sprintf("transient failure; retry %d/%d in %s: %s", attempt+1, r.job.MaxAttempts, delay, msg), &attempt, nil)
	return errors.New(msg)
}

// compensate reverts committed batches of a partial job in reverse order using
// the approved immutable inverse SQL of the same revision.
func (w *Worker) compensate(r *run) error {
	ctx, span := w.tracer.StartSpan(r.ctx, "job.compensate", slog.Int64("job_id", r.job.ID))
	defer span.End()
	req, err := w.domain.Get(r.book, r.job.RequestID)
	if err != nil {
		return w.notStarted(r, "cannot load migration request: "+safeErr(err))
	}
	restore := StatusPartial
	if r.job.BatchesReverted > 0 {
		restore = StatusPartiallyReverted
	}
	if req.Status != lifecycle.Failed {
		return w.finish(r, restore, "compensation requires a failed/partial execution; request is "+req.Status)
	}
	if err := w.domain.RevalidateApproval(ctx, req.ID); err != nil {
		return w.finish(r, restore, "compensation rejected by revalidation: "+domainReason(err))
	}
	def, err := w.domain.RevisionDetails(r.book, req.ID)
	if err != nil {
		return w.finish(r, restore, "cannot load approved SQL: "+domainReason(err))
	}
	inverse := def.SQLDown
	if req.Action == lifecycle.Rollback {
		inverse = def.SQLUp
	}
	if strings.TrimSpace(inverse) == "" {
		return w.finish(r, restore, "migration is irreversible; compensation is unsafe — create a corrective migration")
	}
	if err := CheckSQLForMode(ModeBatch, inverse); err != nil {
		return w.finish(r, restore, "inverse SQL does not follow the batch $1/$2 contract; compensation is unsafe — create a corrective migration")
	}
	ranges, err := LedgerRanges(ctx, r.target, r.job.ID)
	if err != nil {
		return w.requeue(r, StatusQueued, w.cfg.UncertainRecheck, "cannot read batch checkpoints: "+safeErr(err))
	}
	if err := w.store.SyncBatches(r.book, r.job.ID, ranges); err != nil {
		return w.requeue(r, StatusQueued, w.cfg.UncertainRecheck, "cannot mirror batch checkpoints: "+safeErr(err))
	}
	reverted := 0
	for _, rg := range ranges {
		if rg.Compensated {
			reverted++
		}
	}
	attempt := 0
	for i := len(ranges) - 1; i >= 0; i-- {
		rg := ranges[i]
		if rg.Compensated {
			continue
		}
		if r.ctx.Err() != nil {
			return w.requeue(r, StatusQueued, 0, "worker interrupted during compensation; resumes from checkpoint")
		}
		from, to := rg.From, rg.To
		bctx, bspan := w.tracer.StartSpan(ctx, "migration.compensate_batch", slog.Int("batch", rg.Seq))
		ev := w.sqlEvent(r, req, inverse, attempt, rg.Seq)
		ev.Event = "sql.compensate"
		committed, rows, outcome, err := w.execUnit(bctx, r, req, attempt, DirectionCompensate, rg.Seq, inverse, &from, &to, ev)
		bspan.End()
		if !committed {
			if outcome == OutcomeUncertain {
				return w.requeue(r, StatusQueued, w.cfg.UncertainRecheck, fmt.Sprintf("compensation of batch %d uncertain; will verify", rg.Seq))
			}
			status := StatusPartial
			if reverted > 0 {
				status = StatusPartiallyReverted
			}
			return w.finish(r, status, fmt.Sprintf("compensation of batch %d failed: %s; %d batch(es) reverted, resume compensation to continue", rg.Seq, safeErr(err), reverted))
		}
		if err := w.store.RecordRevert(r.book, r.job.ID, rg.Seq, rows); err != nil {
			w.logger.LogActivity(bctx, "batch.mirror", "system", dbLabel(r.job), "error", slog.Int64("job_id", r.job.ID))
		}
		reverted++
		w.metrics.AddProcessedBatches(1)
		w.metrics.AddProcessedRows(rows)
		w.logger.LogActivity(bctx, "batch.compensated", "system", dbLabel(r.job), "committed",
			slog.Int64("job_id", r.job.ID), slog.Int("batch", rg.Seq), slog.Int64("rows", rows))
	}
	return w.finish(r, StatusReverted, fmt.Sprintf("all %d committed batch(es) compensated in reverse order", reverted))
}

func (w *Worker) sqlEvent(r *run, req lifecycle.Request, sqlText string, attempt, seq int) observability.SQLEvent {
	return observability.SQLEvent{
		Actor:       fmt.Sprintf("user:%d", r.actor(req)),
		Database:    dbLabel(r.job),
		Query:       sqlText,
		MigrationID: req.MigrationID,
		JobID:       fmt.Sprintf("%d", r.job.ID),
		Version:     req.RevisionID,
		Attempt:     attempt,
		Batch:       seq,
	}
}

func (w *Worker) logAttempt(r *run, req lifecycle.Request, execID int64, attempt int, status, msg string) {
	ev := w.sqlEvent(r, req, "", attempt, 0)
	ev.ExecutionID = execID
	ev.Status = status
	if msg != "" {
		ev.Err = errors.New(msg)
	}
	if spec, err := w.domain.LoadApprovedExecution(r.book, req.ID); err == nil {
		ev.Query = spec.SQL
	}
	ev.Duration = time.Since(r.start)
	w.logger.LogMigrationAttempt(r.book, ev)
}

func (w *Worker) event(ctx context.Context, job Job, event, status, msg string, attempt, batch *int) {
	if err := w.store.AddEvent(context.WithoutCancel(ctx), job.ID, event, status, migration.SanitizeErrorText(msg), w.cfg.WorkerID, attempt, batch); err != nil {
		w.logger.LogActivity(ctx, "job.event", "system", dbLabel(job), "error", slog.Int64("job_id", job.ID), slog.String("event", event))
	}
}

func dbLabel(j Job) string {
	if j.DBName != "" {
		return j.DBName
	}
	return fmt.Sprintf("db:%d", j.DBID)
}

func safeErr(err error) string {
	if err == nil {
		return ""
	}
	return migration.SanitizeErrorText(err.Error())
}

// domainReason maps lifecycle errors to a stable, safe explanation.
func domainReason(err error) string {
	switch {
	case errors.Is(err, lifecycle.ErrForbidden):
		return "requester no longer has View + Edit permission"
	case errors.Is(err, lifecycle.ErrConflict):
		return "approval, stakeholder snapshot, database availability or linear order is no longer valid"
	case errors.Is(err, lifecycle.ErrNotFound):
		return "migration request or revision not found"
	case errors.Is(err, lifecycle.ErrInvalid):
		return "invalid migration request"
	default:
		return safeErr(err)
	}
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func intPtr(v int) *int { return &v }
