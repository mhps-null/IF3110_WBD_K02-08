package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/example/if3110-milestone1/internal/contract"
)

type queryFunc func(string, []driver.NamedValue) (driver.Rows, error)
type mockDriver struct{ query queryFunc }
type mockConn struct{ driver *mockDriver }
type mockTx struct{}
type staticRows struct {
	cols  []string
	data  [][]driver.Value
	index int
}

var driverSequence atomic.Uint64

func (d *mockDriver) Open(string) (driver.Conn, error)  { return &mockConn{d}, nil }
func (c *mockConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not implemented") }
func (c *mockConn) Close() error                        { return nil }
func (c *mockConn) Begin() (driver.Tx, error)           { return mockTx{}, nil }
func (c *mockConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if !opts.ReadOnly {
		return nil, errors.New("viewer transaction must be read-only")
	}
	return mockTx{}, nil
}
func (c *mockConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.driver.query(q, args)
}
func (c *mockConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("session settings must not be changed")
}
func (mockTx) Commit() error            { return nil }
func (mockTx) Rollback() error          { return nil }
func (r *staticRows) Columns() []string { return r.cols }
func (r *staticRows) Close() error      { return nil }
func (r *staticRows) Next(dest []driver.Value) error {
	if r.index == len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.index])
	r.index++
	return nil
}
func rows(n int, data ...[]driver.Value) *staticRows {
	cols := make([]string, n)
	for i := range cols {
		cols[i] = fmt.Sprintf("column%d", i)
	}
	return &staticRows{cols: cols, data: data}
}
func testDB(t *testing.T, q queryFunc) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("mysql_unit_%d", driverSequence.Add(1))
	sql.Register(name, &mockDriver{q})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestIdentifiers(t *testing.T) {
	if got := QuoteIdentifier("user`name"); got != "`user``name`" {
		t.Fatal(got)
	}
	if got := QualifyTable("app", "users"); got != "`app`.`users`" {
		t.Fatal(got)
	}
}

// One small metadata fixture exercises both dialects, including their CHECK/index differences.
func snapshotQuery(maria bool) queryFunc {
	userCheck, orderCheck := "users_check", "orders_check"
	if maria {
		userCheck, orderCheck = "chk_id", "chk_id"
	}
	return func(q string, _ []driver.NamedValue) (driver.Rows, error) {
		switch {
		case q == "SELECT DATABASE()":
			return rows(1, []driver.Value{"app"}), nil
		case strings.Contains(q, "column_name = 'table_name'"):
			var found int64
			if maria {
				found = 1
			}
			return rows(1, []driver.Value{found}), nil
		case strings.Contains(q, "column_name = 'expression'"):
			var found int64
			if !maria {
				found = 1
			}
			return rows(1, []driver.Value{found}), nil
		case strings.Contains(q, "table_name = 'check_constraints'"):
			return rows(1, []driver.Value{int64(1)}), nil
		case strings.Contains(q, "FROM information_schema.tables"):
			return rows(2, []driver.Value{"app", "users"}, []driver.Value{"app", "orders"}), nil
		case strings.Contains(q, "FROM information_schema.columns"):
			return rows(8,
				[]driver.Value{"app", "users", "id", "int", "NO", nil, "PRI", int64(1)},
				[]driver.Value{"app", "users", "email", "varchar(100)", "YES", "none", "UNI", int64(2)},
				[]driver.Value{"app", "orders", "id", "int", "NO", nil, "PRI", int64(1)}), nil
		case strings.Contains(q, "FROM information_schema.table_constraints"):
			return rows(4,
				[]driver.Value{"app", "users", "PRIMARY", "PRIMARY KEY"},
				[]driver.Value{"app", "users", "email_unique", "UNIQUE"},
				[]driver.Value{"app", "users", userCheck, "CHECK"},
				[]driver.Value{"app", "orders", "PRIMARY", "PRIMARY KEY"},
				[]driver.Value{"app", "orders", "user_fk", "FOREIGN KEY"},
				[]driver.Value{"app", "orders", orderCheck, "CHECK"}), nil
		case strings.Contains(q, "FROM information_schema.key_column_usage"):
			return rows(8,
				[]driver.Value{"app", "users", "PRIMARY", "id", int64(1), nil, nil, nil},
				[]driver.Value{"app", "users", "email_unique", "email", int64(1), nil, nil, nil},
				[]driver.Value{"app", "orders", "PRIMARY", "id", int64(1), nil, nil, nil},
				[]driver.Value{"app", "orders", "user_fk", "id", int64(1), "app", "users", "id"}), nil
		case strings.Contains(q, "FROM information_schema.referential_constraints"):
			return rows(5, []driver.Value{"app", "orders", "user_fk", "SET NULL", "CASCADE"}), nil
		case strings.Contains(q, "FROM information_schema.check_constraints"):
			if maria {
				return rows(4, []driver.Value{"app", "users", userCheck, "id > 0"}, []driver.Value{"app", "orders", orderCheck, "id >= 0"}), nil
			}
			return rows(3, []driver.Value{"app", userCheck, "id > 0"}, []driver.Value{"app", orderCheck, "id >= 0"}), nil
		case strings.Contains(q, "FROM information_schema.statistics"):
			if maria {
				return rows(7, []driver.Value{"app", "users", "idx.users.email", int64(0), int64(1), "email", "BTREE"}), nil
			}
			return rows(8, []driver.Value{"app", "users", "idx.users.email", int64(0), int64(1), nil, "BTREE", "lower(email)"}), nil
		}
		return nil, fmt.Errorf("unexpected query: %s", q)
	}
}
func TestSnapshot(t *testing.T) {
	for _, maria := range []bool{false, true} {
		t.Run(fmt.Sprintf("MariaDB=%t", maria), func(t *testing.T) {
			snapshot, err := NewIntrospector().Snapshot(context.Background(), testDB(t, snapshotQuery(maria)))
			if err != nil {
				t.Fatal(err)
			}
			tables := map[string]contract.Table{}
			for _, table := range snapshot.Tables {
				tables[table.Name] = table
			}
			users, orders := tables["users"], tables["orders"]
			if len(tables) != 2 || len(users.Columns) != 2 || !users.Columns[0].IsPrimaryKey || !users.Columns[1].Nullable || users.Columns[1].Default == nil {
				t.Fatalf("columns: %+v", users.Columns)
			}
			for _, table := range []contract.Table{users, orders} {
				want := "id > 0"
				if table.Name == "orders" {
					want = "id >= 0"
				}
				found := false
				for _, c := range table.Constraints {
					if c.Type == "CHECK" {
						found = true
						if c.Definition != want {
							t.Fatalf("%s CHECK: %s", table.Name, c.Definition)
						}
					}
					if c.Type == "FOREIGN KEY" && (c.RefTable != "app.users" || len(c.RefColumns) != 1 || c.RefColumns[0] != "id") {
						t.Fatalf("foreign key: %+v", c)
					}
					if c.Type == "FOREIGN KEY" && (c.RefSchema != "app" || c.RefTableName != "users" || c.OnDelete != "SET NULL" || c.OnUpdate != "CASCADE") {
						t.Fatalf("foreign key rules lost: %+v", c)
					}
				}
				if !found {
					t.Fatal("CHECK missing")
				}
			}
			if len(users.Indexes) != 1 || users.Indexes[0].Name != "idx.users.email" || !users.Indexes[0].Unique {
				t.Fatalf("index: %+v", users.Indexes)
			}
			want := "lower(email)"
			if maria {
				want = "email"
			}
			if len(users.Indexes[0].Columns) != 1 || users.Indexes[0].Columns[0] != want {
				t.Fatal("index columns lost")
			}
		})
	}
	t.Run("metadata failure", func(t *testing.T) {
		db := testDB(t, func(q string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(q, "FROM information_schema.check_constraints") {
				return nil, errors.New("metadata unavailable")
			}
			return snapshotQuery(false)(q, args)
		})
		if _, err := NewIntrospector().Snapshot(context.Background(), db); err == nil {
			t.Fatal("metadata error ignored")
		}
	})
}

func TestRows(t *testing.T) {
	for _, tc := range []struct {
		name, version, schema, table string
		p                            contract.PageRequest
		wantError                    bool
	}{
		{"MySQL", "8.0.35", "", "users", contract.PageRequest{Page: 2, PageSize: 2, OrderBy: "id", Desc: true}, false},
		{"MariaDB", "11.8-MariaDB", "app", "users", contract.PageRequest{}, false},
		{"cross-database", "8.0", "other", "users", contract.PageRequest{}, true},
		{"unknown table", "8.0", "app", "missing", contract.PageRequest{}, true},
		{"unsafe sort", "8.0", "app", "users", contract.PageRequest{OrderBy: "id; DROP TABLE users"}, true},
		{"version error", "", "app", "users", contract.PageRequest{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var countSQL, dataSQL string
			db := testDB(t, func(q string, args []driver.NamedValue) (driver.Rows, error) {
				switch {
				case q == "SELECT DATABASE()":
					return rows(1, []driver.Value{"app"}), nil
				case q == "SELECT VERSION()":
					if tc.version == "" {
						return nil, errors.New("version unavailable")
					}
					return rows(1, []driver.Value{tc.version}), nil
				case strings.Contains(q, "FROM information_schema.columns"):
					if len(args) != 2 || args[1].Value != "app" {
						t.Fatal("active database not used")
					}
					if args[0].Value == "missing" {
						return rows(1), nil
					}
					return rows(1, []driver.Value{"id"}), nil
				case strings.Contains(q, "COUNT(1)"):
					countSQL = q
					return rows(1, []driver.Value{int64(3)}), nil
				case strings.Contains(q, "LIMIT"):
					dataSQL = q
					return &staticRows{cols: []string{"id"}, data: [][]driver.Value{{int64(1)}}}, nil
				}
				return nil, fmt.Errorf("unexpected query: %s", q)
			})
			page, err := NewIntrospector().Rows(context.Background(), db, tc.schema, tc.table, tc.p)
			if tc.wantError {
				if err == nil || dataSQL != "" || countSQL != "" {
					t.Fatalf("invalid request executed: %v", err)
				}
				if tc.name == "cross-database" && !errors.Is(err, contract.ErrAccessDenied) {
					t.Fatalf("cross-db: %v", err)
				}
				return
			}
			if err != nil || page.Total != 3 || len(page.Rows) != 1 {
				t.Fatalf("page=%+v err=%v", page, err)
			}
			marker := "MAX_EXECUTION_TIME(5000)"
			suffix := "ORDER BY `id` DESC LIMIT 2 OFFSET 2"
			if tc.name == "MariaDB" {
				marker = "SET STATEMENT max_statement_time = 5 FOR"
				suffix = "ORDER BY `id` ASC LIMIT 20 OFFSET 0"
			}
			if !strings.Contains(countSQL, marker) || !strings.Contains(dataSQL, marker) || !strings.Contains(dataSQL, suffix) {
				t.Fatalf("count=%s data=%s", countSQL, dataSQL)
			}
		})
	}
}
