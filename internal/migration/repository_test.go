package migration

import (
	"context"
	"testing"
)

func TestMemoryRepositoryLifecycle(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	m1 := &Migration{
		DBID:      1,
		Title:     "Add posts table",
		SQLText:   "CREATE TABLE posts (id int);",
		CreatedBy: 42,
	}
	if err := repo.CreateDraft(ctx, m1); err != nil {
		t.Fatalf("failed to create draft: %v", err)
	}
	if m1.ID == 0 {
		t.Errorf("expected generated ID")
	}
	if m1.Status != StatusDraft {
		t.Errorf("expected status 'draft', got '%s'", m1.Status)
	}

	fetched, err := repo.GetByID(ctx, m1.ID)
	if err != nil {
		t.Fatalf("failed to get draft: %v", err)
	}
	if fetched.Title != "Add posts table" {
		t.Errorf("expected title 'Add posts table', got '%s'", fetched.Title)
	}

	fetched.Title = "Add posts table updated"
	fetched.SQLText = "CREATE TABLE posts (id bigint);"
	if err := repo.UpdateDraft(ctx, fetched); err != nil {
		t.Fatalf("failed to update draft: %v", err)
	}

	updated, _ := repo.GetByID(ctx, m1.ID)
	if updated.Title != "Add posts table updated" {
		t.Errorf("expected updated title")
	}

	checksum := ComputeChecksum(updated.SQLText)
	executed, err := repo.RecordExecution(ctx, m1.ID, StatusSuccess, 99, nil, checksum)
	if err != nil {
		t.Fatalf("failed to record execution: %v", err)
	}
	if executed.Version == nil || *executed.Version != 1 {
		t.Errorf("expected version 1, got %v", executed.Version)
	}
	if executed.Status != StatusSuccess {
		t.Errorf("expected status 'success', got '%s'", executed.Status)
	}

	err = repo.UpdateDraft(ctx, &Migration{
		ID:      m1.ID,
		Title:   "Hacking executed migration",
		SQLText: "DROP TABLE all;",
	})
	if err == nil {
		t.Errorf("expected error when trying to update executed migration")
	}

	m2 := &Migration{
		DBID:      1,
		Title:     "Add comments table",
		SQLText:   "CREATE TABLE comments (id int);",
		CreatedBy: 42,
	}
	_ = repo.CreateDraft(ctx, m2)
	executed2, err := repo.RecordExecution(ctx, m2.ID, StatusSuccess, 99, nil, ComputeChecksum(m2.SQLText))
	if err != nil {
		t.Fatalf("failed to record second execution: %v", err)
	}
	if executed2.Version == nil || *executed2.Version != 2 {
		t.Errorf("expected sequential version 2, got %v", executed2.Version)
	}

	list, err := repo.ListByDB(ctx, 1)
	if err != nil {
		t.Fatalf("failed to list migrations: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 migrations in list, got %d", len(list))
	}
}
