package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCreateGroup_ValidatesName(t *testing.T) {
	h := newHandlerForTest(nil, &fakeRevoker{})
	cases := []struct{ name, group string }{
		{"empty", ""},
		{"whitespace only", "   "},
		{"too long", strings.Repeat("a", 101)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			form := url.Values{"name": {c.group}}
			req := httptest.NewRequest(http.MethodPost, "/admin/groups", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()
			h.CreateGroup(rr, req)

			if loc := rr.Header().Get("Location"); loc != "/admin/groups?flash=error" {
				t.Fatalf("got %q, want flash=error", loc)
			}
		})
	}
}

func TestAddMember_InvalidUserID(t *testing.T) {
	h := newHandlerForTest(nil, &fakeRevoker{})
	for _, uid := range []string{"", "abc", "0", "-1"} {
		t.Run("uid="+uid, func(t *testing.T) {
			form := url.Values{"user_id": {uid}}
			req := httptest.NewRequest(http.MethodPost, "/admin/groups/1/members", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.SetPathValue("id", "1")
			rr := httptest.NewRecorder()
			h.AddMember(rr, req)

			if loc := rr.Header().Get("Location"); loc != "/admin/groups/1?flash=error" {
				t.Fatalf("got %q, want flash=error", loc)
			}
		})
	}
}

func TestRemoveMember_InvalidPath(t *testing.T) {
	h := newHandlerForTest(nil, &fakeRevoker{})
	req := httptest.NewRequest(http.MethodPost, "/admin/groups/abc/members/1/remove", nil)
	req.SetPathValue("id", "abc")
	req.SetPathValue("uid", "1")
	rr := httptest.NewRecorder()
	h.RemoveMember(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rr.Code)
	}
}

func TestUpdateGroup_InvalidID(t *testing.T) {
	h := newHandlerForTest(nil, &fakeRevoker{})
	req := httptest.NewRequest(http.MethodPost, "/admin/groups/abc/edit", nil)
	req.SetPathValue("id", "abc")
	rr := httptest.NewRecorder()
	h.UpdateGroup(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rr.Code)
	}
}

func TestUpdateGroup_ValidatesName(t *testing.T) {
	h := newHandlerForTest(nil, &fakeRevoker{})
	cases := []struct{ name, group string }{
		{"empty", ""},
		{"whitespace only", "   "},
		{"too long", strings.Repeat("a", 101)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			form := url.Values{"name": {c.group}}
			req := httptest.NewRequest(http.MethodPost, "/admin/groups/42/edit", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.SetPathValue("id", "42")
			rr := httptest.NewRecorder()
			h.UpdateGroup(rr, req)

			if loc := rr.Header().Get("Location"); loc != "/admin/groups/42?flash=error" {
				t.Fatalf("got %q, want /admin/groups/42?flash=error", loc)
			}
		})
	}
}
