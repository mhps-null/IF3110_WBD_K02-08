package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

func TestMySQLIsDDLStatement(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		expected bool
	}{
		{"create table", "CREATE TABLE products (id int);", true},
		{"alter table", "ALTER TABLE products ADD COLUMN price decimal;", true},
		{"drop table", "DROP TABLE products;", true},
		{"truncate table", "TRUNCATE products;", true},
		{"rename table", "RENAME TABLE products TO old_products;", true},
		{"ddl after comments", "/* migration */ -- comment\n ALTER TABLE products ADD COLUMN sku varchar(20);", true},
		{"select query", "SELECT * FROM products WHERE id = 1;", false},
		{"insert query", "INSERT INTO products (id) VALUES (1);", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsDDLStatement(tc.sql)
			if got != tc.expected {
				t.Errorf("IsDDLStatement(%q) = %v, expected %v", tc.sql, got, tc.expected)
			}
		})
	}
}

func TestMySQLExecutorValidation(t *testing.T) {
	exec := NewExecutor()
	ctx := context.Background()

	_, err := exec.Execute(ctx, nil, "")
	if err == nil {
		t.Errorf("expected error on empty query")
	}

	plan, err := exec.Explain(ctx, nil, "CREATE TABLE items (id int);", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Root.NodeType != "DDL Statement (EXPLAIN tidak tersedia)" {
		t.Errorf("expected DDL explanation plan, got %s", plan.Root.NodeType)
	}
}

func TestMySQLImplicitCommitDetection(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want bool
	}{
		{"create table", "CREATE TABLE t (id int)", true},
		{"alter table", "ALTER TABLE t ADD c int", true},
		{"truncate", "TRUNCATE TABLE t", true},
		{"grant", "GRANT SELECT ON app.* TO 'reader'@'%'", true},
		{"analyze", "ANALYZE TABLE t", true},
		{"stored procedure call", "CALL migrate_schema()", true},
		{"commented ddl", "/* comment */ CREATE TABLE t (id int)", true},
		{"select", "SELECT 1", false},
		{"insert", "INSERT INTO t VALUES (1)", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsImplicitCommitStatement(tc.sql); got != tc.want {
				t.Fatalf("IsImplicitCommitStatement(%q) = %v, want %v", tc.sql, got, tc.want)
			}
		})
	}
}

func TestMySQLMultipleStatementDetection(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want bool
	}{
		{"single statement", "SELECT 1", false},
		{"one trailing semicolon", "SELECT 1;", false},
		{"semicolon in string literal", "SELECT 'one;two';", false},
		{"semicolon in identifier", "SELECT `part;name` FROM t", false},
		{"trailing comment", "SELECT 1; /* comment ; */", false},
		{"two statements", "INSERT INTO t VALUES (1); CREATE TABLE x (id int)", true},
		{"second after line comment", "SELECT 1; -- comment\n SELECT 2", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasMultipleStatements(tc.sql); got != tc.want {
				t.Fatalf("HasMultipleStatements(%q) = %v, want %v", tc.sql, got, tc.want)
			}
		})
	}
}

func TestMySQLTransactionControlIsRejected(t *testing.T) {
	for _, query := range []string{
		"COMMIT",
		"ROLLBACK",
		"START TRANSACTION",
		"SAVEPOINT before_change",
		"SET autocommit = 1",
		"/* comment */ SET @@SESSION.autocommit = 1",
	} {
		t.Run(query, func(t *testing.T) {
			_, err := NewExecutor().Execute(context.Background(), nil, query)
			if err == nil || !strings.Contains(err.Error(), "transaction control") && !strings.Contains(err.Error(), "autocommit") {
				t.Fatalf("expected transaction control rejection, got %v", err)
			}
		})
	}
}

type recordingExecState struct {
	beginCalls    int
	commitCalls   int
	rollbackCalls int
	execCalls     int
	lastSQL       string
	execErr       error
	commitErr     error
	rollbackErr   error
	query         func(string) (driver.Rows, error)
	readOnly      bool
}

type recordingExecConnector struct{ state *recordingExecState }

func (c recordingExecConnector) Connect(context.Context) (driver.Conn, error) {
	return &recordingExecConn{state: c.state}, nil
}
func (c recordingExecConnector) Driver() driver.Driver {
	return recordingExecDriver{state: c.state}
}

type recordingExecDriver struct{ state *recordingExecState }

func (d recordingExecDriver) Open(string) (driver.Conn, error) {
	return &recordingExecConn{state: d.state}, nil
}

type recordingExecConn struct{ state *recordingExecState }

func (c *recordingExecConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("Prepare is not implemented")
}
func (c *recordingExecConn) Close() error { return nil }
func (c *recordingExecConn) Begin() (driver.Tx, error) {
	c.state.beginCalls++
	return &recordingExecTx{state: c.state}, nil
}
func (c *recordingExecConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.state.beginCalls++
	c.state.readOnly = opts.ReadOnly
	return &recordingExecTx{state: c.state}, nil
}
func (c *recordingExecConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.state.execCalls++
	c.state.lastSQL = query
	if c.state.execErr != nil {
		return nil, c.state.execErr
	}
	return driver.RowsAffected(3), nil
}

type recordingExecTx struct{ state *recordingExecState }

func (tx *recordingExecTx) Commit() error {
	tx.state.commitCalls++
	return tx.state.commitErr
}
func (tx *recordingExecTx) Rollback() error {
	tx.state.rollbackCalls++
	return tx.state.rollbackErr
}

func openRecordingExecDB(t *testing.T, state *recordingExecState) *sql.DB {
	t.Helper()
	db := sql.OpenDB(recordingExecConnector{state: state})
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestMySQLExecutorRejectsMultipleStatementsBeforeDatabaseAccess(t *testing.T) {
	_, err := NewExecutor().Execute(context.Background(), nil, "INSERT INTO t VALUES (1); CREATE TABLE x (id int)")
	if err == nil || !strings.Contains(err.Error(), "multiple SQL statements") {
		t.Fatalf("expected multi-statement rejection, got %v", err)
	}
}

func TestMySQLDDLExecutesWithoutTransaction(t *testing.T) {
	state := &recordingExecState{}
	db := openRecordingExecDB(t, state)

	res, err := NewExecutor().Execute(context.Background(), db, "ALTER TABLE items ADD COLUMN sku varchar(20)")
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if state.beginCalls != 0 || state.commitCalls != 0 || state.rollbackCalls != 0 {
		t.Fatalf("DDL should bypass explicit transactions: begins=%d commits=%d rollbacks=%d", state.beginCalls, state.commitCalls, state.rollbackCalls)
	}
	if state.execCalls != 1 {
		t.Fatalf("ExecContext calls = %d, want 1", state.execCalls)
	}
	if res.RowsAffected != 3 {
		t.Fatalf("RowsAffected = %d, want 3", res.RowsAffected)
	}
}

func TestMySQLDDLFailureDoesNotClaimRollback(t *testing.T) {
	state := &recordingExecState{execErr: errors.New("synthetic DDL failure")}
	db := openRecordingExecDB(t, state)

	_, err := NewExecutor().Execute(context.Background(), db, "CREATE TABLE items (id int)")
	if err == nil {
		t.Fatal("expected DDL error")
	}
	if !strings.Contains(err.Error(), "cannot be rolled back") || !strings.Contains(err.Error(), "implicitly committed") {
		t.Fatalf("error must warn that rollback is not guaranteed, got: %v", err)
	}
	if state.beginCalls != 0 || state.rollbackCalls != 0 {
		t.Fatalf("DDL failure must not pretend an explicit rollback occurred: begins=%d rollbacks=%d", state.beginCalls, state.rollbackCalls)
	}
}

func TestMySQLDMLUsesTransaction(t *testing.T) {
	state := &recordingExecState{}
	db := openRecordingExecDB(t, state)

	_, err := NewExecutor().Execute(context.Background(), db, "UPDATE items SET price = 10")
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if state.beginCalls != 1 || state.commitCalls != 1 || state.rollbackCalls != 0 {
		t.Fatalf("DML transaction calls = begin:%d commit:%d rollback:%d, want 1:1:0", state.beginCalls, state.commitCalls, state.rollbackCalls)
	}
}

func TestMySQLCommitErrorReportsUnknownOutcome(t *testing.T) {
	state := &recordingExecState{commitErr: errors.New("connection lost during commit")}
	db := openRecordingExecDB(t, state)

	_, err := NewExecutor().Execute(context.Background(), db, "UPDATE items SET price = 10")
	if err == nil || !strings.Contains(err.Error(), "outcome may be uncertain") {
		t.Fatalf("expected uncertain commit outcome, got %v", err)
	}
	if state.rollbackCalls != 0 {
		t.Fatalf("executor must not imply that rollback resolves a commit error; rollbacks=%d", state.rollbackCalls)
	}
}

func TestMySQLRollbackFailureIsReported(t *testing.T) {
	state := &recordingExecState{
		execErr:     errors.New("synthetic query failure"),
		rollbackErr: errors.New("connection lost during rollback"),
	}
	db := openRecordingExecDB(t, state)

	_, err := NewExecutor().Execute(context.Background(), db, "UPDATE items SET price = 10")
	if err == nil || !strings.Contains(err.Error(), "rollback could not be confirmed") {
		t.Fatalf("expected unconfirmed rollback error, got %v", err)
	}
}
