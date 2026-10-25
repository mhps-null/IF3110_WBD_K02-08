package postgres

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

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

type queryFunc func(string, []driver.NamedValue) (driver.Rows, error)
type mockDriver struct {
	query       queryFunc
	failTimeout bool
}
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
func (c *mockConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	if q != "SET LOCAL statement_timeout = 5000" || c.driver.failTimeout {
		return nil, errors.New("timeout setting failed")
	}
	return driver.RowsAffected(0), nil
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
func testDB(t *testing.T, d *mockDriver) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("postgres_unit_%d", driverSequence.Add(1))
	sql.Register(name, d)
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestIdentifiers(t *testing.T) {
	for input, want := range map[string]string{"users": "\"users\"", "user\"name": "\"user\"\"name\""} {
		if got := QuoteIdentifier(input); got != want {
			t.Fatalf("quote %q: %s", input, got)
		}
	}
	if got := QualifyTable("", "users"); got != "\"public\".\"users\"" {
		t.Fatal(got)
	}
}

func TestSnapshotQueryError(t *testing.T) {
	db := testDB(t, &mockDriver{query: func(string, []driver.NamedValue) (driver.Rows, error) { return nil, errors.New("metadata unavailable") }})
	if _, err := NewIntrospector().Snapshot(context.Background(), db); err == nil {
		t.Fatal("query error ignored")
	}
}

func TestRows(t *testing.T) {
	for _, tc := range []struct {
		name, table string
		page        contract.PageRequest
		sql         string
		wantError   bool
	}{
		{"defaults", "users", contract.PageRequest{}, "ORDER BY \"id\" ASC LIMIT 20 OFFSET 0", false},
		{"page and sort", "users", contract.PageRequest{Page: 2, PageSize: 2, OrderBy: "nama", Desc: true}, "ORDER BY \"nama\" DESC LIMIT 2 OFFSET 2", false},
		{"bounds", "users", contract.PageRequest{Page: -1, PageSize: 500}, "LIMIT 100 OFFSET 0", false},
		{"unknown table", "missing", contract.PageRequest{}, "", true},
		{"unsafe sort", "users", contract.PageRequest{OrderBy: "id; DROP TABLE users"}, "", true},
		{"case-sensitive column", "users", contract.PageRequest{OrderBy: "NAMA"}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dataSQL string
			db := testDB(t, &mockDriver{query: func(q string, args []driver.NamedValue) (driver.Rows, error) {
				switch {
				case strings.Contains(q, "FROM pg_attribute"):
					rows := &staticRows{cols: []string{"attname"}}
					if len(args) != 2 || args[0].Value != "public" {
						t.Fatal("metadata schema/table not parameterized")
					}
					if args[1].Value == "users" {
						rows.data = [][]driver.Value{{"id"}, {"Nama"}, {"nama"}}
					}
					return rows, nil
				case strings.Contains(q, "COUNT(1)"):
					return &staticRows{cols: []string{"count"}, data: [][]driver.Value{{int64(3)}}}, nil
				case strings.Contains(q, "LIMIT"):
					dataSQL = q
					return &staticRows{cols: []string{"id", "Nama", "nama"}, data: [][]driver.Value{{int64(1), "John", []byte("john")}}}, nil
				default:
					return nil, fmt.Errorf("unexpected query: %s", q)
				}
			}})
			page, err := NewIntrospector().Rows(context.Background(), db, "public", tc.table, tc.page)
			if tc.wantError {
				if err == nil || dataSQL != "" {
					t.Fatalf("invalid request executed: sql=%q err=%v", dataSQL, err)
				}
				if tc.table == "missing" && !errors.Is(err, contract.ErrNotFound) {
					t.Fatalf("unknown table: %v", err)
				}
				if tc.table != "missing" && !strings.Contains(err.Error(), "invalid order column") {
					t.Fatalf("sort validation: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if page.Total != 3 || len(page.Rows) != 1 || page.Rows[0][2] != "john" {
				t.Fatalf("page: %+v", page)
			}
			if !strings.Contains(dataSQL, tc.sql) || !strings.Contains(dataSQL, "\"public\".\"users\"") {
				t.Fatal(dataSQL)
			}
		})
	}
	t.Run("timeout setup failure", func(t *testing.T) {
		db := testDB(t, &mockDriver{failTimeout: true})
		if _, err := NewIntrospector().Rows(context.Background(), db, "public", "users", contract.PageRequest{}); err == nil {
			t.Fatal("timeout setup error ignored")
		}
	})
}
