package postgres

import (
	"context"
	"testing"
)

func TestIsNonTransactionalStatement(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		expected bool
	}{
		{"normal create table", "CREATE TABLE users (id serial);", false},
		{"normal create index", "CREATE INDEX idx_name ON users(name);", false},
		{"create index concurrently", "CREATE INDEX CONCURRENTLY idx_name ON users(name);", true},
		{"drop index concurrently", "DROP INDEX CONCURRENTLY idx_name;", true},
		{"vacuum full", "VACUUM FULL users;", true},
		{"vacuum normal", "VACUUM;", true},
		{"cluster", "CLUSTER users USING idx_name;", true},
		{"insert statement", "INSERT INTO users (name) VALUES ('alice');", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsNonTransactionalStatement(tc.sql)
			if got != tc.expected {
				t.Errorf("IsNonTransactionalStatement(%q) = %v, expected %v", tc.sql, got, tc.expected)
			}
		})
	}
}

func TestIsDDLStatement(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		expected bool
	}{
		{"create table", "CREATE TABLE posts (id int);", true},
		{"alter table", "ALTER TABLE posts ADD COLUMN title text;", true},
		{"drop table", "DROP TABLE posts;", true},
		{"truncate table", "TRUNCATE posts;", true},
		{"select query", "SELECT * FROM posts;", false},
		{"insert query", "INSERT INTO posts (title) VALUES ('test');", false},
		{"update query", "UPDATE posts SET title = 'new';", false},
		{"delete query", "DELETE FROM posts WHERE id = 1;", false},
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

func TestPostgresExecutorValidation(t *testing.T) {
	exec := NewExecutor()
	ctx := context.Background()

	// Empty query
	_, err := exec.Execute(ctx, nil, "   ")
	if err == nil {
		t.Errorf("expected error executing empty query")
	}

	// Non-transactional query rejection
	_, err = exec.Execute(ctx, nil, "CREATE INDEX CONCURRENTLY idx_test ON test(col);")
	if err == nil {
		t.Errorf("expected rejection of non-transactional statement")
	}

	// DDL in explain returns graceful plan without error
	plan, err := exec.Explain(ctx, nil, "CREATE TABLE demo (id int);", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Root.NodeType != "DDL Statement (EXPLAIN tidak tersedia)" {
		t.Errorf("expected DDL explanation plan, got %s", plan.Root.NodeType)
	}
}
