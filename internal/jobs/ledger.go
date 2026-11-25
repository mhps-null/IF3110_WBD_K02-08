package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// The ledger lives in each TARGET database. A ledger row is inserted in the same
// transaction as the migration statement / batch it describes, so its presence is
// proof that the change committed, and its primary key makes a duplicate delivery
// of the same job/batch unable to commit twice.
const ledgerSchema = "_dbplatform"

var ledgerDDL = []string{
	`CREATE SCHEMA IF NOT EXISTS _dbplatform`,
	`CREATE TABLE IF NOT EXISTS _dbplatform.migration_ledger (
		job_id        BIGINT  NOT NULL,
		direction     TEXT    NOT NULL CHECK (direction IN ('apply', 'compensate')),
		batch_seq     INTEGER NOT NULL CHECK (batch_seq >= 0),
		cursor_from   BIGINT,
		cursor_to     BIGINT,
		rows_affected BIGINT  NOT NULL DEFAULT 0,
		request_id    BIGINT  NOT NULL,
		attempt       INTEGER NOT NULL,
		worker_id     TEXT    NOT NULL,
		committed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (job_id, direction, batch_seq)
	)`,
	`COMMENT ON TABLE _dbplatform.migration_ledger IS 'Managed by the IF3110 database platform worker. Do not modify.'`,
}

// LedgerEntry is one committed unit of work. BatchSeq 0 means a normal-mode
// (non-batched) execution of the whole statement.
type LedgerEntry struct {
	JobID        int64
	Direction    string
	BatchSeq     int
	CursorFrom   *int64
	CursorTo     *int64
	RowsAffected int64
	RequestID    int64
	Attempt      int
	WorkerID     string
}

// EnsureLedger creates the ledger schema/table if needed. Callers hold the target
// database lock, so concurrent DDL from two workers cannot race on one database.
func EnsureLedger(ctx context.Context, db *sql.DB) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('_dbplatform.migration_ledger') IS NOT NULL`).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	for _, stmt := range ledgerDDL {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			// 42P06 duplicate_schema / 42P07 duplicate_table / 23505 catalog race.
			if s := SQLState(err); s == "42P06" || s == "42P07" || s == "23505" {
				continue
			}
			return fmt.Errorf("prepare migration ledger: %w", err)
		}
	}
	return nil
}

// insertLedger records an entry inside the caller's transaction. It returns false
// when the same (job, direction, batch) already committed, i.e. a duplicate.
func insertLedger(ctx context.Context, tx *sql.Tx, e LedgerEntry) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, `INSERT INTO _dbplatform.migration_ledger
		(job_id,direction,batch_seq,cursor_from,cursor_to,rows_affected,request_id,attempt,worker_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (job_id,direction,batch_seq) DO NOTHING RETURNING 1`,
		e.JobID, e.Direction, e.BatchSeq, e.CursorFrom, e.CursorTo, e.RowsAffected, e.RequestID, e.Attempt, e.WorkerID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// LedgerHas reports whether an entry committed (used to verify uncertain commits).
func LedgerHas(ctx context.Context, db *sql.DB, jobID int64, direction string, seq int) (bool, error) {
	var ok bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM _dbplatform.migration_ledger
		WHERE job_id=$1 AND direction=$2 AND batch_seq=$3)`, jobID, direction, seq).Scan(&ok)
	return ok, err
}

// LedgerRange is a committed batch range and whether it has been compensated.
type LedgerRange struct {
	Seq          int
	From, To     int64
	Rows         int64
	Compensated  bool
	RevertedRows int64
}

// LedgerRanges returns the committed apply batches of a job (seq ascending) with
// their compensation state, straight from the target database.
func LedgerRanges(ctx context.Context, db *sql.DB, jobID int64) ([]LedgerRange, error) {
	rows, err := db.QueryContext(ctx, `SELECT a.batch_seq,a.cursor_from,a.cursor_to,a.rows_affected,
			c.batch_seq IS NOT NULL, COALESCE(c.rows_affected,0)
		FROM _dbplatform.migration_ledger a
		LEFT JOIN _dbplatform.migration_ledger c
		  ON c.job_id=a.job_id AND c.direction='compensate' AND c.batch_seq=a.batch_seq
		WHERE a.job_id=$1 AND a.direction='apply' AND a.batch_seq > 0
		ORDER BY a.batch_seq`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LedgerRange{}
	for rows.Next() {
		var r LedgerRange
		var from, to sql.NullInt64
		if err := rows.Scan(&r.Seq, &from, &to, &r.Rows, &r.Compensated, &r.RevertedRows); err != nil {
			return nil, err
		}
		if !from.Valid || !to.Valid {
			return nil, fmt.Errorf("ledger batch %d of job %d has no cursor range", r.Seq, jobID)
		}
		r.From, r.To = from.Int64, to.Int64
		out = append(out, r)
	}
	return out, rows.Err()
}

// quoteQualified quotes a validated "table" or "schema.table" identifier.
func quoteQualified(name string) (string, error) {
	parts, err := SplitQualified(name)
	if err != nil {
		return "", err
	}
	return pgx.Identifier(parts).Sanitize(), nil
}

func quoteIdent(name string) (string, error) {
	if !ValidIdentifier(name) {
		return "", ErrInvalid
	}
	return pgx.Identifier{name}.Sanitize(), nil
}

// CursorPlan describes a validated batch cursor over an integer unique column.
type CursorPlan struct {
	Table  string // quoted
	Column string // quoted
}

// ValidateCursor checks that the batch table exists, the cursor column is an
// integer type, and the column alone is covered by a unique index (PK/UNIQUE),
// as required for a stable "WHERE col > last ORDER BY col LIMIT n" cursor.
func ValidateCursor(ctx context.Context, db *sql.DB, table, column string) (CursorPlan, error) {
	qt, err := quoteQualified(table)
	if err != nil {
		return CursorPlan{}, err
	}
	qc, err := quoteIdent(column)
	if err != nil {
		return CursorPlan{}, err
	}
	var typ sql.NullString
	var unique bool
	err = db.QueryRowContext(ctx, `SELECT format_type(a.atttypid, a.atttypmod),
			EXISTS(SELECT 1 FROM pg_index i WHERE i.indrelid=a.attrelid AND i.indisunique
			       AND i.indnkeyatts=1 AND i.indkey[0]=a.attnum AND i.indpred IS NULL)
		FROM pg_attribute a
		WHERE a.attrelid=to_regclass($1) AND a.attname=$2 AND a.attnum>0 AND NOT a.attisdropped`, qt, column).Scan(&typ, &unique)
	if errors.Is(err, sql.ErrNoRows) {
		return CursorPlan{}, fmt.Errorf("batch cursor column %s.%s not found: %w", table, column, ErrInvalid)
	}
	if err != nil {
		return CursorPlan{}, err
	}
	switch strings.ToLower(typ.String) {
	case "smallint", "integer", "bigint":
	default:
		return CursorPlan{}, fmt.Errorf("batch cursor column must be an integer type, got %q: %w", typ.String, ErrInvalid)
	}
	if !unique {
		return CursorPlan{}, fmt.Errorf("batch cursor column must have a single-column unique index or primary key: %w", ErrInvalid)
	}
	return CursorPlan{Table: qt, Column: qc}, nil
}

// scopeQuery and nextBoundQuery build cursor queries from quoted identifiers only.
func (p CursorPlan) scopeQuery() string {
	return `SELECT min(` + p.Column + `), max(` + p.Column + `), count(*) FROM ` + p.Table
}

// nextBoundQuery returns the inclusive upper cursor of the next batch without
// loading the rows: only the batch's cursor values are scanned, and only their
// max is returned to the worker.
func (p CursorPlan) nextBoundQuery() string {
	return `SELECT max(c) FROM (SELECT ` + p.Column + ` AS c FROM ` + p.Table +
		` WHERE ` + p.Column + ` > $1 AND ` + p.Column + ` <= $2 ORDER BY ` + p.Column + ` LIMIT $3) s`
}
