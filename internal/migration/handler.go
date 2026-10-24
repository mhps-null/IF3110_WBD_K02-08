package migration

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/example/if3110-milestone1/internal/contract"
)

type Handler struct {
	service  Service
	renderer Renderer
	guards   contract.Guards
}

func NewHandler(service Service, renderer Renderer, guards contract.Guards) *Handler {
	return &Handler{
		service:  service,
		renderer: renderer,
		guards:   guards,
	}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	viewDB := h.guards.ViewDB
	if viewDB == nil {
		viewDB = func(next http.Handler) http.Handler { return next }
	}
	editDB := h.guards.EditDB
	if editDB == nil {
		editDB = func(next http.Handler) http.Handler { return next }
	}

	mux.Handle("GET /databases/{id}/migrations", viewDB(http.HandlerFunc(h.ListMigrations)))
	mux.Handle("GET /databases/{id}/migrations/new", editDB(http.HandlerFunc(h.NewMigrationPage)))
	mux.Handle("POST /databases/{id}/migrations/new", editDB(http.HandlerFunc(h.PrefillMigrationPage)))
	mux.Handle("POST /databases/{id}/migrations", editDB(http.HandlerFunc(h.CreateMigration)))
	mux.Handle("GET /databases/{id}/migrations/{mid}", viewDB(http.HandlerFunc(h.MigrationDetail)))
	mux.Handle("POST /databases/{id}/migrations/{mid}", editDB(http.HandlerFunc(h.UpdateMigration)))
	mux.Handle("POST /databases/{id}/migrations/{mid}/execute", editDB(http.HandlerFunc(h.ExecuteMigration)))
	mux.Handle("POST /databases/{id}/migrations/{mid}/explain", editDB(http.HandlerFunc(h.ExplainMigration)))
}

func (h *Handler) ListMigrations(w http.ResponseWriter, r *http.Request) {
	dbID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || dbID <= 0 {
		http.Error(w, "ID database tidak valid", http.StatusBadRequest)
		return
	}

	principal, _ := contract.PrincipalFrom(r.Context())
	csrf, _ := contract.CSRFTokenFrom(r.Context())
	flash := r.URL.Query().Get("flash")

	migrations, err := h.service.ListMigrations(r.Context(), dbID)
	if err != nil {
		http.Error(w, "Gagal memuat riwayat migration.", http.StatusInternalServerError)
		return
	}

	pageData := contract.PageData{
		Principal: principal,
		CSRFToken: csrf,
		Flash:     flash,
		Data: map[string]any{
			"DBID":       dbID,
			"Migrations": migrations,
		},
	}

	if err := h.renderer.Render(w, "migration/index.html", pageData); err != nil {
		http.Error(w, "Terjadi kesalahan internal.", http.StatusInternalServerError)
	}
}

func (h *Handler) NewMigrationPage(w http.ResponseWriter, r *http.Request) {
	dbID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || dbID <= 0 {
		http.Error(w, "ID database tidak valid", http.StatusBadRequest)
		return
	}

	principal, _ := contract.PrincipalFrom(r.Context())
	csrf, _ := contract.CSRFTokenFrom(r.Context())
	flash := r.URL.Query().Get("flash")

	pageData := contract.PageData{
		Principal: principal,
		CSRFToken: csrf,
		Flash:     flash,
		Data: map[string]any{
			"DBID":    dbID,
			"Title":   "",
			"SQLText": "",
		},
	}

	if err := h.renderer.Render(w, "migration/new.html", pageData); err != nil {
		http.Error(w, "Terjadi kesalahan internal.", http.StatusInternalServerError)
	}
}

func (h *Handler) PrefillMigrationPage(w http.ResponseWriter, r *http.Request) {
	dbID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || dbID <= 0 {
		http.Error(w, "ID database tidak valid", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Form tidak valid", http.StatusBadRequest)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	sqlText := strings.TrimSpace(r.FormValue("sql"))
	if sqlText == "" {
		sqlText = strings.TrimSpace(r.FormValue("sql_text"))
	}

	principal, _ := contract.PrincipalFrom(r.Context())
	csrf, _ := contract.CSRFTokenFrom(r.Context())

	pageData := contract.PageData{
		Principal: principal,
		CSRFToken: csrf,
		Flash:     "SQL berhasil di-prefill dari Schema Diff. Silakan tinjau dan simpan sebagai draft.",
		Data: map[string]any{
			"DBID":    dbID,
			"Title":   title,
			"SQLText": sqlText,
		},
	}

	if err := h.renderer.Render(w, "migration/new.html", pageData); err != nil {
		http.Error(w, "Terjadi kesalahan internal.", http.StatusInternalServerError)
	}
}

func (h *Handler) CreateMigration(w http.ResponseWriter, r *http.Request) {
	dbID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || dbID <= 0 {
		http.Error(w, "ID database tidak valid", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Data form tidak valid", http.StatusBadRequest)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	sqlText := strings.TrimSpace(r.FormValue("sql_text"))

	principal, _ := contract.PrincipalFrom(r.Context())

	if title == "" {
		h.renderCreateMigrationError(w, r, dbID, title, sqlText, principal, "Judul migration tidak boleh kosong.", http.StatusBadRequest)
		return
	}
	if sqlText == "" {
		h.renderCreateMigrationError(w, r, dbID, title, sqlText, principal, "Query SQL migration tidak boleh kosong.", http.StatusBadRequest)
		return
	}

	m, err := h.service.CreateDraft(r.Context(), dbID, title, sqlText, principal.UserID)
	if err != nil {
		h.renderCreateMigrationError(w, r, dbID, title, sqlText, principal, "Draft migration tidak dapat disimpan. Silakan coba lagi.", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/databases/%d/migrations/%d", dbID, m.ID), http.StatusSeeOther)
}

func (h *Handler) renderCreateMigrationError(w http.ResponseWriter, r *http.Request, dbID int64, title, sqlText string, principal contract.Principal, message string, status int) {
	csrf, _ := contract.CSRFTokenFrom(r.Context())
	pageData := contract.PageData{
		Principal: principal,
		CSRFToken: csrf,
		Flash:     message,
		Data: map[string]any{
			"DBID":    dbID,
			"Title":   title,
			"SQLText": sqlText,
			"Error":   message,
		},
	}
	w.WriteHeader(status)
	if err := h.renderer.Render(w, "migration/new.html", pageData); err != nil {
		// Never expose template or driver errors in the HTTP response.
		_, _ = fmt.Fprintln(w, "Terjadi kesalahan internal.")
	}
}

func (h *Handler) MigrationDetail(w http.ResponseWriter, r *http.Request) {
	dbID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || dbID <= 0 {
		http.Error(w, "ID database tidak valid", http.StatusBadRequest)
		return
	}
	mid, err := strconv.ParseInt(r.PathValue("mid"), 10, 64)
	if err != nil || mid <= 0 {
		http.Error(w, "ID migration tidak valid", http.StatusBadRequest)
		return
	}

	principal, _ := contract.PrincipalFrom(r.Context())
	csrf, _ := contract.CSRFTokenFrom(r.Context())
	flash := r.URL.Query().Get("flash")

	m, err := h.service.GetMigration(r.Context(), dbID, mid)
	if err != nil {
		if errors.Is(err, contract.ErrNotFound) {
			http.Error(w, "Migration tidak ditemukan", http.StatusNotFound)
			return
		}
		http.Error(w, "Terjadi kesalahan internal.", http.StatusInternalServerError)
		return
	}

	pageData := contract.PageData{
		Principal: principal,
		CSRFToken: csrf,
		Flash:     flash,
		Data: map[string]any{
			"DBID":      dbID,
			"Migration": m,
		},
	}

	if err := h.renderer.Render(w, "migration/detail.html", pageData); err != nil {
		http.Error(w, "Terjadi kesalahan internal.", http.StatusInternalServerError)
	}
}

func (h *Handler) UpdateMigration(w http.ResponseWriter, r *http.Request) {
	dbID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || dbID <= 0 {
		http.Error(w, "ID database tidak valid", http.StatusBadRequest)
		return
	}
	mid, err := strconv.ParseInt(r.PathValue("mid"), 10, 64)
	if err != nil || mid <= 0 {
		http.Error(w, "ID migration tidak valid", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Data form tidak valid", http.StatusBadRequest)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	sqlText := strings.TrimSpace(r.FormValue("sql_text"))
	principal, _ := contract.PrincipalFrom(r.Context())

	_, err = h.service.UpdateDraft(r.Context(), dbID, mid, title, sqlText, principal.UserID)
	if err != nil {
		if errors.Is(err, contract.ErrNotFound) {
			http.Error(w, "Migration tidak ditemukan", http.StatusNotFound)
		} else {
			http.Error(w, "Draft migration tidak dapat diperbarui", http.StatusBadRequest)
		}
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/databases/%d/migrations/%d", dbID, mid), http.StatusSeeOther)
}

func (h *Handler) ExecuteMigration(w http.ResponseWriter, r *http.Request) {
	dbID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || dbID <= 0 {
		http.Error(w, "ID database tidak valid", http.StatusBadRequest)
		return
	}
	mid, err := strconv.ParseInt(r.PathValue("mid"), 10, 64)
	if err != nil || mid <= 0 {
		http.Error(w, "ID migration tidak valid", http.StatusBadRequest)
		return
	}

	principal, _ := contract.PrincipalFrom(r.Context())

	updatedM, res, execErr := h.service.ExecuteMigration(r.Context(), dbID, mid, principal.UserID)
	if errors.Is(execErr, contract.ErrNotFound) {
		http.Error(w, "Migration tidak ditemukan", http.StatusNotFound)
		return
	}
	if updatedM == nil {
		// Some failures happen before execution is finalized (for example, target
		// connection errors). Reload the record so the detail template never
		// receives a nil Migration.
		updatedM, err = h.service.GetMigration(r.Context(), dbID, mid)
		if err != nil {
			http.Error(w, "Gagal memuat detail migration", http.StatusInternalServerError)
			return
		}
	}
	csrf, _ := contract.CSRFTokenFrom(r.Context())

	var flash string
	if errors.Is(execErr, ErrTargetCommittedMetadataUnknown) {
		flash = "SQL pada database target mungkin sudah berhasil dijalankan, tetapi riwayat migration gagal dikonfirmasi. Jangan jalankan ulang sebelum memeriksa database target dan riwayat migration."
	} else if execErr != nil {
		flash = "Migration tidak berhasil diselesaikan. Periksa status database dan riwayat migration sebelum mencoba lagi."
	} else {
		flash = fmt.Sprintf("Migration berhasil dieksekusi (Versi %v) dalam %v. Baris terdampak: %d",
			*updatedM.Version, res.Duration, res.RowsAffected)
	}

	pageData := contract.PageData{
		Principal: principal,
		CSRFToken: csrf,
		Flash:     flash,
		Data: map[string]any{
			"DBID":       dbID,
			"Migration":  updatedM,
			"ExecResult": res,
			"ExecError":  execErr,
		},
	}

	if err := h.renderer.Render(w, "migration/detail.html", pageData); err != nil {
		http.Error(w, "Terjadi kesalahan internal.", http.StatusInternalServerError)
	}
}

func (h *Handler) ExplainMigration(w http.ResponseWriter, r *http.Request) {
	dbID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || dbID <= 0 {
		http.Error(w, "ID database tidak valid", http.StatusBadRequest)
		return
	}
	mid, err := strconv.ParseInt(r.PathValue("mid"), 10, 64)
	if err != nil || mid <= 0 {
		http.Error(w, "ID migration tidak valid", http.StatusBadRequest)
		return
	}

	m, err := h.service.GetMigration(r.Context(), dbID, mid)
	if err != nil {
		if errors.Is(err, contract.ErrNotFound) {
			http.Error(w, "Migration tidak ditemukan", http.StatusNotFound)
		} else {
			http.Error(w, "Gagal memuat migration", http.StatusInternalServerError)
		}
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Form tidak valid", http.StatusBadRequest)
		return
	}

	analyze := strings.EqualFold(r.FormValue("analyze"), "true")
	principal, _ := contract.PrincipalFrom(r.Context())
	csrf, _ := contract.CSRFTokenFrom(r.Context())

	plan, err := h.service.ExplainMigration(r.Context(), dbID, mid, analyze)
	if err != nil {
		if errors.Is(err, contract.ErrNotFound) {
			http.Error(w, "Migration tidak ditemukan", http.StatusNotFound)
		} else {
			http.Error(w, "Gagal menjalankan EXPLAIN", http.StatusBadRequest)
		}
		return
	}

	pageData := contract.PageData{
		Principal: principal,
		CSRFToken: csrf,
		Flash:     "Analisis query execution plan berhasil diperoleh.",
		Data: map[string]any{
			"DBID":      dbID,
			"Migration": m,
			"Plan":      plan,
			"Analyzed":  analyze,
		},
	}

	if err := h.renderer.Render(w, "migration/explain.html", pageData); err != nil {
		http.Error(w, "Terjadi kesalahan internal.", http.StatusInternalServerError)
	}
}
