package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/observability"
)

type accessibleWithEngineLister interface {
	ListAccessibleWithEngine(ctx context.Context, userID int64) ([]contract.AccessibleDB, error)
}

type apiDatabaseItem struct {
	DBID       int64                `json:"db_id"`
	Name       string               `json:"name"`
	Engine     contract.Engine      `json:"engine,omitempty"`
	Level      contract.AccessLevel `json:"level"`
	LevelLabel string               `json:"level_label"`
}

// RegisterAPIRoutes mounts JSON endpoints for database selection and read-only exploration.
func (h *Handler) RegisterAPIRoutes(mux *http.ServeMux, authenticate func(http.Handler) http.Handler) {
	if authenticate == nil {
		authenticate = func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeViewerJSON(w, http.StatusForbidden, map[string]string{"error": "authentication middleware not configured"})
			})
		}
	}
	viewDB := h.guards.ViewDB
	if viewDB == nil {
		viewDB = func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeViewerJSON(w, http.StatusForbidden, map[string]string{"error": "security guard not configured"})
			})
		}
	}

	mux.Handle("GET /api/databases", authenticate(http.HandlerFunc(h.APIListDatabases)))
	mux.Handle("GET /api/databases/{id}/schema", authenticate(viewDB(http.HandlerFunc(h.APIDatabaseSchema))))
	mux.Handle("GET /api/databases/{id}/tables/{table}", authenticate(viewDB(http.HandlerFunc(h.APITableDetail))))
	mux.Handle("GET /api/databases/{id}/tables/{table}/data", authenticate(viewDB(http.HandlerFunc(h.APITableData))))
}

func (h *Handler) APIListDatabases(w http.ResponseWriter, r *http.Request) {
	principal, ok := contract.PrincipalFrom(r.Context())
	if !ok || principal.UserID == 0 {
		writeViewerJSON(w, http.StatusUnauthorized, map[string]string{"error": "Autentikasi diperlukan."})
		return
	}

	var (
		accessible []contract.AccessibleDB
		err        error
	)
	if enriched, ok := h.accessChecker.(accessibleWithEngineLister); ok {
		accessible, err = enriched.ListAccessibleWithEngine(r.Context(), principal.UserID)
	} else {
		accessible, err = h.accessChecker.ListAccessible(r.Context(), principal.UserID)
	}
	if err != nil {
		writeViewerJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memuat daftar database."})
		return
	}

	items := make([]apiDatabaseItem, 0, len(accessible))
	for _, db := range accessible {
		items = append(items, apiDatabaseItem{
			DBID:       db.DBID,
			Name:       db.Name,
			Engine:     db.Engine,
			Level:      db.Level,
			LevelLabel: db.Level.Label(),
		})
	}
	writeViewerJSON(w, http.StatusOK, map[string]any{"databases": items})
}

func (h *Handler) APIDatabaseSchema(w http.ResponseWriter, r *http.Request) {
	dbID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || dbID <= 0 {
		writeViewerJSON(w, http.StatusBadRequest, map[string]string{"error": "ID database tidak valid."})
		return
	}

	ctx, endSpan := h.startViewerSpan(r.Context(), "viewer.schema_introspect", slog.Int64("db_id", dbID))
	defer endSpan(nil)

	dbLabel := fmt.Sprintf("db#%d", dbID)
	start := time.Now()
	h.logSQLStart(ctx, dbLabel, "SELECT schema, tables, columns, constraints, indexes FROM pg_catalog /* read-only viewer */", nil)

	db, engine, err := h.targetDB.Open(ctx, dbID)
	if err != nil {
		h.logSQLResult(ctx, dbLabel, "SELECT schema introspection", nil, time.Since(start), nil, err)
		writeViewerJSON(w, http.StatusBadGateway, map[string]string{"error": "Tidak dapat terhubung ke database target."})
		return
	}
	intro, err := h.introspectorFor(engine)
	if err != nil {
		h.logSQLResult(ctx, dbLabel, "SELECT schema introspection", nil, time.Since(start), nil, err)
		writeViewerJSON(w, http.StatusInternalServerError, map[string]string{"error": "Engine database tidak didukung."})
		return
	}

	snapshot, err := intro.Snapshot(ctx, db)
	if err != nil {
		h.logSQLResult(ctx, dbLabel, "SELECT schema introspection", nil, time.Since(start), nil, err)
		if errors.Is(err, contract.ErrAccessDenied) {
			writeViewerJSON(w, http.StatusForbidden, map[string]string{"error": "Akses ke database ditolak."})
			return
		}
		writeViewerJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal membaca skema database."})
		return
	}

	tableCount := int64(len(snapshot.Tables))
	h.logSQLResult(ctx, dbLabel, "SELECT schema introspection", nil, time.Since(start), &tableCount, nil)

	principal, _ := contract.PrincipalFrom(ctx)
	level, _ := h.accessChecker.EffectiveLevel(ctx, principal.UserID, dbID)

	writeViewerJSON(w, http.StatusOK, map[string]any{
		"db_id":       dbID,
		"engine":      engine,
		"level":       level,
		"level_label": level.Label(),
		"snapshot":    snapshot,
	})
}

func (h *Handler) APITableDetail(w http.ResponseWriter, r *http.Request) {
	dbID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || dbID <= 0 {
		writeViewerJSON(w, http.StatusBadRequest, map[string]string{"error": "ID database tidak valid."})
		return
	}
	tableName := strings.TrimSpace(r.PathValue("table"))
	if tableName == "" {
		writeViewerJSON(w, http.StatusBadRequest, map[string]string{"error": "Nama tabel tidak boleh kosong."})
		return
	}

	safeTable := observability.SanitizeLogString(tableName)
	ctx, endSpan := h.startViewerSpan(r.Context(), "viewer.table_detail", slog.Int64("db_id", dbID), slog.String("table", safeTable))
	defer endSpan(nil)

	dbLabel := fmt.Sprintf("db#%d", dbID)
	start := time.Now()
	h.logSQLStart(ctx, dbLabel, "SELECT table metadata FROM pg_catalog /* read-only viewer */", []any{safeTable})

	db, engine, err := h.targetDB.Open(ctx, dbID)
	if err != nil {
		h.logSQLResult(ctx, dbLabel, "SELECT table metadata", []any{safeTable}, time.Since(start), nil, err)
		writeViewerJSON(w, http.StatusBadGateway, map[string]string{"error": "Tidak dapat terhubung ke database target."})
		return
	}
	intro, err := h.introspectorFor(engine)
	if err != nil {
		writeViewerJSON(w, http.StatusInternalServerError, map[string]string{"error": "Engine database tidak didukung."})
		return
	}

	schema := defaultSchemaForEngine(r.URL.Query().Get("schema"), engine)
	safeSchema := observability.SanitizeLogString(schema)
	snapshot, err := intro.Snapshot(ctx, db)
	if err != nil {
		h.logSQLResult(ctx, dbLabel, "SELECT table metadata", []any{safeSchema, safeTable}, time.Since(start), nil, err)
		if errors.Is(err, contract.ErrAccessDenied) {
			writeViewerJSON(w, http.StatusForbidden, map[string]string{"error": "Akses ke database ditolak."})
			return
		}
		writeViewerJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal membaca detail tabel."})
		return
	}

	var found *contract.Table
	for i := range snapshot.Tables {
		t := &snapshot.Tables[i]
		if strings.EqualFold(t.Name, tableName) && (schema == "" || strings.EqualFold(t.Schema, schema)) {
			found = t
			break
		}
	}
	if found == nil {
		writeViewerJSON(w, http.StatusNotFound, map[string]string{"error": "Tabel tidak ditemukan."})
		return
	}

	cols := int64(len(found.Columns))
	h.logSQLResult(ctx, dbLabel, "SELECT table metadata", []any{found.Schema, found.Name}, time.Since(start), &cols, nil)
	writeViewerJSON(w, http.StatusOK, map[string]any{
		"db_id":  dbID,
		"engine": engine,
		"table":  found,
	})
}

func (h *Handler) APITableData(w http.ResponseWriter, r *http.Request) {
	dbID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || dbID <= 0 {
		writeViewerJSON(w, http.StatusBadRequest, map[string]string{"error": "ID database tidak valid."})
		return
	}
	tableName := strings.TrimSpace(r.PathValue("table"))
	if tableName == "" {
		writeViewerJSON(w, http.StatusBadRequest, map[string]string{"error": "Nama tabel tidak boleh kosong."})
		return
	}

	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
		page = p
	}
	pageSize := 20
	if ps, err := strconv.Atoi(r.URL.Query().Get("page_size")); err == nil && ps > 0 && ps <= 100 {
		pageSize = ps
	}
	orderBy := strings.TrimSpace(r.URL.Query().Get("order_by"))
	desc := strings.EqualFold(r.URL.Query().Get("desc"), "true") || strings.EqualFold(r.URL.Query().Get("order_dir"), "desc")

	safeTable := observability.SanitizeLogString(tableName)
	safeOrderBy := observability.SanitizeLogString(orderBy)
	ctx, endSpan := h.startViewerSpan(r.Context(), "viewer.table_rows", slog.Int64("db_id", dbID), slog.String("table", safeTable))
	defer endSpan(nil)

	dbLabel := fmt.Sprintf("db#%d", dbID)
	start := time.Now()

	db, engine, err := h.targetDB.Open(ctx, dbID)
	if err != nil {
		h.logSQLResult(ctx, dbLabel, "SELECT table rows (read-only)", []any{safeTable, page, pageSize}, time.Since(start), nil, err)
		writeViewerJSON(w, http.StatusBadGateway, map[string]string{"error": "Tidak dapat terhubung ke database target."})
		return
	}
	intro, err := h.introspectorFor(engine)
	if err != nil {
		writeViewerJSON(w, http.StatusInternalServerError, map[string]string{"error": "Engine database tidak didukung."})
		return
	}

	schema := defaultSchemaForEngine(r.URL.Query().Get("schema"), engine)
	safeSchema := observability.SanitizeLogString(schema)
	queryDesc := fmt.Sprintf("SELECT * FROM %s.%s /* read-only paginated viewer */", safeSchema, safeTable)
	params := []any{page, pageSize, safeOrderBy, desc}
	h.logSQLStart(ctx, dbLabel, queryDesc, params)

	rowPage, err := intro.Rows(ctx, db, schema, tableName, contract.PageRequest{
		Page:     page,
		PageSize: pageSize,
		OrderBy:  orderBy,
		Desc:     desc,
	})
	if err != nil {
		h.logSQLResult(ctx, dbLabel, queryDesc, params, time.Since(start), nil, err)
		if errors.Is(err, contract.ErrAccessDenied) {
			writeViewerJSON(w, http.StatusForbidden, map[string]string{"error": "Akses lintas database ditolak."})
			return
		}
		if errors.Is(err, contract.ErrNotFound) {
			writeViewerJSON(w, http.StatusNotFound, map[string]string{"error": "Tabel tidak ditemukan."})
			return
		}
		writeViewerJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memuat data tabel."})
		return
	}

	rowCount := int64(len(rowPage.Rows))
	h.logSQLResult(ctx, dbLabel, queryDesc, params, time.Since(start), &rowCount, nil)

	totalPages := int((rowPage.Total + int64(pageSize) - 1) / int64(pageSize))
	if totalPages < 1 {
		totalPages = 1
	}

	writeViewerJSON(w, http.StatusOK, map[string]any{
		"db_id":       dbID,
		"engine":      engine,
		"schema":      schema,
		"table":       tableName,
		"columns":     rowPage.Columns,
		"rows":        rowPage.Rows,
		"total":       rowPage.Total,
		"page":        page,
		"page_size":   pageSize,
		"total_pages": totalPages,
		"order_by":    orderBy,
		"desc":        desc,
	})
}

func (h *Handler) startViewerSpan(ctx context.Context, name string, attrs ...slog.Attr) (context.Context, func(error)) {
	if h.tracer == nil {
		return ctx, func(error) {}
	}
	nextCtx, span := h.tracer.StartSpan(ctx, name, attrs...)
	return nextCtx, func(err error) {
		if err != nil {
			span.RecordError(err)
		}
		span.End()
	}
}

func (h *Handler) logSQLStart(ctx context.Context, dbName, query string, params []any) {
	if h.obsLogger == nil {
		return
	}
	h.obsLogger.LogSQLStart(ctx, observability.SQLEvent{
		Database: dbName,
		Query:    query,
		Params:   params,
	})
}

func (h *Handler) logSQLResult(ctx context.Context, dbName, query string, params []any, dur time.Duration, rows *int64, err error) {
	if h.obsLogger == nil {
		return
	}
	h.obsLogger.LogSQLResult(ctx, observability.SQLEvent{
		Database:     dbName,
		Query:        query,
		Params:       params,
		Duration:     dur,
		RowsAffected: rows,
		Err:          err,
	})
}

func writeViewerJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
