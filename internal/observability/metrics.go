package observability

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	JobStatusWaiting = "waiting"
	JobStatusRunning = "running"
	JobStatusSuccess = "success"
	JobStatusFailed  = "failed"
)

var boundedJobStatuses = []string{
	JobStatusWaiting,
	JobStatusRunning,
	JobStatusSuccess,
	JobStatusFailed,
	"pending",
	"partial",
	"partially_reverted",
	"cancelled",
}

// MetricsRecorder defines counters and gauges for worker and connection pool instrumentation.
type MetricsRecorder interface {
	SetJobStatusCount(status string, count int64)
	IncJobStatus(status string, delta int64)
	IncJobRetry()
	ObserveQueueWait(d time.Duration)
	ObserveLockWait(d time.Duration)
	ObserveMigrationDuration(status string, d time.Duration)
	AddProcessedBatches(n int64)
	AddProcessedRows(n int64)
	SetPoolConns(dbName string, active, idle int)
	ObservePoolWait(dbName string, d time.Duration)
}

// NoopMetrics is a no-op MetricsRecorder implementation.
type NoopMetrics struct{}

func (NoopMetrics) SetJobStatusCount(string, int64)               {}
func (NoopMetrics) IncJobStatus(string, int64)                    {}
func (NoopMetrics) IncJobRetry()                                  {}
func (NoopMetrics) ObserveQueueWait(time.Duration)                {}
func (NoopMetrics) ObserveLockWait(time.Duration)                 {}
func (NoopMetrics) ObserveMigrationDuration(string, time.Duration) {}
func (NoopMetrics) AddProcessedBatches(int64)                     {}
func (NoopMetrics) AddProcessedRows(int64)                        {}
func (NoopMetrics) SetPoolConns(string, int, int)                 {}
func (NoopMetrics) ObservePoolWait(string, time.Duration)         {}

// PoolStat holds connection pool metrics for a database.
type PoolStat struct {
	Active, Idle int
	WaitCount    int64
	WaitDuration time.Duration
}

// InMemoryMetrics stores thread-safe metrics for testing and Prometheus exposition.
type InMemoryMetrics struct {
	mu                sync.RWMutex
	JobCounts         map[string]int64
	Retries           int64
	QueueWaitTotal    time.Duration
	QueueWaitCount    int64
	LockWaitTotal     time.Duration
	LockWaitCount     int64
	MigrationDurTotal map[string]time.Duration
	MigrationDurCount map[string]int64
	BatchesProcessed  int64
	RowsProcessed     int64
	Pools             map[string]PoolStat
}

func NewInMemoryMetrics() *InMemoryMetrics {
	m := &InMemoryMetrics{
		JobCounts:         make(map[string]int64),
		MigrationDurTotal: make(map[string]time.Duration),
		MigrationDurCount: make(map[string]int64),
		Pools:             make(map[string]PoolStat),
	}
	for _, s := range []string{JobStatusWaiting, JobStatusRunning, JobStatusSuccess, JobStatusFailed} {
		m.JobCounts[s] = 0
	}
	return m
}

func sanitizeStatusLabel(s string) string {
	v := strings.ToLower(strings.TrimSpace(s))
	for _, allowed := range boundedJobStatuses {
		if v == allowed {
			return v
		}
	}
	return "other"
}

func (m *InMemoryMetrics) SetJobStatusCount(s string, n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.JobCounts[sanitizeStatusLabel(s)] = n
}

func (m *InMemoryMetrics) IncJobStatus(s string, delta int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.JobCounts[sanitizeStatusLabel(s)] += delta
}

func (m *InMemoryMetrics) IncJobRetry() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Retries++
}

func (m *InMemoryMetrics) ObserveQueueWait(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.QueueWaitTotal += d
	m.QueueWaitCount++
}

func (m *InMemoryMetrics) ObserveLockWait(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.LockWaitTotal += d
	m.LockWaitCount++
}

func (m *InMemoryMetrics) ObserveMigrationDuration(s string, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lbl := sanitizeStatusLabel(s)
	m.MigrationDurTotal[lbl] += d
	m.MigrationDurCount[lbl]++
}

func (m *InMemoryMetrics) AddProcessedBatches(n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.BatchesProcessed += n
}

func (m *InMemoryMetrics) AddProcessedRows(n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RowsProcessed += n
}

func (m *InMemoryMetrics) SetPoolConns(db string, active, idle int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.Pools[db]
	st.Active, st.Idle = active, idle
	m.Pools[db] = st
}

func (m *InMemoryMetrics) ObservePoolWait(db string, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.Pools[db]
	st.WaitCount++
	st.WaitDuration += d
	m.Pools[db] = st
}

// SetPoolSnapshot updates active, idle, and cumulative wait counters from sql.DBStats.
func (m *InMemoryMetrics) SetPoolSnapshot(db string, active, idle int, waitCount int64, waitDuration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Pools[db] = PoolStat{
		Active:       active,
		Idle:         idle,
		WaitCount:    waitCount,
		WaitDuration: waitDuration,
	}
}

// WritePrometheus writes all recorded metrics in Prometheus text exposition format.
func (m *InMemoryMetrics) WritePrometheus(w io.Writer) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var b strings.Builder
	b.WriteString("# HELP dbplatform_migration_jobs Migration jobs currently in each status.\n")
	b.WriteString("# TYPE dbplatform_migration_jobs gauge\n")
	for _, s := range sortedMapKeys(m.JobCounts) {
		fmt.Fprintf(&b, "dbplatform_migration_jobs{status=%q} %d\n", s, m.JobCounts[s])
	}

	b.WriteString("# HELP dbplatform_migration_job_retries_total Total migration job retries.\n")
	b.WriteString("# TYPE dbplatform_migration_job_retries_total counter\n")
	fmt.Fprintf(&b, "dbplatform_migration_job_retries_total %d\n", m.Retries)

	b.WriteString("# HELP dbplatform_migration_batches_processed_total Total migration batches processed.\n")
	b.WriteString("# TYPE dbplatform_migration_batches_processed_total counter\n")
	fmt.Fprintf(&b, "dbplatform_migration_batches_processed_total %d\n", m.BatchesProcessed)

	b.WriteString("# HELP dbplatform_migration_rows_processed_total Total migration rows processed.\n")
	b.WriteString("# TYPE dbplatform_migration_rows_processed_total counter\n")
	fmt.Fprintf(&b, "dbplatform_migration_rows_processed_total %d\n", m.RowsProcessed)

	b.WriteString("# HELP dbplatform_migration_queue_wait_seconds_sum Total time jobs spent waiting in queue.\n")
	b.WriteString("# TYPE dbplatform_migration_queue_wait_seconds_sum counter\n")
	fmt.Fprintf(&b, "dbplatform_migration_queue_wait_seconds_sum %g\n", m.QueueWaitTotal.Seconds())
	b.WriteString("# HELP dbplatform_migration_queue_wait_seconds_count Total queue wait observations.\n")
	b.WriteString("# TYPE dbplatform_migration_queue_wait_seconds_count counter\n")
	fmt.Fprintf(&b, "dbplatform_migration_queue_wait_seconds_count %d\n", m.QueueWaitCount)

	b.WriteString("# HELP dbplatform_migration_lock_wait_seconds_sum Total time jobs spent waiting for DB lock.\n")
	b.WriteString("# TYPE dbplatform_migration_lock_wait_seconds_sum counter\n")
	fmt.Fprintf(&b, "dbplatform_migration_lock_wait_seconds_sum %g\n", m.LockWaitTotal.Seconds())
	b.WriteString("# HELP dbplatform_migration_lock_wait_seconds_count Total lock wait observations.\n")
	b.WriteString("# TYPE dbplatform_migration_lock_wait_seconds_count counter\n")
	fmt.Fprintf(&b, "dbplatform_migration_lock_wait_seconds_count %d\n", m.LockWaitCount)

	b.WriteString("# HELP dbplatform_migration_duration_seconds_sum Total migration execution time by status.\n")
	b.WriteString("# TYPE dbplatform_migration_duration_seconds_sum counter\n")
	for _, s := range sortedMapKeys(m.MigrationDurTotal) {
		fmt.Fprintf(&b, "dbplatform_migration_duration_seconds_sum{status=%q} %g\n", s, m.MigrationDurTotal[s].Seconds())
	}
	b.WriteString("# HELP dbplatform_migration_duration_seconds_count Total completed migration runs by status.\n")
	b.WriteString("# TYPE dbplatform_migration_duration_seconds_count counter\n")
	for _, s := range sortedMapKeys(m.MigrationDurCount) {
		fmt.Fprintf(&b, "dbplatform_migration_duration_seconds_count{status=%q} %d\n", s, m.MigrationDurCount[s])
	}

	b.WriteString("# HELP dbplatform_db_pool_connections Database connection pool connections by state.\n")
	b.WriteString("# TYPE dbplatform_db_pool_connections gauge\n")
	for _, pool := range sortedMapKeys(m.Pools) {
		st := m.Pools[pool]
		fmt.Fprintf(&b, "dbplatform_db_pool_connections{pool=%q,state=\"active\"} %d\n", pool, st.Active)
		fmt.Fprintf(&b, "dbplatform_db_pool_connections{pool=%q,state=\"idle\"} %d\n", pool, st.Idle)
	}

	b.WriteString("# HELP dbplatform_db_pool_wait_total Total connections waited for in pool.\n")
	b.WriteString("# TYPE dbplatform_db_pool_wait_total counter\n")
	for _, pool := range sortedMapKeys(m.Pools) {
		fmt.Fprintf(&b, "dbplatform_db_pool_wait_total{pool=%q} %d\n", pool, m.Pools[pool].WaitCount)
	}

	b.WriteString("# HELP dbplatform_db_pool_wait_seconds_total Total seconds waited for a pooled connection.\n")
	b.WriteString("# TYPE dbplatform_db_pool_wait_seconds_total counter\n")
	for _, pool := range sortedMapKeys(m.Pools) {
		fmt.Fprintf(&b, "dbplatform_db_pool_wait_seconds_total{pool=%q} %g\n", pool, m.Pools[pool].WaitDuration.Seconds())
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// Handler returns an http.Handler serving Prometheus metrics at GET /metrics.
func (m *InMemoryMetrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_ = m.WritePrometheus(w)
	})
}

func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
