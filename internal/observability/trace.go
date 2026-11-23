package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

const (
	TraceParentHeader   = "traceparent"
	CorrelationIDHeader = "X-Correlation-ID"
)

type (
	spanContextKey   struct{}
	correlationIDKey struct{}
)

type SpanContext struct {
	TraceID      string `json:"trace_id"`
	SpanID       string `json:"span_id"`
	ParentSpanID string `json:"parent_span_id,omitempty"`
	Sampled      bool   `json:"sampled"`
}

func (sc SpanContext) Valid() bool {
	return isValidHex(sc.TraceID, 32) && isValidHex(sc.SpanID, 16)
}

func (sc SpanContext) TraceParent() string {
	flag := "00"
	if sc.Sampled {
		flag = "01"
	}
	return fmt.Sprintf("00-%s-%s-%s", sc.TraceID, sc.SpanID, flag)
}

// SpanRecord represents an exported span.
type SpanRecord struct {
	TraceID      string         `json:"trace_id"`
	SpanID       string         `json:"span_id"`
	ParentSpanID string         `json:"parent_span_id,omitempty"`
	Name         string         `json:"name"`
	Service      string         `json:"service"`
	StartTime    time.Time      `json:"start_time"`
	EndTime      time.Time      `json:"end_time"`
	DurationMs   int64          `json:"duration_ms"`
	Status       string         `json:"status"`
	Error        string         `json:"error,omitempty"`
	Attributes   map[string]any `json:"attributes,omitempty"`
}

// SpanExporter exports finished spans.
type SpanExporter interface {
	ExportSpan(rec SpanRecord)
}

// ConsoleSpanExporter writes JSON spans to an io.Writer.
type ConsoleSpanExporter struct {
	mu sync.Mutex
	w  io.Writer
}

func NewConsoleSpanExporter(w io.Writer) *ConsoleSpanExporter {
	return &ConsoleSpanExporter{w: w}
}

func (e *ConsoleSpanExporter) ExportSpan(rec SpanRecord) {
	if e == nil || e.w == nil {
		return
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, _ = e.w.Write(append(b, '\n'))
}

// Tracer creates and exports spans across API, queue, worker, and DB operations.
type Tracer struct {
	service  string
	exporter SpanExporter
}

func NewTracer(service string, exporter SpanExporter) *Tracer {
	if service == "" {
		service = "if3110-platform"
	}
	return &Tracer{service: service, exporter: exporter}
}

// Span tracks an individual traced operation.
type Span struct {
	mu       sync.Mutex
	tracer   *Tracer
	ctx      SpanContext
	name     string
	start    time.Time
	status   string
	errMsg   string
	attrs    map[string]any
	finished bool
}

func (t *Tracer) StartSpan(ctx context.Context, name string, attrs ...slog.Attr) (context.Context, *Span) {
	parent, hasParent := SpanContextFrom(ctx)
	traceID, parentID := parent.TraceID, parent.SpanID
	if !hasParent || !isValidHex(traceID, 32) {
		traceID, parentID = randomHex(16), ""
	}

	sc := SpanContext{TraceID: traceID, SpanID: randomHex(8), ParentSpanID: parentID, Sampled: true}
	attrMap := make(map[string]any, len(attrs)+1)
	if corrID := CorrelationIDFrom(ctx); corrID != "" {
		attrMap["correlation_id"] = corrID
	}
	for _, a := range attrs {
		attrMap[a.Key] = RedactValue(a.Key, a.Value.Any())
	}

	s := &Span{tracer: t, ctx: sc, name: name, start: time.Now().UTC(), status: "ok", attrs: attrMap}
	ctx = WithSpanContext(ctx, sc)
	if CorrelationIDFrom(ctx) == "" {
		ctx = WithCorrelationID(ctx, sc.TraceID)
	}
	return ctx, s
}

func (s *Span) Context() SpanContext { return s.ctx }

func (s *Span) SetAttr(key string, val any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attrs[key] = RedactValue(key, val)
}

func (s *Span) RecordError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = "error"
	s.errMsg = RedactString(err.Error())
}

func (s *Span) End() {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	s.finished = true
	end := time.Now().UTC()
	rec := SpanRecord{
		TraceID: s.ctx.TraceID, SpanID: s.ctx.SpanID, ParentSpanID: s.ctx.ParentSpanID,
		Name: s.name, Service: "if3110-platform", StartTime: s.start, EndTime: end,
		DurationMs: end.Sub(s.start).Milliseconds(), Status: s.status, Error: s.errMsg,
		Attributes: s.attrs,
	}
	var exp SpanExporter
	if s.tracer != nil {
		rec.Service, exp = s.tracer.service, s.tracer.exporter
	}
	s.mu.Unlock()
	if exp != nil {
		exp.ExportSpan(rec)
	}
}

func WithSpanContext(ctx context.Context, sc SpanContext) context.Context {
	return context.WithValue(ctx, spanContextKey{}, sc)
}

func SpanContextFrom(ctx context.Context) (SpanContext, bool) {
	if ctx == nil {
		return SpanContext{}, false
	}
	sc, ok := ctx.Value(spanContextKey{}).(SpanContext)
	return sc, ok
}

func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationIDKey{}, strings.TrimSpace(id))
}

func CorrelationIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(correlationIDKey{}).(string); ok && id != "" {
		return id
	}
	if sc, ok := SpanContextFrom(ctx); ok {
		return sc.TraceID
	}
	return ""
}

func InjectTraceContext(ctx context.Context, carrier map[string]string) {
	if carrier == nil {
		return
	}
	if sc, ok := SpanContextFrom(ctx); ok && sc.Valid() {
		carrier[TraceParentHeader] = sc.TraceParent()
	}
	if corrID := CorrelationIDFrom(ctx); corrID != "" {
		carrier[CorrelationIDHeader] = corrID
	}
}

// ExtractTraceContext restores SpanContext and correlation ID from a carrier map into ctx.
func ExtractTraceContext(ctx context.Context, carrier map[string]string) context.Context {
	if carrier == nil {
		return ctx
	}
	if sc, ok := ParseTraceParent(carrier[TraceParentHeader]); ok {
		ctx = WithSpanContext(ctx, sc)
	}
	if corrID := carrier[CorrelationIDHeader]; corrID != "" {
		ctx = WithCorrelationID(ctx, corrID)
	} else if sc, ok := SpanContextFrom(ctx); ok && sc.TraceID != "" {
		ctx = WithCorrelationID(ctx, sc.TraceID)
	}
	return ctx
}

// ParseTraceParent parses a W3C traceparent string ("00-<32-hex>-<16-hex>-<2-hex>").
func ParseTraceParent(header string) (SpanContext, bool) {
	p := strings.Split(strings.TrimSpace(header), "-")
	if len(p) != 4 || p[0] != "00" || !isValidHex(p[1], 32) || !isValidHex(p[2], 16) || len(p[3]) != 2 {
		return SpanContext{}, false
	}
	return SpanContext{TraceID: strings.ToLower(p[1]), SpanID: strings.ToLower(p[2]), Sampled: p[3] == "01"}, true
}

type statusCaptureWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusCaptureWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusCaptureWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Middleware traces HTTP requests and emits structured request logs.
func Middleware(tracer *Tracer, logger *Logger) func(http.Handler) http.Handler {
	if tracer == nil {
		tracer = NewTracer("api", nil)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			safeMethod := SanitizeLogString(r.Method)
			safePath := SanitizeLogString(r.URL.Path)
			safeCorr := SanitizeLogString(r.Header.Get(CorrelationIDHeader))

			ctx := ExtractTraceContext(r.Context(), map[string]string{
				TraceParentHeader:   SanitizeLogString(r.Header.Get(TraceParentHeader)),
				CorrelationIDHeader: safeCorr,
			})
			ctx, span := tracer.StartSpan(ctx, safeMethod+" "+safePath,
				slog.String("http.method", safeMethod),
				slog.String("http.path", safePath),
			)
			defer span.End()

			w.Header().Set(TraceParentHeader, span.Context().TraceParent())
			w.Header().Set(CorrelationIDHeader, SanitizeLogString(CorrelationIDFrom(ctx)))

			sw := &statusCaptureWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r.WithContext(ctx))

			code := sw.status
			if code == 0 {
				code = http.StatusOK
			}
			span.SetAttr("http.status_code", code)
			status := "success"
			if code >= 500 {
				status = "error"
				span.RecordError(fmt.Errorf("http status %d", code))
			} else if code >= 400 {
				status = "rejected"
			}

			if logger != nil {
				actor := "anonymous"
				if p, ok := contract.PrincipalFrom(ctx); ok && p.Username != "" {
					actor = p.Username
				}
				logger.LogActivity(ctx, "http.request", actor, "", status,
					slog.String("method", safeMethod),
					slog.String("path", safePath),
					slog.Int("http_status", code),
				)
			}
		})
	}
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return strings.Repeat("1", nBytes*2)
	}
	return hex.EncodeToString(b)
}

func isValidHex(s string, expectedLen int) bool {
	if len(s) != expectedLen || strings.Trim(s, "0") == "" {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
