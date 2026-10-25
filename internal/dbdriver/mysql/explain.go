package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/explain"
)

var readableExplainSQL = regexp.MustCompile(`(?i)^\s*(SELECT|WITH|TABLE)\b`)

func (e *Executor) Explain(ctx context.Context, db *sql.DB, sqlText string, analyze bool) (contract.Plan, error) {
	trimmed := strings.TrimSpace(sqlText)
	if trimmed == "" {
		return contract.Plan{}, errors.New("SQL query cannot be empty")
	}
	if IsTransactionControlStatement(trimmed) || HasMultipleStatements(trimmed) {
		return contract.Plan{}, errors.New("EXPLAIN requires one statement without transaction control")
	}
	if IsDDLStatement(trimmed) {
		return contract.Plan{
			Root:   contract.PlanNode{NodeType: "DDL Statement (EXPLAIN tidak tersedia)"},
			Raw:    "EXPLAIN tidak didukung untuk DDL statement pada MySQL/MariaDB.",
			Notice: "Statement DDL tidak mendukung EXPLAIN; tidak ada SQL yang dijalankan.",
		}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Validate the SQL with estimated EXPLAIN first. A syntax error in the user's
	// query must not be mistaken for unsupported ANALYZE syntax.
	estimated, err := e.explainJSON(ctx, db, trimmed)
	if err != nil || !analyze {
		return estimated, err
	}
	if !readableExplainSQL.MatchString(stripLeadingComments(trimmed)) {
		estimated.Notice = "Analisis aktual dibatasi pada query baca. SQL ini hanya ditampilkan sebagai estimasi EXPLAIN; data tidak diubah."
		return estimated, nil
	}
	var version string
	if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		return contract.Plan{}, fmt.Errorf("detect EXPLAIN engine: %w", err)
	}
	mariaDB := strings.Contains(strings.ToLower(version), "mariadb")
	prefix := "EXPLAIN ANALYZE FORMAT=TREE "
	if mariaDB {
		prefix = "ANALYZE FORMAT=JSON "
	} else {
		var major, minor, patch int
		if _, err := fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch); err == nil && major == 8 && minor == 0 && patch < 21 {
			prefix = "EXPLAIN ANALYZE " // FORMAT=TREE on ANALYZE was added in 8.0.21.
		}
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return contract.Plan{}, fmt.Errorf("begin read-only analysis: %w", err)
	}
	defer tx.Rollback()
	raw, queryErr := readExplainOutput(ctx, tx, prefix+trimmed)
	// Release the transaction before fallback, including when the pool has only
	// one connection. Never leave a transaction open while requesting another.
	if rollbackErr := tx.Rollback(); rollbackErr != nil {
		if ctx.Err() != nil {
			return contract.Plan{}, ctx.Err()
		}
		return contract.Plan{}, fmt.Errorf("close analysis transaction: %w", rollbackErr)
	}
	if queryErr != nil {
		var driverErr *mysqldriver.MySQLError
		if errors.As(queryErr, &driverErr) && (driverErr.Number == 1064 || driverErr.Number == 1235) {
			estimated.Notice = "Analisis aktual tidak didukung untuk query atau versi database ini. Hasil berikut adalah estimasi EXPLAIN, tanpa metrik aktual."
			return estimated, nil
		}
		return contract.Plan{}, fmt.Errorf("actual query analysis failed: %w", queryErr)
	}
	var plan contract.Plan
	if mariaDB {
		plan, err = explain.ParseMySQLPlan(raw, true)
		plan.MetricsNote = "MariaDB: waktu node adalah total milidetik; baris aktual adalah rata-rata baris yang dibaca per loop. Metrik yang tidak diberikan engine ditampilkan sebagai —."
	} else {
		plan, err = explain.ParseMySQLAnalyze(raw)
		plan.MetricsNote = "MySQL: waktu node dan baris aktual adalah rata-rata per loop. Waktu eksekusi ditampilkan dari node root jika root berjalan satu kali. Metrik yang tidak tersedia ditampilkan sebagai —."
	}
	return plan, err
}

type explainQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readExplainOutput(ctx context.Context, queryer explainQueryer, query string) (string, error) {
	rows, err := queryer.QueryContext(ctx, query)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var part string
		if err := rows.Scan(&part); err != nil {
			return "", err
		}
		parts = append(parts, part)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(parts) == 0 {
		return "", errors.New("EXPLAIN returned no output")
	}
	return strings.Join(parts, "\n"), nil
}

func (e *Executor) explainJSON(ctx context.Context, db *sql.DB, sqlText string) (contract.Plan, error) {
	raw, err := readExplainOutput(ctx, db, "EXPLAIN FORMAT=JSON "+sqlText)
	if err != nil {
		return contract.Plan{}, fmt.Errorf("estimated EXPLAIN failed: %w", err)
	}
	return explain.ParseMySQLPlan(raw, false)
}
