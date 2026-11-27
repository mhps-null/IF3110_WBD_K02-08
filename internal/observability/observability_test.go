package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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
	if strings.Contains(out, "\n") && len(strings.Split(strings.TrimSpace(out), "\n")) != 1 {
		t.Fatalf("expected single JSON log line, got: %s", out)
	}
}

func TestLogInjectionSanitization(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelInfo)
	logger.LogActivity(context.Background(), "http.request", "alice\nfake_actor", "db\r\nfake", "success",
		slog.String("path", "/api/databases\n{\"level\":\"ERROR\"}"),
	)
	logger.LogSQLStart(context.Background(), SQLEvent{
		Actor:    "bob\r\ninjected",
		Database: "pg\nfake",
		Query:    "SELECT *\nFROM users\rWHERE id = 1",
		Params:   []any{"val\ninjected"},
	})
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 log lines without injected newlines, got %d: %s", len(lines), buf.String())
	}
	for _, line := range lines {
		if strings.Contains(line, `\n`) || strings.Contains(line, `\r`) {
			t.Fatalf("expected CR/LF control characters to be stripped, got: %s", line)
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

	InjectTraceContext(context.Background(), nil)
	_ = ExtractTraceContext(context.Background(), nil)
	_, _ = SpanContextFrom(nil)
	if CorrelationIDFrom(nil) != "" {
		t.Fatal("expected empty correlation ID from nil ctx")
	}

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
	apiSpan.End()
	apiSpan.SetAttr("after_end", "ignored")
	apiSpan.RecordError(errors.New("ignored_after_end"))

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

	var nilLogger *Logger
	nilLogger.LogActivity(context.Background(), "noop", "sys", "db", "ok")
	nilLogger.LogSQLStart(context.Background(), SQLEvent{})

	ctx, span := NewTracer("worker", nil).StartSpan(context.Background(), "migration.attempt_run")
	sqlErr := fakeSQLError{msg: "deadlock detected password=secret123", state: "40P01"}

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
				w.WriteHeader(http.StatusTeapot)
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

func TestMetricsRecorderPrometheusAndConcurrency(t *testing.T) {
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
	m.SetJobStatusCount("unbounded_user_id_999", 1) // Must be clamped to "other"
	m.SetPoolSnapshot("pg_replica", 2, 5, 3, 15*time.Millisecond)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from /metrics, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`dbplatform_migration_jobs{status="waiting"} 5`,
		`dbplatform_migration_jobs{status="running"} 10`,
		`dbplatform_migration_jobs{status="other"} 1`,
		`dbplatform_migration_job_retries_total 10`,
		`dbplatform_migration_batches_processed_total 20`,
		`dbplatform_migration_rows_processed_total 1000`,
		`dbplatform_db_pool_connections{pool="pg_target",state="active"} 4`,
		`dbplatform_db_pool_connections{pool="pg_target",state="idle"} 2`,
		`dbplatform_db_pool_wait_total{pool="pg_target"} 10`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected Prometheus metric %q in output:\n%s", want, body)
		}
	}
	if strings.Contains(body, "unbounded_user_id_999") {
		t.Fatalf("unbounded label leaked into Prometheus output:\n%s", body)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOTLPAndMultiSpanExporter(t *testing.T) {
	var nilMulti *MultiSpanExporter
	nilMulti.ExportSpan(SpanRecord{})
	var nilOTLP *OTLPSpanExporter
	nilOTLP.ExportSpan(SpanRecord{})
	NewOTLPSpanExporter("").ExportSpan(SpanRecord{})

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	if _, ok := NewDefaultSpanExporter(io.Discard).(*ConsoleSpanExporter); !ok {
		t.Fatal("expected ConsoleSpanExporter when OTEL_EXPORTER_OTLP_ENDPOINT is empty")
	}
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318/v1/traces")
	if _, ok := NewDefaultSpanExporter(io.Discard).(*MultiSpanExporter); !ok {
		t.Fatal("expected MultiSpanExporter when OTEL_EXPORTER_OTLP_ENDPOINT is set")
	}

	var received []byte
	var gotPath string
	otlpExp := NewOTLPSpanExporter("http://otel-collector:4318")
	otlpExp.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		received, _ = io.ReadAll(r.Body)
		rec := httptest.NewRecorder()
		rec.WriteHeader(http.StatusOK)
		return rec.Result(), nil
	})

	var consoleBuf bytes.Buffer
	multi := NewMultiSpanExporter(nil, NewConsoleSpanExporter(&consoleBuf), otlpExp)
	tracer := NewTracer("api", multi)

	parentCtx, parentSpan := tracer.StartSpan(context.Background(), "api.parent_ok")
	parentSpan.End()

	_, childSpan := tracer.StartSpan(parentCtx, "api.otlp_test", slog.String("db", "ecommerce_pg"))
	childSpan.RecordError(errors.New("timeout"))
	childSpan.End()

	if consoleBuf.Len() == 0 {
		t.Fatal("expected console exporter to receive span")
	}
	if gotPath != "/v1/traces" {
		t.Fatalf("expected OTLP path /v1/traces, got %s", gotPath)
	}
	if len(received) == 0 || !strings.Contains(string(received), `"name":"api.otlp_test"`) || !strings.Contains(string(received), `"parentSpanId"`) {
		t.Fatalf("expected OTLP server to receive child span payload, got: %s", string(received))
	}

	// Transport error must not panic
	otlpExp.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("collector unreachable")
	})
	otlpExp.ExportSpan(SpanRecord{Name: "unreachable_test"})

	// Verify OTLP log exporter & NewDefaultLogger forward correlated JSON logs to /v1/logs
	var logPayload []byte
	var logPath string
	logExp := NewOTLPLogExporter("http://otel-collector:4318", "if3110-worker")
	logExp.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		logPath = r.URL.Path
		logPayload, _ = io.ReadAll(r.Body)
		rec := httptest.NewRecorder()
		rec.WriteHeader(http.StatusOK)
		return rec.Result(), nil
	})
	logExp.ExportLogLine([]byte(`{"time":"2026-11-27T00:00:00Z","level":"INFO","event":"migration.attempt","actor":"user:1","database":"ecommerce_pg","status":"success","correlation_id":"corr-1","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7"}`))
	if logPath != "/v1/logs" {
		t.Fatalf("expected OTLP log path /v1/logs, got %s", logPath)
	}
	if !strings.Contains(string(logPayload), `"traceId":"4bf92f3577b34da6a3ce929d0e0e4736"`) || !strings.Contains(string(logPayload), `"stringValue":"if3110-worker"`) {
		t.Fatalf("expected OTLP log payload to carry traceId and service.name, got: %s", string(logPayload))
	}
}

func TestObservabilityBonusArtifactsValidation(t *testing.T) {
	dashBytes, err := os.ReadFile("../../docker/observability/grafana/dashboards/if3110-dbplatform.json")
	if err != nil {
		t.Fatalf("read Grafana dashboard JSON: %v", err)
	}
	var dash map[string]any
	if err := json.Unmarshal(dashBytes, &dash); err != nil {
		t.Fatalf("invalid Grafana dashboard JSON: %v", err)
	}
	dashStr := string(dashBytes)
	for _, metric := range []string{
		"dbplatform_migration_jobs",
		"dbplatform_migration_job_retries_total",
		"dbplatform_migration_batches_processed_total",
		"dbplatform_migration_rows_processed_total",
		"dbplatform_migration_queue_wait_seconds_sum",
		"dbplatform_migration_lock_wait_seconds_sum",
		"dbplatform_migration_duration_seconds_sum",
		"dbplatform_db_pool_connections",
		"dbplatform_db_pool_wait_seconds_total",
	} {
		if !strings.Contains(dashStr, metric) {
			t.Errorf("Grafana dashboard missing required metric %q", metric)
		}
	}

	k6Bytes, err := os.ReadFile("../../docker/k6/stress_test.js")
	if err != nil {
		t.Fatalf("read k6 stress test script: %v", err)
	}
	k6Str := string(k6Bytes)
	for _, required := range []string{
		"stages:",
		"thresholds:",
		"p(95)<500",
		"api_error_rate",
		"job_queue_wait_ms",
		"job_completion_duration_ms",
		"/api/v2/jobs/",
	} {
		if !strings.Contains(k6Str, required) {
			t.Errorf("k6 stress test script missing required element %q", required)
		}
	}
}
