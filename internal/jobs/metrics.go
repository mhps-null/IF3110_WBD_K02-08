package jobs

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/observability"
)

// PromMetrics implements observability.MetricsRecorder and renders the
// Prometheus text exposition format (no extra dependency). Every label has a
// bounded value set: job status, migration status, pool name. Job/user IDs and
// SQL are never labels; they live in logs and spans.
type PromMetrics struct {
	mu          sync.Mutex
	jobGauge    map[string]int64
	transitions map[string]int64
	retries     int64
	queueWait   *histogram
	lockWait    *histogram
	duration    map[string]*histogram
	batches     int64
	rows        int64
	pools       map[string]observability.PoolStat
	poolWait    map[string]*histogram
}

var _ observability.MetricsRecorder = (*PromMetrics)(nil)

var secondsBuckets = []float64{0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600}

func NewPromMetrics() *PromMetrics {
	m := &PromMetrics{
		jobGauge:    map[string]int64{},
		transitions: map[string]int64{},
		queueWait:   newHistogram(secondsBuckets),
		lockWait:    newHistogram(secondsBuckets),
		duration:    map[string]*histogram{},
		pools:       map[string]observability.PoolStat{},
		poolWait:    map[string]*histogram{},
	}
	for _, s := range AllStatuses {
		m.jobGauge[s] = 0
	}
	return m
}

func allowedStatus(s string) string {
	for _, v := range AllStatuses {
		if v == s {
			return s
		}
	}
	return "other"
}

func (m *PromMetrics) SetJobStatusCount(status string, n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobGauge[allowedStatus(status)] = n
}

func (m *PromMetrics) IncJobStatus(status string, delta int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.transitions[allowedStatus(status)] += delta
}

func (m *PromMetrics) IncJobRetry() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retries++
}

func (m *PromMetrics) ObserveQueueWait(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queueWait.observe(d.Seconds())
}

func (m *PromMetrics) ObserveLockWait(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lockWait.observe(d.Seconds())
}

func (m *PromMetrics) ObserveMigrationDuration(status string, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := allowedStatus(status)
	h := m.duration[s]
	if h == nil {
		h = newHistogram(secondsBuckets)
		m.duration[s] = h
	}
	h.observe(d.Seconds())
}

func (m *PromMetrics) AddProcessedBatches(n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batches += n
}

func (m *PromMetrics) AddProcessedRows(n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows += n
}

func (m *PromMetrics) SetPoolConns(db string, active, idle int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.pools[db]
	st.Active, st.Idle = active, idle
	m.pools[db] = st
}

func (m *PromMetrics) ObservePoolWait(db string, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.poolWait[db]
	if h == nil {
		h = newHistogram(secondsBuckets)
		m.poolWait[db] = h
	}
	h.observe(d.Seconds())
}

// SyncPools copies live database/sql pool statistics into the gauges.
func (m *PromMetrics) SyncPools(stats []contract.PoolStat) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range stats {
		m.pools[s.DBName] = observability.PoolStat{Active: s.InUse, Idle: s.Idle, WaitCount: s.WaitCount, WaitDuration: s.WaitDuration}
	}
}

// WriteTo renders all metrics in Prometheus text format 0.0.4.
func (m *PromMetrics) WriteTo(w io.Writer) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	gauge := func(name, help string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
	}
	counter := func(name, help string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n", name, help, name)
	}

	gauge("dbplatform_migration_jobs", "Migration jobs currently in each status (queue snapshot).")
	for _, s := range sortedKeys(m.jobGauge) {
		fmt.Fprintf(&b, "dbplatform_migration_jobs{status=%q} %d\n", s, m.jobGauge[s])
	}
	counter("dbplatform_migration_job_transitions_total", "Job status transitions observed by this worker.")
	for _, s := range sortedKeys(m.transitions) {
		fmt.Fprintf(&b, "dbplatform_migration_job_transitions_total{status=%q} %d\n", s, m.transitions[s])
	}
	counter("dbplatform_migration_job_retries_total", "Automatic retries scheduled after transient failures.")
	fmt.Fprintf(&b, "dbplatform_migration_job_retries_total %d\n", m.retries)
	counter("dbplatform_migration_batches_processed_total", "Batches committed (apply or compensation).")
	fmt.Fprintf(&b, "dbplatform_migration_batches_processed_total %d\n", m.batches)
	counter("dbplatform_migration_rows_processed_total", "Rows affected by committed migration statements and batches.")
	fmt.Fprintf(&b, "dbplatform_migration_rows_processed_total %d\n", m.rows)

	m.queueWait.write(&b, "dbplatform_migration_queue_wait_seconds", "Time from enqueue to first worker claim.", "")
	m.lockWait.write(&b, "dbplatform_migration_lock_wait_seconds", "Time spent acquiring the cross-worker database lock.", "")
	fmt.Fprintf(&b, "# HELP dbplatform_migration_duration_seconds Wall time of one job delivery by final status.\n# TYPE dbplatform_migration_duration_seconds histogram\n")
	for _, s := range sortedKeys(m.duration) {
		m.duration[s].writeSeries(&b, "dbplatform_migration_duration_seconds", fmt.Sprintf("status=%q", s))
	}

	gauge("dbplatform_db_pool_connections", "Target database pool connections by state.")
	for _, p := range sortedKeys(m.pools) {
		st := m.pools[p]
		fmt.Fprintf(&b, "dbplatform_db_pool_connections{pool=%q,state=\"active\"} %d\n", p, st.Active)
		fmt.Fprintf(&b, "dbplatform_db_pool_connections{pool=%q,state=\"idle\"} %d\n", p, st.Idle)
	}
	counter("dbplatform_db_pool_wait_total", "Connections waited for (database/sql WaitCount).")
	for _, p := range sortedKeys(m.pools) {
		fmt.Fprintf(&b, "dbplatform_db_pool_wait_total{pool=%q} %d\n", p, m.pools[p].WaitCount)
	}
	counter("dbplatform_db_pool_wait_seconds_total", "Total time waited for a pooled connection.")
	for _, p := range sortedKeys(m.pools) {
		fmt.Fprintf(&b, "dbplatform_db_pool_wait_seconds_total{pool=%q} %s\n", p, fmtFloat(m.pools[p].WaitDuration.Seconds()))
	}
	n, err := io.WriteString(w, b.String())
	return int64(n), err
}

// Handler serves /metrics.
func (m *PromMetrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = m.WriteTo(w)
	})
}

// RunGaugeRefresher periodically snapshots queue depth by status (shared across
// all workers, read from the metadata DB) and target pool stats.
func (m *PromMetrics) RunGaugeRefresher(ctx context.Context, store *Store, pools contract.PoolStatsProvider, every time.Duration) {
	if every <= 0 {
		every = 5 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if counts, err := store.StatusCounts(ctx); err == nil {
			for s, n := range counts {
				m.SetJobStatusCount(s, n)
			}
		}
		if pools != nil {
			m.SyncPools(pools.PoolStats())
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type histogram struct {
	bounds []float64
	counts []int64
	sum    float64
	count  int64
}

func newHistogram(bounds []float64) *histogram {
	return &histogram{bounds: bounds, counts: make([]int64, len(bounds))}
}

func (h *histogram) observe(v float64) {
	if math.IsNaN(v) || v < 0 {
		v = 0
	}
	for i, ub := range h.bounds {
		if v <= ub {
			h.counts[i]++
		}
	}
	h.sum += v
	h.count++
}

func (h *histogram) write(b *strings.Builder, name, help, labels string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s histogram\n", name, help, name)
	h.writeSeries(b, name, labels)
}

func (h *histogram) writeSeries(b *strings.Builder, name, labels string) {
	sep := ""
	if labels != "" {
		sep = ","
	}
	for i, ub := range h.bounds {
		fmt.Fprintf(b, "%s_bucket{%s%sle=%q} %d\n", name, labels, sep, fmtFloat(ub), h.counts[i])
	}
	fmt.Fprintf(b, "%s_bucket{%s%sle=\"+Inf\"} %d\n", name, labels, sep, h.count)
	if labels != "" {
		fmt.Fprintf(b, "%s_sum{%s} %s\n%s_count{%s} %d\n", name, labels, fmtFloat(h.sum), name, labels, h.count)
	} else {
		fmt.Fprintf(b, "%s_sum %s\n%s_count %d\n", name, fmtFloat(h.sum), name, h.count)
	}
}

func fmtFloat(v float64) string { return fmt.Sprintf("%g", v) }

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
