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
			form := url.Values{"group_id": {gid}, "level_1": {"1"}}
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
