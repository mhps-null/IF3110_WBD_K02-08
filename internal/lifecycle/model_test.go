package lifecycle

import "testing"

func TestValidateDefinition(t *testing.T) {
	good := DefinitionInput{DBID: 1, Title: "add", SQLUp: "ALTER TABLE a ADD COLUMN b int", SQLDown: "ALTER TABLE a DROP COLUMN b"}
	if err := ValidateDefinition(good); err != nil {
		t.Fatal(err)
	}

	bad := []DefinitionInput{{DBID: 1, Title: "x", SQLUp: "select 1"}, {DBID: 1, Title: "x", SQLUp: "select 1", SQLDown: "select 2", IrreversibleReason: "unsafe"}, {DBID: 1, Title: "x", SQLUp: "select 1", IrreversibleReason: " "}}
	for _, v := range bad {
		if ValidateDefinition(v) == nil {
			t.Fatalf("expected invalid: %+v", v)
		}
	}
	if Checksum("a", "b", "") == Checksum("a", "c", "") {
		t.Fatal("checksum not sensitive to SQL down")
	}
}

func TestHideRollbackByExecutionID(t *testing.T) {
	id := int64(2)
	h := []Execution{
		{ID: 1, AttemptID: 1, Action: Up, Status: Success},
		{ID: 2, AttemptID: 2, Action: Up, Status: Success},
		{ID: 3, AttemptID: 3, Action: Rollback, Status: Failed, RollbackOf: &id},
		{ID: 4, AttemptID: 4, Action: Rollback, Status: Success, RollbackOf: &id},
		{ID: 5, AttemptID: 5, Action: Forward, Status: Success},
	}
	shown := HideRollback(h)
	if len(shown) != 3 || shown[0].ID != 1 || shown[1].ID != 3 || shown[2].ID != 5 {
		t.Fatalf("unexpected history %+v", shown)
	}
	if len(h) != 5 {
		t.Fatal("original history mutated")
	}
}

func TestHideRollbackKeepsFailedAttemptsForSameExecution(t *testing.T) {
	target := int64(20)
	history := []Execution{
		{ID: 20, AttemptID: 201, Action: Up, Status: Failed, Attempt: 1},
		{ID: 20, AttemptID: 202, Action: Up, Status: Success, Attempt: 2},
		{ID: 30, AttemptID: 301, Action: Rollback, Status: Failed, RollbackOf: &target, Attempt: 1},
		{ID: 30, AttemptID: 302, Action: Rollback, Status: Success, RollbackOf: &target, Attempt: 2},
		{ID: 40, AttemptID: 401, Action: Forward, Status: Success, Attempt: 1},
	}

	got := HideRollback(history)
	if len(got) != 3 || got[0].AttemptID != 201 || got[1].AttemptID != 301 || got[2].AttemptID != 401 {
		t.Fatalf("failed attempts and later forward should remain visible: %+v", got)
	}
}

func TestLinearOrder(t *testing.T) {
	v := []committed{{id: 1, migration: 10, action: Up}, {id: 2, migration: 20, action: Up}}
	stack, err := activeStack(v)
	if err != nil || len(stack) != 2 {
		t.Fatalf("%v %v", stack, err)
	}

	target := int64(2)
	v = append(v, committed{id: 3, migration: 20, action: Rollback, target: &target})
	stack, err = activeStack(v)
	if err != nil || len(stack) != 1 || stack[0].id != 1 {
		t.Fatalf("%v %v", stack, err)
	}

	v = append(v, committed{id: 4, migration: 20, action: Forward})
	stack, err = activeStack(v)
	if err != nil || len(stack) != 2 || stack[1].id != 4 {
		t.Fatalf("%v %v", stack, err)
	}

	target = 1
	v = append(v, committed{id: 5, migration: 10, action: Rollback, target: &target})
	if _, err = activeStack(v); err == nil {
		t.Fatal("out-of-order rollback accepted")
	}
}
