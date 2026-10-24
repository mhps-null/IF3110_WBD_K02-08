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

	// Default pass-through guards
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

	// Verify that prefill DID NOT save to DB
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

	// Expect redirect to detail page
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

	// Verify status became success
	m, _ := svc.GetMigration(ctx, draft.DBID, draft.ID)
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

	// Guard simulating View Only permission (EditDB rejects with 403 Forbidden)
	guards := contract.Guards{
		ViewDB: func(next http.Handler) http.Handler { return next },
		EditDB: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "Access denied: View Only permission cannot perform migration mutations", http.StatusForbidden)
			})
		},
	}

	h := NewHandler(svc, renderer, guards)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// 1. GET /databases/1/migrations should be ALLOWED for View Only
	req := httptest.NewRequest("GET", "/databases/1/migrations", nil)
	req = withUserPrincipal(req, 2, "bob_view_only")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected View Only to view list, got status %d", rec.Code)
	}

	// 2. POST /databases/1/migrations (create) should be FORBIDDEN (403)
	req = httptest.NewRequest("POST", "/databases/1/migrations", nil)
	req = withUserPrincipal(req, 2, "bob_view_only")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for View Only trying to create migration, got %d", rec.Code)
	}

	// 3. POST /databases/1/migrations/1/execute should be FORBIDDEN (403)
	req = httptest.NewRequest("POST", "/databases/1/migrations/1/execute", nil)
	req = withUserPrincipal(req, 2, "bob_view_only")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for View Only trying to execute migration, got %d", rec.Code)
	}
}

func TestHandlersRejectMigrationFromDifferentDatabase(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		form   url.Values
	}{
		{
			name:   "detail",
			method: http.MethodGet,
			path:   "/databases/1/migrations/1",
		},
		{
			name:   "update",
			method: http.MethodPost,
			path:   "/databases/1/migrations/1",
			form: url.Values{
				"title":    {"Tampered title"},
				"sql_text": {"DROP TABLE users;"},
			},
		},
		{
			name:   "execute",
			method: http.MethodPost,
			path:   "/databases/1/migrations/1/execute",
		},
		{
			name:   "explain",
			method: http.MethodPost,
			path:   "/databases/1/migrations/1/explain",
			form:   url.Values{"analyze": {"false"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, svc, renderer := setupTestHandler()
			ctx := context.Background()
			draft, err := svc.CreateDraft(ctx, 2, "Foreign database draft", "SELECT 1;", 7)
			if err != nil {
				t.Fatal(err)
			}
			if draft.ID != 1 {
				t.Fatalf("test setup migration ID=%d, want 1", draft.ID)
			}

			mux := http.NewServeMux()
			h.RegisterRoutes(mux)

			body := strings.NewReader("")
			if tc.form != nil {
				body = strings.NewReader(tc.form.Encode())
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			if tc.form != nil {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			req = withUserPrincipal(req, 7, "alice")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status=%d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
			}

			stored, err := svc.GetMigration(ctx, draft.DBID, draft.ID)
			if err != nil {
				t.Fatalf("reload draft: %v", err)
			}
			if stored.DBID != 2 || stored.Title != "Foreign database draft" || stored.Status != StatusDraft {
				t.Fatalf("foreign migration was changed: DBID=%d title=%q status=%q", stored.DBID, stored.Title, stored.Status)
			}
			if renderer.lastTemplate != "" {
				t.Fatalf("handler rendered %q for a migration from another database", renderer.lastTemplate)
			}
		})
	}
}
