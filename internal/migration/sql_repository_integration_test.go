//go:build integration

package migration

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestSQLRepositoryIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping database integration test")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Skipf("cannot reach database at TEST_DATABASE_URL: %v", err)
	}

	repo := NewSQLRepository(db)

	// Create test user for created_by
	testUsername := fmt.Sprintf("test_mig_user_%d", time.Now().UnixNano())
	var userID int64
	err = db.QueryRowContext(ctx, `
		INSERT INTO users (username, password_hash, is_platform_admin, is_active)
		VALUES ($1, 'dummyhash', false, true)
		RETURNING id
	`, testUsername).Scan(&userID)
	if err != nil {
		t.Fatalf("failed to insert test user: %v", err)
	}
	defer func() {
		_, _ = db.ExecContext(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	}()

	// Create test db_connection
	var dbID int64
	err = db.QueryRowContext(ctx, `
		INSERT INTO db_connections (name, engine, host, port, database_name, username, password_enc)
		VALUES ($1, 'postgres', 'localhost', 5432, 'test_target', 'postgres', 'dummy')
		RETURNING id
	`, fmt.Sprintf("test_conn_%d", time.Now().UnixNano())).Scan(&dbID)
	if err != nil {
		t.Fatalf("failed to insert test db_connection: %v", err)
	}
	defer func() {
		_, _ = db.ExecContext(context.Background(), "DELETE FROM migrations WHERE db_id = $1", dbID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM db_connections WHERE id = $1", dbID)
	}()

	// 1. Create Draft
	draft := &Migration{
		DBID:      dbID,
		Title:     "Integration test draft",
		SQLText:   "CREATE TABLE int_test (id int);",
		CreatedBy: userID,
	}
	if err := repo.CreateDraft(ctx, draft); err != nil {
		t.Fatalf("failed to create draft: %v", err)
	}
	if draft.ID == 0 {
		t.Fatalf("expected assigned ID")
	}

	// 2. Fetch by ID
	fetched, err := repo.GetByID(ctx, draft.ID)
	if err != nil {
		t.Fatalf("failed to get draft by ID: %v", err)
	}
	if fetched.Title != draft.Title {
		t.Errorf("expected title '%s', got '%s'", draft.Title, fetched.Title)
	}

	// 3. Update Draft
	fetched.Title = "Updated integration draft"
	if err := repo.UpdateDraft(ctx, fetched); err != nil {
		t.Fatalf("failed to update draft: %v", err)
	}

	// 4. Record Execution Success
	checksum := ComputeChecksum(fetched.SQLText)
	executed, err := repo.RecordExecution(ctx, draft.ID, StatusSuccess, userID, nil, checksum)
	if err != nil {
		t.Fatalf("failed to record execution: %v", err)
	}
	if executed.Version == nil || *executed.Version != 1 {
		t.Errorf("expected linear version 1, got %v", executed.Version)
	}
	if executed.Status != StatusSuccess {
		t.Errorf("expected status 'success', got '%s'", executed.Status)
	}

	// 5. List by DB
	list, err := repo.ListByDB(ctx, dbID)
	if err != nil {
		t.Fatalf("failed to list migrations: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 migration in list, got %d", len(list))
	}

	// Hold an execution reservation open while a second migration in the same
	// database attempts to reserve execution. The second reservation must wait
	// for the first one to finalize so versions are allocated in execution order.
	draft2 := &Migration{DBID: dbID, Title: "Concurrent draft A", SQLText: "SELECT 2;", CreatedBy: userID}
	draft3 := &Migration{DBID: dbID, Title: "Concurrent draft B", SQLText: "SELECT 3;", CreatedBy: userID}
	if err := repo.CreateDraft(ctx, draft2); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateDraft(ctx, draft3); err != nil {
		t.Fatal(err)
	}
	firstReservation, err := repo.BeginExecution(ctx, dbID, draft2.ID)
	if err != nil {
		t.Fatalf("reserve first concurrent draft: %v", err)
	}
	secondStarted := make(chan struct{})
	secondResult := make(chan struct {
		reservation ExecutionReservation
		err         error
	}, 1)
	go func() {
		close(secondStarted)
		reservation, err := repo.BeginExecution(ctx, dbID, draft3.ID)
		secondResult <- struct {
			reservation ExecutionReservation
			err         error
		}{reservation: reservation, err: err}
	}()
	<-secondStarted
	select {
	case result := <-secondResult:
		if result.reservation != nil {
			result.reservation.Abort()
		}
		firstReservation.Abort()
		t.Fatal("second execution reservation did not wait for the database lock")
	case <-time.After(100 * time.Millisecond):
	}

	firstExecuted, err := firstReservation.Complete(ctx, StatusSuccess, userID, nil, ComputeChecksum(draft2.SQLText))
	if err != nil {
		firstReservation.Abort()
		t.Fatalf("complete first concurrent migration: %v", err)
	}
	second := <-secondResult
	if second.err != nil {
		t.Fatalf("reserve second concurrent draft: %v", second.err)
	}
	secondExecuted, err := second.reservation.Complete(ctx, StatusSuccess, userID, nil, ComputeChecksum(draft3.SQLText))
	if err != nil {
		second.reservation.Abort()
		t.Fatalf("complete second concurrent migration: %v", err)
	}
	if firstExecuted.Version == nil || *firstExecuted.Version != 2 {
		t.Fatalf("first reserved execution version=%v, want 2", firstExecuted.Version)
	}
	if secondExecuted.Version == nil || *secondExecuted.Version != 3 {
		t.Fatalf("second reserved execution version=%v, want 3", secondExecuted.Version)
	}
}
