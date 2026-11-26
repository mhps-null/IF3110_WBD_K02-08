package audit

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/observability"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/testutil"
)

type stubAuditDriver struct{}

type stubAuditConn struct {
	mode string
}

type stubAuditRows struct {
	cols []string
	data [][]driver.Value
	idx  int
}

func (stubAuditDriver) Open(dsn string) (driver.Conn, error) {
	return &stubAuditConn{mode: dsn}, nil
}
func (*stubAuditConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not implemented") }
func (*stubAuditConn) Begin() (driver.Tx, error)           { return nil, errors.New("not implemented") }
func (*stubAuditConn) Close() error                        { return nil }

func (c *stubAuditConn) ExecContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Result, error) {
	if c.mode == "fail_exec" {
		return nil, errors.New("exec failed")
	}
	return driver.RowsAffected(1), nil
}

func (c *stubAuditConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "COUNT(1)") {
		if c.mode == "fail_count" {
			return nil, errors.New("count failed")
		}
		return &stubAuditRows{cols: []string{"count"}, data: [][]driver.Value{{int64(1)}}}, nil
	}
	if c.mode == "fail_query" {
		return nil, errors.New("query failed")
	}
	now := time.Now().UTC()
	return &stubAuditRows{
		cols: []string{"id", "created_at", "actor", "event", "database_name", "status", "correlation_id", "trace_id", "span_id", "details"},
		data: [][]driver.Value{
			{int64(1), now, "admin", "migration.approved", "ecommerce_pg", "success", "corr-1", "trace-1", "span-1", []byte(`{"migration_id":42}`)},
		},
	}, nil
}

func (r *stubAuditRows) Columns() []string { return r.cols }
func (r *stubAuditRows) Close() error      { return nil }
func (r *stubAuditRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.idx])
	r.idx++
	return nil
}

func init() {
	sql.Register("audit_stub_driver", stubAuditDriver{})
}

func TestAuditStoreRecordRedactionAndFiltering(t *testing.T) {
	store := NewMemoryStore()
	tracer := observability.NewTracer("api", nil)

	ctx := contract.WithPrincipal(context.Background(), contract.Principal{UserID: 1, Username: "admin", IsPlatformAdmin: true})
	ctx = observability.WithCorrelationID(ctx, "corr-audit-1")
	ctx, span := tracer.StartSpan(ctx, "migration.approve")
	defer span.End()

	err := store.Record(ctx, Entry{
		Event:    "migration.approved",
		Database: "test_ecommerce_pg",
		Status:   "",
		Details: map[string]any{
			"migration_id": 10,
			"password":     "plainSecretShouldBeRedacted",
			"dsn":          "postgres://user:dsnSecret@localhost:5433/db",
		},
	})
	if err != nil {
		t.Fatalf("unexpected record error: %v", err)
	}

	_ = store.Record(context.Background(), Entry{
		Event:    "migration.rejected",
		Database: "test_hr_pg",
		Status:   "rejected",
	})

	page, err := store.List(ctx, Filter{Actor: "admin", Event: "migration.approved", Database: "test_ecommerce_pg", Status: "success"})
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("expected 1 filtered audit entry, got page=%+v err=%v", page, err)
	}

	item := page.Items[0]
	if item.TraceID != span.Context().TraceID || item.SpanID != span.Context().SpanID || item.CorrelationID != "corr-audit-1" {
		t.Fatalf("expected trace correlation in audit entry, got %+v", item)
	}
	if item.Details["password"] != observability.RedactedValue {
		t.Fatalf("expected password detail to be redacted, got %v", item.Details["password"])
	}
	if strings.Contains(item.Details["dsn"].(string), "dsnSecret") {
		t.Fatalf("expected dsn detail password to be redacted, got %v", item.Details["dsn"])
	}

	// Out-of-range pagination returns empty items with accurate total
	emptyPage, err := store.List(ctx, Filter{Page: 5, PageSize: 10})
	if err != nil || len(emptyPage.Items) != 0 || emptyPage.Total != 2 {
		t.Fatalf("expected empty out-of-range page with total=2, got %+v err=%v", emptyPage, err)
	}

	// Filter mismatch cases
	noMatch, _ := store.List(ctx, Filter{Event: "nonexistent"})
	if noMatch.Total != 0 {
		t.Fatalf("expected 0 matches for nonexistent event, got %d", noMatch.Total)
	}
	noStatusMatch, _ := store.List(ctx, Filter{Status: "unknown"})
	if noStatusMatch.Total != 0 {
		t.Fatalf("expected 0 matches for unknown status, got %d", noStatusMatch.Total)
	}
}

func TestSQLStoreRecordAndList(t *testing.T) {
	db, err := sql.Open("audit_stub_driver", "ok")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var logBuf bytes.Buffer
	logger := observability.NewLogger(&logBuf, slog.LevelInfo)
	store := NewSQLStore(db, logger)

	ctx := contract.WithPrincipal(context.Background(), contract.Principal{UserID: 1, Username: "admin", IsPlatformAdmin: true})
	err = store.Record(ctx, Entry{
		Event:    "migration.approved",
		Database: "ecommerce_pg",
		Status:   "success",
		Details:  map[string]any{"password": "secret123", "migration_id": 42},
	})
	if err != nil {
		t.Fatalf("SQLStore.Record failed: %v", err)
	}
	if !strings.Contains(logBuf.String(), `"event":"migration.approved"`) {
		t.Fatalf("expected SQLStore.Record to emit structured log, got: %s", logBuf.String())
	}

	page, err := store.List(ctx, Filter{
		Actor:    "admin",
		Event:    "migration.approved",
		Database: "ecommerce_pg",
		Status:   "success",
		Page:     1,
		PageSize: 10,
	})
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("SQLStore.List failed: page=%+v err=%v", page, err)
	}
	if page.Items[0].Actor != "admin" || page.Items[0].Details["migration_id"] != float64(42) {
		t.Fatalf("unexpected item from SQLStore.List: %+v", page.Items[0])
	}

	// Error cases
	for _, mode := range []string{"fail_exec", "fail_count", "fail_query"} {
		errDB, _ := sql.Open("audit_stub_driver", mode)
		errStore := NewSQLStore(errDB, nil)
		if mode == "fail_exec" {
			if err := errStore.Record(ctx, Entry{Event: "test"}); err == nil {
				t.Errorf("expected Record error in mode %s", mode)
			}
		} else {
			if _, err := errStore.List(ctx, Filter{}); err == nil {
				t.Errorf("expected List error in mode %s", mode)
			}
		}
		_ = errDB.Close()
	}
}

func TestAuditHandlerSuccessFailureAndAccessDenied(t *testing.T) {
	store := NewMemoryStore()
	_ = store.Record(context.Background(), Entry{Actor: "admin", Event: "user.create", Database: "", Status: "success"})
	_ = store.Record(context.Background(), Entry{Actor: "alice", Event: "migration.execute", Database: "ecommerce_pg", Status: "failed"})

	guards := testutil.NewFakeGuards(testutil.NewFakeAccessChecker())
	handler := NewHandler(store, guards)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Access Denied, non-admin user
	reqForbidden := httptest.NewRequest(http.MethodGet, "/api/audit-logs", nil)
	reqForbidden = reqForbidden.WithContext(contract.WithPrincipal(reqForbidden.Context(), contract.Principal{
		UserID: 2, Username: "alice", IsPlatformAdmin: false,
	}))
	recForbidden := httptest.NewRecorder()
	mux.ServeHTTP(recForbidden, reqForbidden)
	if recForbidden.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for non-admin, got %d", recForbidden.Code)
	}

	// Success, platform admin with filter
	reqOK := httptest.NewRequest(http.MethodGet, "/api/audit-logs?actor=alice&status=failed&page=1&page_size=10", nil)
	reqOK = reqOK.WithContext(contract.WithPrincipal(reqOK.Context(), contract.Principal{
		UserID: 1, Username: "admin", IsPlatformAdmin: true,
	}))
	recOK := httptest.NewRecorder()
	mux.ServeHTTP(recOK, reqOK)
	if recOK.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for admin, got %d", recOK.Code)
	}
	var page Page
	if err := json.Unmarshal(recOK.Body.Bytes(), &page); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Actor != "alice" {
		t.Fatalf("unexpected filtered response: %+v", page)
	}

	// Failure: store error returns 500
	store.Err = errors.New("db connection lost")
	reqErr := httptest.NewRequest(http.MethodGet, "/api/audit-logs", nil)
	reqErr = reqErr.WithContext(contract.WithPrincipal(reqErr.Context(), contract.Principal{
		UserID: 1, Username: "admin", IsPlatformAdmin: true,
	}))
	recErr := httptest.NewRecorder()
	mux.ServeHTTP(recErr, reqErr)
	if recErr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 Internal Server Error on store failure, got %d", recErr.Code)
	}
	if err := store.Record(context.Background(), Entry{Event: "fail"}); err == nil {
		t.Fatal("expected MemoryStore.Record to fail when Err is set")
	}

	// Default fail-closed guards when nil middleware or nil admin guard is passed
	defaultHandler := NewHandler(NewMemoryStore(), contract.Guards{})
	nilAuthMux := http.NewServeMux()
	defaultHandler.RegisterRoutesWithAuth(nilAuthMux, nil)
	recNilAuth := httptest.NewRecorder()
	nilAuthMux.ServeHTTP(recNilAuth, httptest.NewRequest(http.MethodGet, "/api/audit-logs", nil))
	if recNilAuth.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when authenticate middleware is nil, got %d", recNilAuth.Code)
	}

	defaultAdminMux := http.NewServeMux()
	defaultHandler.RegisterRoutes(defaultAdminMux)
	recDefAdminForbidden := httptest.NewRecorder()
	defaultAdminMux.ServeHTTP(recDefAdminForbidden, httptest.NewRequest(http.MethodGet, "/api/audit-logs", nil))
	if recDefAdminForbidden.Code != http.StatusForbidden {
		t.Fatalf("expected 403 from default admin guard without principal, got %d", recDefAdminForbidden.Code)
	}
}
