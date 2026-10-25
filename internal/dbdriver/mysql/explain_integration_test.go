//go:build integration

package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

func TestExplainIntegration(t *testing.T) {
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
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var version string
	if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("server version: %s", version)
	table := QuoteIdentifier(fmt.Sprintf("test_explain_%d", time.Now().UnixNano()))
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (id INT PRIMARY KEY,value VARCHAR(20)) ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := db.ExecContext(clean, "DROP TABLE "+table); err != nil {
			t.Errorf("fixture cleanup: %v", err)
		}
	}()
	if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES(1,'original'),(2,'original')"); err != nil {
		t.Fatal(err)
	}
	executor := NewExecutor()
	query := "SELECT * FROM " + table + " WHERE id>0"
	estimated, err := executor.Explain(ctx, db, query, false)
	if err != nil || estimated.Analyzed || estimated.Raw == "" {
		t.Fatalf("estimated plan: %+v err=%v", estimated, err)
	}
	actual, err := executor.Explain(ctx, db, query, true)
	if err != nil {
		t.Fatal(err)
	}
	if !actual.Analyzed {
		if !strings.HasPrefix(version, "5.") || actual.Notice == "" {
			t.Fatalf("expected actual analysis on this server: %+v", actual)
		}
	} else {
		var hasMetrics func(contract.PlanNode) bool
		hasMetrics = func(node contract.PlanNode) bool {
			if node.ActualRowsPerLoop != nil && node.ActualLoops != nil && node.ActualTimeMs != nil {
				return true
			}
			for _, child := range node.Children {
				if hasMetrics(child) {
					return true
				}
			}
			return false
		}
		if !hasMetrics(actual.Root) {
			t.Fatalf("actual node metrics missing: %+v", actual)
		}
	}
	writePlan, err := executor.Explain(ctx, db, "UPDATE "+table+" SET value='changed'", true)
	if err != nil || writePlan.Analyzed || writePlan.Notice == "" {
		t.Fatalf("write must use estimated fallback: %+v err=%v", writePlan, err)
	}
	var changed int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE value='changed'").Scan(&changed); err != nil || changed != 0 {
		t.Fatalf("analysis modified fixture data: changed=%d err=%v", changed, err)
	}
	if _, err := executor.Explain(ctx, db, "SELECT missing_column FROM "+table, true); err == nil {
		t.Fatal("invalid SQL must not be reported as a successful fallback")
	}
}
