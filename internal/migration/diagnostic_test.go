package migration

import (
	"errors"
	"testing"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

func TestSanitizeErrorText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "clean simple error",
			input:    "syntax error at or near \"CREATE\"",
			expected: "syntax error at or near \"CREATE\"",
		},
		{
			name:     "strip pq prefix",
			input:    "pq: relation \"employees\" does not exist",
			expected: "relation \"employees\" does not exist",
		},
		{
			name:     "mask password",
			input:    "connect failed: password=mysecretpassword host=localhost",
			expected: "connect failed: password=REDACTED host=localhost",
		},
		{
			name:     "mask URI credentials",
			input:    "failed to open postgres://user:secret123@localhost:5432/db",
			expected: "failed to open postgres://user:REDACTED@localhost:5432/db",
		},
		{
			name:     "mask server paths",
			input:    "error at /home/user/project/file.sql line 42",
			expected: "error at [internal_path] line 42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeErrorText(tt.input)
			if got != tt.expected {
				t.Errorf("SanitizeErrorText(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestDiagnoseErrorCategories(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		engine       contract.Engine
		wantState    string
		wantCategory string
	}{
		{
			name:         "syntax error",
			err:          errors.New("syntax error at or near \"CREATE\""),
			engine:       contract.EnginePostgres,
			wantState:    "42601",
			wantCategory: "Kesalahan Sintaks SQL (Syntax Error)",
		},
		{
			name:         "undefined table",
			err:          errors.New("relation \"employees\" does not exist"),
			engine:       contract.EnginePostgres,
			wantState:    "42P01",
			wantCategory: "Tabel Tidak Ditemukan (Undefined Table)",
		},
		{
			name:         "undefined column",
			err:          errors.New("column \"email\" does not exist"),
			engine:       contract.EnginePostgres,
			wantState:    "42703",
			wantCategory: "Kolom Tidak Ditemukan (Undefined Column)",
		},
		{
			name:         "unique violation",
			err:          errors.New("duplicate key value violates unique constraint \"idx_email\""),
			engine:       contract.EnginePostgres,
			wantState:    "23505",
			wantCategory: "Pelanggaran Batasan Unik (Unique Violation)",
		},
		{
			name:         "mysql table missing",
			err:          errors.New("Error 1146 (42S02): Table 'test.items' doesn't exist"),
			engine:       contract.EngineMySQL,
			wantState:    "42S02",
			wantCategory: "Tabel Tidak Ditemukan (Undefined Table)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diag := DiagnoseError(tt.err, nil, tt.engine)
			if diag == nil {
				t.Fatal("expected diagnostic, got nil")
			}
			if diag.SQLState != tt.wantState {
				t.Errorf("SQLState = %q, want %q", diag.SQLState, tt.wantState)
			}
			if diag.Category != tt.wantCategory {
				t.Errorf("Category = %q, want %q", diag.Category, tt.wantCategory)
			}
			if diag.CauseMessage == "" || diag.Suggestion == "" {
				t.Error("CauseMessage and Suggestion must not be empty")
			}
			if diag.RollbackStatus == "" || diag.RollbackNote == "" {
				t.Error("RollbackStatus and RollbackNote must not be empty")
			}
		})
	}
}
