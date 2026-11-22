package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

type fakeSQLError struct{ msg, state string }

func (e fakeSQLError) Error() string    { return e.msg }
func (e fakeSQLError) SQLState() string { return e.state }

type fakeStringer struct{ val string }

func (s fakeStringer) String() string { return s.val }

func TestRedactionComprehensive(t *testing.T) {
	for _, key := range []string{
		"password", "PASS", "user_passwd", "session_secret", "db_encryption_key",
		"csrf_token", "session_id", "Set-Cookie", "Authorization", "dsn",
		"DATABASE_URL", "user_credential", "api_key",
	} {
		if !IsSensitiveKey(key) {
			t.Errorf("expected key %q to be sensitive", key)
		}
	}
	for _, safeKey := range []string{"username", "database", "status", "query", "rows", "bypass"} {
		if IsSensitiveKey(safeKey) {
			t.Errorf("expected key %q to be non-sensitive", safeKey)
		}
	}

	if RedactString("") != "" {
		t.Fatal("expected empty string to remain empty")
	}

	rawText := "pg=postgres://admin:pgSecret@localhost:5432/db " +
		"mysql=root:mySecret@tcp(127.0.0.1:3306)/inv " +
		"kv=password=kvSecret1 token:kvSecret2 auth=Bearer jwtSecretToken123"
	redacted := RedactString(rawText)
	for _, secret := range []string{"pgSecret", "mySecret", "kvSecret1", "kvSecret2", "jwtSecretToken123"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("expected %q to be redacted in %q", secret, redacted)
		}
	}

	if RedactValue("password", 12345) != RedactedValue {
		t.Fatal("expected sensitive key with non-string value to be redacted")
	}
	if RedactValue("err", errors.New("failed: password=errSecret")) != "failed: password="+RedactedValue {
		t.Fatalf("unexpected error redaction: %v", RedactValue("err", errors.New("failed: password=errSecret")))
	}
	var nilErr error
	if RedactValue("note", nilErr) != nil {
		t.Fatal("expected nil error to stay nil")
	}
	if RedactValue("str", fakeStringer{val: "token=strSecret"}) != "token="+RedactedValue {
		t.Fatalf("unexpected stringer redaction: %v", RedactValue("str", fakeStringer{val: "token=strSecret"}))
	}
	if RedactParams(nil) != nil {
		t.Fatal("expected nil params to return nil")
	}
	params := RedactParams([]any{"postgres://u:paramSecret@host/db", 42, true})
	if strings.Contains(params[0].(string), "paramSecret") || params[1] != 42 || params[2] != true {
		t.Fatalf("unexpected RedactParams output: %+v", params)
	}

	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelInfo)
	logger.LogActivity(context.Background(), "auth.login", "admin", "meta", "success",
		slog.String("password", "superSecretPass123"),
		slog.Group("nested",
			slog.String("session_id", "rawSessionToken"),
			slog.String("info", "dsn=postgres://u:nestedSecret@localhost:5432/db"),
		),
		slog.Any("params", []any{"secret=sliceSecret", 99}),
	)

	out := buf.String()
	for _, s := range []string{"superSecretPass123", "rawSessionToken", "nestedSecret", "sliceSecret"} {
		if strings.Contains(out, s) {
			t.Fatalf("secret %q leaked in structured log: %s", s, out)
		}
	}
}

func TestTraceParentParsingAndValidation(t *testing.T) {
	validTP := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	sc, ok := ParseTraceParent(validTP)
	if !ok || !sc.Valid() || !sc.Sampled || sc.TraceParent() != validTP {
		t.Fatalf("failed to parse valid sampled traceparent: %+v (ok=%v)", sc, ok)
	}

	unsampledTP := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00"
	scUnsampled, ok := ParseTraceParent(unsampledTP)
	if !ok || scUnsampled.Sampled || scUnsampled.TraceParent() != unsampledTP {
		t.Fatalf("failed to parse unsampled traceparent: %+v", scUnsampled)
	}

	for _, invalid := range []string{
		"",
		"01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		"00-zzzz2f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-short-span-01",
	} {
		if _, ok := ParseTraceParent(invalid); ok {
			t.Errorf("expected invalid traceparent %q to be rejected", invalid)
		}
	}
}

func TestEndToEndTracePropagationAndSpanExport(t *testing.T) {
	var logBuf, spanBuf bytes.Buffer
	logger := NewLogger(&logBuf, slog.LevelInfo)
	exporter := NewConsoleSpanExporter(&spanBuf)
	apiTracer := NewTracer("", exporter)
	workerTracer := NewTracer("worker", exporter)

	// Nil safety checks
	InjectTraceContext(context.Background(), nil)
	_ = ExtractTraceContext(context.Background(), nil)
	_, _ = SpanContextFrom(nil)
	if CorrelationIDFrom(nil) != "" {
		t.Fatal("expected empty correlation ID from nil ctx")
	}

	// 1. API span -> Queue carrier
	baseCtx := WithCorrelationID(context.Background(), "corr-e2e-100")
	apiCtx, apiSpan := apiTracer.StartSpan(baseCtx, "api.submit_migration",
		slog.Int64("migration_id", 42),
		slog.String("password", "shouldBeRedactedInSpan"),
	)
	apiSpan.SetAttr("token", "secretSpanToken")
	apiSC := apiSpan.Context()

	carrier := make(map[string]string)
	InjectTraceContext(apiCtx, carrier)
	apiSpan.End()
	apiSpan.End() // Idempotent second call must not export duplicate span

	// 2. Queue -> Worker -> Lock -> Batch/Query
	workerInCtx := ExtractTraceContext(context.Background(), carrier)
	workerCtx, workerSpan := workerTracer.StartSpan(workerInCtx, "worker.run_migration")
	lockCtx, lockSpan := workerTracer.StartSpan(workerCtx, "worker.acquire_db_lock")
	lockSpan.End()

	batchCtx, batchSpan := workerTracer.StartSpan(lockCtx, "db.batch_query", slog.Int("batch", 1))
	rows := int64(250)
	ev := SQLEvent{
		Actor: "leader_a", Database: "ecommerce_pg",
		Query: "UPDATE orders SET status = $1 WHERE id > $2", Params: []any{"done", 1000},
		Duration: 15 * time.Millisecond, RowsAffected: &rows,
		MigrationID: 42, JobID: "job-77", ExecutionID: 501, Version: 4, Attempt: 1, Batch: 1,
	}
	logger.LogSQLStart(batchCtx, ev)
	logger.LogSQLResult(batchCtx, ev)
	logger.LogSQLCommit(batchCtx, ev)
	batchSpan.End()
	workerSpan.End()

	// Verify 4 spans exported: api, lock, batch, worker
	spanLines := strings.Split(strings.TrimSpace(spanBuf.String()), "\n")
	if len(spanLines) != 4 {
		t.Fatalf("expected 4 exported spans, got %d: %s", len(spanLines), spanBuf.String())
	}
	if strings.Contains(spanBuf.String(), "shouldBeRedactedInSpan") || strings.Contains(spanBuf.String(), "secretSpanToken") {
		t.Fatalf("span attributes were not redacted: %s", spanBuf.String())
	}

	var apiSpanRec, workerSpanRec SpanRecord
	_ = json.Unmarshal([]byte(spanLines[0]), &apiSpanRec)
	_ = json.Unmarshal([]byte(spanLines[3]), &workerSpanRec)
	if apiSpanRec.Service != "if3110-platform" {
		t.Errorf("expected default service name if3110-platform, got %q", apiSpanRec.Service)
	}
	if workerSpanRec.TraceID != apiSC.TraceID || workerSpanRec.ParentSpanID != apiSC.SpanID {
		t.Fatalf("worker span lineage mismatch: %+v vs api %+v", workerSpanRec, apiSC)
	}

	// Fallback correlation ID from trace_id when carrier only has traceparent
	onlyTP := map[string]string{TraceParentHeader: apiSC.TraceParent()}
	extracted := ExtractTraceContext(context.Background(), onlyTP)
	if CorrelationIDFrom(extracted) != apiSC.TraceID {
		t.Fatalf("expected correlation_id fallback to trace_id %q, got %q", apiSC.TraceID, CorrelationIDFrom(extracted))
	}
}

func TestSQLLifecycleAndMigrationAttemptLogging(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelInfo)
	if logger.Slog() == nil {
		t.Fatal("expected underlying slog.Logger to be non-nil")
	}

	// Nil logger safety
	var nilLogger *Logger
	nilLogger.LogActivity(context.Background(), "noop", "sys", "db", "ok")
	nilLogger.LogSQLStart(context.Background(), SQLEvent{})

	ctx, span := NewTracer("worker", nil).StartSpan(context.Background(), "migration.attempt_run")
	sqlErr := fakeSQLError{msg: "deadlock detected password=secret123", state: "40P01"}

	// 1. Log failed migration attempt with SQL copy & extracted SQLSTATE
	logger.LogMigrationAttempt(ctx, SQLEvent{
		Database:    "ecommerce_pg",
		Query:       "ALTER TABLE orders ADD COLUMN notes TEXT",
		Duration:    8 * time.Millisecond,
		Err:         sqlErr,
		MigrationID: 12,
		ExecutionID: 33,
		Version:     2,
		JobID:       "job-12",
		Attempt:     2,
	})
	// 2. Log rollback with explicit SQLState
	logger.LogSQLRollback(ctx, SQLEvent{
		Database:    "ecommerce_pg",
		MigrationID: 12,
		ExecutionID: 33,
		Attempt:     2,
		SQLState:    "40P01",
	})
	span.RecordError(nil)
	span.RecordError(sqlErr)
	span.End()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 log lines, got %d: %s", len(lines), buf.String())
	}

	var attemptEntry, rollbackEntry map[string]any
	_ = json.Unmarshal([]byte(lines[0]), &attemptEntry)
	_ = json.Unmarshal([]byte(lines[1]), &rollbackEntry)

	if attemptEntry["event"] != EventMigrationAttempt || attemptEntry["status"] != "failed" || attemptEntry["level"] != "ERROR" {
		t.Fatalf("unexpected migration attempt log entry: %+v", attemptEntry)
	}
	if attemptEntry["sqlstate"] != "40P01" || attemptEntry["query"] != "ALTER TABLE orders ADD COLUMN notes TEXT" {
		t.Fatalf("missing sqlstate or query copy in attempt log: %+v", attemptEntry)
	}
	if attemptEntry["actor"] != "system" {
		t.Fatalf("expected default actor 'system', got %v", attemptEntry["actor"])
	}
	if strings.Contains(lines[0], "secret123") {
		t.Fatalf("secret leaked in error field: %s", lines[0])
	}
	if rollbackEntry["event"] != EventSQLRollback || rollbackEntry["status"] != "rolled_back" {
		t.Fatalf("unexpected rollback entry: %+v", rollbackEntry)
	}
}

func TestHTTPMiddlewareStatusCategories(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		withUser   bool
		wantStatus string
		wantSpanSt string
	}{
		{name: "200 implicit write", statusCode: 0, withUser: true, wantStatus: "success", wantSpanSt: "ok"},
		{name: "403 forbidden", statusCode: http.StatusForbidden, withUser: false, wantStatus: "rejected", wantSpanSt: "ok"},
		{name: "500 server error", statusCode: http.StatusInternalServerError, withUser: true, wantStatus: "error", wantSpanSt: "error"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logBuf, spanBuf bytes.Buffer
			logger := NewLogger(&logBuf, slog.LevelInfo)
			tracer := NewTracer("api", NewConsoleSpanExporter(&spanBuf))

			h := Middleware(tracer, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.statusCode == 0 {
					_, _ = w.Write([]byte("ok"))
					return
				}
				w.WriteHeader(tc.statusCode)
				w.WriteHeader(http.StatusTeapot) // Second WriteHeader should be ignored
			}))

			req := httptest.NewRequest(http.MethodGet, "/api/databases", nil)
			if tc.withUser {
				req = req.WithContext(contract.WithPrincipal(req.Context(), contract.Principal{UserID: 1, Username: "alice"}))
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			var spanRec SpanRecord
			if err := json.Unmarshal(bytes.TrimSpace(spanBuf.Bytes()), &spanRec); err != nil {
				t.Fatalf("unmarshal span: %v", err)
			}
			if spanRec.Status != tc.wantSpanSt {
				t.Errorf("expected span status %q, got %q", tc.wantSpanSt, spanRec.Status)
			}

			var logEntry map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(logBuf.Bytes()), &logEntry); err != nil {
				t.Fatalf("unmarshal log: %v", err)
			}
			if logEntry["status"] != tc.wantStatus {
				t.Errorf("expected log status %q, got %v", tc.wantStatus, logEntry["status"])
			}
			wantActor := "anonymous"
			if tc.withUser {
				wantActor = "alice"
			}
			if logEntry["actor"] != wantActor {
				t.Errorf("expected actor %q, got %v", wantActor, logEntry["actor"])
			}
		})
	}
}

func TestMetricsRecorderConcurrencyAndNoop(t *testing.T) {
	var noop NoopMetrics
	noop.SetJobStatusCount(JobStatusWaiting, 1)
	noop.IncJobStatus(JobStatusRunning, 1)
	noop.IncJobRetry()
	noop.ObserveQueueWait(time.Millisecond)
	noop.ObserveLockWait(time.Millisecond)
	noop.ObserveMigrationDuration(JobStatusSuccess, time.Millisecond)
	noop.AddProcessedBatches(1)
	noop.AddProcessedRows(10)
	noop.SetPoolConns("db", 1, 1)
	noop.ObservePoolWait("db", time.Millisecond)

	m := NewInMemoryMetrics()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.IncJobStatus(JobStatusRunning, 1)
			m.IncJobRetry()
			m.ObserveQueueWait(5 * time.Millisecond)
			m.ObserveLockWait(2 * time.Millisecond)
			m.ObserveMigrationDuration(JobStatusSuccess, 20*time.Millisecond)
			m.AddProcessedBatches(2)
			m.AddProcessedRows(100)
			m.SetPoolConns("pg_target", 4, 2)
			m.ObservePoolWait("pg_target", time.Millisecond)
		}()
	}
	wg.Wait()
	m.SetJobStatusCount(JobStatusWaiting, 5)

	if m.JobCounts[JobStatusRunning] != 10 || m.JobCounts[JobStatusWaiting] != 5 {
		t.Fatalf("unexpected job counts: %+v", m.JobCounts)
	}
	if m.Retries != 10 || m.BatchesProcessed != 20 || m.RowsProcessed != 1000 {
		t.Fatalf("unexpected counters: retries=%d batches=%d rows=%d", m.Retries, m.BatchesProcessed, m.RowsProcessed)
	}
	if m.QueueWaitCount != 10 || m.LockWaitCount != 10 || m.MigrationDurCount[JobStatusSuccess] != 10 {
		t.Fatalf("unexpected observation counts")
	}
	if m.Pools["pg_target"].Active != 4 || m.Pools["pg_target"].WaitCount != 10 {
		t.Fatalf("unexpected pool metrics: %+v", m.Pools["pg_target"])
	}
}
