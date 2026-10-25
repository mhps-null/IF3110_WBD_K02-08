package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSetAccess_InvalidGroupID(t *testing.T) {
	h := newHandlerForTest(nil, &fakeRevoker{})

	for _, gid := range []string{"", "abc", "0", "-1"} {
		t.Run("gid="+gid, func(t *testing.T) {
			form := url.Values{"csrf_token": {"token"}, "db_id": {"1"}, "group_id": {gid}, "level": {"1"}}
			req := httptest.NewRequest(http.MethodPost, "/admin/access", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()
			h.SetAccess(rr, req)

			if loc := rr.Header().Get("Location"); loc != "/admin/access?flash=error" {
				t.Fatalf("got %q, want flash=error", loc)
			}
		})
	}
}

func TestSetAccess_InvalidDatabaseID(t *testing.T) {
	h := newHandlerForTest(nil, &fakeRevoker{})

	for _, dbID := range []string{"", "abc", "0", "-1"} {
		t.Run(dbID, func(t *testing.T) {
			form := url.Values{"csrf_token": {"token"}, "db_id": {dbID}, "group_id": {"1"}, "level": {"1"}}
			req := httptest.NewRequest(http.MethodPost, "/admin/access", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()
			h.SetAccess(rr, req)

			if loc := rr.Header().Get("Location"); loc != "/admin/access?flash=error" {
				t.Fatalf("got %q, want flash=error", loc)
			}
		})
	}
}

func TestSetAccess_InvalidLevelValue(t *testing.T) {
	h := newHandlerForTest(nil, &fakeRevoker{})

	for _, lvl := range []string{"3", "-1", "abc", "99", "undefined"} {
		t.Run("lvl="+lvl, func(t *testing.T) {
			form := url.Values{"csrf_token": {"token"}, "db_id": {"1"}, "group_id": {"1"}, "level": {lvl}}
			req := httptest.NewRequest(http.MethodPost, "/admin/access", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()
			h.SetAccess(rr, req)

			if loc := rr.Header().Get("Location"); loc != "/admin/access?flash=error" {
				t.Fatalf("got %q, want flash=error", loc)
			}
		})
	}
}

func TestSetAccess_UnexpectedFieldsOrMultipleValues(t *testing.T) {
	h := newHandlerForTest(nil, &fakeRevoker{})

	t.Run("unexpected field", func(t *testing.T) {
		form := url.Values{"csrf_token": {"token"}, "db_id": {"1"}, "group_id": {"1"}, "level": {"1"}, "unknown": {"value"}}
		req := httptest.NewRequest(http.MethodPost, "/admin/access", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		h.SetAccess(rr, req)

		if loc := rr.Header().Get("Location"); loc != "/admin/access?flash=error" {
			t.Fatalf("got %q, want flash=error", loc)
		}
	})

	t.Run("multiple values for same level", func(t *testing.T) {
		form := url.Values{"csrf_token": {"token"}, "db_id": {"1"}, "group_id": {"1"}, "level": {"1", "2"}}
		req := httptest.NewRequest(http.MethodPost, "/admin/access", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		h.SetAccess(rr, req)

		if loc := rr.Header().Get("Location"); loc != "/admin/access?flash=error" {
			t.Fatalf("got %q, want flash=error", loc)
		}
	})
}
