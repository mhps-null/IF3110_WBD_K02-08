package observability

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// MultiSpanExporter fans out finished spans to multiple exporters, like console and OTLP Collector.
type MultiSpanExporter struct {
	exporters []SpanExporter
}

func NewMultiSpanExporter(exporters ...SpanExporter) *MultiSpanExporter {
	active := make([]SpanExporter, 0, len(exporters))
	for _, e := range exporters {
		if e != nil {
			active = append(active, e)
		}
	}
	return &MultiSpanExporter{exporters: active}
}

func (m *MultiSpanExporter) ExportSpan(rec SpanRecord) {
	if m == nil {
		return
	}
	for _, e := range m.exporters {
		e.ExportSpan(rec)
	}
}

// NewDefaultSpanExporter builds a console exporter and attaches an OTLP HTTP exporter
func NewDefaultSpanExporter(console io.Writer) SpanExporter {
	consoleExp := NewConsoleSpanExporter(console)
	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if endpoint == "" {
		return consoleExp
	}
	return NewMultiSpanExporter(consoleExp, NewOTLPSpanExporter(endpoint))
}

// OTLPSpanExporter sends spans in OTLP/HTTP JSON format to an OpenTelemetry Collector.
type OTLPSpanExporter struct {
	endpoint string
	client   *http.Client
}

func NewOTLPSpanExporter(endpoint string) *OTLPSpanExporter {
	ep := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if ep != "" && !strings.HasSuffix(ep, "/v1/traces") {
		ep += "/v1/traces"
	}
	return &OTLPSpanExporter{
		endpoint: ep,
		client:   &http.Client{Timeout: 3 * time.Second},
	}
}

func (e *OTLPSpanExporter) ExportSpan(rec SpanRecord) {
	if e == nil || e.endpoint == "" {
		return
	}
	payload := buildOTLPPayload(rec)
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func buildOTLPPayload(rec SpanRecord) map[string]any {
	attrs := make([]map[string]any, 0, len(rec.Attributes))
	for k, v := range rec.Attributes {
		attrs = append(attrs, map[string]any{
			"key":   k,
			"value": map[string]any{"stringValue": fmt.Sprint(v)},
		})
	}
	statusCode := 1 
	if rec.Status == "error" {
		statusCode = 2 
	}
	spanObj := map[string]any{
		"traceId":           rec.TraceID,
		"spanId":            rec.SpanID,
		"name":              rec.Name,
		"kind":              1,
		"startTimeUnixNano": fmt.Sprintf("%d", rec.StartTime.UnixNano()),
		"endTimeUnixNano":   fmt.Sprintf("%d", rec.EndTime.UnixNano()),
		"attributes":        attrs,
		"status": map[string]any{
			"code":    statusCode,
			"message": rec.Error,
		},
	}
	if rec.ParentSpanID != "" {
		spanObj["parentSpanId"] = rec.ParentSpanID
	}
	return map[string]any{
		"resourceSpans": []map[string]any{
			{
				"resource": map[string]any{
					"attributes": []map[string]any{
						{"key": "service.name", "value": map[string]any{"stringValue": rec.Service}},
					},
				},
				"scopeSpans": []map[string]any{
					{
						"scope": map[string]any{"name": "if3110.observability"},
						"spans": []map[string]any{spanObj},
					},
				},
			},
		},
	}
}
