package jobs

import (
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Outcome classifies how a target-database error must be handled.
type Outcome int

const (
	// OutcomePermanent: PostgreSQL rejected the statement (syntax, constraint,
	// permission, ...). The transaction was rolled back; retrying cannot help.
	OutcomePermanent Outcome = iota
	// OutcomeTransient: the transaction definitely did not commit and the cause is
	// temporary (serialization failure, deadlock, lock timeout, shutdown, ...).
	OutcomeTransient
	// OutcomeUncertain: the connection failed while a transaction was in flight
	// or during COMMIT, so the commit outcome is unknown and MUST be verified
	// against the target ledger before any retry.
	OutcomeUncertain
)

func (o Outcome) String() string {
	switch o {
	case OutcomePermanent:
		return "permanent"
	case OutcomeTransient:
		return "transient"
	default:
		return "uncertain"
	}
}

// transientSQLStates are SQLSTATEs reported by a live server for which the
// transaction is known to have been rolled back and a retry may succeed.
var transientSQLStates = map[string]bool{
	"40001": true, // serialization_failure
	"40P01": true, // deadlock_detected
	"55P03": true, // lock_not_available (lock_timeout)
	"57014": true, // query_canceled (statement_timeout)
	"53000": true, // insufficient_resources
	"53100": true, // disk_full
	"53200": true, // out_of_memory
	"53300": true, // too_many_connections
	"57P03": true, // cannot_connect_now
}

// connectionLostSQLStates mean the server/connection went away; any in-flight
// transaction outcome is uncertain from the client's perspective.
var connectionLostSQLStates = map[string]bool{
	"57P01": true, // admin_shutdown
	"57P02": true, // crash_shutdown
	"08000": true, "08003": true, "08006": true, "08001": true, "08004": true, "08007": true,
}

// SQLState extracts a PostgreSQL SQLSTATE from an error chain, if present.
func SQLState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// Classify maps an error raised after BEGIN on the target database (during a
// statement or COMMIT). A live server error with SQLSTATE means the transaction
// was rolled back; a lost connection means the outcome is unknown.
func Classify(err error) Outcome {
	if err == nil {
		return OutcomePermanent
	}
	if state := SQLState(err); state != "" {
		switch {
		case connectionLostSQLStates[state]:
			return OutcomeUncertain
		case transientSQLStates[state]:
			return OutcomeTransient
		default:
			return OutcomePermanent
		}
	}
	// No SQLSTATE: network/driver/context error (EOF, reset, timeout, cancelled
	// context). The server may have committed before the connection dropped, so the
	// outcome is treated conservatively as uncertain and verified via the ledger.
	return OutcomeUncertain
}

// ClassifyBeforeWork maps an error raised before any statement was sent in the
// transaction (BEGIN, connection acquisition): nothing can have committed.
func ClassifyBeforeWork(err error) Outcome {
	if state := SQLState(err); state != "" && !transientSQLStates[state] && !connectionLostSQLStates[state] {
		return OutcomePermanent
	}
	return OutcomeTransient
}

// Backoff returns the bounded exponential delay before retry number n (1-based):
// base, 2*base, 4*base, ... capped at max.
func Backoff(n int, base, max time.Duration) time.Duration {
	if n < 1 {
		n = 1
	}
	if base <= 0 {
		base = time.Second
	}
	d := base
	for i := 1; i < n; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	if max > 0 && d > max {
		return max
	}
	return d
}
