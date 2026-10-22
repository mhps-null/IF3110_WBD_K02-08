package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/example/if3110-milestone1/internal/contract"
)

func TestCSRF(t *testing.T) {
	middleware := NewCSRFMiddleware([]byte("12345678901234567890123456789012"), false)
	token, err := middleware.generate()
	if err != nil {
		t.Fatal(err)
	}
	other, err := middleware.generate()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, formToken string
		want            int
	}{
		{"matching", token, 204}, {"missing", "", 403}, {"mismatched", other, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"csrf_token": {tc.formToken}}
			req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})
			called := false
			rec := httptest.NewRecorder()
			middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if CSRFTokenFromRequest(r) != token {
					t.Fatal("token not propagated")
				}
				w.WriteHeader(204)
			})).ServeHTTP(rec, req)
			if rec.Code != tc.want || called != (tc.want == 204) {
				t.Fatalf("status=%d called=%t", rec.Code, called)
			}
		})
	}
}
func TestRendererUsesHTMLEscaping(t *testing.T) {
	renderer, err := NewRenderer("../../web/templates")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if err := renderer.Render(rec, "login", contract.PageData{Flash: "<script>alert(1)</script>"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.Body.String(), "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatal("untrusted HTML not escaped")
	}
}
