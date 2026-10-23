package migration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/example/if3110-milestone1/internal/contract"
)

type mockRenderer struct {
	lastTemplate string
	lastData     contract.PageData
}

func (m *mockRenderer) Render(w http.ResponseWriter, templateRelativePath string, data contract.PageData) error {
	m.lastTemplate = templateRelativePath
	m.lastData = data
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("rendered:" + templateRelativePath))
	return nil
}

func setupTestHandler() (*Handler, Service, *mockRenderer) {
	repo := NewMemoryRepository()
	targetDB := &FakeTargetDB{Engine: contract.EnginePostgres}
	exec := &mockExecutor{
		execResult: contract.ExecResult{RowsAffected: 0},
		planResult: contract.Plan{
			Root: contract.PlanNode{NodeType: "Result"},
		},
	}
	executors := map[contract.Engine]contract.Executor{
		contract.EnginePostgres: exec,
	}
	svc := NewCustomService(repo, targetDB, executors)
	renderer := &mockRenderer{}

	guards := contract.Guards{
		ViewDB: func(next http.Handler) http.Handler { return next },
		EditDB: func(next http.Handler) http.Handler { return next },
	}

	handler := NewHandler(svc, renderer, guards)
	return handler, svc, renderer
}

func withUserPrincipal(req *http.Request, userID int64, username string) *http.Request {
	ctx := contract.WithPrincipal(req.Context(), contract.Principal{
		UserID:          userID,
		Username:        username,
		IsPlatformAdmin: false,
	})
	return req.WithContext(ctx)
}

func TestHandlerListMigrations(t *testing.T) {
	h, svc, r := setupTestHandler()
	ctx := context.Background()
	_, _ = svc.CreateDraft(ctx, 1, "Init schema", "CREATE TABLE a (id int);", 1)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/databases/1/migrations", nil)
	req = withUserPrincipal(req, 1, "alice")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	if r.lastTemplate != "migration/index.html" {
		t.Errorf("expected template migration/index.html, got %s", r.lastTemplate)
	}
}

func TestHandlerNewPage(t *testing.T) {
	h, _, r := setupTestHandler()

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/databases/1/migrations/new", nil)
	req = withUserPrincipal(req, 1, "alice")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	if r.lastTemplate != "migration/new.html" {
		t.Errorf("expected template migration/new.html, got %s", r.lastTemplate)
	}
}

func TestHandlerPrefillPage(t *testing.T) {
	h, svc, r := setupTestHandler()

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	formData := url.Values{
		"title": {"Prefilled from Schema Diff"},
		"sql":   {"ALTER TABLE users ADD COLUMN age int;"},
	}
	req := httptest.NewRequest("POST", "/databases/1/migrations/new", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withUserPrincipal(req, 1, "alice")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	if r.lastTemplate != "migration/new.html" {
		t.Errorf("expected template migration/new.html, got %s", r.lastTemplate)
	}

	list, _ := svc.ListMigrations(context.Background(), 1)
	if len(list) != 0 {
		t.Errorf("expected prefill route to NOT save migration to DB, got %d", len(list))
	}
}

func TestHandlerCreateDraft(t *testing.T) {
	h, _, _ := setupTestHandler()

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	formData := url.Values{
		"title":    {"Add orders table"},
		"sql_text": {"CREATE TABLE orders (id int);"},
	}
	req := httptest.NewRequest("POST", "/databases/1/migrations", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withUserPrincipal(req, 1, "alice")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 See Other, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if loc != "/databases/1/migrations/1" {
		t.Errorf("expected redirect to /databases/1/migrations/1, got %s", loc)
	}
}

func TestHandlerExecuteMigration(t *testing.T) {
	h, svc, r := setupTestHandler()
	ctx := context.Background()
	draft, _ := svc.CreateDraft(ctx, 1, "Add items", "CREATE TABLE items (id int);", 1)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/databases/1/migrations/1/execute", nil)
	req = withUserPrincipal(req, 1, "alice")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	if r.lastTemplate != "migration/detail.html" {
		t.Errorf("expected template migration/detail.html, got %s", r.lastTemplate)
	}

	m, _ := svc.GetMigration(ctx, draft.ID)
	if m.Status != StatusSuccess {
		t.Errorf("expected status 'success', got %s", m.Status)
	}
}

func TestHandlerExplainMigration(t *testing.T) {
	h, svc, r := setupTestHandler()
	ctx := context.Background()
	_, _ = svc.CreateDraft(ctx, 1, "Select items", "SELECT * FROM items;", 1)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	formData := url.Values{"analyze": {"true"}}
	req := httptest.NewRequest("POST", "/databases/1/migrations/1/explain", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withUserPrincipal(req, 1, "alice")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	if r.lastTemplate != "migration/explain.html" {
		t.Errorf("expected template migration/explain.html, got %s", r.lastTemplate)
	}
}

func TestHandlerAuthorizationRejection(t *testing.T) {
	repo := NewMemoryRepository()
	targetDB := &FakeTargetDB{Engine: contract.EnginePostgres}
	svc := NewCustomService(repo, targetDB, nil)
	renderer := &mockRenderer{}

	guards := contract.Guards{
		ViewDB: func(next http.Handler) http.Handler { return next },
		EditDB: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "Access denied", http.StatusForbidden)
			})
		},
	}

	h := NewHandler(svc, renderer, guards)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/databases/1/migrations", nil)
	req = withUserPrincipal(req, 2, "bob_view_only")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected View Only to view list, got status %d", rec.Code)
	}

	req = httptest.NewRequest("POST", "/databases/1/migrations", nil)
	req = withUserPrincipal(req, 2, "bob_view_only")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for View Only trying to create migration, got %d", rec.Code)
	}

	req = httptest.NewRequest("POST", "/databases/1/migrations/1/execute", nil)
	req = withUserPrincipal(req, 2, "bob_view_only")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for View Only trying to execute migration, got %d", rec.Code)
	}
}
