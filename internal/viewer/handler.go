package viewer

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

// Handler manages HTTP endpoints for schema exploration and paginated table data.
type Handler struct {
	targetDB      contract.TargetDB
	introspectors map[contract.Engine]contract.Introspector
	accessChecker contract.AccessChecker
	renderer      Renderer
	guards        contract.Guards
}

func defaultSchemaForEngine(rawSchema string, engine contract.Engine) string {
	if engine == contract.EngineMySQL || engine == contract.EngineMariaDB {
		if rawSchema == "public" {
			return ""
		}
		return rawSchema
	}
	if rawSchema == "" {
		return "public"
	}
	return rawSchema
}

func NewHandler(targetDB contract.TargetDB, introspector contract.Introspector, accessChecker contract.AccessChecker, renderer Renderer, guards contract.Guards) *Handler {
	introspectors := make(map[contract.Engine]contract.Introspector)
	if introspector != nil {
		introspectors[contract.EnginePostgres] = introspector
	}
	return &Handler{
		targetDB:      targetDB,
		introspectors: introspectors,
		accessChecker: accessChecker,
		renderer:      renderer,
		guards:        guards,
	}
}

func NewMultiEngineHandler(targetDB contract.TargetDB, introspectors map[contract.Engine]contract.Introspector, accessChecker contract.AccessChecker, renderer Renderer, guards contract.Guards) *Handler {
	copyMap := make(map[contract.Engine]contract.Introspector, len(introspectors))
	for k, v := range introspectors {
		copyMap[k] = v
	}
	return &Handler{
		targetDB:      targetDB,
		introspectors: copyMap,
		accessChecker: accessChecker,
		renderer:      renderer,
		guards:        guards,
	}
}

func (h *Handler) introspectorFor(engine contract.Engine) (contract.Introspector, error) {
	if intro, ok := h.introspectors[engine]; ok && intro != nil {
		return intro, nil
	}
	return nil, fmt.Errorf("no introspector configured for engine: %s", engine)
}

// RegisterRoutes binds database viewer endpoints with authorization guards.
// It preserves the current unit-test interface; production wiring should use
// RegisterRoutesWithAuth so /databases receives an authenticated principal.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	h.RegisterRoutesWithAuth(mux, func(next http.Handler) http.Handler { return next })
}

// RegisterRoutesWithAuth authenticates every viewer route and checks database access
// before schema/data handlers execute.
func (h *Handler) RegisterRoutesWithAuth(mux *http.ServeMux, authenticate func(http.Handler) http.Handler) {
	if authenticate == nil {
		authenticate = func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "Access denied: authentication middleware not configured", http.StatusForbidden)
			})
		}
	}
	viewDB := h.guards.ViewDB
	if viewDB == nil {
		viewDB = func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "Access denied: security guard not configured", http.StatusForbidden)
			})
		}
	}

	mux.Handle("GET /databases", authenticate(http.HandlerFunc(h.ListDatabases)))
	mux.Handle("GET /databases/{id}/schema", authenticate(viewDB(http.HandlerFunc(h.DatabaseSchema))))
	mux.Handle("GET /databases/{id}/tables/{table}", authenticate(viewDB(http.HandlerFunc(h.TableDetail))))
	mux.Handle("GET /databases/{id}/tables/{table}/data", authenticate(viewDB(http.HandlerFunc(h.TableData))))
}

func (h *Handler) ListDatabases(w http.ResponseWriter, r *http.Request) {
	principal, ok := contract.PrincipalFrom(r.Context())
	if !ok || principal.UserID == 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	accessible, err := h.accessChecker.ListAccessible(r.Context(), principal.UserID)
	if err != nil {
		http.Error(w, "failed to query accessible databases", http.StatusInternalServerError)
		return
	}

	csrf, _ := contract.CSRFTokenFrom(r.Context())

	data := struct {
		Databases []contract.AccessibleDB
	}{
		Databases: accessible,
	}

	if err := h.renderer.Render(w, "viewer/databases.html", contract.PageData{
		Principal: principal,
		CSRFToken: csrf,
		Data:      data,
	}); err != nil {
		http.Error(w, "failed to render page", http.StatusInternalServerError)
		return
	}
}

func (h *Handler) DatabaseSchema(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	dbID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid database id", http.StatusBadRequest)
		return
	}

	db, engine, err := h.targetDB.Open(r.Context(), dbID)
	if err != nil {
		http.Error(w, "unable to connect to target database", http.StatusBadGateway)
		return
	}

	intro, err := h.introspectorFor(engine)
	if err != nil {
		http.Error(w, "unsupported database engine", http.StatusInternalServerError)
		return
	}

	snapshot, err := intro.Snapshot(r.Context(), db)
	if err != nil {
		if errors.Is(err, contract.ErrAccessDenied) {
			http.Error(w, "Access denied: database access prohibited", http.StatusForbidden)
			return
		}
		http.Error(w, "failed to introspect database schema", http.StatusInternalServerError)
		return
	}

	principal, _ := contract.PrincipalFrom(r.Context())
	csrf, _ := contract.CSRFTokenFrom(r.Context())

	data := struct {
		DBID     int64
		Engine   contract.Engine
		Snapshot contract.SchemaSnapshot
	}{
		DBID:     dbID,
		Engine:   engine,
		Snapshot: snapshot,
	}

	if err := h.renderer.Render(w, "viewer/schema.html", contract.PageData{
		Principal: principal,
		CSRFToken: csrf,
		Data:      data,
	}); err != nil {
		http.Error(w, "failed to render page", http.StatusInternalServerError)
		return
	}
}

func (h *Handler) TableDetail(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	dbID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid database id", http.StatusBadRequest)
		return
	}

	tableName := r.PathValue("table")
	if tableName == "" {
		http.Error(w, "table name cannot be empty", http.StatusBadRequest)
		return
	}

	rawSchema := r.URL.Query().Get("schema")

	db, engine, err := h.targetDB.Open(r.Context(), dbID)
	if err != nil {
		http.Error(w, "unable to connect to target database", http.StatusBadGateway)
		return
	}

	intro, err := h.introspectorFor(engine)
	if err != nil {
		http.Error(w, "unsupported database engine", http.StatusInternalServerError)
		return
	}

	schema := defaultSchemaForEngine(rawSchema, engine)

	snapshot, err := intro.Snapshot(r.Context(), db)
	if err != nil {
		if errors.Is(err, contract.ErrAccessDenied) {
			http.Error(w, "Access denied: database access prohibited", http.StatusForbidden)
			return
		}
		http.Error(w, "failed to introspect database schema", http.StatusInternalServerError)
		return
	}

	var foundTable *contract.Table
	for idx := range snapshot.Tables {
		t := &snapshot.Tables[idx]
		if strings.EqualFold(t.Name, tableName) && (schema == "" || strings.EqualFold(t.Schema, schema)) {
			foundTable = t
			break
		}
	}

	if foundTable == nil {
		http.Error(w, "table not found", http.StatusNotFound)
		return
	}

	principal, _ := contract.PrincipalFrom(r.Context())
	csrf, _ := contract.CSRFTokenFrom(r.Context())

	data := struct {
		DBID   int64
		Engine contract.Engine
		Table  *contract.Table
	}{
		DBID:   dbID,
		Engine: engine,
		Table:  foundTable,
	}

	if err := h.renderer.Render(w, "viewer/table.html", contract.PageData{
		Principal: principal,
		CSRFToken: csrf,
		Data:      data,
	}); err != nil {
		http.Error(w, "failed to render page", http.StatusInternalServerError)
		return
	}
}

func (h *Handler) TableData(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	dbID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid database id", http.StatusBadRequest)
		return
	}

	tableName := r.PathValue("table")
	if tableName == "" {
		http.Error(w, "table name cannot be empty", http.StatusBadRequest)
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

	db, engine, err := h.targetDB.Open(r.Context(), dbID)
	if err != nil {
		http.Error(w, "unable to connect to target database", http.StatusBadGateway)
		return
	}

	intro, err := h.introspectorFor(engine)
	if err != nil {
		http.Error(w, "unsupported database engine", http.StatusInternalServerError)
		return
	}

	schema := defaultSchemaForEngine(r.URL.Query().Get("schema"), engine)

	rowPage, err := intro.Rows(r.Context(), db, schema, tableName, contract.PageRequest{
		Page:     page,
		PageSize: pageSize,
		OrderBy:  orderBy,
		Desc:     desc,
	})
	if err != nil {
		if errors.Is(err, contract.ErrAccessDenied) {
			http.Error(w, "Access denied: cross-database access prohibited", http.StatusForbidden)
			return
		}
		http.Error(w, "failed to query table data", http.StatusInternalServerError)
		return
	}

	principal, _ := contract.PrincipalFrom(r.Context())
	csrf, _ := contract.CSRFTokenFrom(r.Context())

	totalPages := int((rowPage.Total + int64(pageSize) - 1) / int64(pageSize))
	if totalPages < 1 {
		totalPages = 1
	}

	data := struct {
		DBID        int64
		Engine      contract.Engine
		Schema      string
		TableName   string
		RowPage     contract.RowPage
		CurrentPage int
		PageSize    int
		TotalPages  int
		OrderBy     string
		Desc        bool
	}{
		DBID:        dbID,
		Engine:      engine,
		Schema:      schema,
		TableName:   tableName,
		RowPage:     rowPage,
		CurrentPage: page,
		PageSize:    pageSize,
		TotalPages:  totalPages,
		OrderBy:     orderBy,
		Desc:        desc,
	}

	if err := h.renderer.Render(w, "viewer/data.html", contract.PageData{
		Principal: principal,
		CSRFToken: csrf,
		Data:      data,
	}); err != nil {
		http.Error(w, "failed to render page", http.StatusInternalServerError)
		return
	}
}
