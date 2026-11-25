package lifecycle

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/audit"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/observability"
)

func TestAuditPersistenceFailureIsReturnedAndSurfaced(t *testing.T) {
	auditStore := audit.NewMemoryStore()
	auditStore.Err = errors.New("storage unavailable")
	handler := Handler{
		Audit:  auditStore,
		Logger: observability.NewLogger(io.Discard, slog.LevelError),
	}

	err := handler.record(context.Background(), "migration.reject", 1, 2, 3)
	if err == nil {
		t.Fatal("expected audit persistence error")
	}

	recorder := httptest.NewRecorder()
	auditFailure(recorder)
	if recorder.Code != 500 || !strings.Contains(recorder.Body.String(), "refresh state before retrying") {
		t.Fatalf("audit failure response must explain that the mutation may have completed: %d %q", recorder.Code, recorder.Body.String())
	}
}
