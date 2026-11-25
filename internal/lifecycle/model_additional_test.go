package lifecycle

import "testing"

func TestDefinitionSemantics(t *testing.T) {
	good := DefinitionInput{DBID: 1, Title: "x", SQLUp: "ALTER TABLE a ADD COLUMN b int", SQLDown: "ALTER TABLE a DROP COLUMN b"}
	if err := ValidateDefinition(good); err != nil {
		t.Fatal(err)
	}

	good.SQLDown = ""
	good.IrreversibleReason = "data destruction"
	if err := ValidateDefinition(good); err != nil {
		t.Fatal(err)
	}

	good.SQLDown = "SELECT 1"
	if err := ValidateDefinition(good); err == nil {
		t.Fatal("down+reason must fail")
	}
	if Checksum("a", "b", "") == Checksum("a", "c", "") {
		t.Fatal("checksum mismatch expected")
	}
}

func TestHistoryHiding(t *testing.T) {
	target := int64(11)
	input := []Execution{
		{ID: 11, AttemptID: 1, Action: Up, Status: Success},
		{ID: 12, AttemptID: 2, Action: Rollback, Status: Failed, RollbackOf: &target},
		{ID: 13, AttemptID: 3, Action: Rollback, Status: Success, RollbackOf: &target},
		{ID: 14, AttemptID: 4, Action: Forward, Status: Success},
	}
	got := HideRollback(input)
	if len(got) != 2 || got[0].ID != 12 || got[1].ID != 14 {
		t.Fatalf("bad %v", got)
	}
}
