package access

import (
	"testing"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

func TestMinLevelKnownActions(t *testing.T) {
	cases := []struct {
		action Action
		want   contract.AccessLevel
	}{
		{ActionViewSchema, contract.AccessViewOnly},
		{ActionViewData, contract.AccessViewOnly},
		{ActionViewHistory, contract.AccessViewOnly},
		{ActionViewSchemaDiff, contract.AccessViewOnly},
		{ActionCreateMigration, contract.AccessViewEdit},
		{ActionEditMigration, contract.AccessViewEdit},
		{ActionExecuteMigration, contract.AccessViewEdit},
	}
	for _, c := range cases {
		got, ok := MinLevel(c.action)
		if !ok {
			t.Fatalf("%s: expected a known action", c.action)
		}
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.action, got, c.want)
		}
	}
}

func TestMinLevelUnknownAction(t *testing.T) {
	if _, ok := MinLevel(Action("delete_everything")); ok {
		t.Fatal("unknown action must not be reported as known")
	}
}

func TestAllows(t *testing.T) {
	cases := []struct {
		level  contract.AccessLevel
		action Action
		want   bool
	}{
		// read actions
		{contract.AccessNone, ActionViewSchema, false},
		{contract.AccessViewOnly, ActionViewSchema, true},
		{contract.AccessViewEdit, ActionViewSchema, true},

		// write actions
		{contract.AccessNone, ActionCreateMigration, false},
		{contract.AccessViewOnly, ActionCreateMigration, false},
		{contract.AccessViewEdit, ActionCreateMigration, true},

		// unknown actions
		{contract.AccessViewEdit, Action("unknown"), false},
	}
	for _, c := range cases {
		if got := Allows(c.level, c.action); got != c.want {
			t.Errorf("Allows(%v, %s) = %v, want %v", c.level, c.action, got, c.want)
		}
	}
}
