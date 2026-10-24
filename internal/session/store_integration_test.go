//go:build integration

package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestRevokeAllForUserIntegration verifies that revocation invalidates every
// session belonging to a user, including a rotated session's previous token,
// without affecting another user's session. TEST_DATABASE_URL must point to a
// dedicated metadata test database with migrations 001 and 002 applied.
func TestRevokeAllForUserIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping test database: %v", err)
	}

	suffix := time.Now().UnixNano()
	createUser := func(username string) int64 {
		t.Helper()
		var id int64
		err := db.QueryRowContext(ctx,
			`INSERT INTO users (username, password_hash) VALUES ($1, $2) RETURNING id`,
			username, "integration-test-hash").Scan(&id)
		if err != nil {
			t.Fatalf("insert test user %q: %v", username, err)
		}
		return id
	}

	var revokedUserID, otherUserID int64
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := db.ExecContext(cleanupCtx,
			`DELETE FROM users WHERE id IN ($1, $2)`, revokedUserID, otherUserID); err != nil {
			t.Errorf("clean up test users: %v", err)
		}
	})
	revokedUserID = createUser(fmt.Sprintf("test_revoke_all_%d", suffix))
	otherUserID = createUser(fmt.Sprintf("test_revoke_other_%d", suffix))

	store := NewSQLStore(db, DefaultConfig())
	now := time.Now().UTC()

	firstToken, _, err := store.Create(ctx, revokedUserID, now)
	if err != nil {
		t.Fatalf("create first target session: %v", err)
	}
	previousToken, _, err := store.Create(ctx, revokedUserID, now)
	if err != nil {
		t.Fatalf("create second target session: %v", err)
	}
	rotationTime := now.Add(time.Second)
	currentToken, _, err := store.Rotate(ctx, previousToken, rotationTime)
	if err != nil {
		t.Fatalf("rotate second target session: %v", err)
	}
	otherToken, _, err := store.Create(ctx, otherUserID, now)
	if err != nil {
		t.Fatalf("create unrelated user session: %v", err)
	}

	if err := store.RevokeAllForUser(ctx, revokedUserID); err != nil {
		t.Fatalf("RevokeAllForUser: %v", err)
	}

	checkAt := rotationTime.Add(2 * time.Second)
	for name, token := range map[string]string{
		"first session":                  firstToken,
		"rotated session current token":  currentToken,
		"rotated session previous token": previousToken,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.Load(ctx, token, checkAt); !errors.Is(err, ErrInvalid) {
				t.Fatalf("session should be revoked; Load error = %v", err)
			}
		})
	}

	if info, err := store.Load(ctx, otherToken, checkAt); err != nil {
		t.Fatalf("another user's session must remain valid: %v", err)
	} else if info.UserID != otherUserID {
		t.Fatalf("loaded session belongs to user %d, want %d", info.UserID, otherUserID)
	}

	// Repeating the operation should be safe and should not reactivate sessions.
	if err := store.RevokeAllForUser(ctx, revokedUserID); err != nil {
		t.Fatalf("second RevokeAllForUser call: %v", err)
	}
	if _, err := store.Load(ctx, currentToken, checkAt); !errors.Is(err, ErrInvalid) {
		t.Fatalf("session became valid after repeated revoke; Load error = %v", err)
	}
}
