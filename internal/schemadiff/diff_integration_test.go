//go:build integration

package schemadiff

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	my "github.com/mhps-null/IF3110_WBD_K02-08/internal/dbdriver/mysql"
	pg "github.com/mhps-null/IF3110_WBD_K02-08/internal/dbdriver/postgres"
)

func diffIntegrationDB(t *testing.T, driver, dsn string) (*sql.DB, context.Context) {
	t.Helper()
	if dsn == "" {
		t.Skip("integration database URL is not set")
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return db, ctx
}

func TestSchemaDiffPostgresIntegration(t *testing.T) {
	db, ctx := diffIntegrationDB(t, "pgx", os.Getenv("TEST_DATABASE_URL"))
	schema := fmt.Sprintf("test_diff_%d", time.Now().UnixNano())
	other := schema + " extra"
	tableName := "order.\"items`"
	parent, child := qualify(schema, tableName, contract.EnginePostgres), qualify(other, tableName, contract.EnginePostgres)
	for _, name := range []string{schema, other} {
		if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+quote(name, contract.EnginePostgres)); err != nil {
			t.Fatal(err)
		}
		name := name
		t.Cleanup(func() {
			if _, err := db.Exec("DROP SCHEMA IF EXISTS " + quote(name, contract.EnginePostgres) + " CASCADE"); err != nil {
				t.Error(err)
			}
		})
	}
	for _, query := range []string{
		"CREATE TABLE " + parent + " (id int PRIMARY KEY, \"select\" text, \"a\"\" b`c()\" int)",
		"CREATE TABLE " + child + " (id int PRIMARY KEY)",
		"ALTER TABLE " + child + " ADD CONSTRAINT \"fk\"\" rule`\" FOREIGN KEY(id) REFERENCES " + parent + "(id) ON DELETE CASCADE ON UPDATE CASCADE",
		"CREATE INDEX \"idx\"\" partial`\" ON " + parent + " (\"a\"\" b`c()\" DESC) WHERE \"select\" IS NOT NULL",
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	intro := pg.NewIntrospector()
	filter := func(snapshot contract.SchemaSnapshot) contract.SchemaSnapshot {
		result := contract.SchemaSnapshot{Engine: contract.EnginePostgres}
		for _, table := range snapshot.Tables {
			if table.Schema == schema || table.Schema == other {
				result.Tables = append(result.Tables, table)
			}
		}
		return result
	}
	desired, err := intro.Snapshot(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	desired = filter(desired)
	if len(desired.Tables) != 2 {
		t.Fatal("same-named tables from two schemas were lost")
	}
	foundPartial := false
	for _, table := range desired.Tables {
		for _, index := range table.Indexes {
			if index.Name == "idx\" partial`" {
				foundPartial = true
				if index.Predicate == "" || len(index.KeyDefinitions) != 1 || !strings.Contains(index.KeyDefinitions[0], "DESC") {
					t.Fatalf("partial index metadata missing: %+v", index)
				}
			}
		}
	}
	if !foundPartial {
		t.Fatal("partial index missing from snapshot")
	}
	for _, name := range []string{child, parent} {
		if _, err := db.ExecContext(ctx, "DROP TABLE "+name); err != nil {
			t.Fatal(err)
		}
	}
	generated := GenerateSQL(Diff(contract.SchemaSnapshot{Engine: contract.EnginePostgres}, desired), contract.EnginePostgres)
	if _, err := db.ExecContext(ctx, generated); err != nil {
		t.Fatalf("generated PostgreSQL DDL failed: %v\n%s", err, generated)
	}
	actual, err := intro.Snapshot(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if changes := Diff(filter(actual), desired); len(changes) != 0 {
		t.Fatalf("round-trip PostgreSQL diff must be empty: %s", GenerateSQL(changes, contract.EnginePostgres))
	}
}

func TestSchemaDiffMySQLIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_MARIADB_URL")
	}
	db, ctx := diffIntegrationDB(t, "mysql", dsn)
	var activeDB string
	if err := db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&activeDB); err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("test_diff_%d", time.Now().UnixNano())
	parentName, childName := prefix+" parent.`", prefix+" child.`"
	parent, child := qualify(activeDB, parentName, contract.EngineMySQL), qualify(activeDB, childName, contract.EngineMySQL)
	t.Cleanup(func() {
		for _, table := range []string{child, parent} {
			if _, err := db.Exec("DROP TABLE IF EXISTS " + table); err != nil {
				t.Error(err)
			}
		}
	})
	for _, query := range []string{
		"CREATE TABLE " + parent + " (`select` int PRIMARY KEY)",
		"CREATE TABLE " + child + " (id int PRIMARY KEY, `parent.id` int, `a\" b``c()` int)",
		"ALTER TABLE " + child + " ADD CONSTRAINT " + quote(prefix+" fk`", contract.EngineMySQL) + " FOREIGN KEY (`parent.id`) REFERENCES " + parent + " (`select`) ON DELETE SET NULL ON UPDATE CASCADE",
		"CREATE INDEX `idx`` special` ON " + child + " (`a\" b``c()`)",
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	intro := my.NewIntrospector()
	filter := func(snapshot contract.SchemaSnapshot) contract.SchemaSnapshot {
		result := contract.SchemaSnapshot{Engine: contract.EngineMySQL, Schema: activeDB}
		for _, table := range snapshot.Tables {
			if table.Name == parentName || table.Name == childName {
				result.Tables = append(result.Tables, table)
			}
		}
		return result
	}
	desired, err := intro.Snapshot(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	desired = filter(desired)
	if len(desired.Tables) != 2 {
		t.Fatal("fixture tables missing from snapshot")
	}
	foundFK := false
	for _, table := range desired.Tables {
		for _, fk := range table.Constraints {
			if fk.Type == "FOREIGN KEY" {
				foundFK = true
				if fk.RefTableName != parentName || fk.RefSchema != activeDB || fk.OnDelete != "SET NULL" || fk.OnUpdate != "CASCADE" {
					t.Fatalf("foreign key metadata lost: %+v", fk)
				}
			}
		}
	}
	if !foundFK {
		t.Fatal("foreign key missing from snapshot")
	}
	for _, name := range []string{child, parent} {
		if _, err := db.ExecContext(ctx, "DROP TABLE "+name); err != nil {
			t.Fatal(err)
		}
	}
	changes := sortChanges(Diff(contract.SchemaSnapshot{Engine: contract.EngineMySQL, Schema: activeDB}, desired))
	for _, change := range changes {
		query := strings.TrimSpace(generateSingle(change, contract.EngineMySQL))
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatalf("generated MySQL DDL failed: %v\n%s", err, query)
		}
	}
	actual, err := intro.Snapshot(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if changes := Diff(filter(actual), desired); len(changes) != 0 {
		t.Fatalf("round-trip MySQL diff must be empty: %s", GenerateSQL(changes, contract.EngineMySQL))
	}
}
