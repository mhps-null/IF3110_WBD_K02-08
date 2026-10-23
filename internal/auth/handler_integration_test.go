//go:build integration

package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/example/if3110-milestone1/internal/contract"
	"github.com/example/if3110-milestone1/internal/password"
	"github.com/example/if3110-milestone1/internal/session"
	"github.com/example/if3110-milestone1/internal/web"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// One real SQL flow covers auth/session interactions; TEST_DATABASE_URL needs the metadata schema.
func TestAuthSessionIntegration(t *testing.T) {
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
	username := fmt.Sprintf("test_auth_%d", time.Now().UnixNano())
	hash, err := password.Hash("valid-test-password")
	if err != nil {
		t.Fatal(err)
	}
	var userID int64
	if err := db.QueryRowContext(ctx, "INSERT INTO users(username,password_hash) VALUES($1,$2) RETURNING id", username, hash).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := db.ExecContext(clean, "DELETE FROM users WHERE id=$1", userID); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	cfg := session.DefaultConfig()
	store := session.NewSQLStore(db, cfg)
	renderer, err := web.NewRenderer("../../web/templates")
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(NewSQLUsers(db), store, renderer, false, cfg, nil)
	bad := httptest.NewRecorder()
	h.Login(bad, loginRequest(username, "wrong"))
	if bad.Code != 401 {
		t.Fatalf("bad login: %d", bad.Code)
	}
	rec := httptest.NewRecorder()
	h.Login(rec, loginRequest(username, "valid-test-password"))
	cookies := rec.Result().Cookies()
	if rec.Code != 303 || len(cookies) != 1 || !cookies[0].HttpOnly {
		t.Fatal("login did not create a session cookie")
	}
	token := cookies[0].Value
	initialSession, err := store.Load(ctx, token, time.Now().UTC())
	if err != nil {
		t.Fatalf("load new session: %v", err)
	}
	absoluteExpiresAt := initialSession.AbsoluteExpiresAt
	protected := h.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := contract.PrincipalFrom(r.Context())
		if !ok || p.UserID != userID {
			t.Fatal("authenticated principal missing")
		}
		w.WriteHeader(204)
	}))
	for _, tc := range []struct {
		token string
		want  int
	}{{token, 204}, {"invalid", 303}} {
		req := httptest.NewRequest("GET", "/", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tc.token})
		rec := httptest.NewRecorder()
		protected.ServeHTTP(rec, req.WithContext(ctx))
		if rec.Code != tc.want {
			t.Fatalf("authentication status=%d", rec.Code)
		}
	}
	// Changing the user's authorization version must rotate the active session
	// on the next authenticated request, without a periodic-rotation delay.
	if _, err := db.ExecContext(ctx, `UPDATE users SET privilege_version = privilege_version + 1 WHERE id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rec = httptest.NewRecorder()
	protected.ServeHTTP(rec, req.WithContext(ctx))
	rotatedCookies := rec.Result().Cookies()
	if rec.Code != http.StatusNoContent || len(rotatedCookies) != 1 || rotatedCookies[0].Value == token {
		t.Fatalf("privilege change did not rotate session: status=%d cookies=%d", rec.Code, len(rotatedCookies))
	}
	token = rotatedCookies[0].Value
	rotatedInfo, err := store.Load(ctx, token, time.Now().UTC())
	if err != nil {
		t.Fatalf("load rotated session: %v", err)
	}
	if rotatedInfo.PrivilegeChanged || rotatedInfo.PrivilegeVersion != rotatedInfo.CurrentPrivilegeVersion {
		t.Fatal("rotated session must capture the latest privilege version")
	}
	if !rotatedInfo.AbsoluteExpiresAt.Equal(absoluteExpiresAt) {
		t.Fatal("privilege rotation must not extend absolute expiration")
	}

	now := time.Now().UTC()
	newToken, _, err := store.Rotate(ctx, token, now)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := store.Load(ctx, token, now.Add(cfg.GracePeriod/2)); err != nil || info.IsCurrentID {
		t.Fatalf("old token within grace: %v", err)
	}
	if _, err := store.Load(ctx, token, now.Add(cfg.GracePeriod+time.Second)); !errors.Is(err, session.ErrInvalid) {
		t.Fatalf("old token after grace: %v", err)
	}
	req = httptest.NewRequest("POST", "/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: newToken})
	rec = httptest.NewRecorder()
	h.Logout(rec, req.WithContext(ctx))
	if rec.Code != 303 {
		t.Fatalf("logout status=%d", rec.Code)
	}
	if _, err := store.Load(ctx, newToken, now); !errors.Is(err, session.ErrInvalid) {
		t.Fatalf("revoked session: %v", err)
	}
	for _, tc := range []struct {
		name    string
		cfg     session.Config
		advance time.Duration
	}{
		{"idle", cfg, cfg.IdleTimeout + time.Second},
		{"absolute", session.Config{IdleTimeout: 24 * time.Hour, AbsoluteTimeout: time.Hour}, time.Hour + time.Second},
	} {
		t.Run(tc.name+" expiry", func(t *testing.T) {
			s := session.NewSQLStore(db, tc.cfg)
			id, _, err := s.Create(ctx, userID, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Load(ctx, id, now.Add(tc.advance)); !errors.Is(err, session.ErrInvalid) {
				t.Fatalf("expired session: %v", err)
			}
		})
	}
}
