package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/example/if3110-milestone1/internal/contract"
)

func TestCSRFMiddlewareAcceptsMatchingSignedToken(t *testing.T) {
	m := NewCSRFMiddleware([]byte("0123456789abcdef0123456789abcdef"), false)
	token, err := m.generate()
	if err != nil {
		t.Fatalf("generate() error = %v", err)
	}
	form := url.Values{"csrf_token": {token}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})
	called := false
	handler := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if got := CSRFTokenFromRequest(r); got != token {
			t.Errorf("CSRFTokenFromRequest() = %q, want token", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if !called {
		t.Fatal("next handler was not called")
	}
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestCSRFMiddlewareRejectsMissingToken(t *testing.T) {
	m := NewCSRFMiddleware([]byte("0123456789abcdef0123456789abcdef"), false)
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=user"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	m.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("next handler must not run") })).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

func TestCSRFMiddlewareRejectsMismatchedToken(t *testing.T) {
	m := NewCSRFMiddleware([]byte("0123456789abcdef0123456789abcdef"), false)
	cookieToken, _ := m.generate()
	otherToken, _ := m.generate()
	form := url.Values{"csrf_token": {otherToken}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: cookieToken})
	recorder := httptest.NewRecorder()
	m.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("next handler must not run") })).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

func TestRendererUsesHTMLEscaping(t *testing.T) {
	renderer, err := NewRenderer("../../web/templates")
	if err != nil {
		t.Fatalf("NewRenderer() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	err = renderer.Render(recorder, "login", contract.PageData{Flash: "<script>alert(1)</script>", CSRFToken: "signed-token"})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("template did not escape untrusted text")
	}
	if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("expected escaped content, body=%q", body)
	}
}
