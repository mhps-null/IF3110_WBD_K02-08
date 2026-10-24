//go:build integration

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	authpkg "github.com/example/if3110-milestone1/internal/auth"
	"github.com/example/if3110-milestone1/internal/session"
)

// csrfFlowUserRepository lets the real login handler run without a database.
// It intentionally returns a generic non-existing-user error so valid CSRF
// requests reach the auth handler but never create a session.
type csrfFlowUserRepository struct {
	lookupCount int
}

func (r *csrfFlowUserRepository) FindByUsername(_ context.Context, _ string) (authpkg.User, error) {
	r.lookupCount++
	return authpkg.User{}, authpkg.ErrUserNotFound
}

func (r *csrfFlowUserRepository) FindByID(_ context.Context, _ int64) (authpkg.User, error) {
	return authpkg.User{}, authpkg.ErrUserNotFound
}

func TestCSRFAuthFlowIntegration(t *testing.T) {
	renderer, err := NewRenderer("../../web/templates")
	if err != nil {
		t.Fatalf("create renderer: %v", err)
	}

	users := &csrfFlowUserRepository{}
	authHandler := authpkg.NewHandler(users, nil, renderer, false, session.DefaultConfig(), nil)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", authHandler.LoginPage)
	mux.HandleFunc("POST /login", authHandler.Login)

	middleware := NewCSRFMiddleware([]byte("integration-test-secret-32-bytes-minimum"), false)
	handler := middleware.Wrap(mux)

	// The login page must issue a CSRF cookie and render the corresponding token
	// into the hidden form field that the browser submits on POST.
	getReq := httptest.NewRequest(http.MethodGet, "/login", nil)
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /login status=%d, want %d", getRec.Code, http.StatusOK)
	}

	var csrfCookie *http.Cookie
	for _, cookie := range getRec.Result().Cookies() {
		if cookie.Name == csrfCookieName {
			csrfCookie = cookie
			break
		}
	}
	if csrfCookie == nil || csrfCookie.Value == "" {
		t.Fatal("GET /login did not issue a CSRF cookie")
	}
	if !csrfCookie.HttpOnly || csrfCookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("CSRF cookie is missing expected HttpOnly/SameSite attributes")
	}

	tokenPattern := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)
	matches := tokenPattern.FindStringSubmatch(getRec.Body.String())
	if len(matches) != 2 {
		t.Fatal("login form does not render the hidden CSRF token")
	}
	formToken := matches[1]
	if formToken != csrfCookie.Value {
		t.Fatal("rendered form CSRF token does not match CSRF cookie")
	}

	loginForm := url.Values{
		"username": {"nonexistent-integration-user"},
		"password": {"irrelevant-test-password"},
	}

	// A state-changing request without the hidden token must be rejected before
	// it reaches the real auth handler/repository.
	missingTokenReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(loginForm.Encode()))
	missingTokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	missingTokenReq.AddCookie(csrfCookie)
	missingTokenRec := httptest.NewRecorder()
	handler.ServeHTTP(missingTokenRec, missingTokenReq)
	if missingTokenRec.Code != http.StatusForbidden {
		t.Fatalf("POST /login without CSRF token status=%d, want %d", missingTokenRec.Code, http.StatusForbidden)
	}
	if users.lookupCount != 0 {
		t.Fatal("auth handler ran despite missing CSRF token")
	}

	// With the cookie and matching hidden field, the request must pass CSRF
	// validation and reach auth. The fake repository then returns a generic
	// failed-login response (401), proving the middleware did not block it.
	validForm := url.Values{
		"csrf_token": {formToken},
		"username":   {"nonexistent-integration-user"},
		"password":   {"irrelevant-test-password"},
	}
	validReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(validForm.Encode()))
	validReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	validReq.AddCookie(csrfCookie)
	validRec := httptest.NewRecorder()
	handler.ServeHTTP(validRec, validReq)
	if validRec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /login with valid CSRF token status=%d, want %d", validRec.Code, http.StatusUnauthorized)
	}
	if users.lookupCount != 1 {
		t.Fatalf("auth handler lookup count=%d, want 1", users.lookupCount)
	}
	if !strings.Contains(validRec.Body.String(), "Username atau password salah.") {
		t.Fatal("valid CSRF request did not reach the expected generic login failure page")
	}
}
