package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCSRFForJSONAPI(t *testing.T) {
	middleware := NewCSRFMiddleware([]byte("12345678901234567890123456789012"), false)
	token, err := middleware.generate()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("accepts JSON body with matching X-CSRF-Token header without consuming body", func(t *testing.T) {
		payload := `{"username":"alice","password":"secret"}`
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", token)
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})

		rec := httptest.NewRecorder()
		var gotBody string
		middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d want=200", rec.Code)
		}
		if gotBody != payload {
			t.Fatalf("body=%q want=%q", gotBody, payload)
		}
	})

	t.Run("rejects JSON mutation missing CSRF header with JSON 403", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/admin/groups", strings.NewReader(`{"name":"team"}`))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})

		rec := httptest.NewRecorder()
		middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status=%d want=403", rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("expected json content type, got %q", rec.Header().Get("Content-Type"))
		}
		var out map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out["error"] == "" {
			t.Fatalf("expected JSON error body, got %q", rec.Body.String())
		}
	})
}

func TestSPAHandler(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><div id='root'></div>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("console.log('ok');"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := NewSPAHandler(dir)

	t.Run("serves static asset", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "console.log") {
			t.Fatalf("unexpected static response: %d %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("falls back to index.html for SPA client routes", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/databases/12/schema", nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "id='root'") {
			t.Fatalf("unexpected fallback response: %d %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("never falls back to HTML for unmatched /api/ routes", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/unknown/route", nil))
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("unexpected /api/ 404 response: %d ct=%q", rec.Code, rec.Header().Get("Content-Type"))
		}
	})
}
