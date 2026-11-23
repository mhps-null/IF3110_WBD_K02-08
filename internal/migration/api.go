package migration

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

// RegisterReadAPIRoutes exposes read-only JSON migration history endpoints from M1
// so the SPA shell can list and inspect existing migrations immediately while Orang 3
// builds the full M2 revision, approval, rollback/forward, and execution APIs.
func (h *Handler) RegisterReadAPIRoutes(mux *http.ServeMux, authenticate func(http.Handler) http.Handler) {
	if authenticate == nil || h.guards.ViewDB == nil {
		return
	}
	wrapView := func(fn http.HandlerFunc) http.Handler { return authenticate(h.guards.ViewDB(fn)) }
	mux.Handle("GET /api/databases/{id}/migrations", wrapView(h.APIListMigrations))
	mux.Handle("GET /api/databases/{id}/migrations/{mid}", wrapView(h.APIGetMigration))
}

func (h *Handler) APIListMigrations(w http.ResponseWriter, r *http.Request) {
	dbID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || dbID <= 0 {
		writeMigrationJSON(w, http.StatusBadRequest, map[string]string{"error": "ID database tidak valid."})
		return
	}
	migrations, err := h.service.ListMigrations(r.Context(), dbID)
	if err != nil {
		writeMigrationJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memuat riwayat migration."})
		return
	}
	if migrations == nil {
		migrations = []*Migration{}
	}
	writeMigrationJSON(w, http.StatusOK, map[string]any{
		"db_id":      dbID,
		"migrations": migrations,
	})
}

func (h *Handler) APIGetMigration(w http.ResponseWriter, r *http.Request) {
	dbID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || dbID <= 0 {
		writeMigrationJSON(w, http.StatusBadRequest, map[string]string{"error": "ID database tidak valid."})
		return
	}
	mid, err := strconv.ParseInt(r.PathValue("mid"), 10, 64)
	if err != nil || mid <= 0 {
		writeMigrationJSON(w, http.StatusBadRequest, map[string]string{"error": "ID migration tidak valid."})
		return
	}
	m, err := h.service.GetMigration(r.Context(), dbID, mid)
	if err != nil {
		if errors.Is(err, contract.ErrNotFound) {
			writeMigrationJSON(w, http.StatusNotFound, map[string]string{"error": "Migration tidak ditemukan."})
			return
		}
		writeMigrationJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memuat detail migration."})
		return
	}
	writeMigrationJSON(w, http.StatusOK, map[string]any{
		"db_id":      dbID,
		"migration":  m,
		"diagnostic": m.Diagnostic(),
	})
}

func writeMigrationJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
