package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/explain"
)

var (
	// Regex patterns for statements that cannot run inside a PostgreSQL transaction block.
	nonTransactionalRegex = regexp.MustCompile(`(?i)\b(CREATE\s+INDEX\s+CONCURRENTLY|DROP\s+INDEX\s+CONCURRENTLY|REINDEX\s+(?:INDEX|TABLE|DATABASE)?\s*CONCURRENTLY|VACUUM|CLUSTER)\b`)
	// Regex pattern for DDL statements (PostgreSQL EXPLAIN does not support DDL commands).
	ddlRegex = regexp.MustCompile(`(?i)^\s*(CREATE|ALTER|DROP|TRUNCATE|GRANT|REVOKE|COMMENT)\b`)
)

// Executor implements contract.Executor for PostgreSQL databases.
type Executor struct{}

// NewExecutor creates a new PostgreSQL Executor instance.
func NewExecutor() *Executor {
	return &Executor{}
}

// IsNonTransactionalStatement checks if the given SQL statement cannot run inside a transaction.
func IsNonTransactionalStatement(sqlText string) bool {
	return nonTransactionalRegex.MatchString(sqlText)
}

// IsDDLStatement checks if the given SQL is a DDL statement.
func IsDDLStatement(sqlText string) bool {
	return ddlRegex.MatchString(sqlText)
}

// Execute executes SQL within a single transaction: COMMIT on success, ROLLBACK on error.
func (e *Executor) Execute(ctx context.Context, db *sql.DB, sqlText string) (contract.ExecResult, error) {
	trimmed := strings.TrimSpace(sqlText)
	if trimmed == "" {
		return contract.ExecResult{}, errors.New("SQL migration query cannot be empty")
	}

	if IsNonTransactionalStatement(trimmed) {
		return contract.ExecResult{}, errors.New("statement cannot run inside a transactional migration (e.g. CREATE INDEX CONCURRENTLY or VACUUM); please use standard DDL statements")
	}

	start := time.Now()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return contract.ExecResult{}, fmt.Errorf("failed to begin transaction: %w", err)
	}

	res, err := tx.ExecContext(ctx, trimmed)
	if err != nil {
		_ = tx.Rollback()
		return contract.ExecResult{
			Duration: time.Since(start),
		}, fmt.Errorf("migration execution failed, changes rolled back: %w", err)
	}

	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return contract.ExecResult{
			Duration: time.Since(start),
		}, fmt.Errorf("transaction commit failed: %w", err)
	}

	var rowsAffected int64
	if res != nil {
		if ra, err := res.RowsAffected(); err == nil {
			rowsAffected = ra
		}
	}

	return contract.ExecResult{
		RowsAffected: rowsAffected,
		Duration:     time.Since(start),
	}, nil
}

// Explain executes EXPLAIN on PostgreSQL and parses the execution plan without modifying database state.
func (e *Executor) Explain(ctx context.Context, db *sql.DB, sqlText string, analyze bool) (contract.Plan, error) {
	trimmed := strings.TrimSpace(sqlText)
	if trimmed == "" {
		return contract.Plan{}, errors.New("SQL query cannot be empty")
	}

	if IsDDLStatement(trimmed) {
		return contract.Plan{
			Root: contract.PlanNode{
				NodeType: "DDL Statement (EXPLAIN tidak tersedia)",
			},
			Analyzed: false,
			Raw:      "EXPLAIN tidak tersedia untuk statement DDL pada PostgreSQL.",
		}, nil
	}

	var rawJSON string
	if analyze {
		// Jalankan dalam transaksi dan rollback untuk mencegah efek samping data
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return contract.Plan{}, fmt.Errorf("failed to start explain analyze transaction: %w", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		query := "EXPLAIN (ANALYZE, FORMAT JSON) " + trimmed
		row := tx.QueryRowContext(ctx, query)
		if err := row.Scan(&rawJSON); err != nil {
			return contract.Plan{}, fmt.Errorf("explain analyze query failed: %w", err)
		}
	} else {
		query := "EXPLAIN (FORMAT JSON) " + trimmed
		row := db.QueryRowContext(ctx, query)
		if err := row.Scan(&rawJSON); err != nil {
			return contract.Plan{}, fmt.Errorf("explain query failed: %w", err)
		}
	}

	return explain.ParsePostgresPlan(rawJSON, analyze)
}
