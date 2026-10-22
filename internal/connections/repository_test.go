package connections

import (
	"context"
	"errors"
	"testing"

	"github.com/example/if3110-milestone1/internal/contract"
)

// Smoke test for the fake used by handler/pool tests; real SQL CRUD has an integration test.
func TestRepository(t *testing.T) {
	repo, err := NewMemoryRepository(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn := &Connection{Name: "test", Engine: contract.EnginePostgres}
	if err := repo.Create(ctx, conn, "secret"); err != nil {
		t.Fatal(err)
	}
	_, password, err := repo.FindByIDWithPassword(ctx, conn.ID)
	if err != nil || password != "secret" {
		t.Fatalf("password=%q err=%v", password, err)
	}
	list, err := repo.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%v err=%v", list, err)
	}
	repo.SetMigrationCount(conn.ID, 1)
	if err := repo.Delete(ctx, conn.ID); !errors.Is(err, ErrHasMigrationHistory) {
		t.Fatalf("delete with history: %v", err)
	}
	repo.SetMigrationCount(conn.ID, 0)
	if err := repo.Delete(ctx, conn.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FindByID(ctx, conn.ID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("deleted connection: %v", err)
	}
}
