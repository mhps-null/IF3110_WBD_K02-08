
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/example/if3110-milestone1/internal/contract"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresIntrospectIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	schema := fmt.Sprintf("test_viewer_%d", time.Now().UnixNano())
	parent, child := QualifyTable(schema, "parent"), QualifyTable(schema, "child")
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+QuoteIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := db.ExecContext(clean, "DROP SCHEMA "+QuoteIdentifier(schema)+" CASCADE"); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	for _, query := range []string{
		"CREATE TABLE " + parent + " (id INTEGER PRIMARY KEY)",
		"CREATE TABLE " + child + " (id INTEGER PRIMARY KEY,parent_id INTEGER REFERENCES " + parent + "(id),name VARCHAR(100) UNIQUE,score INTEGER NOT NULL DEFAULT 1 CHECK(score>=0))",
		"CREATE INDEX idx_score ON " + child + "(score)",
		"INSERT INTO " + parent + " VALUES(1)",
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 3; i++ {
		if _, err := db.ExecContext(ctx, "INSERT INTO "+child+"(id,parent_id,name,score) VALUES($1,1,$2,$1)", i, fmt.Sprintf("row%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	intro := NewIntrospector()
	snapshot, err := intro.Snapshot(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	var table *contract.Table
	for i := range snapshot.Tables {
		candidate := &snapshot.Tables[i]
		if candidate.Schema == schema && candidate.Name == "child" {
			table = candidate
			break
		}
	}
	if table == nil || len(table.Columns) != 4 {
		t.Fatalf("table/columns missing: %+v", table)
	}
	columns := map[string]contract.Column{}
	for _, c := range table.Columns {
		columns[c.Name] = c
	}
	if !columns["id"].IsPrimaryKey || columns["name"].DataType != "character varying(100)" || !columns["name"].Nullable || columns["score"].Default == nil {
		t.Fatalf("columns: %+v", columns)
	}
	kinds := map[string]bool{}
	for _, c := range table.Constraints {
		kinds[c.Type] = true
		if c.Type == "FOREIGN KEY" && (c.RefTable != schema+".parent" || len(c.RefColumns) != 1 || c.RefColumns[0] != "id") {
			t.Fatalf("FK: %+v", c)
		}
	}
	for _, kind := range []string{"PRIMARY KEY", "FOREIGN KEY", "UNIQUE", "CHECK"} {
		if !kinds[kind] {
			t.Fatalf("missing %s", kind)
		}
	}
	indexed := false
	for _, idx := range table.Indexes {
		if idx.Name == "idx_score" && idx.Method == "btree" && len(idx.Columns) == 1 && idx.Columns[0] == "score" {
			indexed = true
		}
	}
	if !indexed {
		t.Fatal("score index missing")
	}
	for _, desc := range []bool{false, true} {
		page, err := intro.Rows(ctx, db, schema, "child", contract.PageRequest{Page: 1, PageSize: 2, OrderBy: "score", Desc: desc})
		if err != nil {
			t.Fatal(err)
		}
		want := int64(1)
		if desc {
			want = 3
		}
		if page.Total != 3 || len(page.Rows) != 2 || page.Rows[0][0] != want {
			t.Fatalf("sort desc=%t: %+v", desc, page)
		}
	}
	page, err := intro.Rows(ctx, db, schema, "child", contract.PageRequest{Page: 2, PageSize: 2, OrderBy: "id"})
	if err != nil || len(page.Rows) != 1 || page.Rows[0][0] != int64(3) {
		t.Fatalf("second page: %+v err=%v", page, err)
	}
	if _, err := intro.Rows(ctx, db, schema, "missing", contract.PageRequest{}); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("unknown table: %v", err)
	}
	if _, err := intro.Rows(ctx, db, schema, "child", contract.PageRequest{OrderBy: "id; DROP TABLE child"}); err == nil || !strings.Contains(err.Error(), "invalid order") {
		t.Fatalf("unsafe sort: %v", err)
	}
}
