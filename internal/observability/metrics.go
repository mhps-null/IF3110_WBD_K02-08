package observability

import (
	"sync"
	"time"
)

const (
	JobStatusWaiting = "waiting"
	JobStatusRunning = "running"
	JobStatusSuccess = "success"
	JobStatusFailed  = "failed"
)

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
	return &InMemoryMetrics{
		JobCounts:         make(map[string]int64),
		MigrationDurTotal: make(map[string]time.Duration),
		MigrationDurCount: make(map[string]int64),
		Pools:             make(map[string]PoolStat),
	}
}

func (m *InMemoryMetrics) SetJobStatusCount(s string, n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.JobCounts[s] = n
}

func (m *InMemoryMetrics) IncJobStatus(s string, delta int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.JobCounts[s] += delta
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
	m.MigrationDurTotal[s] += d
	m.MigrationDurCount[s]++
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
