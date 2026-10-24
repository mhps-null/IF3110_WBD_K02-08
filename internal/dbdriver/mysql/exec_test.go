package mysql

import (
	"context"
	"testing"
)

func TestMySQLIsDDLStatement(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		expected bool
	}{
		{"create table", "CREATE TABLE products (id int);", true},
		{"alter table", "ALTER TABLE products ADD COLUMN price decimal;", true},
		{"drop table", "DROP TABLE products;", true},
		{"truncate table", "TRUNCATE products;", true},
		{"rename table", "RENAME TABLE products TO old_products;", true},
		{"select query", "SELECT * FROM products WHERE id = 1;", false},
		{"insert query", "INSERT INTO products (id) VALUES (1);", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsDDLStatement(tc.sql)
			if got != tc.expected {
				t.Errorf("IsDDLStatement(%q) = %v, expected %v", tc.sql, got, tc.expected)
			}
		})
	}
}

func TestMySQLExecutorValidation(t *testing.T) {
	exec := NewExecutor()
	ctx := context.Background()

	_, err := exec.Execute(ctx, nil, "")
	if err == nil {
		t.Errorf("expected error on empty query")
	}

	plan, err := exec.Explain(ctx, nil, "CREATE TABLE items (id int);", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Root.NodeType != "DDL Statement (EXPLAIN tidak tersedia)" {
		t.Errorf("expected DDL explanation plan, got %s", plan.Root.NodeType)
	}
}
