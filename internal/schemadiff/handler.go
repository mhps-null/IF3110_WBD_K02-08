package schemadiff

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/admin"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

type Handler struct {
	checker       contract.AccessChecker
	introspectors map[contract.Engine]contract.Introspector
	targetDB      contract.TargetDB
	renderer      admin.Renderer
	logger        *slog.Logger
}

func NewHandler(checker contract.AccessChecker, introspectors map[contract.Engine]contract.Introspector, targetDB contract.TargetDB, renderer admin.Renderer, logger *slog.Logger) *Handler {
	if checker == nil {
		panic("schemadiff: NewHandler requires a non-nil AccessChecker")
	}
	if len(introspectors) == 0 {
		panic("schemadiff: NewHandler requires at least one Introspector")
	}
	for engine, introspector := range introspectors {
		if !supportedDiffEngine(engine) || introspector == nil {
			panic("schemadiff: NewHandler received an invalid engine introspector")
		}
	}
	if targetDB == nil {
		panic("schemadiff: NewHandler requires a non-nil TargetDB")
	}
	if renderer == nil {
		renderer = admin.NewTemplateRenderer("web/templates")
	}
	if logger == nil {
		logger = slog.Default()
	}

	introspectorCopy := make(map[contract.Engine]contract.Introspector, len(introspectors))
	for engine, introspector := range introspectors {
		introspectorCopy[engine] = introspector
	}

	return &Handler{
		checker:       checker,
		introspectors: introspectorCopy,
		targetDB:      targetDB,
		renderer:      renderer,
		logger:        logger,
	}
}

// mendaftarkan route schema diff. Hanya user dengan akses ViewDB yg blh lihat.
func (h *Handler) Routes(mux *http.ServeMux, authenticate, viewDBGuard func(http.Handler) http.Handler) {
	if authenticate == nil {
		authenticate = func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "authentication middleware not configured", http.StatusForbidden)
			})
		}
	}
	if viewDBGuard == nil {
		viewDBGuard = func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "database access guard not configured", http.StatusForbidden)
			})
		}
	}
	wrap := func(handler http.HandlerFunc) http.Handler {
		return authenticate(viewDBGuard(handler))
	}

	mux.Handle("GET /databases/{id}/schema-diff", wrap(h.ShowDiffForm))
	mux.Handle("POST /databases/{id}/schema-diff", wrap(h.DoDiff))
}

type diffFormData struct {
	SourceDBID int64
	Databases  []contract.AccessibleDB
}

func (h *Handler) ShowDiffForm(w http.ResponseWriter, r *http.Request) {
	sourceID, ok := parseID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}

	principal, ok := contract.PrincipalFrom(r.Context())
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if !h.requireViewAccess(w, r, principal.UserID, sourceID) {
		return
	}

	dbs, err := h.checker.ListAccessible(r.Context(), principal.UserID)
	if err != nil {
		h.logger.Error("schemadiff: list accessible databases failed", "user_id", principal.UserID)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if _, found := findAccessibleDB(dbs, sourceID); !found {
		http.Error(w, "Akses ditolak", http.StatusForbidden)
		return
	}

	h.render(w, r, "schemadiff/form.html", contract.PageData{
		Data: diffFormData{SourceDBID: sourceID, Databases: dbs},
	})
}

type diffResultData struct {
	SourceDBID   int64
	SourceDBName string
	TargetDBName string
	Changes      []string
	GeneratedSQL string
	Engine       string
	CanEdit      bool
}

func (h *Handler) DoDiff(w http.ResponseWriter, r *http.Request) {
	sourceID, ok := parseID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}

	principal, ok := contract.PrincipalFrom(r.Context())
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	targetID, err := strconv.ParseInt(r.FormValue("target_id"), 10, 64)
	if err != nil || targetID <= 0 || targetID == sourceID {
		http.Error(w, "Target database tidak valid atau sama dengan sumber", http.StatusBadRequest)
		return
	}

	// Re-check authorization for both databases in the handler, not only in the
	// route guard. The target ID comes from user-controlled form data and must be
	// authorized before either database is opened or introspected.
	if !h.requireViewAccess(w, r, principal.UserID, sourceID) || !h.requireViewAccess(w, r, principal.UserID, targetID) {
		return
	}

	accessible, err := h.checker.ListAccessible(r.Context(), principal.UserID)
	if err != nil {
		h.logger.Error("schemadiff: list accessible databases failed", "user_id", principal.UserID)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	sourceAccess, sourceFound := findAccessibleDB(accessible, sourceID)
	targetAccess, targetFound := findAccessibleDB(accessible, targetID)
	if !sourceFound || !targetFound {
		http.Error(w, "Akses ditolak", http.StatusForbidden)
		return
	}

	// Open databases only after verifying the principal can view both.
	sourceDB, sourceEngine, err := h.targetDB.Open(r.Context(), sourceID)
	if err != nil {
		h.logger.Error("schemadiff: open source db failed", "id", sourceID)
		http.Error(w, "Gagal membuka database sumber", http.StatusInternalServerError)
		return
	}
	// targetDB is a pool manager: Open returns a shared *sql.DB pool, not a
	// request-owned connection. Do not Close it here; PoolManager owns its
	// lifecycle and closes all cached pools during server shutdown.

	targetDBConn, targetEngine, err := h.targetDB.Open(r.Context(), targetID)
	if err != nil {
		h.logger.Error("schemadiff: open target db failed", "id", targetID)
		http.Error(w, "Gagal membuka database target", http.StatusInternalServerError)
		return
	}
	// Do not close targetDBConn for the same shared-pool ownership reason.

	// The generated migration is submitted against the source database, so the
	// target snapshot must use the same SQL dialect. Cross-engine translation is
	// not implemented and must fail closed rather than emit unsafe SQL.
	if sourceEngine != targetEngine {
		h.logger.Warn("schemadiff: cross-engine diff is not supported",
			"source_engine", sourceEngine,
			"target_engine", targetEngine,
		)
		http.Error(w, "Schema diff lintas-DBMS belum didukung; pilih database dengan DBMS yang sama.", http.StatusUnprocessableEntity)
		return
	}
	if !supportedDiffEngine(sourceEngine) {
		h.logger.Warn("schemadiff: unsupported database engine", "engine", sourceEngine)
		http.Error(w, "DBMS tidak didukung untuk schema diff.", http.StatusUnprocessableEntity)
		return
	}

	introspector, ok := h.introspectors[sourceEngine]
	if !ok || introspector == nil {
		h.logger.Warn("schemadiff: introspector not configured", "engine", sourceEngine)
		http.Error(w, "Introspector untuk DBMS ini belum dikonfigurasi.", http.StatusUnprocessableEntity)
		return
	}

	// Read both schemas with the introspector matching the source/target engine.
	snapA, err := introspector.Snapshot(r.Context(), sourceDB)
	if err != nil {
		h.logger.Error("schemadiff: snapshot source failed", "id", sourceID)
		http.Error(w, "Gagal membaca schema sumber", http.StatusInternalServerError)
		return
	}

	snapB, err := introspector.Snapshot(r.Context(), targetDBConn)
	if err != nil {
		h.logger.Error("schemadiff: snapshot target failed", "id", targetID)
		http.Error(w, "Gagal membaca schema target", http.StatusInternalServerError)
		return
	}

	// proses Diff & Generate SQL
	changes := Diff(snapA, snapB)
	sql := GenerateSQL(changes, sourceEngine)

	var formatted []string
	for _, c := range changes {
		formatted = append(formatted, FormatChange(c))
	}

	h.render(w, r, "schemadiff/result.html", contract.PageData{
		Data: diffResultData{
			SourceDBID:   sourceID,
			SourceDBName: sourceAccess.Name,
			TargetDBName: targetAccess.Name,
			Changes:      formatted,
			GeneratedSQL: sql,
			Engine:       string(sourceEngine),
			CanEdit:      sourceAccess.Level >= contract.AccessViewEdit,
		},
	})
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, name string, data contract.PageData) {
	p, _ := contract.PrincipalFrom(r.Context())
	data.Principal = p
	data.CSRFToken = csrfToken(r.Context())

	if err := h.renderer.Render(w, name, data); err != nil {
		h.logger.Error("schemadiff: render failed", "template", name)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *Handler) requireViewAccess(w http.ResponseWriter, r *http.Request, userID, dbID int64) bool {
	level, err := h.checker.EffectiveLevel(r.Context(), userID, dbID)
	if err != nil {
		h.logger.Error("schemadiff: access check failed", "user_id", userID, "database_id", dbID)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return false
	}
	if level < contract.AccessViewOnly {
		http.Error(w, "Akses ditolak", http.StatusForbidden)
		return false
	}
	return true
}

func supportedDiffEngine(engine contract.Engine) bool {
	switch engine {
	case contract.EnginePostgres, contract.EngineMySQL, contract.EngineMariaDB:
		return true
	default:
		return false
	}
}

func findAccessibleDB(databases []contract.AccessibleDB, dbID int64) (contract.AccessibleDB, bool) {
	for _, database := range databases {
		if database.DBID == dbID && database.Level >= contract.AccessViewOnly {
			return database, true
		}
	}
	return contract.AccessibleDB{}, false
}

func parseID(r *http.Request, key string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(key), 10, 64)
	return id, err == nil && id > 0
}

func csrfToken(ctx context.Context) string {
	token, _ := contract.CSRFTokenFrom(ctx)
	return token
}
