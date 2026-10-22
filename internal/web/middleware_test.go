package web

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityMiddleware(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	req := httptest.NewRequest("GET", "/reset/secret-session?password=hidden", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: "secret-session-value"})
	rec := httptest.NewRecorder()
	handler := RequestLogger(logger, SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })))
	handler.ServeHTTP(rec, req)
	for _, name := range []string{"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy", "Content-Security-Policy"} {
		if rec.Header().Get(name) == "" {
			t.Errorf("missing %s", name)
		}
	}
	for _, secret := range []string{"reset/secret-session", "password=hidden", "secret-session-value"} {
		if strings.Contains(output.String(), secret) {
			t.Errorf("sensitive request data logged: %s", secret)
		}
	}
	if !strings.Contains(output.String(), "duration_ms") {
		t.Fatal("request was not logged")
	}
}
