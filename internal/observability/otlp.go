package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
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

// NewDefaultSpanExporter builds a console exporter and attaches an async OTLP HTTP exporter
// when OTEL_EXPORTER_OTLP_ENDPOINT is set.
func NewDefaultSpanExporter(console io.Writer) SpanExporter {
	consoleExp := NewConsoleSpanExporter(console)
	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if endpoint == "" {
		return consoleExp
	}
	return NewMultiSpanExporter(consoleExp, NewAsyncOTLPSpanExporter(endpoint))
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
	postOTLPJSON(e.client, e.endpoint, body)
}

// AsyncOTLPSpanExporter buffers spans so HTTP/worker paths never block on collector network I/O.
type AsyncOTLPSpanExporter struct {
	inner *OTLPSpanExporter
	ch    chan SpanRecord
	once  sync.Once
	wg    sync.WaitGroup
}

func NewAsyncOTLPSpanExporter(endpoint string) *AsyncOTLPSpanExporter {
	a := &AsyncOTLPSpanExporter{
		inner: NewOTLPSpanExporter(endpoint),
		ch:    make(chan SpanRecord, 2048),
	}
	for i := 0; i < 2; i++ {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			for rec := range a.ch {
				a.inner.ExportSpan(rec)
			}
		}()
	}
	return a
}

func (a *AsyncOTLPSpanExporter) ExportSpan(rec SpanRecord) {
	if a == nil || a.inner == nil {
		return
	}
	select {
	case a.ch <- rec:
	default:
	}
}

func (a *AsyncOTLPSpanExporter) Close() {
	if a == nil {
		return
	}
	a.once.Do(func() {
		close(a.ch)
		a.wg.Wait()
	})
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

// OTLPLogExporter sends structured JSON log lines to an OpenTelemetry Collector (/v1/logs).
type OTLPLogExporter struct {
	endpoint string
	service  string
	client   *http.Client
}

func NewOTLPLogExporter(endpoint, service string) *OTLPLogExporter {
	ep := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	ep = strings.TrimSuffix(ep, "/v1/traces")
	if ep != "" && !strings.HasSuffix(ep, "/v1/logs") {
		ep += "/v1/logs"
	}
	if strings.TrimSpace(service) == "" {
		service = "if3110-platform"
	}
	return &OTLPLogExporter{
		endpoint: ep,
		service:  service,
		client:   &http.Client{Timeout: 3 * time.Second},
	}
}

func (e *OTLPLogExporter) ExportLogLine(rawLine []byte) {
	if e == nil || e.endpoint == "" {
		return
	}
	trimmed := bytes.TrimSpace(rawLine)
	if len(trimmed) == 0 {
		return
	}
	payload := buildOTLPLogPayload(e.service, trimmed)
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	postOTLPJSON(e.client, e.endpoint, body)
}

type otlpTeeLogWriter struct {
	console io.Writer
	exp     *OTLPLogExporter
	ch      chan []byte
	once    sync.Once
	closed  bool
	mu      sync.RWMutex
	wg      sync.WaitGroup
}

func newOTLPTeeLogWriter(console io.Writer, endpoint, service string) *otlpTeeLogWriter {
	if console == nil {
		console = os.Stdout
	}
	w := &otlpTeeLogWriter{
		console: console,
		exp:     NewOTLPLogExporter(endpoint, service),
		ch:      make(chan []byte, 2048),
	}
	for i := 0; i < 2; i++ {
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			for line := range w.ch {
				w.exp.ExportLogLine(line)
			}
		}()
	}
	return w
}

func (w *otlpTeeLogWriter) Write(p []byte) (int, error) {
	n, err := w.console.Write(p)
	w.mu.RLock()
	if !w.closed && len(p) > 0 {
		cp := append([]byte(nil), p...)
		select {
		case w.ch <- cp:
		default:
		}
	}
	w.mu.RUnlock()
	return n, err
}

func (w *otlpTeeLogWriter) Close() {
	if w == nil {
		return
	}
	w.once.Do(func() {
		w.mu.Lock()
		w.closed = true
		close(w.ch)
		w.mu.Unlock()
		w.wg.Wait()
	})
}

func buildOTLPLogPayload(service string, rawLine []byte) map[string]any {
	nowNano := fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	sevText := "INFO"
	sevNum := 9
	traceID := ""
	spanID := ""
	attrs := make([]map[string]any, 0, 8)

	var parsed map[string]any
	if err := json.Unmarshal(rawLine, &parsed); err == nil {
		if ts, ok := parsed["time"].(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				nowNano = fmt.Sprintf("%d", t.UTC().UnixNano())
			}
		}
		if lvl, ok := parsed["level"].(string); ok && lvl != "" {
			sevText = strings.ToUpper(lvl)
			switch sevText {
			case "DEBUG":
				sevNum = 5
			case "WARN", "WARNING":
				sevNum = 13
			case "ERROR":
				sevNum = 17
			default:
				sevNum = 9
			}
		}
		if tid, ok := parsed["trace_id"].(string); ok && isValidHex(tid, 32) {
			traceID = tid
		}
		if sid, ok := parsed["span_id"].(string); ok && isValidHex(sid, 16) {
			spanID = sid
		}
		for _, k := range []string{"event", "actor", "database", "status", "correlation_id", "job_id", "sqlstate"} {
			if v, ok := parsed[k]; ok && v != nil && fmt.Sprint(v) != "" {
				attrs = append(attrs, map[string]any{
					"key":   k,
					"value": map[string]any{"stringValue": fmt.Sprint(v)},
				})
			}
		}
	}

	rec := map[string]any{
		"timeUnixNano":         nowNano,
		"observedTimeUnixNano": nowNano,
		"severityNumber":       sevNum,
		"severityText":         sevText,
		"body":                 map[string]any{"stringValue": string(rawLine)},
		"attributes":           attrs,
	}
	if traceID != "" {
		rec["traceId"] = traceID
	}
	if spanID != "" {
		rec["spanId"] = spanID
	}

	return map[string]any{
		"resourceLogs": []map[string]any{
			{
				"resource": map[string]any{
					"attributes": []map[string]any{
						{"key": "service.name", "value": map[string]any{"stringValue": service}},
					},
				},
				"scopeLogs": []map[string]any{
					{
						"scope":      map[string]any{"name": "if3110.observability"},
						"logRecords": []map[string]any{rec},
					},
				},
			},
		},
	}
}

func postOTLPJSON(client *http.Client, endpoint string, body []byte) {
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// StartOTLPMetricsPusher periodically ships a Prometheus exposition snapshot over OTLP/HTTP
// (/v1/metrics) when OTEL_EXPORTER_OTLP_ENDPOINT is configured, keeping /metrics as the single
// source of truth while bridging host-to-container firewalls via the OTel Collector.
func StartOTLPMetricsPusher(ctx context.Context, service string, snapshotFn func() string, every time.Duration) {
	endpoint := strings.TrimRight(strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")), "/")
	endpoint = strings.TrimSuffix(endpoint, "/v1/traces")
	endpoint = strings.TrimSuffix(endpoint, "/v1/logs")
	if endpoint == "" || snapshotFn == nil {
		return
	}
	if !strings.HasSuffix(endpoint, "/v1/metrics") {
		endpoint += "/v1/metrics"
	}
	if strings.TrimSpace(service) == "" {
		service = "if3110-platform"
	}
	if every <= 0 {
		every = 5 * time.Second
	}
	startNano := fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	client := &http.Client{Timeout: 3 * time.Second}

	pushOnce := func() {
		text := snapshotFn()
		if strings.TrimSpace(text) == "" {
			return
		}
		payload := buildOTLPMetricsFromPromText(service, startNano, text)
		body, err := json.Marshal(payload)
		if err != nil {
			return
		}
		postOTLPJSON(client, endpoint, body)
	}

	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		pushOnce()
		for {
			select {
			case <-ctx.Done():
				pushOnce()
				return
			case <-t.C:
				pushOnce()
			}
		}
	}()
}

func buildOTLPMetricsFromPromText(service, startNano, promText string) map[string]any {
	nowNano := fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	types := map[string]string{}
	type sample struct {
		labels map[string]string
		val    float64
	}
	samples := map[string][]sample{}

	for _, raw := range strings.Split(promText, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# TYPE ") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				types[parts[2]] = parts[3]
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		name, labels, val, ok := parsePromSampleLine(line)
		if !ok {
			continue
		}
		samples[name] = append(samples[name], sample{labels: labels, val: val})
	}

	otlpMetrics := make([]map[string]any, 0, len(types))
	for mName, mType := range types {
		switch mType {
		case "gauge":
			pts := make([]map[string]any, 0, len(samples[mName]))
			for _, s := range samples[mName] {
				pts = append(pts, map[string]any{
					"attributes":   mapToOTLPAttrs(s.labels),
					"timeUnixNano": nowNano,
					"asDouble":     s.val,
				})
			}
			if len(pts) > 0 {
				otlpMetrics = append(otlpMetrics, map[string]any{
					"name":  mName,
					"gauge": map[string]any{"dataPoints": pts},
				})
			}
		case "counter":
			pts := make([]map[string]any, 0, len(samples[mName]))
			for _, s := range samples[mName] {
				pts = append(pts, map[string]any{
					"attributes":        mapToOTLPAttrs(s.labels),
					"startTimeUnixNano": startNano,
					"timeUnixNano":      nowNano,
					"asDouble":          s.val,
				})
			}
			if len(pts) > 0 {
				otlpMetrics = append(otlpMetrics, map[string]any{
					"name": mName,
					"sum": map[string]any{
						"aggregationTemporality": 2,
						"isMonotonic":            true,
						"dataPoints":             pts,
					},
				})
			}
		case "histogram":
			type histSeries struct {
				attrs   map[string]string
				bounds  []float64
				cumCnts []int64
				infCnt  int64
				sum     float64
				count   int64
			}
			groups := map[string]*histSeries{}
			keyOf := func(m map[string]string) string {
				var parts []string
				for k, v := range m {
					if k != "le" {
						parts = append(parts, k+"="+v)
					}
				}
				return strings.Join(parts, ",")
			}
			baseOf := func(m map[string]string) map[string]string {
				cp := map[string]string{}
				for k, v := range m {
					if k != "le" {
						cp[k] = v
					}
				}
				return cp
			}
			for _, s := range samples[mName+"_bucket"] {
				k := keyOf(s.labels)
				g := groups[k]
				if g == nil {
					g = &histSeries{attrs: baseOf(s.labels)}
					groups[k] = g
				}
				le := s.labels["le"]
				if le == "+Inf" {
					g.infCnt = int64(s.val)
				} else if b, err := strconv.ParseFloat(le, 64); err == nil {
					g.bounds = append(g.bounds, b)
					g.cumCnts = append(g.cumCnts, int64(s.val))
				}
			}
			for _, s := range samples[mName+"_sum"] {
				if g := groups[keyOf(s.labels)]; g != nil {
					g.sum = s.val
				}
			}
			for _, s := range samples[mName+"_count"] {
				if g := groups[keyOf(s.labels)]; g != nil {
					g.count = int64(s.val)
				}
			}
			pts := make([]map[string]any, 0, len(groups))
			for _, g := range groups {
				deltas := make([]string, 0, len(g.cumCnts)+1)
				prev := int64(0)
				for _, c := range g.cumCnts {
					d := c - prev
					if d < 0 {
						d = 0
					}
					deltas = append(deltas, strconv.FormatInt(d, 10))
					prev = c
				}
				last := g.infCnt - prev
				if last < 0 {
					last = 0
				}
				deltas = append(deltas, strconv.FormatInt(last, 10))
				pts = append(pts, map[string]any{
					"attributes":        mapToOTLPAttrs(g.attrs),
					"startTimeUnixNano": startNano,
					"timeUnixNano":      nowNano,
					"count":             strconv.FormatInt(g.count, 10),
					"sum":               g.sum,
					"explicitBounds":    g.bounds,
					"bucketCounts":      deltas,
				})
			}
			if len(pts) > 0 {
				otlpMetrics = append(otlpMetrics, map[string]any{
					"name": mName,
					"histogram": map[string]any{
						"aggregationTemporality": 2,
						"dataPoints":             pts,
					},
				})
			}
		}
	}

	return map[string]any{
		"resourceMetrics": []map[string]any{
			{
				"resource": map[string]any{
					"attributes": []map[string]any{
						{"key": "service.name", "value": map[string]any{"stringValue": service}},
					},
				},
				"scopeMetrics": []map[string]any{
					{
						"scope":   map[string]any{"name": "if3110.observability"},
						"metrics": otlpMetrics,
					},
				},
			},
		},
	}
}

func parsePromSampleLine(line string) (string, map[string]string, float64, bool) {
	labels := map[string]string{}
	var name, valStr string
	if openIdx := strings.IndexByte(line, '{'); openIdx > 0 {
		closeIdx := strings.LastIndexByte(line, '}')
		if closeIdx <= openIdx {
			return "", nil, 0, false
		}
		name = strings.TrimSpace(line[:openIdx])
		valStr = strings.TrimSpace(line[closeIdx+1:])
		rawLabels := line[openIdx+1 : closeIdx]
		for _, pair := range splitPromLabels(rawLabels) {
			eq := strings.IndexByte(pair, '=')
			if eq <= 0 {
				continue
			}
			k := strings.TrimSpace(pair[:eq])
			v := strings.Trim(strings.TrimSpace(pair[eq+1:]), `"`)
			labels[k] = v
		}
	} else {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return "", nil, 0, false
		}
		name, valStr = fields[0], fields[1]
	}
	v, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return "", nil, 0, false
	}
	return name, labels, v, true
}

func splitPromLabels(s string) []string {
	var out []string
	inQuote := false
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '"' && (i == 0 || s[i-1] != '\\') {
			inQuote = !inQuote
		} else if s[i] == ',' && !inQuote {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func mapToOTLPAttrs(m map[string]string) []map[string]any {
	out := make([]map[string]any, 0, len(m))
	for k, v := range m {
		out = append(out, map[string]any{
			"key":   k,
			"value": map[string]any{"stringValue": v},
		})
	}
	return out
}
