package schemadiff

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/example/if3110-milestone1/internal/admin"
	"github.com/example/if3110-milestone1/internal/contract"
)

type Handler struct {
	store *admin.Store
	introspector contract.Introspector
	targetDB contract.TargetDB
	renderer admin.Renderer
	logger *slog.Logger
}

func NewHandler(store *admin.Store, introspector contract.Introspector, targetDB contract.TargetDB, renderer admin.Renderer, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}

	return &Handler{
		store: store,
		introspector: introspector,
		targetDB: targetDB,
		renderer: renderer,
		logger: logger,
	}
}

// mendaftarkan route schema diff. Hanya user dengan akses ViewDB yg blh lihat.
func (h *Handler) Routes(mux *http.ServeMux, authenticate, viewDBGuard func(http.Handler) http.Handler) {
	wrap := func(handler http.HandlerFunc) http.Handler {
		return authenticate(viewDBGuard(handler))
	}

	mux.Handle("GET /databases/{id}/schema-diff", wrap(h.ShowDiffForm))
	mux.Handle("POST /databases/{id}/schema-diff", wrap(h.DoDiff))
}

type diffFormData struct {
	SourceDBID int64
	Databases []admin.Database
}

func (h *Handler) ShowDiffForm(w http.ResponseWriter, r *http.Request) {
	sourceID, ok := parseID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}

	dbs, err := h.store.ListDatabases(r.Context())
	if err != nil {
		h.logger.Error("schemadiff: list db failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	h.render(w, r, "schemadiff/form.html", contract.PageData{
		Data: diffFormData{SourceDBID: sourceID, Databases: dbs},
	})
}

type diffResultData struct {
	SourceDBID int64
	SourceDBName string
	TargetDBName string
	Changes []string
	GeneratedSQL string
	Engine string
}

func (h *Handler) DoDiff(w http.ResponseWriter, r *http.Request) {
	sourceID, ok := parseID(r, "id")
	if !ok {
		http.NotFound(w, r)
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

	// dapetin koneksi ke database target
	sourceDB, engine, err := h.targetDB.Open(r.Context(), sourceID)
	if err != nil {
		h.logger.Error("schemadiff: open source db failed", "id", sourceID)
		http.Error(w, "Gagal membuka database sumber", http.StatusInternalServerError)
		return
	}
	defer sourceDB.Close()

	targetDBConn, _, err := h.targetDB.Open(r.Context(), targetID)
	if err != nil {
		h.logger.Error("schemadiff: open target db failed", "id", targetID)
		http.Error(w, "Gagal membuka database target", http.StatusInternalServerError)
		return
	}
	defer targetDBConn.Close()

	// ambil Snapshot
	snapA, err := h.introspector.Snapshot(r.Context(), sourceDB)
	if err != nil {
		h.logger.Error("schemadiff: snapshot source failed", "id", sourceID)
		http.Error(w, "Gagal membaca schema sumber", http.StatusInternalServerError)
		return
	}

	snapB, err := h.introspector.Snapshot(r.Context(), targetDBConn)
	if err != nil {
		h.logger.Error("schemadiff: snapshot target failed", "id", targetID)
		http.Error(w, "Gagal membaca schema target", http.StatusInternalServerError)
		return
	}

	// proses Diff & Generate SQL
	changes := Diff(snapA, snapB)
	sql := GenerateSQL(changes, engine)

	var formatted []string
	for _, c := range changes {
		formatted = append(formatted, FormatChange(c))
	}

	// cari nama database buat UI
	var sourceName, targetName string
	dbs, _ := h.store.ListDatabases(r.Context())
	for _, d := range dbs {
		if d.ID == sourceID {
			sourceName = d.Name
		}
		if d.ID == targetID {
			targetName = d.Name
		}
	}

	h.render(w, r, "schemadiff/result.html", contract.PageData{
		Data: diffResultData{
			SourceDBID: sourceID,
			SourceDBName: sourceName,
			TargetDBName: targetName,
			Changes: formatted,
			GeneratedSQL: sql,
			Engine: string(engine),
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

func parseID(r *http.Request, key string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(key), 10, 64)
	return id, err == nil && id > 0
}

func csrfToken(ctx context.Context) string {
	token, _ := contract.CSRFTokenFrom(ctx)
	return token
}
