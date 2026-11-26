// Command worker is the asynchronous migration worker (separate Go service).
// Run any number of instances against the same metadata database: jobs are
// claimed with leases and each target database is protected by a cross-worker
// PostgreSQL advisory lock.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/connections"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/jobs"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/lifecycle"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/observability"
)

func main() {
	obsLogger := observability.NewLogger(os.Stdout, slog.LevelInfo)
	logger := obsLogger.Slog()
	tracer := observability.NewTracer("worker", observability.NewConsoleSpanExporter(os.Stderr))
	metrics := jobs.NewPromMetrics()

	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		logger.Error("DATABASE_URL is required")
		os.Exit(1)
	}
	meta, err := sql.Open("pgx", dsn)
	if err != nil {
		logger.Error("cannot initialize metadata database; check DATABASE_URL")
		os.Exit(1)
	}
	defer meta.Close()
	concurrency := envInt("WORKER_CONCURRENCY", 4)
	// Each in-flight job pins one metadata connection for its lock session.
	meta.SetMaxOpenConns(concurrency*3 + 4)
	meta.SetMaxIdleConns(concurrency + 2)
	meta.SetConnMaxLifetime(30 * time.Minute)
	pingCtx, cancelPing := context.WithTimeout(context.Background(), 5*time.Second)
	err = meta.PingContext(pingCtx)
	cancelPing()
	if err != nil {
		logger.Error("cannot connect to metadata database; check availability and DATABASE_URL")
		os.Exit(1)
	}

	key, err := connections.LoadEncryptionKey()
	if err != nil {
		logger.Error("invalid database encryption configuration; check DB_ENCRYPTION_KEY")
		os.Exit(1)
	}
	repo, err := connections.NewSQLRepository(meta, key)
	if err != nil {
		logger.Error("cannot initialize database connection repository")
		os.Exit(1)
	}
	pools := connections.NewPoolManager(repo, "")
	pools.SetMetrics(metrics)
	defer func() { _ = pools.CloseAll() }()

	workerID := strings.TrimSpace(os.Getenv("WORKER_ID"))
	if workerID == "" {
		host, _ := os.Hostname()
		workerID = host + "-" + randomSuffix()
	}
	cfg := jobs.Config{
		WorkerID:     observability.SanitizeLogString(workerID),
		Concurrency:  concurrency,
		Lease:        envDuration("WORKER_LEASE", 30*time.Second),
		PollInterval: envDuration("WORKER_POLL_INTERVAL", 500*time.Millisecond),
		RetryBase:    envDuration("WORKER_RETRY_BASE", 2*time.Second),
		RetryMax:     envDuration("WORKER_RETRY_MAX", time.Minute),
		LockTimeout:  envDuration("WORKER_LOCK_TIMEOUT", 30*time.Second),
	}
	worker := jobs.NewWorker(cfg, meta, lifecycle.NewStore(meta), pools, obsLogger, tracer, metrics)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go metrics.RunGaugeRefresher(ctx, jobs.NewStore(meta), pools, 5*time.Second)

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		hctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := meta.PingContext(hctx); err != nil {
			http.Error(w, "metadata database unavailable\n", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	addr := envOr("WORKER_METRICS_ADDR", ":9091")
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		logger.Info("worker metrics endpoint starting", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("worker metrics endpoint stopped")
		}
	}()

	if err := worker.Run(ctx); err != nil {
		logger.Error("worker stopped with error")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func randomSuffix() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil && v > 0 && v <= 64 {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v, err := time.ParseDuration(strings.TrimSpace(os.Getenv(key))); err == nil && v > 0 {
		return v
	}
	return fallback
}
