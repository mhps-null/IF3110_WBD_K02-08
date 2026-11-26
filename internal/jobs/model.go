// Package jobs is the asynchronous migration execution engine (Orang 4).
//
// The API publishes a durable job (PostgreSQL-backed queue in the metadata
// database, brokerless per QnA) and returns its ID immediately. Separate worker
// processes (cmd/worker) claim jobs with a lease, take a cross-worker lock per
// target database, revalidate through the lifecycle domain (Orang 3), execute the
// immutable approved SQL in normal or batch mode, and report every attempt back to
// the lifecycle history. Each committed change is paired, in the same target
// transaction, with a row in the target-side ledger (_dbplatform.migration_ledger),
// which is the evidence used for idempotency, resume, and uncertain-commit recovery.
package jobs

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// Job statuses. Values are bounded and safe to use as metric labels.
const (
	StatusQueued            = "queued"
	StatusRetrying          = "retrying"
	StatusRunning           = "running"
	StatusCompensating      = "compensating"
	StatusUncertain         = "uncertain"
	StatusSuccess           = "success"
	StatusFailed            = "failed"
	StatusPartial           = "partial"
	StatusPartiallyReverted = "partially_reverted"
	StatusReverted          = "reverted"
	StatusCancelled         = "cancelled"
	StatusRejected          = "rejected"
)

// AllStatuses lists every job status, used for metrics gauges and API validation.
var AllStatuses = []string{
	StatusQueued, StatusRetrying, StatusRunning, StatusCompensating, StatusUncertain,
	StatusSuccess, StatusFailed, StatusPartial, StatusPartiallyReverted, StatusReverted,
	StatusCancelled, StatusRejected,
}

const (
	ModeNormal = "normal"
	ModeBatch  = "batch"

	OpApply      = "apply"
	OpCompensate = "compensate"

	DirectionApply      = "apply"
	DirectionCompensate = "compensate"
)

const (
	DefaultBatchSize   = 10000
	MaxBatchSize       = 1000000
	DefaultMaxAttempts = 3
)

var (
	ErrInvalid   = errors.New("invalid job request")
	ErrConflict  = errors.New("job state conflict")
	ErrForbidden = errors.New("permission denied")
	ErrNotFound  = errors.New("job not found")
)

// Job is the queue record plus progress counters shown in the SPA.
type Job struct {
	ID              int64      `json:"id"`
	RequestID       int64      `json:"request_id"`
	DBID            int64      `json:"db_id"`
	DBName          string     `json:"db_name,omitempty"`
	MigrationID     int64      `json:"migration_id"`
	RevisionID      int64      `json:"revision_id"`
	Title           string     `json:"title"`
	Action          string     `json:"action"`
	RequestStatus   string     `json:"request_status"`
	Mode            string     `json:"mode"`
	Op              string     `json:"op"`
	Status          string     `json:"status"`
	BatchTable      string     `json:"batch_table,omitempty"`
	BatchColumn     string     `json:"batch_column,omitempty"`
	BatchSize       int        `json:"batch_size,omitempty"`
	SourceJobID     *int64     `json:"source_job_id,omitempty"`
	ScopeFrom       *int64     `json:"scope_from,omitempty"`
	ScopeTo         *int64     `json:"scope_to,omitempty"`
	TotalRows       *int64     `json:"total_rows,omitempty"`
	RowsDone        int64      `json:"rows_done"`
	BatchesDone     int        `json:"batches_done"`
	RowsReverted    int64      `json:"rows_reverted"`
	BatchesReverted int        `json:"batches_reverted"`
	LastCursor      *int64     `json:"last_cursor,omitempty"`
	MaxAttempts     int        `json:"max_attempts"`
	RetryCount      int        `json:"retry_count"`
	Attempts        int        `json:"attempts"`
	Deliveries      int        `json:"deliveries"`
	Recoveries      int        `json:"recoveries"`
	NextRunAt       time.Time  `json:"next_run_at"`
	LeaseOwner      string     `json:"lease_owner,omitempty"`
	LeaseExpiresAt  *time.Time `json:"lease_expires_at,omitempty"`
	HeartbeatAt     *time.Time `json:"heartbeat_at,omitempty"`
	CancelRequested bool       `json:"cancel_requested"`
	LastError       string     `json:"last_error,omitempty"`
	Held            bool       `json:"held"`
	TraceParent     string     `json:"-"`
	ClaimedFrom     string     `json:"-"` // status before this worker claimed the job
	CorrelationID   string     `json:"correlation_id,omitempty"`
	EnqueuedBy      *int64     `json:"enqueued_by,omitempty"`
	QueuedAt        time.Time  `json:"queued_at"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// Batch is one committed (or reverted) batch range: (CursorFrom, CursorTo].
type Batch struct {
	Seq          int        `json:"batch_seq"`
	CursorFrom   int64      `json:"cursor_from"`
	CursorTo     int64      `json:"cursor_to"`
	RowsAffected int64      `json:"rows_affected"`
	Status       string     `json:"status"`
	CommittedAt  time.Time  `json:"committed_at"`
	RevertedAt   *time.Time `json:"reverted_at,omitempty"`
}

// Event is an append-only operational timeline entry.
type Event struct {
	ID        int64     `json:"id"`
	Event     string    `json:"event"`
	Status    string    `json:"status"`
	Message   string    `json:"message,omitempty"`
	WorkerID  string    `json:"worker_id,omitempty"`
	Attempt   *int      `json:"attempt,omitempty"`
	BatchSeq  *int      `json:"batch_seq,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ExecuteOptions is the body of the Execute endpoint.
type ExecuteOptions struct {
	Mode        string `json:"mode"`
	BatchTable  string `json:"batch_table"`
	BatchColumn string `json:"batch_column"`
	BatchSize   int    `json:"batch_size"`
	MaxAttempts int    `json:"max_attempts"`
}

// identifierPattern accepts plain or schema-qualified PostgreSQL identifiers.
// Identifiers are additionally quoted with pgx.Identifier before use.
var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

// SplitQualified validates and splits "table" or "schema.table".
func SplitQualified(name string) ([]string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrInvalid
	}
	parts := strings.Split(name, ".")
	if len(parts) > 2 {
		return nil, ErrInvalid
	}
	for _, p := range parts {
		if !identifierPattern.MatchString(p) {
			return nil, ErrInvalid
		}
	}
	return parts, nil
}

// ValidIdentifier reports whether a single column/table identifier is acceptable.
func ValidIdentifier(name string) bool { return identifierPattern.MatchString(name) }

// Normalize validates options and fills defaults. Rollback mode is derived later
// from the execution being rolled back, so it is validated separately.
func (o ExecuteOptions) Normalize() (ExecuteOptions, error) {
	o.Mode = strings.ToLower(strings.TrimSpace(o.Mode))
	if o.Mode == "" {
		o.Mode = ModeNormal
	}
	if o.MaxAttempts == 0 {
		o.MaxAttempts = DefaultMaxAttempts
	}
	if o.MaxAttempts < 1 || o.MaxAttempts > 10 {
		return o, ErrInvalid
	}
	switch o.Mode {
	case ModeNormal:
		o.BatchTable, o.BatchColumn, o.BatchSize = "", "", 0
	case ModeBatch:
		o.BatchTable = strings.TrimSpace(o.BatchTable)
		o.BatchColumn = strings.TrimSpace(o.BatchColumn)
		if _, err := SplitQualified(o.BatchTable); err != nil {
			return o, ErrInvalid
		}
		if !ValidIdentifier(o.BatchColumn) {
			return o, ErrInvalid
		}
		if o.BatchSize == 0 {
			o.BatchSize = DefaultBatchSize
		}
		if o.BatchSize < 1 || o.BatchSize > MaxBatchSize {
			return o, ErrInvalid
		}
	default:
		return o, ErrInvalid
	}
	return o, nil
}

var (
	placeholder1 = regexp.MustCompile(`\$1\b`)
	placeholder2 = regexp.MustCompile(`\$2\b`)
	anyParam     = regexp.MustCompile(`\$[0-9]+\b`)
)

// CheckSQLForMode enforces the batch SQL contract:
//   - batch SQL must reference $1 (exclusive lower cursor) and $2 (inclusive upper
//     cursor) and no other positional parameter;
//   - normal SQL must not contain positional parameters.
func CheckSQLForMode(mode, sqlText string) error {
	if strings.TrimSpace(sqlText) == "" {
		return ErrInvalid
	}
	switch mode {
	case ModeBatch:
		if !placeholder1.MatchString(sqlText) || !placeholder2.MatchString(sqlText) {
			return ErrInvalid
		}
		for _, m := range anyParam.FindAllString(sqlText, -1) {
			if m != "$1" && m != "$2" {
				return ErrInvalid
			}
		}
	case ModeNormal:
		if anyParam.MatchString(sqlText) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

// Terminal reports whether no worker will pick the job up without user action.
func Terminal(status string) bool {
	switch status {
	case StatusSuccess, StatusFailed, StatusPartial, StatusPartiallyReverted,
		StatusReverted, StatusCancelled, StatusRejected:
		return true
	}
	return false
}

// HoldReason returns the database hold reason for a status, or "" if the status
// does not hold the database for other migrations.
func HoldReason(status string) string {
	switch status {
	case StatusPartial, StatusPartiallyReverted, StatusCompensating, StatusUncertain:
		return status
	}
	return ""
}
