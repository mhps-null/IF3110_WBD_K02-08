package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/example/if3110-milestone1/internal/contract"
	"github.com/example/if3110-milestone1/internal/explain"
)

var ddlRegex = regexp.MustCompile(`(?i)^\s*(CREATE|ALTER|DROP|TRUNCATE|RENAME)\b`)

type Executor struct{}

func NewExecutor() *Executor {
	return &Executor{}
}

func IsDDLStatement(sqlText string) bool {
	return ddlRegex.MatchString(sqlText)
}

func (e *Executor) Execute(ctx context.Context, db *sql.DB, sqlText string) (contract.ExecResult, error) {
	trimmed := strings.TrimSpace(sqlText)
	if trimmed == "" {
		return contract.ExecResult{}, errors.New("SQL query cannot be empty")
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
		}, fmt.Errorf("migration execution failed on MySQL: %w", err)
	}

	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return contract.ExecResult{
			Duration: time.Since(start),
		}, fmt.Errorf("transaction commit failed on MySQL: %w", err)
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
			Raw:      "EXPLAIN tidak didukung untuk DDL statement pada MySQL/MariaDB.",
		}, nil
	}

	var query string
	if analyze {
		query = "EXPLAIN ANALYZE " + trimmed
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return contract.Plan{}, fmt.Errorf("failed to start explain analyze transaction: %w", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		var rawText string
		row := tx.QueryRowContext(ctx, query)
		if err := row.Scan(&rawText); err != nil {
			return e.explainJSON(ctx, db, trimmed, false)
		}

		return contract.Plan{
			Root: contract.PlanNode{
				NodeType: "MySQL EXPLAIN ANALYZE",
			},
			Analyzed: true,
			Raw:      rawText,
		}, nil
	}

	return e.explainJSON(ctx, db, trimmed, analyze)
}

func (e *Executor) explainJSON(ctx context.Context, db *sql.DB, trimmed string, analyze bool) (contract.Plan, error) {
	query := "EXPLAIN FORMAT=JSON " + trimmed
	var rawJSON string
	row := db.QueryRowContext(ctx, query)
	if err := row.Scan(&rawJSON); err != nil {
		return contract.Plan{}, fmt.Errorf("mysql explain format=json query failed: %w", err)
	}

	return explain.ParseMySQLPlan(rawJSON, analyze)
}
