package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/audit"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/observability"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/password"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/session"
)

type stubAccessChecker struct {
	dbs []contract.AccessibleDB
}

func (s stubAccessChecker) EffectiveLevel(context.Context, int64, int64) (contract.AccessLevel, error) {
	return contract.AccessNone, nil
}
func (s stubAccessChecker) ListAccessible(context.Context, int64) ([]contract.AccessibleDB, error) {
	return s.dbs, nil
}

type stubGroupInspector struct {
	groups []contract.UserGroupMembership
}

func (s stubGroupInspector) ListGroupsForDatabase(context.Context, int64) ([]contract.DatabaseGroupAccess, error) {
	return nil, nil
}
func (s stubGroupInspector) ListUserGroups(context.Context, int64) ([]contract.UserGroupMembership, error) {
	return s.groups, nil
}

func TestAPILoginSuccessNoSecretLeakAndAudit(t *testing.T) {
	secretPass := "SuperSecretPassword123!"
	hash, err := password.Hash(secretPass)
	if err != nil {
		t.Fatal(err)
	}
	var logBuf bytes.Buffer
	obsLogger := observability.NewLogger(&logBuf, slog.LevelDebug)
	auditStore := audit.NewMemoryStore()
	sessions := &fakeSessions{valid: map[string]contract.SessionInfo{}}

	h := NewHandler(
		fakeUserRepo{User{ID: 7, Username: "alice", PasswordHash: hash, IsActive: true, IsPlatformAdmin: false}},
		sessions, fakeRenderer{}, true, session.DefaultConfig(), obsLogger.Slog(),
	)
	h.SetAuditStore(auditStore)
	h.SetObservabilityLogger(obsLogger)

	mux := http.NewServeMux()
	h.RegisterAPIRoutes(mux)

	body := `{"username":"alice","password":"` + secretPass + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(contract.WithCSRFToken(req.Context(), "csrf-token-value"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want=200 body=%s", rec.Code, rec.Body.String())
	}

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie missing or insecure: %+v", cookies)
	}
	rawSessionID := cookies[0].Value

	// Verify session ID, plaintext password, and hash never appear in response body or logs.
	respBody := rec.Body.String()
	for _, forbidden := range []string{rawSessionID, secretPass, hash} {
		if strings.Contains(respBody, forbidden) {
			t.Fatalf("response body leaked sensitive value %q", forbidden)
		}
		if strings.Contains(logBuf.String(), forbidden) {
			t.Fatalf("log output leaked sensitive value %q", forbidden)
		}
	}

	var parsed apiAuthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if parsed.User.UserID != 7 || parsed.User.Username != "alice" || parsed.User.IsPlatformAdmin {
		t.Fatalf("unexpected user payload: %+v", parsed.User)
	}

	page, err := auditStore.List(context.Background(), audit.Filter{Event: "auth.login"})
	if err != nil || page.Total != 1 || page.Items[0].Status != "success" {
		t.Fatalf("expected recorded auth.login audit event, got %+v err=%v", page, err)
	}
}

func TestAPILoginFailureCases(t *testing.T) {
	hash, err := password.Hash("correct-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		body   string
		active bool
		want   int
	}{
		{"invalid json", "{bad", true, http.StatusBadRequest},
		{"empty credentials", `{"username":"","password":""}`, true, http.StatusUnauthorized},
		{"wrong password", `{"username":"alice","password":"wrong"}`, true, http.StatusUnauthorized},
		{"unknown user", `{"username":"bob","password":"correct-password"}`, true, http.StatusUnauthorized},
		{"inactive user", `{"username":"alice","password":"correct-password"}`, false, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions := &fakeSessions{valid: map[string]contract.SessionInfo{}}
			h := NewHandler(
				fakeUserRepo{User{ID: 1, Username: "alice", PasswordHash: hash, IsActive: tc.active}},
				sessions, fakeRenderer{}, false, session.DefaultConfig(), nil,
			)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(tc.body))
			h.APILogin(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.want, rec.Body.String())
			}
			if len(sessions.valid) != 0 {
				t.Fatal("failed login must not create a session")
			}
		})
	}
}

func TestAPIMeAndLogoutAndUnauthenticatedJSON(t *testing.T) {
	sessions := &fakeSessions{valid: map[string]contract.SessionInfo{}}
	token, _, err := sessions.Create(context.Background(), 5, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}

	h := NewHandler(
		fakeUserRepo{User{ID: 5, Username: "leader1", IsActive: true}},
		sessions, fakeRenderer{}, false, session.DefaultConfig(), nil,
	)
	h.SetAccessChecker(stubAccessChecker{
		dbs: []contract.AccessibleDB{
			{DBID: 10, Name: "prod_pg", Engine: contract.EnginePostgres, Level: contract.AccessViewEdit},
		},
	})
	h.SetGroupInspector(stubGroupInspector{
		groups: []contract.UserGroupMembership{
			{GroupID: 2, GroupName: "Backend", IsLeader: true},
		},
	})

	mux := http.NewServeMux()
	h.RegisterAPIRoutes(mux)

	// Unauthenticated request to /api/auth/me must return 401 JSON (not 303 redirect).
	unauthReq := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	unauthRec := httptest.NewRecorder()
	mux.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /api/auth/me status=%d want=401", unauthRec.Code)
	}
	if !strings.Contains(unauthRec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("expected JSON content-type on 401, got %q", unauthRec.Header().Get("Content-Type"))
	}

	// Authenticated /api/auth/me returns user, groups, accessible_databases, and csrf_token.
	meReq := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	meReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	meReq = meReq.WithContext(contract.WithCSRFToken(meReq.Context(), "valid-csrf"))
	meRec := httptest.NewRecorder()
	mux.ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Fatalf("/api/auth/me status=%d want=200 body=%s", meRec.Code, meRec.Body.String())
	}
	var me apiMeResponse
	if err := json.Unmarshal(meRec.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode /api/auth/me: %v", err)
	}
	if me.User.Username != "leader1" || len(me.Groups) != 1 || !me.Groups[0].IsLeader || len(me.AccessibleDatabases) != 1 || me.AccessibleDatabases[0].LevelLabel != "View + Edit" {
		t.Fatalf("unexpected /api/auth/me payload: %+v", me)
	}

	// Logout revokes session and clears cookie.
	logoutReq := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	logoutReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	logoutRec := httptest.NewRecorder()
	mux.ServeHTTP(logoutRec, logoutReq)
	if logoutRec.Code != http.StatusOK || len(sessions.valid) != 0 {
		t.Fatalf("logout failed: code=%d remaining=%d", logoutRec.Code, len(sessions.valid))
	}

	// Subsequent /api/auth/me must fail with 401.
	afterReq := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	afterReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	afterRec := httptest.NewRecorder()
	mux.ServeHTTP(afterRec, afterReq)
	if afterRec.Code != http.StatusUnauthorized {
		t.Fatalf("post-logout /api/auth/me status=%d want=401", afterRec.Code)
	}
}
