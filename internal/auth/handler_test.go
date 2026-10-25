package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/password"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/session"
)

type fakeUserRepo struct{ user User }

func (f fakeUserRepo) FindByUsername(_ context.Context, name string) (User, error) {
	if name != f.user.Username {
		return User{}, ErrUserNotFound
	}
	return f.user, nil
}
func (f fakeUserRepo) FindByID(_ context.Context, id int64) (User, error) {
	if id != f.user.ID {
		return User{}, ErrUserNotFound
	}
	return f.user, nil
}

type fakeSessions struct {
	valid        map[string]contract.SessionInfo
	rotateErr    error
	loadErr      error
	createErr    error
	verifiedHash string
}

func (f *fakeSessions) CreateVerified(ctx context.Context, id int64, hash string, now time.Time) (string, contract.SessionInfo, error) {
	f.verifiedHash = hash
	if f.createErr != nil {
		return "", contract.SessionInfo{}, f.createErr
	}
	return f.Create(ctx, id, now)
}

func (f *fakeSessions) Create(_ context.Context, id int64, now time.Time) (string, contract.SessionInfo, error) {
	info := contract.SessionInfo{UserID: id, RotatedAt: now, AbsoluteExpiresAt: now.Add(time.Hour), IsCurrentID: true}
	f.valid["test-session"] = info
	return "test-session", info, nil
}
func (f *fakeSessions) Load(_ context.Context, id string, _ time.Time) (contract.SessionInfo, error) {
	if f.loadErr != nil {
		return contract.SessionInfo{}, f.loadErr
	}
	info, ok := f.valid[id]
	if !ok {
		return info, session.ErrInvalid
	}
	return info, nil
}
func (f *fakeSessions) Rotate(ctx context.Context, id string, now time.Time) (string, contract.SessionInfo, error) {
	if f.rotateErr != nil {
		return "", contract.SessionInfo{}, f.rotateErr
	}
	info, err := f.Load(ctx, id, now)
	if err != nil {
		return "", info, err
	}
	delete(f.valid, id)
	info.RotatedAt = now
	info.IsCurrentID = true
	info.PrivilegeVersion = info.CurrentPrivilegeVersion
	info.PrivilegeChanged = false
	f.valid[id+"-new"] = info
	return id + "-new", info, nil
}
func (f *fakeSessions) Revoke(_ context.Context, id string) error { delete(f.valid, id); return nil }
func (f *fakeSessions) RevokeAllForUser(_ context.Context, user int64) error {
	for id, info := range f.valid {
		if info.UserID == user {
			delete(f.valid, id)
		}
	}
	return nil
}

type failingIDUserRepo struct{ err error }

func (f failingIDUserRepo) FindByUsername(context.Context, string) (User, error) {
	return User{}, ErrUserNotFound
}
func (f failingIDUserRepo) FindByID(context.Context, int64) (User, error) {
	return User{}, f.err
}

type fakeRenderer struct{}

func (fakeRenderer) Render(w http.ResponseWriter, _ string, data contract.PageData) error {
	_, err := io.WriteString(w, data.Flash)
	return err
}

type fakeFlashStore struct {
	message string
	kind    string
}

func (f *fakeFlashStore) Set(_ http.ResponseWriter, kind, message string) {
	f.kind = kind
	f.message = message
}

func (f *fakeFlashStore) Consume(_ http.ResponseWriter, _ *http.Request) (message, kind string) {
	message, kind = f.message, f.kind
	f.message, f.kind = "", ""
	return message, kind
}

func loginRequest(username, plain string) *http.Request {
	form := url.Values{"username": {username}, "password": {plain}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func TestLogin(t *testing.T) {
	hash, err := password.Hash("valid-test-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, username, plain string
		active                bool
		want                  int
	}{
		{"success", "alice", "valid-test-password", true, 303},
		{"wrong password", "alice", "wrong", true, 401},
		{"unknown user", "missing", "wrong", true, 401},
		{"inactive", "alice", "valid-test-password", false, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions := &fakeSessions{valid: map[string]contract.SessionInfo{}}
			h := NewHandler(fakeUserRepo{User{ID: 1, Username: "alice", PasswordHash: hash, IsActive: tc.active}}, sessions, fakeRenderer{}, true, session.DefaultConfig(), nil)
			rec := httptest.NewRecorder()
			h.Login(rec, loginRequest(tc.username, tc.plain))
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d", rec.Code, tc.want)
			}
			if tc.want == 401 {
				if rec.Body.String() != "Username atau password salah." || len(sessions.valid) != 0 {
					t.Fatal("failed login disclosed details or created a session")
				}
				return
			}
			cookies := rec.Result().Cookies()
			if sessions.verifiedHash != hash {
				t.Fatal("session creation must receive the password hash verified by login")
			}
			if len(cookies) != 1 || cookies[0].Name != sessionCookieName || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
				t.Fatal("session cookie missing/insecure")
			}
			if _, ok := sessions.valid[cookies[0].Value]; !ok {
				t.Fatal("session not created")
			}
		})
	}
}

func TestLoginRejectsCredentialsChangedBeforeSessionCreation(t *testing.T) {
	hash, err := password.Hash("valid-test-password")
	if err != nil {
		t.Fatal(err)
	}
	sessions := &fakeSessions{
		valid: map[string]contract.SessionInfo{}, createErr: session.ErrCredentialsChanged,
	}
	h := NewHandler(fakeUserRepo{User{ID: 1, Username: "alice", PasswordHash: hash, IsActive: true}}, sessions, fakeRenderer{}, false, session.DefaultConfig(), nil)
	rec := httptest.NewRecorder()
	h.Login(rec, loginRequest("alice", "valid-test-password"))
	if rec.Code != http.StatusUnauthorized || len(rec.Result().Cookies()) != 0 || len(sessions.valid) != 0 {
		t.Fatal("stale credentials must fail login without issuing a session cookie")
	}
	if rec.Body.String() != "Username atau password salah." {
		t.Fatal("credential changes must use the generic login failure message")
	}
}

func TestAuthenticateAndLogout(t *testing.T) {
	sessions := &fakeSessions{valid: map[string]contract.SessionInfo{}}
	h := NewHandler(fakeUserRepo{User{ID: 1, Username: "alice", IsActive: true}}, sessions, fakeRenderer{}, false, session.DefaultConfig(), nil)
	token, _, err := sessions.Create(context.Background(), 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	protected := h.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := contract.PrincipalFrom(r.Context())
		if !ok || principal.UserID != 1 {
			t.Fatal("principal missing")
		}
		w.WriteHeader(204)
	}))
	for _, tc := range []struct {
		token string
		want  int
	}{{"", 303}, {"invalid", 303}, {token, 204}} {
		req := httptest.NewRequest("GET", "/", nil)
		if tc.token != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tc.token})
		}
		rec := httptest.NewRecorder()
		protected.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("token=%q status=%d", tc.token, rec.Code)
		}
	}
	req := httptest.NewRequest("POST", "/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	h.Logout(rec, req)
	if rec.Code != 303 || len(sessions.valid) != 0 || rec.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout did not revoke session and clear cookie")
	}
}

func TestAuthenticateRotatesWhenPrivilegeChanges(t *testing.T) {
	sessions := &fakeSessions{valid: map[string]contract.SessionInfo{}}
	h := NewHandler(fakeUserRepo{User{ID: 1, Username: "alice", IsActive: true}}, sessions, fakeRenderer{}, false, session.DefaultConfig(), nil)
	token, info, err := sessions.Create(context.Background(), 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	info.PrivilegeVersion = 0
	info.CurrentPrivilegeVersion = 1
	info.PrivilegeChanged = true
	sessions.valid[token] = info

	protected := h.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusNoContent)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName || cookies[0].Value == token {
		t.Fatal("privilege change must rotate the session cookie")
	}
	rotated, ok := sessions.valid[cookies[0].Value]
	if !ok || rotated.PrivilegeChanged || rotated.PrivilegeVersion != rotated.CurrentPrivilegeVersion {
		t.Fatal("rotated session must record the current privilege version")
	}
}

func TestAuthenticateFailsClosedWhenRequiredPrivilegeRotationFails(t *testing.T) {
	sessions := &fakeSessions{
		valid:     map[string]contract.SessionInfo{},
		rotateErr: context.DeadlineExceeded,
	}
	h := NewHandler(fakeUserRepo{User{ID: 1, Username: "alice", IsActive: true}}, sessions, fakeRenderer{}, false, session.DefaultConfig(), nil)
	token, info, err := sessions.Create(context.Background(), 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	info.PrivilegeVersion = 0
	info.CurrentPrivilegeVersion = 1
	info.PrivilegeChanged = true
	sessions.valid[token] = info

	nextCalled := false
	protected := h.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusServiceUnavailable)
	}
	if nextCalled {
		t.Fatal("protected handler ran even though required rotation failed")
	}
}

func TestAuthenticateRejectsPreviousTokenWithStalePrivilegeVersion(t *testing.T) {
	sessions := &fakeSessions{valid: map[string]contract.SessionInfo{}}
	h := NewHandler(fakeUserRepo{User{ID: 1, Username: "alice", IsActive: true}}, sessions, fakeRenderer{}, false, session.DefaultConfig(), nil)
	token, info, err := sessions.Create(context.Background(), 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	info.IsCurrentID = false
	info.PrivilegeVersion = 0
	info.CurrentPrivilegeVersion = 1
	info.PrivilegeChanged = true
	sessions.valid[token] = info

	nextCalled := false
	protected := h.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusServiceUnavailable)
	}
	if nextCalled {
		t.Fatal("protected handler ran with a stale privilege snapshot")
	}
}

func TestLoginAndLogoutFlashMessages(t *testing.T) {
	hash, err := password.Hash("valid-test-password")
	if err != nil {
		t.Fatal(err)
	}
	users := fakeUserRepo{User{ID: 1, Username: "alice", PasswordHash: hash, IsActive: true}}
	sessions := &fakeSessions{valid: map[string]contract.SessionInfo{}}
	flash := &fakeFlashStore{}
	h := NewHandler(users, sessions, fakeRenderer{}, false, session.DefaultConfig(), nil, flash)

	loginRec := httptest.NewRecorder()
	h.Login(loginRec, loginRequest("alice", "valid-test-password"))
	if loginRec.Code != http.StatusSeeOther || flash.kind != "success" || flash.message != "Berhasil masuk." {
		t.Fatalf("login redirect/flash = (%d, %q, %q)", loginRec.Code, flash.kind, flash.message)
	}

	homeReq := httptest.NewRequest(http.MethodGet, "/", nil)
	homeReq = homeReq.WithContext(contract.WithPrincipal(homeReq.Context(), contract.Principal{UserID: 1, Username: "alice"}))
	homeRec := httptest.NewRecorder()
	h.RenderHome(homeRec, homeReq)
	if homeRec.Body.String() != "Berhasil masuk." || flash.message != "" {
		t.Fatalf("home flash not consumed/rendered: body=%q remaining=%q", homeRec.Body.String(), flash.message)
	}

	logoutReq := httptest.NewRequest(http.MethodPost, "/logout", nil)
	logoutReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "test-session"})
	logoutRec := httptest.NewRecorder()
	h.Logout(logoutRec, logoutReq)
	if logoutRec.Code != http.StatusSeeOther || flash.kind != "success" || flash.message != "Berhasil keluar." {
		t.Fatalf("logout redirect/flash = (%d, %q, %q)", logoutRec.Code, flash.kind, flash.message)
	}

	loginPageReq := httptest.NewRequest(http.MethodGet, "/login", nil)
	loginPageRec := httptest.NewRecorder()
	h.LoginPage(loginPageRec, loginPageReq)
	if loginPageRec.Body.String() != "Berhasil keluar." || flash.message != "" {
		t.Fatalf("login page flash not consumed/rendered: body=%q remaining=%q", loginPageRec.Body.String(), flash.message)
	}
}

func TestAuthenticateTransientStoreErrorPreservesSessionCookie(t *testing.T) {
	sessions := &fakeSessions{valid: map[string]contract.SessionInfo{}}
	token, _, err := sessions.Create(context.Background(), 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	sessions.loadErr = context.DeadlineExceeded
	h := NewHandler(fakeUserRepo{User{ID: 1, Username: "alice", IsActive: true}}, sessions, fakeRenderer{}, false, session.DefaultConfig(), nil)
	nextCalled := false
	protected := h.Authenticate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { nextCalled = true }))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || nextCalled {
		t.Fatalf("status=%d nextCalled=%v", rec.Code, nextCalled)
	}
	if _, ok := sessions.valid[token]; !ok {
		t.Fatal("transient store error revoked the session")
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == sessionCookieName && cookie.MaxAge == -1 {
			t.Fatal("transient store error cleared the session cookie")
		}
	}
}

func TestAuthenticateTransientUserLookupErrorPreservesSession(t *testing.T) {
	sessions := &fakeSessions{valid: map[string]contract.SessionInfo{}}
	token, _, err := sessions.Create(context.Background(), 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(failingIDUserRepo{err: context.DeadlineExceeded}, sessions, fakeRenderer{}, false, session.DefaultConfig(), nil)
	nextCalled := false
	protected := h.Authenticate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { nextCalled = true }))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || nextCalled {
		t.Fatalf("status=%d nextCalled=%v", rec.Code, nextCalled)
	}
	if _, ok := sessions.valid[token]; !ok {
		t.Fatal("transient user lookup error revoked the session")
	}
}

func TestAuthenticateRotatesPeriodically(t *testing.T) {
	cfg := session.DefaultConfig()
	sessions := &fakeSessions{
		valid: map[string]contract.SessionInfo{},
	}
	h := NewHandler(
		fakeUserRepo{
			User{ID: 1, Username: "alice", IsActive: true},
		},
		sessions,
		fakeRenderer{},
		false,
		cfg,
		nil,
	)

	createdAt := time.Now().UTC()
	token, info, err := sessions.Create(
		context.Background(),
		1,
		createdAt,
	)
	if err != nil {
		t.Fatal(err)
	}

	absoluteExpiration := info.AbsoluteExpiresAt

	// Simulate a session whose rotation interval has elapsed.
	info.RotatedAt = createdAt.Add(-cfg.RotateInterval - time.Second)
	sessions.valid[token] = info

	nextCalled := false
	protected := h.Authenticate(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			nextCalled = true
			w.WriteHeader(http.StatusNoContent)
		},
	))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{
		Name:  sessionCookieName,
		Value: token,
	})

	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent || !nextCalled {
		t.Fatalf(
			"status=%d nextCalled=%v",
			rec.Code,
			nextCalled,
		)
	}

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 ||
		cookies[0].Name != sessionCookieName ||
		cookies[0].Value == token {
		t.Fatal("session must rotate when the rotation interval has elapsed")
	}

	rotated, ok := sessions.valid[cookies[0].Value]
	if !ok {
		t.Fatal("rotated session was not stored")
	}

	if !rotated.RotatedAt.After(info.RotatedAt) {
		t.Fatal("rotation timestamp was not updated")
	}

	if !rotated.AbsoluteExpiresAt.Equal(absoluteExpiration) {
		t.Fatal("periodic rotation must not extend absolute expiration")
	}
}
