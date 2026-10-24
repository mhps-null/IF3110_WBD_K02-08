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
)

var (
	// ddlRegex is used only to decide whether EXPLAIN is meaningful for a statement.
	ddlRegex = regexp.MustCompile(`(?i)^\s*(CREATE|ALTER|DROP|TRUNCATE|RENAME)\b`)
	// implicitCommitRegex covers MySQL/MariaDB statements that cannot be made
	// rollback-safe by wrapping them in a database/sql transaction.
	implicitCommitRegex = regexp.MustCompile(`(?i)^\s*(CREATE|ALTER|DROP|TRUNCATE|RENAME|ANALYZE|CACHE\s+INDEX|CALL|FLUSH|GRANT|INSTALL|LOCK\s+TABLES|OPTIMIZE|REPAIR|RESET|REVOKE|SET\s+PASSWORD|SHUTDOWN|START\s+(REPLICA|SLAVE)|STOP\s+(REPLICA|SLAVE)|UNINSTALL|UNLOCK\s+TABLES)\b`)
	// transactionControlRegex prevents user SQL from taking control of the
	// transaction that the executor manages itself.
	transactionControlRegex = regexp.MustCompile(`(?i)^\s*(BEGIN|START\s+TRANSACTION|COMMIT|ROLLBACK|SAVEPOINT|RELEASE\s+SAVEPOINT)\b|^\s*SET\s+(SESSION\s+|GLOBAL\s+)?(@@(?:SESSION\.)?)?autocommit\b`)
	leadingCommentsRegex    = regexp.MustCompile(`(?is)^\s*(?:(?:--[^\r\n]*|#[^\r\n]*|/\*.*?\*/)\s*)*`)
)

type Executor struct{}

func NewExecutor() *Executor {
	return &Executor{}
}

func stripLeadingComments(sqlText string) string {
	return leadingCommentsRegex.ReplaceAllString(sqlText, "")
}

func IsDDLStatement(sqlText string) bool {
	return ddlRegex.MatchString(stripLeadingComments(sqlText))
}

// IsImplicitCommitStatement reports statements that must not be executed inside
// a database/sql transaction on MySQL/MariaDB. These statements can implicitly
// commit, so wrapping them in a transaction would falsely imply rollback safety.
func IsImplicitCommitStatement(sqlText string) bool {
	return implicitCommitRegex.MatchString(stripLeadingComments(sqlText))
}

// IsTransactionControlStatement reports SQL that tries to manage transaction
// boundaries or changes autocommit, which the executor must control itself.
func IsTransactionControlStatement(sqlText string) bool {
	return transactionControlRegex.MatchString(stripLeadingComments(sqlText))
}

// HasMultipleStatements detects executable SQL after the first statement
// terminator while ignoring semicolons inside quotes and comments. One statement
// per migration prevents a DDL statement later in a batch from silently
// committing earlier statements that the executor treated as transactional.
func HasMultipleStatements(sqlText string) bool {
	var quote byte
	lineComment, blockComment, statementEnded := false, false, false
	for i := 0; i < len(sqlText); i++ {
		ch := sqlText[i]
		if lineComment {
			if ch == '\n' || ch == '\r' {
				lineComment = false
			}
			continue
		}
		if blockComment {
			if ch == '*' && i+1 < len(sqlText) && sqlText[i+1] == '/' {
				blockComment = false
				i++
			}
			continue
		}
		if quote != 0 {
			if ch == '\\' && quote != '`' && i+1 < len(sqlText) {
				i++
				continue
			}
			if ch == quote {
				if i+1 < len(sqlText) && sqlText[i+1] == quote {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		if ch == '-' && i+1 < len(sqlText) && sqlText[i+1] == '-' && (i+2 == len(sqlText) || sqlText[i+2] <= ' ') {
			lineComment = true
			i++
			continue
		}
		if ch == '#' {
			lineComment = true
			continue
		}
		if ch == '/' && i+1 < len(sqlText) && sqlText[i+1] == '*' {
			blockComment = true
			i++
			continue
		}
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '\f' {
			continue
		}
		if statementEnded {
			return true
		}
		switch ch {
		case '\'', '"', '`':
			quote = ch
		case ';':
			statementEnded = true
		}
	}
	return false
}

func (e *Executor) Execute(ctx context.Context, db *sql.DB, sqlText string) (contract.ExecResult, error) {
	trimmed := strings.TrimSpace(sqlText)
	if trimmed == "" {
		return contract.ExecResult{}, errors.New("SQL query cannot be empty")
	}
	if IsTransactionControlStatement(trimmed) {
		return contract.ExecResult{}, errors.New("transaction control and autocommit statements are managed by the migration executor and are not allowed")
	}
	if HasMultipleStatements(trimmed) {
		return contract.ExecResult{}, errors.New("multiple SQL statements in one migration are not allowed; create one migration per statement")
	}

	start := time.Now()
	if IsImplicitCommitStatement(trimmed) {
		// MySQL/MariaDB DDL and several administrative statements implicitly
		// commit. Run them without an explicit transaction rather than suggesting
		// that database/sql Rollback can undo their effects.
		res, err := db.ExecContext(ctx, trimmed)
		if err != nil {
			return contract.ExecResult{Duration: time.Since(start)}, fmt.Errorf("MySQL/MariaDB statement failed; it may have implicitly committed and cannot be rolled back by the migration executor: %w", err)
		}
		return resultFromSQLResult(res, start), nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return contract.ExecResult{}, fmt.Errorf("failed to begin transaction: %w", err)
	}

	res, err := tx.ExecContext(ctx, trimmed)
	if err != nil {
		rollbackErr := tx.Rollback()
		result := contract.ExecResult{Duration: time.Since(start)}
		if rollbackErr != nil {
			return result, fmt.Errorf("migration execution failed on MySQL and transaction rollback could not be confirmed: %w (rollback error: %v)", err, rollbackErr)
		}
		return result, fmt.Errorf("migration execution failed on MySQL; transaction rolled back: %w", err)
	}

	if err := tx.Commit(); err != nil {
		// A commit error is ambiguous: the server may have committed before the
		// client lost confirmation. A subsequent Rollback cannot resolve this.
		return contract.ExecResult{Duration: time.Since(start)}, fmt.Errorf("MySQL transaction commit failed; target outcome may be uncertain: %w", err)
	}

	return resultFromSQLResult(res, start), nil
}

func resultFromSQLResult(res sql.Result, start time.Time) contract.ExecResult {
	var rowsAffected int64
	if res != nil {
		if ra, err := res.RowsAffected(); err == nil {
			rowsAffected = ra
		}
	}
	return contract.ExecResult{RowsAffected: rowsAffected, Duration: time.Since(start)}
}
