//go:build integration

package connections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/example/if3110-milestone1/internal/contract"
	_ "github.com/jackc/pgx/v5/stdlib"
)


func TestSQLRepositoryIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	repo, err := NewSQLRepository(db, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	conn := &Connection{Name: fmt.Sprintf("test_%d", time.Now().UnixNano()), Engine: contract.EnginePostgres,
		Host: "localhost", Port: 5432, DatabaseName: "test", Username: "user", SSLMode: "disable"}
	var userID int64
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, item := range []struct {
			query string
			id    int64
		}{
			{"DELETE FROM migrations WHERE db_id=$1", conn.ID},
			{"DELETE FROM db_connections WHERE id=$1", conn.ID},
			{"DELETE FROM users WHERE id=$1", userID},
		} {
			if _, err := db.ExecContext(clean, item.query, item.id); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
	})
	if err := db.QueryRowContext(ctx, "INSERT INTO users(username,password_hash) VALUES($1,'test') RETURNING id", conn.Name).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(ctx, conn, "original-test-password"); err != nil {
		t.Fatal(err)
	}
	found, password, err := repo.FindByIDWithPassword(ctx, conn.ID)
	if err != nil || password != "original-test-password" || found.PasswordEnc == password {
		t.Fatalf("create/read: %v", err)
	}
	oldPassword := found.PasswordEnc
	found.Name += "_updated"
	if err := repo.Update(ctx, found, nil); err != nil {
		t.Fatal(err)
	}
	found, err = repo.FindByID(ctx, conn.ID)
	if err != nil || found.PasswordEnc != oldPassword || found.Name != conn.Name+"_updated" {
		t.Fatalf("update: %v", err)
	}
	newPassword := "changed-test-password"
	if err := repo.Update(ctx, found, &newPassword); err != nil {
		t.Fatal(err)
	}
	if _, password, err := repo.FindByIDWithPassword(ctx, conn.ID); err != nil || password != newPassword {
		t.Fatalf("password update: %v", err)
	}
	list, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, item := range list {
		if item.ID == conn.ID {
			listed = true
		}
	}
	if !listed {
		t.Fatal("connection missing from list")
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO migrations(db_id,title,sql_text,status,created_by) VALUES($1,'test','SELECT 1','draft',$2)", conn.ID, userID); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, conn.ID); !errors.Is(err, ErrHasMigrationHistory) {
		t.Fatalf("delete with history: %v", err)
	}
	if _, err := repo.FindByID(ctx, conn.ID); err != nil {
		t.Fatal("protected connection was deleted")
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM migrations WHERE db_id=$1", conn.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, conn.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FindByID(ctx, conn.ID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("delete: %v", err)
	}
}
