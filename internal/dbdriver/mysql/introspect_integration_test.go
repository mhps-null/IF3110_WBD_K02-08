//go:build integration

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

func TestMySQLIntrospectIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_MARIADB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_MYSQL_URL or TEST_MARIADB_URL is not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	parentName, childName := "parent_"+suffix, "child_"+suffix
	parent, child := QuoteIdentifier(parentName), QuoteIdentifier(childName)
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, name := range []string{child, parent} {
			if _, err := db.ExecContext(clean, "DROP TABLE IF EXISTS "+name); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
	})
	for _, query := range []string{
		"CREATE TABLE " + parent + "(id INT PRIMARY KEY) ENGINE=InnoDB",
		"CREATE TABLE " + child + "(id INT PRIMARY KEY,parent_id INT,name VARCHAR(100) UNIQUE,score INT NOT NULL DEFAULT 1," +
			"CONSTRAINT fk_" + suffix + " FOREIGN KEY(parent_id) REFERENCES " + parent + "(id),CONSTRAINT chk_" + suffix + " CHECK(score>=0)) ENGINE=InnoDB",
		"CREATE INDEX idx_score ON " + child + "(score)",
		"INSERT INTO " + parent + " VALUES(1)",
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 3; i++ {
		if _, err := db.ExecContext(ctx, "INSERT INTO "+child+"(id,parent_id,name,score) VALUES(?,1,?,?)", i, fmt.Sprintf("row%d", i), i); err != nil {
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
		if snapshot.Tables[i].Name == childName {
			table = &snapshot.Tables[i]
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
	if !columns["id"].IsPrimaryKey || !strings.HasPrefix(columns["name"].DataType, "varchar(100)") || !columns["name"].Nullable || columns["score"].Default == nil {
		t.Fatalf("columns: %+v", columns)
	}
	kinds := map[string]bool{}
	for _, c := range table.Constraints {
		kinds[c.Type] = true
		if c.Type == "FOREIGN KEY" && (c.RefTable != table.Schema+"."+parentName || len(c.RefColumns) != 1 || c.RefColumns[0] != "id") {
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
		if idx.Name == "idx_score" && strings.EqualFold(idx.Method, "btree") && len(idx.Columns) == 1 && idx.Columns[0] == "score" {
			indexed = true
		}
	}
	if !indexed {
		t.Fatal("score index missing")
	}
	for _, desc := range []bool{false, true} {
		page, err := intro.Rows(ctx, db, "", childName, contract.PageRequest{Page: 1, PageSize: 2, OrderBy: "score", Desc: desc})
		if err != nil {
			t.Fatal(err)
		}
		want := "1"
		if desc {
			want = "3"
		}
		if page.Total != 3 || len(page.Rows) != 2 || fmt.Sprint(page.Rows[0][0]) != want {
			t.Fatalf("sort desc=%t: %+v", desc, page)
		}
	}
	page, err := intro.Rows(ctx, db, "", childName, contract.PageRequest{Page: 2, PageSize: 2, OrderBy: "id"})
	if err != nil || len(page.Rows) != 1 || fmt.Sprint(page.Rows[0][0]) != "3" {
		t.Fatalf("second page: %+v err=%v", page, err)
	}
	if _, err := intro.Rows(ctx, db, "", childName, contract.PageRequest{OrderBy: "id; DROP TABLE child"}); err == nil {
		t.Fatal("unsafe sort accepted")
	}
	if _, err := intro.Rows(ctx, db, "not_the_connected_database", childName, contract.PageRequest{}); !errors.Is(err, contract.ErrAccessDenied) {
		t.Fatalf("cross-database access: %v", err)
	}
}
