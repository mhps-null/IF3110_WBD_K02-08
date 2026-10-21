package web

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeadersAreSet(t *testing.T) {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(recorder, req)
	for _, header := range []string{"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy", "Content-Security-Policy"} {
		if recorder.Header().Get(header) == "" {
			t.Errorf("security header %q missing", header)
		}
	}
}

func TestRequestLoggerDoesNotLogPathQueryOrCookie(t *testing.T) {
	var logOutput bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logOutput, nil))
	req := httptest.NewRequest(http.MethodGet, "/reset/secret-session?password=hidden", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: "secret-session-value"})
	recorder := httptest.NewRecorder()
	RequestLogger(logger, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(recorder, req)
	logLine := logOutput.String()
	for _, secret := range []string{"reset/secret-session", "password=hidden", "secret-session-value"} {
		if strings.Contains(logLine, secret) {
			t.Errorf("log contains sensitive request data %q", secret)
		}
	}
	if !strings.Contains(logLine, "duration_ms") {
		t.Errorf("expected safe request metadata, got %q", logLine)
	}
}
