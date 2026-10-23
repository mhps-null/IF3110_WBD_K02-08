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
