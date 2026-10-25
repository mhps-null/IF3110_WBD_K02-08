package schemadiff

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

func TestDiffSchemaIdentity(t *testing.T) {
	// Include a pair that collides if identity is built by joining with a dot.
	a := makeSnapshot(
		contract.Table{Schema: "public", Name: "users"},
		contract.Table{Schema: "audit", Name: "users"},
		contract.Table{Schema: "a.b", Name: "c"},
		contract.Table{Schema: "a", Name: "b.c"},
	)
	b := makeSnapshot(
		contract.Table{Schema: "a", Name: "b.c"},
		contract.Table{Schema: "a.b", Name: "c", Columns: []contract.Column{{Name: "new", DataType: "int"}}},
		contract.Table{Schema: "audit", Name: "users"},
		contract.Table{Schema: "public", Name: "users", Columns: []contract.Column{{Name: "new", DataType: "text"}}},
	)
	changes := Diff(a, b)
	if len(changes) != 2 || changes[0].TableSchema != "a.b" || changes[0].TableName != "c" || changes[1].TableSchema != "public" {
		t.Fatalf("tables from distinct schemas were conflated: %+v", changes)
	}
	if !strings.Contains(FormatChange(changes[1]), "public.users") {
		t.Fatal("change description must identify the schema")
	}
	if got := GenerateSQL(Diff(a, a), contract.EnginePostgres); got != "-- No schema changes detected." {
		t.Fatal(got)
	}
}

func TestDiffMySQLSourceNamespace(t *testing.T) {
	makeDB := func(schema string) contract.SchemaSnapshot {
		return contract.SchemaSnapshot{Engine: contract.EngineMySQL, Schema: schema, Tables: []contract.Table{{
			Schema: schema, Name: "child", Constraints: []contract.Constraint{{
				Name: "fk_child", Type: "FOREIGN KEY", Columns: []string{"parent_id"}, RefTable: schema + ".parent.table",
				RefSchema: schema, RefTableName: "parent.table", RefColumns: []string{"id"}, OnDelete: "CASCADE", OnUpdate: "RESTRICT",
			}},
		}}}
	}
	a, b := makeDB("source"), makeDB("reference")
	if changes := Diff(a, b); len(changes) != 0 {
		t.Fatalf("database names alone must not cause DROP/CREATE or FK changes: %+v", changes)
	}
	if b.Tables[0].Schema != "reference" || b.Tables[0].Constraints[0].RefSchema != "reference" {
		t.Fatal("diff mutated the reference snapshot")
	}
	empty := contract.SchemaSnapshot{Engine: contract.EngineMySQL, Schema: "empty_source"}
	sql := GenerateSQL(Diff(empty, b), contract.EngineMySQL)
	if strings.Contains(sql, "`reference`") || !strings.Contains(sql, "REFERENCES `empty_source`.`parent.table`") {
		t.Fatalf("generated SQL must use the source even when it has no tables: %s", sql)
	}
}

func TestDiffPartialIndexAndForeignKeyRules(t *testing.T) {
	old := contract.Table{Schema: "public", Name: "child",
		Indexes:     []contract.Index{{Name: "partial", Columns: []string{"id"}, Predicate: "id > 0"}},
		Constraints: []contract.Constraint{{Name: "fk", Type: "FOREIGN KEY", OnDelete: "RESTRICT", OnUpdate: "RESTRICT"}},
	}
	updated := old
	updated.Indexes = append([]contract.Index(nil), old.Indexes...)
	updated.Constraints = append([]contract.Constraint(nil), old.Constraints...)
	updated.Indexes[0].Predicate = "id > 1"
	updated.Constraints[0].OnUpdate = "CASCADE"
	counts := map[ChangeType]int{}
	for _, change := range Diff(makeSnapshot(old), makeSnapshot(updated)) {
		counts[change.Type]++
	}
	if !reflect.DeepEqual(counts, map[ChangeType]int{DropIndex: 1, AddIndex: 1, DropConstraint: 1, AddConstraint: 1}) {
		t.Fatalf("predicate/action changes were lost: %+v", counts)
	}
}

func TestGenerateSQLGolden(t *testing.T) {
	for _, engine := range []contract.Engine{contract.EnginePostgres, contract.EngineMySQL, contract.EngineMariaDB} {
		t.Run(string(engine), func(t *testing.T) {
			changes := []Change{
				{Type: CreateTable, TableSchema: "app space", TableName: "select", TableDef: &contract.Table{Schema: "app space", Name: "select", Columns: []contract.Column{{Name: "a\" b`c()", DataType: "int", Nullable: true}}}},
				{Type: DropIndex, TableSchema: "app space", TableName: "order.items", Index: &contract.Index{Name: "idx\" space`"}},
				{Type: AddColumn, TableSchema: "audit", TableName: "order.items", ColumnName: "select", NewColumn: &contract.Column{DataType: "text", Nullable: true}},
				{Type: AddConstraint, TableSchema: "app space", TableName: "order.items", Constraint: &contract.Constraint{Name: "fk\" space`", Type: "FOREIGN KEY", Columns: []string{"a\" b`c()"}, RefSchema: "app space", RefTableName: "parent.table", RefColumns: []string{"select"}, OnDelete: "CASCADE", OnUpdate: "SET NULL"}},
				{Type: AddIndex, TableSchema: "app space", TableName: "order.items", Index: &contract.Index{Name: "idx\" space`", Columns: []string{"a\" b`c()", "select"}, Method: "btree"}},
			}
			if engine == contract.EnginePostgres {
				changes[len(changes)-1].Index.Predicate = `"select" IS NOT NULL`
			}
			want, err := os.ReadFile(filepath.Join("testdata", string(engine)+".golden.sql"))
			if err != nil {
				t.Fatal(err)
			}
			if got := GenerateSQL(changes, engine); got != strings.TrimSpace(strings.ReplaceAll(string(want), "\r\n", "\n")) {
				t.Fatalf("SQL differs from reviewed golden:\n%s", got)
			}
		})
	}
}

func TestGenerateSQLMySQLKeyDependencies(t *testing.T) {
	changes := []Change{
		{Type: AddConstraint, TableName: "a_child", Constraint: &contract.Constraint{Name: "fk_parent", Type: "FOREIGN KEY", Columns: []string{"parent_id"}, RefTableName: "z_parent", RefColumns: []string{"id"}}},
		{Type: AddIndex, TableName: "a_child", Index: &contract.Index{Name: "fk_parent", Columns: []string{"parent_id"}}},
		{Type: AddConstraint, TableName: "z_parent", Constraint: &contract.Constraint{Name: "PRIMARY", Type: "PRIMARY KEY", Columns: []string{"id"}}},
	}
	sql := GenerateSQL(changes, contract.EngineMariaDB)
	pk := strings.Index(sql, "ALTER TABLE `z_parent` ADD PRIMARY KEY (`id`);")
	index := strings.Index(sql, "CREATE INDEX `fk_parent`")
	fk := strings.Index(sql, "ADD CONSTRAINT `fk_parent`")
	if pk < 0 || index < 0 || fk < pk || fk < index {
		t.Fatalf("unique keys and explicit indexes must precede the FK without naming PRIMARY as a constraint:\n%s", sql)
	}
}
