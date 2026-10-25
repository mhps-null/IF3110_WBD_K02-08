package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/web"
)

type fakeMatrixStore struct {
	groups       []Group
	databases    []Database
	access       []GroupAccess
	savedEntries map[int64]int
	savedGroupID int64
}

func (f *fakeMatrixStore) ListGroups(ctx context.Context) ([]Group, error) {
	return f.groups, nil
}

func (f *fakeMatrixStore) ListDatabases(ctx context.Context) ([]Database, error) {
	return f.databases, nil
}

func (f *fakeMatrixStore) ListGroupAccess(ctx context.Context) ([]GroupAccess, error) {
	return f.access, nil
}

func (f *fakeMatrixStore) SetGroupAccessLevels(ctx context.Context, groupID int64, entries map[int64]int) error {
	f.savedGroupID = groupID
	f.savedEntries = make(map[int64]int, len(entries))
	for k, v := range entries {
		f.savedEntries[k] = v
	}
	return nil
}

func TestSetAccess_WebPayloadLimitExceeded(t *testing.T) {
	h := newHandlerForTest(nil, &fakeRevoker{})

	hugePayload := strings.Repeat("a", (64<<10)+1024)
	form := url.Values{"group_id": {"1"}, "level_1": {"1"}, "padding": {hugePayload}}

	req := httptest.NewRequest(http.MethodPost, "/admin/access", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.SetAccess(rr, req)

	if loc := rr.Header().Get("Location"); loc != "/admin/access?flash=error" {
		t.Fatalf("expected redirect to /admin/access?flash=error on oversized body, got %q (code %d)", loc, rr.Code)
	}
}

func TestAccessMatrixTemplate_RendersAllRequiredFields(t *testing.T) {
	output := httptest.NewRecorder()
	renderer := NewTemplateRenderer("../../web/templates")
	err := renderer.Render(output, "admin/access.html", contract.PageData{
		Data: matrixPageData{
			Databases: []Database{{ID: 10, Name: "AppDB"}},
			Groups:    []Group{{ID: 2, Name: "Developers"}},
			AccessEntries: []accessMatrixEntry{
				{DatabaseName: "AppDB", GroupName: "Developers", Level: 1},
			},
		},
	})
	if err != nil {
		t.Fatalf("render access template: %v", err)
	}

	html := output.Body.String()
	if !strings.Contains(html, `<form method="post" action="/admin/access"`) ||
		!strings.Contains(html, `<select id="database" name="db_id" required>`) ||
		!strings.Contains(html, `<select id="group" name="group_id" required>`) ||
		!strings.Contains(html, `<select id="level" name="level" required>`) ||
		!strings.Contains(html, `<option value="">Pilih database</option>`) ||
		!strings.Contains(html, `<option value="">Pilih group</option>`) ||
		!strings.Contains(html, `<option value="">Pilih level akses</option>`) ||
		!strings.Contains(html, "Daftar matriks akses") ||
		!strings.Contains(html, "Developers") {
		t.Fatal("expected a single form with all required dropdowns and the saved access list")
	}
}

func TestSetAccess_WithCSRFMiddleware(t *testing.T) {
	fakeStore := &fakeMatrixStore{
		groups:    []Group{{ID: 1, Name: "Admins"}},
		databases: []Database{{ID: 10, Name: "AppDB"}},
	}

	h := &Handler{
		store:    &Store{},
		revoker:  &fakeRevoker{},
		renderer: NewTemplateRenderer("../../web/templates"),
	}

	csrfMW := web.NewCSRFMiddleware([]byte("12345678901234567890123456789012"), false)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/access", h.SetAccess)
	handler := csrfMW.Wrap(mux)

	t.Run("payload over 64KiB rejected by CSRF middleware before handler", func(t *testing.T) {
		cookieVal := "valid-token-format"
		_ = cookieVal

		oversizedForm := make(url.Values)
		oversizedForm.Set("group_id", "1")
		oversizedForm.Set("level_10", "2")
		oversizedForm.Set("data", string(bytes.Repeat([]byte("X"), 65<<10)))

		req := httptest.NewRequest(http.MethodPost, "/admin/access", strings.NewReader(oversizedForm.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request for request exceeding 64KiB, got %d", rec.Code)
		}
	})

	_ = fakeStore
}
