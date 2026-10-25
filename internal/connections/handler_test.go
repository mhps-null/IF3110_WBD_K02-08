package connections

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/testutil"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/web"
)

type dummyRenderer struct{ err error }

func (d *dummyRenderer) Render(w http.ResponseWriter, _ string, _ contract.PageData) error {
	if d.err != nil {
		return d.err
	}
	_, err := w.Write([]byte("rendered"))
	return err
}

func connectionForm() url.Values {
	return url.Values{"name": {"app"}, "engine": {"postgres"}, "host": {"localhost"}, "port": {"5432"},
		"database_name": {"app"}, "username": {"user"}, "password": {"secret"}}
}

func connectionRequest(h http.Handler, method, path string, form url.Values, admin bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(contract.WithPrincipal(req.Context(), contract.Principal{UserID: 1, IsPlatformAdmin: admin}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestConnectionHandlers(t *testing.T) {
	repo, err := NewMemoryRepository(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	pool := NewPoolManager(repo, "pool_test_driver")
	t.Cleanup(func() { _ = pool.CloseAll() })
	mux := http.NewServeMux()
	NewHandler(repo, pool, &dummyRenderer{}, testutil.NewFakeGuards(testutil.NewFakeAccessChecker())).RegisterRoutes(mux)
	ctx := context.Background()
	rec := connectionRequest(mux, "POST", "/admin/connections", connectionForm(), true)
	if rec.Code != 303 || !strings.Contains(rec.Header().Get("Location"), "msg=created") {
		t.Fatalf("create: %d %v", rec.Code, rec.Header())
	}
	list, err := repo.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%v err=%v", list, err)
	}
	for _, path := range []string{"/admin/connections", "/admin/connections/1/edit"} {
		if rec := connectionRequest(mux, "GET", path, nil, true); rec.Code != 200 {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
	form := connectionForm()
	form.Set("host", "newhost")
	form.Set("password", "")
	rec = connectionRequest(mux, "POST", "/admin/connections/1/edit", form, true)
	updated, password, err := repo.FindByIDWithPassword(ctx, 1)
	if rec.Code != 303 || err != nil || updated.Host != "newhost" || password != "secret" || pool.invalidation[1] != 1 {
		t.Fatalf("update: status=%d password-preserved=%t err=%v", rec.Code, password == "secret", err)
	}
	for _, field := range []string{"engine", "port"} {
		t.Run("invalid "+field, func(t *testing.T) {
			invalid := connectionForm()
			invalid.Set(field, "invalid")
			rec := connectionRequest(mux, "POST", "/admin/connections", invalid, true)
			list, _ := repo.List(ctx)
			if rec.Code != 303 || !strings.Contains(rec.Header().Get("Location"), "invalid_"+field) || len(list) != 1 {
				t.Fatal("invalid form accepted")
			}
		})
	}
	repo.SetMigrationCount(1, 1)
	rec = connectionRequest(mux, "POST", "/admin/connections/1/delete", nil, true)
	if !strings.Contains(rec.Header().Get("Location"), "err=") {
		t.Fatal("delete with history accepted")
	}
	if _, err := repo.FindByID(ctx, 1); err != nil {
		t.Fatal("connection removed despite migration history")
	}
	repo.SetMigrationCount(1, 0)
	rec = connectionRequest(mux, "POST", "/admin/connections/1/delete", nil, true)
	if rec.Code != 303 || pool.invalidation[1] != 2 {
		t.Fatal("delete did not invalidate pool")
	}
	if _, err := repo.FindByID(ctx, 1); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("delete: %v", err)
	}
}

func TestAdminGuardEnforcement(t *testing.T) {
	repo, err := NewMemoryRepository(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, guards := range []contract.Guards{testutil.NewFakeGuards(testutil.NewFakeAccessChecker()), {}} {
		mux := http.NewServeMux()
		NewHandler(repo, nil, &dummyRenderer{}, guards).RegisterRoutes(mux)
		for _, tc := range []struct{ method, path string }{
			{"GET", "/admin/connections"}, {"GET", "/admin/connections/1/edit"},
			{"POST", "/admin/connections"}, {"POST", "/admin/connections/1/edit"},
			{"POST", "/admin/connections/1/delete"}, {"POST", "/admin/connections/test"},
		} {
			if rec := connectionRequest(mux, tc.method, tc.path, connectionForm(), false); rec.Code != 403 {
				t.Fatalf("%s %s: %d", tc.method, tc.path, rec.Code)
			}
		}
	}
}

func TestConnectionHandlerFailures(t *testing.T) {
	repo, err := NewMemoryRepository(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	guards := testutil.NewFakeGuards(testutil.NewFakeAccessChecker())
	mux := http.NewServeMux()
	NewHandler(repo, nil, &dummyRenderer{err: errors.New("secret internal path")}, guards).RegisterRoutes(mux)
	rec := connectionRequest(mux, "GET", "/admin/connections", nil, true)
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret") {
		t.Fatal("renderer failure leaked details")
	}
	csrf := web.NewCSRFMiddleware([]byte("12345678901234567890123456789012"), false)
	if rec := connectionRequest(csrf.Wrap(mux), "POST", "/admin/connections", connectionForm(), true); rec.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	// Cancelled context exercises error masking without a live network connection.
	req := httptest.NewRequest("POST", "/admin/connections/test", strings.NewReader(connectionForm().Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx, cancel := context.WithCancel(contract.WithPrincipal(req.Context(), contract.Principal{UserID: 1, IsPlatformAdmin: true}))
	cancel()
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "\"success\":false") || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("test connection: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRegisterRoutesWithAuthEstablishesPrincipalBeforeAdminGuard(t *testing.T) {
	repo, err := NewMemoryRepository(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	guards := testutil.NewFakeGuards(testutil.NewFakeAccessChecker())
	mux := http.NewServeMux()
	authCalled := false
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authCalled = true
			r = r.WithContext(contract.WithPrincipal(r.Context(), contract.Principal{UserID: 1, IsPlatformAdmin: true}))
			next.ServeHTTP(w, r)
		})
	}
	NewHandler(repo, nil, &dummyRenderer{}, guards).RegisterRoutesWithAuth(mux, authenticate)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/connections", nil))
	if rec.Code != http.StatusOK || !authCalled {
		t.Fatalf("status=%d authCalled=%t", rec.Code, authCalled)
	}
}
