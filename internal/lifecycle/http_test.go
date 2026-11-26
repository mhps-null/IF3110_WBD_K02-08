package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/audit"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/observability"
)

func TestValidationFailureReportsFieldAndPosition(t *testing.T) {
	recorder := httptest.NewRecorder()
	err := sqlValidationFailure("sql_down", &pgconn.PgError{
		Code: "42601", Position: 8, Message: "raw diagnostic",
	})
	failure(recorder, err)

	var response struct {
		Error      string             `json:"error"`
		Validation sqlValidationError `json:"validation"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != 400 || response.Validation.Field != "sql_down" ||
		response.Validation.SQLState != "42601" || response.Validation.Position != 8 ||
		!strings.Contains(response.Error, "SQL Down") || strings.Contains(recorder.Body.String(), "raw diagnostic") {
		t.Fatalf("unexpected validation response: %d %s", recorder.Code, recorder.Body)
	}
}

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
