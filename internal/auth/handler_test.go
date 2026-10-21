package auth

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/example/if3110-milestone1/internal/contract"
	"github.com/example/if3110-milestone1/internal/password"
	"github.com/example/if3110-milestone1/internal/session"
)

type fakeUserRepo struct {
	byName map[string]User
	byID   map[int64]User
}

func (f fakeUserRepo) FindByUsername(_ context.Context, name string) (User, error) {
	u, ok := f.byName[name]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return u, nil
}
func (f fakeUserRepo) FindByID(_ context.Context, id int64) (User, error) {
	u, ok := f.byID[id]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return u, nil
}

type fakeSessions struct {
	valid       map[string]contract.SessionInfo
	revoked     []string
	createdUser int64
	createErr   error
}

func newFakeSessions() *fakeSessions { return &fakeSessions{valid: map[string]contract.SessionInfo{}} }
func (f *fakeSessions) Create(_ context.Context, userID int64, now time.Time) (string, contract.SessionInfo, error) {
	if f.createErr != nil {
		return "", contract.SessionInfo{}, f.createErr
	}
	raw := "opaque-session-token-test-only"
	info := contract.SessionInfo{UserID: userID, CreatedAt: now, LastSeenAt: now, RotatedAt: now, AbsoluteExpiresAt: now.Add(8 * time.Hour), IsCurrentID: true}
	f.valid[raw] = info
	f.createdUser = userID
	return raw, info, nil
}
func (f *fakeSessions) Load(_ context.Context, raw string, _ time.Time) (contract.SessionInfo, error) {
	info, ok := f.valid[raw]
	if !ok {
		return contract.SessionInfo{}, session.ErrInvalid
	}
	return info, nil
}
func (f *fakeSessions) Rotate(_ context.Context, raw string, now time.Time) (string, contract.SessionInfo, error) {
	info, ok := f.valid[raw]
	if !ok {
		return "", contract.SessionInfo{}, session.ErrInvalid
	}
	delete(f.valid, raw)
	newID := raw + "-rotated"
	info.RotatedAt = now
	info.IsCurrentID = true
	f.valid[newID] = info
	return newID, info, nil
}
func (f *fakeSessions) Revoke(_ context.Context, raw string) error {
	f.revoked = append(f.revoked, raw)
	delete(f.valid, raw)
	return nil
}
func (f *fakeSessions) RevokeAllForUser(_ context.Context, userID int64) error {
	for raw, info := range f.valid {
		if info.UserID == userID {
			delete(f.valid, raw)
		}
	}
	return nil
}

type fakeRenderer struct{}

func (fakeRenderer) Render(w http.ResponseWriter, _ string, data contract.PageData) error {
	_, _ = io.WriteString(w, data.Flash)
	return nil
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func testHandler(t *testing.T, users fakeUserRepo, sessions *fakeSessions) *Handler {
	t.Helper()
	return NewHandler(users, sessions, fakeRenderer{}, true, session.DefaultConfig(), testLogger())
}

func TestLoginSuccessCreatesSessionCookie(t *testing.T) {
	hash, err := password.Hash("secure-password-for-test")
	if err != nil {
		t.Fatal(err)
	}
	user := User{ID: 7, Username: "alice", PasswordHash: hash, IsActive: true}
	sessions := newFakeSessions()
	h := testHandler(t, fakeUserRepo{byName: map[string]User{"alice": user}, byID: map[int64]User{7: user}}, sessions)
	form := url.Values{"username": {"alice"}, "password": {"secure-password-for-test"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	h.Login(recorder, req)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if sessions.createdUser != user.ID {
		t.Fatalf("created session for user %d, want %d", sessions.createdUser, user.ID)
	}
	cookies := recorder.Result().Cookies()
	var found *http.Cookie
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			found = c
			break
		}
	}
	if found == nil {
		t.Fatal("session cookie was not set")
	}
	if !found.HttpOnly || !found.Secure || found.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie flags are insecure: %#v", found)
	}
	if strings.Contains(recorder.Body.String(), found.Value) {
		t.Fatal("session ID leaked into response body")
	}
}

func TestLoginFailureIsGeneric(t *testing.T) {
	hash, err := password.Hash("valid-test-password")
	if err != nil {
		t.Fatal(err)
	}
	user := User{ID: 1, Username: "known", PasswordHash: hash, IsActive: true}
	h := testHandler(t, fakeUserRepo{byName: map[string]User{"known": user}, byID: map[int64]User{1: user}}, newFakeSessions())
	attempt := func(username, plain string) (int, string) {
		t.Helper()
		form := url.Values{"username": {username}, "password": {plain}}
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		recorder := httptest.NewRecorder()
		h.Login(recorder, req)
		return recorder.Code, recorder.Body.String()
	}
	missingStatus, missingBody := attempt("missing", "bad-password")
	wrongStatus, wrongBody := attempt("known", "bad-password")
	if missingStatus != http.StatusUnauthorized || wrongStatus != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d and %d", missingStatus, wrongStatus)
	}
	if missingBody != wrongBody || !strings.Contains(missingBody, "Username atau password salah.") {
		t.Fatalf("login errors are not generic: %q vs %q", missingBody, wrongBody)
	}
}

func TestAuthenticateRejectsInvalidSession(t *testing.T) {
	sessions := newFakeSessions()
	h := testHandler(t, fakeUserRepo{byName: map[string]User{}, byID: map[int64]User{}}, sessions)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "invalid-token"})
	recorder := httptest.NewRecorder()
	called := false
	h.Authenticate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(recorder, req)
	if called {
		t.Fatal("protected handler ran with an invalid session")
	}
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/login" {
		t.Fatalf("unexpected response: %d, location=%q", recorder.Code, recorder.Header().Get("Location"))
	}
}

func TestLogoutRevokesSession(t *testing.T) {
	sessions := newFakeSessions()
	sessions.valid["old-session"] = contract.SessionInfo{UserID: 1, AbsoluteExpiresAt: time.Now().Add(time.Hour)}
	h := testHandler(t, fakeUserRepo{byName: map[string]User{}, byID: map[int64]User{}}, sessions)
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "old-session"})
	recorder := httptest.NewRecorder()
	h.Logout(recorder, req)
	if len(sessions.revoked) != 1 || sessions.revoked[0] != "old-session" {
		t.Fatalf("session was not revoked: %#v", sessions.revoked)
	}
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/login" {
		t.Fatalf("unexpected response: %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
}

func TestInactiveUserCannotLogin(t *testing.T) {
	hash, err := password.Hash("valid-test-password")
	if err != nil {
		t.Fatal(err)
	}
	user := User{ID: 1, Username: "inactive", PasswordHash: hash, IsActive: false}
	sessions := newFakeSessions()
	h := testHandler(t, fakeUserRepo{byName: map[string]User{"inactive": user}, byID: map[int64]User{1: user}}, sessions)
	form := url.Values{"username": {"inactive"}, "password": {"valid-test-password"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	h.Login(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if sessions.createdUser != 0 {
		t.Fatal("inactive user must not receive a session")
	}
}
