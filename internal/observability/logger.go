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
	cleanEvent := SanitizeLogString(event)
	cleanActor := SanitizeLogString(resolveActor(ctx, actor))
	cleanDB := SanitizeLogString(database)
	cleanStatus := SanitizeLogString(status)

	sanitizedExtra := make([]slog.Attr, len(extra))
	for i, a := range extra {
		sanitizedExtra[i] = sanitizeLogAttr(a)
	}
	attrs := append(baseAttrs(ctx, cleanEvent, cleanActor, cleanDB, cleanStatus), sanitizedExtra...)
	level := slog.LevelInfo
	if cleanStatus == "error" || cleanStatus == "failed" {
		level = slog.LevelError
	}
	l.slogger.LogAttrs(ctx, level, cleanEvent, attrs...)
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

	cleanEvent := SanitizeLogString(ev.Event)
	cleanActor := SanitizeLogString(resolveActor(ctx, ev.Actor))
	cleanDB := SanitizeLogString(ev.Database)
	cleanStatus := SanitizeLogString(ev.Status)

	attrs := baseAttrs(ctx, cleanEvent, cleanActor, cleanDB, cleanStatus)
	if ev.Query != "" {
		attrs = append(attrs, slog.String("query", RedactString(ev.Query)))
	}
	if len(ev.Params) > 0 {
		attrs = append(attrs, slog.Any("params", RedactParams(ev.Params)))
	}
	if ev.Duration > 0 || cleanEvent != EventSQLStart {
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
		attrs = append(attrs, slog.String("job_id", SanitizeLogString(ev.JobID)))
	}
	if ev.Attempt > 0 {
		attrs = append(attrs, slog.Int("attempt", ev.Attempt))
	}
	if ev.Batch > 0 {
		attrs = append(attrs, slog.Int("batch", ev.Batch))
	}
	if state := resolveSQLState(ev.SQLState, ev.Err); state != "" {
		attrs = append(attrs, slog.String("sqlstate", SanitizeLogString(state)))
	}
	if ev.Err != nil {
		attrs = append(attrs, slog.String("error", RedactString(ev.Err.Error())))
	}

	level := slog.LevelInfo
	if ev.Err != nil || cleanStatus == "failed" || cleanStatus == "error" {
		level = slog.LevelError
	}
	l.slogger.LogAttrs(ctx, level, cleanEvent, attrs...)
}

func baseAttrs(ctx context.Context, event, actor, database, status string) []slog.Attr {
	corrID := SanitizeLogString(CorrelationIDFrom(ctx))
	sc, hasSpan := SpanContextFrom(ctx)
	attrs := []slog.Attr{
		slog.String("event", SanitizeLogString(event)),
		slog.String("actor", SanitizeLogString(actor)),
		slog.String("database", SanitizeLogString(database)),
		slog.String("status", SanitizeLogString(status)),
		slog.String("correlation_id", corrID),
	}
	if hasSpan {
		attrs = append(attrs, slog.String("trace_id", SanitizeLogString(sc.TraceID)), slog.String("span_id", SanitizeLogString(sc.SpanID)))
	}
	return attrs
}

func sanitizeLogAttr(a slog.Attr) slog.Attr {
	if IsSensitiveKey(a.Key) {
		return slog.String(a.Key, RedactedValue)
	}
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, RedactString(a.Value.String()))
	case slog.KindGroup:
		group := a.Value.Group()
		cleaned := make([]slog.Attr, len(group))
		for i, ga := range group {
			cleaned[i] = sanitizeLogAttr(ga)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(cleaned...)}
	case slog.KindAny:
		return slog.Any(a.Key, RedactValue(a.Key, a.Value.Any()))
	default:
		return a
	}
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
