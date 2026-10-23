package migration

import (
	"testing"
)

func TestModelValidation(t *testing.T) {
	tests := []struct {
		name    string
		m       Migration
		wantErr bool
	}{
		{
			name: "valid draft",
			m: Migration{
				Title:   "Create users table",
				SQLText: "CREATE TABLE users (id serial);",
				Status:  StatusDraft,
			},
			wantErr: false,
		},
		{
			name: "empty title",
			m: Migration{
				Title:   "   ",
				SQLText: "CREATE TABLE users (id serial);",
				Status:  StatusDraft,
			},
			wantErr: true,
		},
		{
			name: "empty sql",
			m: Migration{
				Title:   "Valid Title",
				SQLText: "   ",
				Status:  StatusDraft,
			},
			wantErr: true,
		},
		{
			name: "invalid status",
			m: Migration{
				Title:   "Valid Title",
				SQLText: "CREATE TABLE t (id int);",
				Status:  "unknown_status",
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.m.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestIsEditable(t *testing.T) {
	draft := &Migration{Status: StatusDraft}
	if !draft.IsEditable() {
		t.Errorf("expected draft to be editable")
	}

	success := &Migration{Status: StatusSuccess}
	if success.IsEditable() {
		t.Errorf("expected success migration to NOT be editable")
	}

	failed := &Migration{Status: StatusFailed}
	if failed.IsEditable() {
		t.Errorf("expected failed migration to NOT be editable")
	}
}

func TestComputeChecksum(t *testing.T) {
	sql1 := "CREATE TABLE users (id serial);"
	sql2 := "  CREATE TABLE users (id serial);  "
	sql3 := "DROP TABLE users;"

	h1 := ComputeChecksum(sql1)
	h2 := ComputeChecksum(sql2)
	h3 := ComputeChecksum(sql3)

	if h1 != h2 {
		t.Errorf("expected trimmed whitespace to produce equal checksums")
	}
	if h1 == h3 {
		t.Errorf("expected different queries to produce different checksums")
	}
	if len(h1) != 64 {
		t.Errorf("expected 64-char hex sha256 checksum, got %d", len(h1))
	}
}
