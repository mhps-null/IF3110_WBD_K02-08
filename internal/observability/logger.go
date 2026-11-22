package observability

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

const (
	EventSQLStart         = "sql.start"
	EventSQLResult        = "sql.result"
	EventSQLCommit        = "sql.commit"
	EventSQLRollback      = "sql.rollback"
	EventMigrationAttempt = "migration.attempt"
)

// SQLEvent holds structured attributes for SQL and migration execution logs.
type SQLEvent struct {
	Event        string
	Actor        string
	Database     string
	Status       string
	Query        string
	Params       []any
	Duration     time.Duration
	RowsAffected *int64
	Err          error
	SQLState     string
	MigrationID  int64
	JobID        string
	ExecutionID  int64
	Version      int64
	Attempt      int
	Batch        int
}

type Logger struct {
	slogger *slog.Logger
}

func NewLogger(w io.Writer, level slog.Level) *Logger {
	if w == nil {
		w = os.Stdout
	}
	return &Logger{
		slogger: slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
			Level:       level,
			ReplaceAttr: RedactAttr,
		})),
	}
}

func (l *Logger) Slog() *slog.Logger { return l.slogger }

func (l *Logger) LogActivity(ctx context.Context, event, actor, database, status string, extra ...slog.Attr) {
	if l == nil || l.slogger == nil {
		return
	}
	attrs := append(baseAttrs(ctx, event, resolveActor(ctx, actor), database, status), extra...)
	level := slog.LevelInfo
	if status == "error" || status == "failed" {
		level = slog.LevelError
	}
	l.slogger.LogAttrs(ctx, level, event, attrs...)
}

func (l *Logger) LogSQLStart(ctx context.Context, ev SQLEvent) {
	l.emitSQL(ctx, ev, EventSQLStart, "started")
}

func (l *Logger) LogSQLResult(ctx context.Context, ev SQLEvent) {
	l.emitSQL(ctx, ev, EventSQLResult, "success")
}

func (l *Logger) LogSQLCommit(ctx context.Context, ev SQLEvent) {
	l.emitSQL(ctx, ev, EventSQLCommit, "committed")
}

func (l *Logger) LogSQLRollback(ctx context.Context, ev SQLEvent) {
	l.emitSQL(ctx, ev, EventSQLRollback, "rolled_back")
}

// LogMigrationAttempt logs an attempt (up, rollback, forward, retry, or failure) with its exact SQL copy.
func (l *Logger) LogMigrationAttempt(ctx context.Context, ev SQLEvent) {
	l.emitSQL(ctx, ev, EventMigrationAttempt, "success")
}

func (l *Logger) emitSQL(ctx context.Context, ev SQLEvent, defaultEvent, defaultStatus string) {
	if l == nil || l.slogger == nil {
		return
	}
	if ev.Event == "" {
		ev.Event = defaultEvent
	}
	if ev.Status == "" {
		if ev.Err != nil {
			ev.Status = "failed"
		} else {
			ev.Status = defaultStatus
		}
	}

	attrs := baseAttrs(ctx, ev.Event, resolveActor(ctx, ev.Actor), ev.Database, ev.Status)
	if ev.Query != "" {
		attrs = append(attrs, slog.String("query", RedactString(ev.Query)))
	}
	if len(ev.Params) > 0 {
		attrs = append(attrs, slog.Any("params", RedactParams(ev.Params)))
	}
	if ev.Duration > 0 || ev.Event != EventSQLStart {
		attrs = append(attrs, slog.Int64("duration_ms", ev.Duration.Milliseconds()))
	}
	if ev.RowsAffected != nil {
		attrs = append(attrs, slog.Int64("rows", *ev.RowsAffected))
	}
	if ev.MigrationID > 0 {
		attrs = append(attrs, slog.Int64("migration_id", ev.MigrationID))
	}
	if ev.ExecutionID > 0 {
		attrs = append(attrs, slog.Int64("execution_id", ev.ExecutionID))
	}
	if ev.Version > 0 {
		attrs = append(attrs, slog.Int64("version", ev.Version))
	}
	if ev.JobID != "" {
		attrs = append(attrs, slog.String("job_id", ev.JobID))
	}
	if ev.Attempt > 0 {
		attrs = append(attrs, slog.Int("attempt", ev.Attempt))
	}
	if ev.Batch > 0 {
		attrs = append(attrs, slog.Int("batch", ev.Batch))
	}
	if state := resolveSQLState(ev.SQLState, ev.Err); state != "" {
		attrs = append(attrs, slog.String("sqlstate", state))
	}
	if ev.Err != nil {
		attrs = append(attrs, slog.String("error", RedactString(ev.Err.Error())))
	}

	level := slog.LevelInfo
	if ev.Err != nil || ev.Status == "failed" || ev.Status == "error" {
		level = slog.LevelError
	}
	l.slogger.LogAttrs(ctx, level, ev.Event, attrs...)
}

func baseAttrs(ctx context.Context, event, actor, database, status string) []slog.Attr {
	corrID := CorrelationIDFrom(ctx)
	sc, hasSpan := SpanContextFrom(ctx)
	attrs := []slog.Attr{
		slog.String("event", event),
		slog.String("actor", actor),
		slog.String("database", database),
		slog.String("status", status),
		slog.String("correlation_id", corrID),
	}
	if hasSpan {
		attrs = append(attrs, slog.String("trace_id", sc.TraceID), slog.String("span_id", sc.SpanID))
	}
	return attrs
}

func resolveActor(ctx context.Context, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if ctx != nil {
		if p, ok := contract.PrincipalFrom(ctx); ok && p.Username != "" {
			return p.Username
		}
	}
	return "system"
}

func resolveSQLState(explicit string, err error) string {
	if explicit != "" {
		return explicit
	}
	var sc interface{ SQLState() string }
	if err != nil && errors.As(err, &sc) {
		return sc.SQLState()
	}
	return ""
}
