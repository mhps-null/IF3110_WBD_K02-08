package mysql

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

type explainRows struct {
	values []driver.Value
}

func (r *explainRows) Columns() []string { return []string{"EXPLAIN"} }
func (r *explainRows) Close() error      { return nil }
func (r *explainRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	dest[0], r.values = r.values[0], r.values[1:]
	return nil
}
func (c *recordingExecConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if c.state.query == nil {
		return nil, errors.New("unexpected query")
	}
	return c.state.query(query)
}

func TestExplainActualAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name, version, raw    string
		queryErr              error
		wantError, wantActual bool
	}{
		{"MySQL TREE", "8.4.0", "-> Result (actual time=0..0 rows=1 loops=1)\n    -> Zero rows (actual time=0..0 rows=0 loops=1)", nil, false, true},
		{"MySQL 8.0.18", "8.0.18", "-> Result (actual time=0..0 rows=1 loops=1)", nil, false, true},
		{"MariaDB JSON", "11.8.0-MariaDB", `{"query_block":{"r_total_time_ms":0,"r_loops":1,"table":{"table_name":"items","r_rows":0,"r_loops":1}}}`, nil, false, true},
		{"unsupported", "5.7.44", "", &mysqldriver.MySQLError{Number: 1064}, false, false},
		{"feature not supported", "8.4.0", "", &mysqldriver.MySQLError{Number: 1235}, false, false},
		{"permission denied", "8.4.0", "", &mysqldriver.MySQLError{Number: 1142}, true, false},
		{"connection lost", "8.4.0", "", errors.New("connection lost"), true, false},
		{"cancelled", "8.4.0", "", context.DeadlineExceeded, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &recordingExecState{}
			state.query = func(query string) (driver.Rows, error) {
				switch {
				case strings.HasPrefix(query, "EXPLAIN FORMAT=JSON "):
					return &explainRows{[]driver.Value{`{"query_block":{"table":{"table_name":"items","rows":1}}}`}}, nil
				case query == "SELECT VERSION()":
					return &explainRows{[]driver.Value{tc.version}}, nil
				default:
					expected := "EXPLAIN ANALYZE FORMAT=TREE "
					if strings.Contains(tc.version, "MariaDB") {
						expected = "ANALYZE FORMAT=JSON "
					}
					if tc.version == "8.0.18" {
						expected = "EXPLAIN ANALYZE "
					}
					if !strings.HasPrefix(query, expected) {
						t.Fatalf("wrong engine syntax: %s", query)
					}
					if tc.queryErr != nil {
						return nil, tc.queryErr
					}
					parts := strings.Split(tc.raw, "\n")
					values := make([]driver.Value, len(parts))
					for i, part := range parts {
						values[i] = part
					}
					return &explainRows{values}, nil
				}
			}
			db := openRecordingExecDB(t, state)
			db.SetMaxOpenConns(1)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			plan, err := NewExecutor().Explain(ctx, db, "SELECT * FROM items", true)
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v, wantError=%t", err, tc.wantError)
			}
			if state.beginCalls != 1 || !state.readOnly || state.rollbackCalls != 1 || state.commitCalls != 0 {
				t.Fatalf("analysis must end a read-only transaction: %+v", state)
			}
			if !tc.wantError && (plan.Analyzed != tc.wantActual || (plan.Notice != "") == tc.wantActual) {
				t.Fatalf("incorrect actual/fallback status: %+v", plan)
			}
			if tc.wantActual && plan.MetricsNote == "" {
				t.Fatal("metric semantics missing")
			}
			if tc.name == "MySQL TREE" && len(plan.Root.Children) != 1 {
				t.Fatal("lost output after the first row")
			}
		})
	}
}

func TestExplainRejectsInvalidSQLAndDoesNotAnalyzeWrites(t *testing.T) {
	for _, sqlText := range []string{"COMMIT", "SELECT 1; COMMIT", "START TRANSACTION"} {
		if _, err := NewExecutor().Explain(context.Background(), nil, sqlText, true); err == nil {
			t.Fatalf("accepted unsafe batch: %s", sqlText)
		}
	}
	state := &recordingExecState{}
	state.query = func(string) (driver.Rows, error) { return nil, &mysqldriver.MySQLError{Number: 1064} }
	db := openRecordingExecDB(t, state)
	if _, err := NewExecutor().Explain(context.Background(), db, "SELECT invalid syntax", true); err == nil || state.beginCalls != 0 {
		t.Fatal("invalid user SQL must fail before attempting actual analysis")
	}
	state.query = func(query string) (driver.Rows, error) {
		if !strings.HasPrefix(query, "EXPLAIN FORMAT=JSON UPDATE ") {
			t.Fatalf("write was analyzed: %s", query)
		}
		return &explainRows{[]driver.Value{`{"query_block":{"table":{"table_name":"items","rows":1}}}`}}, nil
	}
	plan, err := NewExecutor().Explain(context.Background(), db, "UPDATE items SET value=1", true)
	if err != nil || plan.Analyzed || plan.Notice == "" || state.beginCalls != 0 {
		t.Fatalf("writes must use explained estimated fallback: %+v err=%v", plan, err)
	}
}
