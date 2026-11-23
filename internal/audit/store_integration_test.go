//go:build integration

package audit

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/observability"
)

func TestSQLStoreIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_METADATA_DSN")
	if dsn == "" {
		dsn = os.Getenv("TEST_DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_METADATA_DSN or TEST_DATABASE_URL is not set")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	ddl, err := os.ReadFile("../../migrations_meta/003_audit_logs.sql")
	if err != nil {
		t.Fatalf("read 003_audit_logs.sql: %v", err)
	}
	if _, err := db.ExecContext(ctx, string(ddl)); err != nil {
		t.Fatalf("apply 003_audit_logs.sql: %v", err)
	}

	store := NewSQLStore(db, nil)
	corrID := "integ-corr-" + time.Now().Format("150405.000")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DELETE FROM audit_logs WHERE correlation_id = $1", corrID)
	})

	reqCtx := contract.WithPrincipal(ctx, contract.Principal{UserID: 1, Username: "integ_admin", IsPlatformAdmin: true})
	reqCtx = observability.WithCorrelationID(reqCtx, corrID)
	reqCtx, span := observability.NewTracer("integ", nil).StartSpan(reqCtx, "audit.integ_test")
	defer span.End()

	err = store.Record(reqCtx, Entry{
		Event:    "migration.execute",
		Database: "test_ecommerce_pg",
		Status:   "success",
		Details: map[string]any{
			"migration_id": 77,
			"password":     "mustNotBeStoredPlain",
		},
	})
	if err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	page, err := store.List(ctx, Filter{Actor: "integ_admin", Event: "migration.execute", Database: "test_ecommerce_pg"})
	if err != nil || page.Total < 1 || len(page.Items) < 1 {
		t.Fatalf("List failed: page=%+v err=%v", page, err)
	}

	found := page.Items[0]
	if found.CorrelationID != corrID || found.TraceID != span.Context().TraceID {
		t.Fatalf("unexpected trace/correlation fields: %+v", found)
	}
	if found.Details["password"] != observability.RedactedValue || strings.Contains(found.Details["password"].(string), "mustNotBeStoredPlain") {
		t.Fatalf("expected password in JSONB details to be redacted, got %+v", found.Details)
	}
}
