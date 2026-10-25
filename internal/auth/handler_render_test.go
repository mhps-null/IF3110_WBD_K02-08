package auth

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/session"
)

type partialFailRenderer struct {
	failName string
}

func (r partialFailRenderer) Render(w http.ResponseWriter, name string, data contract.PageData) error {
	if name == r.failName {
		_, _ = io.WriteString(w, "PARTIAL INTERNAL TEMPLATE CONTENT")
		return errors.New("internal template path /secret should not be exposed")
	}
	if name == "error" {
		_, _ = io.WriteString(w, "SAFE ERROR: "+data.Flash)
		return nil
	}
	_, _ = io.WriteString(w, "OK")
	return nil
}

func TestRenderFailureUsesGenericErrorTemplateWithoutPartialBody(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewHandler(
		fakeUserRepo{},
		&fakeSessions{valid: map[string]contract.SessionInfo{}},
		partialFailRenderer{failName: "login"},
		false,
		session.DefaultConfig(),
		logger,
	)

	rec := httptest.NewRecorder()
	h.LoginPage(rec, httptest.NewRequest(http.MethodGet, "/login", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusInternalServerError)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "SAFE ERROR: Terjadi kesalahan. Silakan coba lagi.") {
		t.Fatalf("safe error template not rendered: %q", body)
	}
	if strings.Contains(body, "PARTIAL INTERNAL TEMPLATE CONTENT") || strings.Contains(body, "/secret") {
		t.Fatalf("partial/internal error leaked in response: %q", body)
	}
}

func TestServiceUnavailableUsesErrorTemplateAndPreservesStatus(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewHandler(
		fakeUserRepo{},
		&fakeSessions{valid: map[string]contract.SessionInfo{}},
		partialFailRenderer{},
		false,
		session.DefaultConfig(),
		logger,
	)

	rec := httptest.NewRecorder()
	h.serviceUnavailable(rec, httptest.NewRequest(http.MethodGet, "/", nil), "Layanan sementara tidak tersedia.")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "SAFE ERROR: Layanan sementara tidak tersedia.") {
		t.Fatalf("error template missing safe message: %q", rec.Body.String())
	}
}

// Compile-time check for the fake auth repository used by the handler tests.
var _ UserRepository = fakeUserRepo{}
