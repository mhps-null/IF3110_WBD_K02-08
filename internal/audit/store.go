package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/observability"
)

// Entry represents a recorded platform audit event.
type Entry struct {
	ID            int64          `json:"id"`
	CreatedAt     time.Time      `json:"created_at"`
	Actor         string         `json:"actor"`
	Event         string         `json:"event"`
	Database      string         `json:"database"`
	Status        string         `json:"status"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	TraceID       string         `json:"trace_id,omitempty"`
	SpanID        string         `json:"span_id,omitempty"`
	Details       map[string]any `json:"details,omitempty"`
}

// Filter specifies pagination and search criteria for listing audit logs.
type Filter struct {
	Actor    string
	Event    string
	Database string
	Status   string
	Page     int
	PageSize int
}

// Page holds a paginated list of audit log entries.
type Page struct {
	Items    []Entry `json:"items"`
	Total    int64   `json:"total"`
	Page     int     `json:"page"`
	PageSize int     `json:"page_size"`
}

// Store persists and queries platform audit logs.
type Store interface {
	Record(ctx context.Context, e Entry) error
	List(ctx context.Context, f Filter) (Page, error)
}

func normalizeEntry(ctx context.Context, e Entry) Entry {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	if e.Actor == "" && ctx != nil {
		if p, ok := contract.PrincipalFrom(ctx); ok && p.Username != "" {
			e.Actor = p.Username
		}
	}
	if e.Actor == "" {
		e.Actor = "system"
	}
	if e.Status == "" {
		e.Status = "success"
	}
	if e.CorrelationID == "" {
		e.CorrelationID = observability.CorrelationIDFrom(ctx)
	}
	if sc, ok := observability.SpanContextFrom(ctx); ok {
		if e.TraceID == "" {
			e.TraceID = sc.TraceID
		}
		if e.SpanID == "" {
			e.SpanID = sc.SpanID
		}
	}
	cleaned := make(map[string]any, len(e.Details))
	for k, v := range e.Details {
		cleaned[k] = observability.RedactValue(k, v)
	}
	e.Details = cleaned
	return e
}

func normalizeFilter(f Filter) Filter {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 || f.PageSize > 100 {
		f.PageSize = 20
	}
	f.Actor = strings.TrimSpace(f.Actor)
	f.Event = strings.TrimSpace(f.Event)
	f.Database = strings.TrimSpace(f.Database)
	f.Status = strings.TrimSpace(f.Status)
	return f
}

// SQLStore persists audit logs in the metadata PostgreSQL database.
type SQLStore struct {
	db     *sql.DB
	logger *observability.Logger
}

func NewSQLStore(db *sql.DB, logger *observability.Logger) *SQLStore {
	return &SQLStore{db: db, logger: logger}
}

func (s *SQLStore) Record(ctx context.Context, e Entry) error {
	e = normalizeEntry(ctx, e)
	detailsJSON, err := json.Marshal(e.Details)
	if err != nil {
		detailsJSON = []byte("{}")
	}

	const q = `INSERT INTO audit_logs (created_at, actor, event, database_name, status, correlation_id, trace_id, span_id, details)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	if _, err := s.db.ExecContext(ctx, q,
		e.CreatedAt, e.Actor, e.Event, e.Database, e.Status,
		e.CorrelationID, e.TraceID, e.SpanID, detailsJSON,
	); err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}
	if s.logger != nil {
		s.logger.LogActivity(ctx, e.Event, e.Actor, e.Database, e.Status)
	}
	return nil
}

func (s *SQLStore) List(ctx context.Context, f Filter) (Page, error) {
	f = normalizeFilter(f)
	var conds []string
	var args []any

	addCond := func(col, val string) {
		if val != "" {
			args = append(args, val)
			conds = append(conds, fmt.Sprintf("%s = $%d", col, len(args)))
		}
	}
	addCond("actor", f.Actor)
	addCond("event", f.Event)
	addCond("database_name", f.Database)
	addCond("status", f.Status)

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	var total int64
	countQuery := "SELECT COUNT(1) FROM audit_logs " + where
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return Page{}, fmt.Errorf("count audit logs: %w", err)
	}

	offset := (f.Page - 1) * f.PageSize
	listArgs := append(append([]any(nil), args...), f.PageSize, offset)
	listQuery := fmt.Sprintf(`SELECT id, created_at, actor, event, database_name, status, correlation_id, trace_id, span_id, details
		FROM audit_logs %s ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d`,
		where, len(args)+1, len(args)+2,
	)

	rows, err := s.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return Page{}, fmt.Errorf("query audit logs: %w", err)
	}
	defer rows.Close()

	items := make([]Entry, 0)
	for rows.Next() {
		var item Entry
		var rawDetails []byte
		if err := rows.Scan(
			&item.ID, &item.CreatedAt, &item.Actor, &item.Event, &item.Database,
			&item.Status, &item.CorrelationID, &item.TraceID, &item.SpanID, &rawDetails,
		); err != nil {
			return Page{}, fmt.Errorf("scan audit log: %w", err)
		}
		if len(rawDetails) > 0 {
			_ = json.Unmarshal(rawDetails, &item.Details)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("iterate audit logs: %w", err)
	}

	return Page{Items: items, Total: total, Page: f.Page, PageSize: f.PageSize}, nil
}

// MemoryStore is an in-memory Store implementation for unit testing.
type MemoryStore struct {
	mu      sync.RWMutex
	entries []Entry
	Err     error
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{entries: make([]Entry, 0)}
}

func (m *MemoryStore) Record(ctx context.Context, e Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	e = normalizeEntry(ctx, e)
	e.ID = int64(len(m.entries) + 1)
	m.entries = append(m.entries, e)
	return nil
}

func (m *MemoryStore) List(_ context.Context, f Filter) (Page, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.Err != nil {
		return Page{}, m.Err
	}
	f = normalizeFilter(f)

	matched := make([]Entry, 0)
	for i := len(m.entries) - 1; i >= 0; i-- {
		e := m.entries[i]
		if f.Actor != "" && !strings.EqualFold(e.Actor, f.Actor) {
			continue
		}
		if f.Event != "" && !strings.EqualFold(e.Event, f.Event) {
			continue
		}
		if f.Database != "" && !strings.EqualFold(e.Database, f.Database) {
			continue
		}
		if f.Status != "" && !strings.EqualFold(e.Status, f.Status) {
			continue
		}
		matched = append(matched, e)
	}

	total := int64(len(matched))
	start := (f.Page - 1) * f.PageSize
	if start >= len(matched) {
		return Page{Items: []Entry{}, Total: total, Page: f.Page, PageSize: f.PageSize}, nil
	}
	end := start + f.PageSize
	if end > len(matched) {
		end = len(matched)
	}
	return Page{Items: matched[start:end], Total: total, Page: f.Page, PageSize: f.PageSize}, nil
}
