package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

type captureRenderer struct{}

func (*captureRenderer) Render(w http.ResponseWriter, _ string, _ contract.PageData) error {
	w.WriteHeader(http.StatusOK)
	return nil
}

type fakeRevoker struct{ err error }

func (f *fakeRevoker) RevokeAllForUser(_ context.Context, _ int64) error { return f.err }

func newHandlerForTest(store *Store, rev contract.SessionRevoker) *Handler {
	return NewHandler(store, rev, &captureRenderer{}, nil)
}

func withPrincipal(r *http.Request, p contract.Principal) *http.Request {
	return r.WithContext(contract.WithPrincipal(r.Context(), p))
}

func TestDeactivateUser_SelfForbidden(t *testing.T) {
	h := newHandlerForTest(nil, &fakeRevoker{})
	req := httptest.NewRequest(http.MethodPost, "/admin/users/7/deactivate", nil)
	req.SetPathValue("id", "7")
	req = withPrincipal(req, contract.Principal{UserID: 7, IsPlatformAdmin: true})
	rr := httptest.NewRecorder()

	h.DeactivateUser(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("got %d, want 303", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/admin/users?flash=error" {
		t.Fatalf("got location %q, want flash=error", loc)
	}
}

func TestDeactivateUser_NotFound(t *testing.T) {
	h := newHandlerForTest(nil, &fakeRevoker{})
	req := httptest.NewRequest(http.MethodPost, "/admin/users/x/deactivate", nil)
	req.SetPathValue("id", "x")
	req = withPrincipal(req, contract.Principal{UserID: 1, IsPlatformAdmin: true})
	rr := httptest.NewRecorder()

	h.DeactivateUser(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rr.Code)
	}
}

func TestCreateUser_ValidatesInput(t *testing.T) {
	h := newHandlerForTest(nil, &fakeRevoker{})
	cases := []struct{ name, user, pass string }{
		{"empty username", "", "password123"},
		{"short password", "bob", "short"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			form := url.Values{"username": {c.user}, "password": {c.pass}}
			req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()
			h.CreateUser(rr, req)

			if loc := rr.Header().Get("Location"); loc != "/admin/users?flash=error" {
				t.Fatalf("got location %q, want flash=error", loc)
			}
		})
	}
}

func TestResetPassword_ValidatesLength(t *testing.T) {
	h := newHandlerForTest(nil, &fakeRevoker{})
	form := url.Values{"password": {"short"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/users/1/reset-password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "1")
	rr := httptest.NewRecorder()
	h.ResetPassword(rr, req)

	if loc := rr.Header().Get("Location"); loc != "/admin/users?flash=error" {
		t.Fatalf("got location %q, want flash=error", loc)
	}
}
