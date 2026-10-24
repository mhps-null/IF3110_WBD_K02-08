package schemadiff

import (
	"testing"

	"github.com/example/if3110-milestone1/internal/contract"
)

func ptr[T any](v T) *T { return &v }

func makeSnapshot(tables ...contract.Table) contract.SchemaSnapshot {
	return contract.SchemaSnapshot{Tables: tables}
}

func TestDiff_CreateAndDropTable(t *testing.T) {
	snapA := makeSnapshot(
		contract.Table{Name: "users"},
	)
	snapB := makeSnapshot(
		contract.Table{Name: "users"},
		contract.Table{Name: "posts"},
	)

	changes := Diff(snapA, snapB)

	if len(changes) != 1 {
		t.Fatalf("want 1 change, got %d", len(changes))
	}
	if changes[0].Type != CreateTable || changes[0].TableName != "posts" {
		t.Errorf("want CreateTable posts, got %+v", changes[0])
	}
	if changes[0].Destructive {
		t.Error("CreateTable must not be destructive")
	}
}

func TestDiff_DropTableIsDestructive(t *testing.T) {
	snapA := makeSnapshot(
		contract.Table{Name: "users"},
		contract.Table{Name: "logs"},
	)
	snapB := makeSnapshot(
		contract.Table{Name: "users"},
	)

	changes := Diff(snapA, snapB)

	if len(changes) != 1 {
		t.Fatalf("want 1 change, got %d", len(changes))
	}
	if changes[0].Type != DropTable || changes[0].TableName != "logs" {
		t.Errorf("want DropTable logs, got %+v", changes[0])
	}
	if !changes[0].Destructive {
		t.Error("DropTable must be destructive")
	}
}

func TestDiff_AddAndDropColumn(t *testing.T) {
	snapA := makeSnapshot(contract.Table{
		Name: "users",
		Columns: []contract.Column{
			{Name: "id", DataType: "int", Nullable: false},
			{Name: "old_col", DataType: "text", Nullable: true},
		},
	})

	snapB := makeSnapshot(contract.Table{
		Name: "users",
		Columns: []contract.Column{
			{Name: "id", DataType: "int", Nullable: false},
			{Name: "new_col", DataType: "varchar", Nullable: true},
		},
	})

	changes := Diff(snapA, snapB)

	if len(changes) != 2 {
		t.Fatalf("want 2 changes, got %d", len(changes))
	}

	var hasAdd, hasDrop bool
	for _, c := range changes {
		if c.Type == AddColumn && c.ColumnName == "new_col" {
			hasAdd = true
		}
		if c.Type == DropColumn && c.ColumnName == "old_col" && c.Destructive {
			hasDrop = true
		}
	}

	if !hasAdd || !hasDrop {
		t.Errorf("missing expected column changes: add=%v drop=%v", hasAdd, hasDrop)
	}
}

func TestDiff_AlterColumn(t *testing.T) {
	snapA := makeSnapshot(contract.Table{
		Name: "users",
		Columns: []contract.Column{
			{Name: "email", DataType: "varchar(100)", Nullable: true},
		},
	})

	snapB := makeSnapshot(contract.Table{
		Name: "users",
		Columns: []contract.Column{
			{Name: "email", DataType: "varchar(255)", Nullable: false},
		},
	})

	changes := Diff(snapA, snapB)

	if len(changes) != 1 {
		t.Fatalf("want 1 change, got %d", len(changes))
	}

	c := changes[0]

	if c.Type != AlterColumn || c.ColumnName != "email" {
		t.Fatalf("want AlterColumn email, got %+v", c)
	}
	if !c.Destructive {
		t.Error("altering to NOT NULL must be destructive")
	}
}

func TestDiff_ConstraintsAndIndexes(t *testing.T) {
	snapshotA := makeSnapshot(contract.Table{
		Name: "users",
		Constraints: []contract.Constraint{
			{Name: "fk_users_team", Type: "FOREIGN KEY", Columns: []string{"team_id"}, RefTable: "teams", RefColumns: []string{"id"}},
			{Name: "uq_users_email", Type: "UNIQUE", Columns: []string{"email"}},
		},
		Indexes: []contract.Index{
			{Name: "uq_users_email", Columns: []string{"email"}, Unique: true, Method: "btree"},
			{Name: "idx_users_name", Columns: []string{"name"}, Method: "btree"},
			{Name: "idx_obsolete", Columns: []string{"legacy"}, Method: "btree"},
		},
	})
	snapshotB := makeSnapshot(contract.Table{
		Name: "users",
		Constraints: []contract.Constraint{
			{Name: "fk_users_team", Type: "FOREIGN KEY", Columns: []string{"team_id"}, RefTable: "groups", RefColumns: []string{"id"}},
			{Name: "uq_users_email", Type: "UNIQUE", Columns: []string{"email"}},
			{Name: "ck_users_email", Type: "CHECK", Definition: "email <> ''"},
		},
		Indexes: []contract.Index{
			{Name: "uq_users_email", Columns: []string{"email"}, Unique: true, Method: "btree"},
			{Name: "idx_users_name", Columns: []string{"name", "id"}, Method: "btree"},
			{Name: "idx_users_active", Columns: []string{"active"}, Method: "btree"},
		},
	})

	changes := Diff(snapshotA, snapshotB)
	counts := map[ChangeType]map[string]int{}
	for _, change := range changes {
		if counts[change.Type] == nil {
			counts[change.Type] = map[string]int{}
		}
		counts[change.Type][changeObjectName(change)]++
	}
	for _, expected := range []struct {
		typeName ChangeType
		name     string
		count    int
	}{
		{DropConstraint, "fk_users_team", 1},
		{AddConstraint, "fk_users_team", 1},
		{AddConstraint, "ck_users_email", 1},
		{DropIndex, "idx_obsolete", 1},
		{DropIndex, "idx_users_name", 1},
		{AddIndex, "idx_users_name", 1},
		{AddIndex, "idx_users_active", 1},
	} {
		if got := counts[expected.typeName][expected.name]; got != expected.count {
			t.Errorf("%s %q count=%d, want %d; changes=%+v", expected.typeName, expected.name, got, expected.count, changes)
		}
	}
	if counts[DropIndex]["uq_users_email"] != 0 || counts[AddIndex]["uq_users_email"] != 0 {
		t.Fatal("index backing an unchanged UNIQUE constraint must not generate separate index changes")
	}
}

func TestDiff_NewTableIncludesConstraintsAndNonConstraintIndexes(t *testing.T) {
	snapshot := makeSnapshot(contract.Table{
		Name:        "teams",
		Columns:     []contract.Column{{Name: "id", DataType: "integer", Nullable: false}},
		Constraints: []contract.Constraint{{Name: "teams_pkey", Type: "PRIMARY KEY", Columns: []string{"id"}}},
		Indexes: []contract.Index{
			{Name: "teams_pkey", Columns: []string{"id"}, Unique: true, Method: "btree"},
			{Name: "idx_teams_name", Columns: []string{"name"}, Method: "btree"},
		},
	})
	changes := Diff(makeSnapshot(), snapshot)
	counts := map[ChangeType]int{}
	for _, change := range changes {
		counts[change.Type]++
	}
	if counts[CreateTable] != 1 || counts[AddConstraint] != 1 || counts[AddIndex] != 1 {
		t.Fatalf("new table must create table, PK constraint, and non-constraint index; got %+v", changes)
	}
	for _, change := range changes {
		if change.Type == AddIndex && change.Index.Name == "teams_pkey" {
			t.Fatal("primary-key backing index must not be generated separately")
		}
	}
}
