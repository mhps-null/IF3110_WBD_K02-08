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
		http.Error(w, fmt.Sprintf("Gagal memuat riwayat migration: %v", err), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

	m, err := h.service.CreateDraft(r.Context(), dbID, title, sqlText, principal.UserID)
	if err != nil {
		csrf, _ := contract.CSRFTokenFrom(r.Context())
		pageData := contract.PageData{
			Principal: principal,
			CSRFToken: csrf,
			Flash:     err.Error(),
			Data: map[string]any{
				"DBID":    dbID,
				"Title":   title,
				"SQLText": sqlText,
				"Error":   err.Error(),
			},
		}
		w.WriteHeader(http.StatusBadRequest)
		_ = h.renderer.Render(w, "migration/new.html", pageData)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/databases/%d/migrations/%d", dbID, m.ID), http.StatusSeeOther)
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

	m, err := h.service.GetMigration(r.Context(), mid)
	if err != nil {
		if errors.Is(err, contract.ErrNotFound) {
			http.Error(w, "Migration tidak ditemukan", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

	_, err = h.service.UpdateDraft(r.Context(), mid, title, sqlText, principal.UserID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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

	updatedM, res, execErr := h.service.ExecuteMigration(r.Context(), mid, principal.UserID)
	csrf, _ := contract.CSRFTokenFrom(r.Context())

	var flash string
	if execErr != nil {
		flash = fmt.Sprintf("Eksekusi gagal dan telah di-rollback: %v", execErr)
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

	_ = h.renderer.Render(w, "migration/detail.html", pageData)
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

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Form tidak valid", http.StatusBadRequest)
		return
	}

	analyze := strings.EqualFold(r.FormValue("analyze"), "true")
	principal, _ := contract.PrincipalFrom(r.Context())
	csrf, _ := contract.CSRFTokenFrom(r.Context())

	plan, err := h.service.ExplainMigration(r.Context(), mid, analyze)
	if err != nil {
		http.Error(w, fmt.Sprintf("Gagal menjalankan EXPLAIN: %v", err), http.StatusBadRequest)
		return
	}

	m, _ := h.service.GetMigration(r.Context(), mid)

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
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
