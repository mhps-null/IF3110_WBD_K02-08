package connections

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/example/if3110-milestone1/internal/contract"
)

type Renderer interface {
	Render(w http.ResponseWriter, templateRelativePath string, data contract.PageData) error
}

// Handler manages HTTP endpoints for database connection profiles.
type Handler struct {
	repo     Repository
	poolMgr  *PoolManager
	renderer Renderer
	guards   contract.Guards
}

func NewHandler(repo Repository, poolMgr *PoolManager, renderer Renderer, guards contract.Guards) *Handler {
	return &Handler{
		repo:     repo,
		poolMgr:  poolMgr,
		renderer: renderer,
		guards:   guards,
	}
}

// RegisterRoutes registers connection management routes with admin authorization guards.
// It preserves the unauthenticated unit-test interface; production wiring should use
// RegisterRoutesWithAuth so the principal is established before authorization runs.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	h.RegisterRoutesWithAuth(mux, func(next http.Handler) http.Handler { return next })
}

// RegisterRoutesWithAuth registers the routes behind authentication and the platform-admin guard.
func (h *Handler) RegisterRoutesWithAuth(mux *http.ServeMux, authenticate func(http.Handler) http.Handler) {
	if authenticate == nil {
		authenticate = func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "Access denied: authentication middleware not configured", http.StatusForbidden)
			})
		}
	}
	admin := h.guards.Admin
	if admin == nil {
		admin = func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "Access denied: security guard not configured", http.StatusForbidden)
			})
		}
	}

	wrap := func(handler http.HandlerFunc) http.Handler {
		return authenticate(admin(handler))
	}
	mux.Handle("GET /admin/connections", wrap(h.List))
	mux.Handle("POST /admin/connections", wrap(h.Create))
	mux.Handle("GET /admin/connections/{id}/edit", wrap(h.EditForm))
	mux.Handle("POST /admin/connections/{id}/edit", wrap(h.Update))
	mux.Handle("POST /admin/connections/{id}/delete", wrap(h.Delete))
	mux.Handle("POST /admin/connections/test", wrap(h.TestConnection))
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	conns, err := h.repo.List(r.Context())
	if err != nil {
		http.Error(w, "failed to list connections", http.StatusInternalServerError)
		return
	}

	principal, _ := contract.PrincipalFrom(r.Context())
	csrf, _ := contract.CSRFTokenFrom(r.Context())

	var flash string
	switch r.URL.Query().Get("msg") {
	case "created":
		flash = "Connection created successfully."
	case "updated":
		flash = "Connection updated successfully."
	case "deleted":
		flash = "Connection deleted successfully."
	}
	if errMsg := r.URL.Query().Get("err"); errMsg != "" {
		flash = "Error: " + errMsg
	}

	data := struct {
		Connections []Connection
	}{
		Connections: conns,
	}

	if err := h.renderer.Render(w, "connections/index.html", contract.PageData{
		Principal: principal,
		CSRFToken: csrf,
		Flash:     flash,
		Data:      data,
	}); err != nil {
		http.Error(w, "failed to render page", http.StatusInternalServerError)
		return
	}
}

func (h *Handler) EditForm(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid connection id", http.StatusBadRequest)
		return
	}

	conn, err := h.repo.FindByID(r.Context(), id)
	if err != nil {
		http.Error(w, "connection not found", http.StatusNotFound)
		return
	}

	principal, _ := contract.PrincipalFrom(r.Context())
	csrf, _ := contract.CSRFTokenFrom(r.Context())

	var flash string
	if errMsg := r.URL.Query().Get("err"); errMsg != "" {
		flash = "Error: " + errMsg
	}

	if err := h.renderer.Render(w, "connections/edit.html", contract.PageData{
		Principal: principal,
		CSRFToken: csrf,
		Flash:     flash,
		Data:      conn,
	}); err != nil {
		http.Error(w, "failed to render page", http.StatusInternalServerError)
		return
	}
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	engine := contract.Engine(strings.ToLower(strings.TrimSpace(r.FormValue("engine"))))
	host := strings.TrimSpace(r.FormValue("host"))
	portStr := strings.TrimSpace(r.FormValue("port"))
	dbName := strings.TrimSpace(r.FormValue("database_name"))
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	sslMode := strings.TrimSpace(r.FormValue("sslmode"))

	if name == "" || host == "" || dbName == "" || username == "" || password == "" {
		http.Redirect(w, r, "/admin/connections?err=missing_required_fields", http.StatusSeeOther)
		return
	}

	if engine != contract.EnginePostgres && engine != contract.EngineMySQL && engine != contract.EngineMariaDB {
		http.Redirect(w, r, "/admin/connections?err=invalid_engine", http.StatusSeeOther)
		return
	}

	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		http.Redirect(w, r, "/admin/connections?err=invalid_port", http.StatusSeeOther)
		return
	}

	if sslMode == "" {
		sslMode = "disable"
	}

	conn := &Connection{
		Name:         name,
		Engine:       engine,
		Host:         host,
		Port:         port,
		DatabaseName: dbName,
		Username:     username,
		SSLMode:      sslMode,
	}

	if err := h.repo.Create(r.Context(), conn, password); err != nil {
		http.Redirect(w, r, "/admin/connections?err=failed_to_save_connection", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/connections?msg=created", http.StatusSeeOther)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid connection id", http.StatusBadRequest)
		return
	}

	existing, err := h.repo.FindByID(r.Context(), id)
	if err != nil {
		http.Error(w, "connection not found", http.StatusNotFound)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	engineStr := strings.ToLower(strings.TrimSpace(r.FormValue("engine")))
	host := strings.TrimSpace(r.FormValue("host"))
	portStr := strings.TrimSpace(r.FormValue("port"))
	dbName := strings.TrimSpace(r.FormValue("database_name"))
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	sslMode := strings.TrimSpace(r.FormValue("sslmode"))

	if name == "" || host == "" || dbName == "" || username == "" {
		http.Redirect(w, r, "/admin/connections/"+idStr+"/edit?err=missing_required_fields", http.StatusSeeOther)
		return
	}

	engine := contract.Engine(engineStr)
	if engine != contract.EnginePostgres && engine != contract.EngineMySQL && engine != contract.EngineMariaDB {
		http.Redirect(w, r, "/admin/connections/"+idStr+"/edit?err=invalid_engine", http.StatusSeeOther)
		return
	}

	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		http.Redirect(w, r, "/admin/connections/"+idStr+"/edit?err=invalid_port", http.StatusSeeOther)
		return
	}

	if sslMode == "" {
		sslMode = "disable"
	}

	existing.Name = name
	existing.Engine = engine
	existing.Host = host
	existing.Port = port
	existing.DatabaseName = dbName
	existing.Username = username
	existing.SSLMode = sslMode

	var newPasswordPtr *string
	if password != "" {
		newPasswordPtr = &password
	}

	if err := h.repo.Update(r.Context(), existing, newPasswordPtr); err != nil {
		http.Redirect(w, r, "/admin/connections/"+idStr+"/edit?err=failed_to_update_connection", http.StatusSeeOther)
		return
	}

	if h.poolMgr != nil {
		_ = h.poolMgr.Invalidate(id)
	}

	http.Redirect(w, r, "/admin/connections?msg=updated", http.StatusSeeOther)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid connection id", http.StatusBadRequest)
		return
	}

	if err := h.repo.Delete(r.Context(), id); err != nil {
		http.Redirect(w, r, "/admin/connections?err=cannot_delete_has_migration_history", http.StatusSeeOther)
		return
	}

	if h.poolMgr != nil {
		_ = h.poolMgr.Invalidate(id)
	}

	http.Redirect(w, r, "/admin/connections?msg=deleted", http.StatusSeeOther)
}

type testConnResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

func (h *Handler) TestConnection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(testConnResponse{Success: false, Message: "Invalid request payload."})
		return
	}

	idStr := strings.TrimSpace(r.FormValue("id"))
	engine := contract.Engine(strings.ToLower(strings.TrimSpace(r.FormValue("engine"))))
	host := strings.TrimSpace(r.FormValue("host"))
	portStr := strings.TrimSpace(r.FormValue("port"))
	dbName := strings.TrimSpace(r.FormValue("database_name"))
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	sslMode := strings.TrimSpace(r.FormValue("sslmode"))

	if engine != contract.EnginePostgres && engine != contract.EngineMySQL && engine != contract.EngineMariaDB {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(testConnResponse{Success: false, Message: "Invalid database engine."})
		return
	}

	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(testConnResponse{Success: false, Message: "Invalid port: must be between 1 and 65535."})
		return
	}

	if host == "" || dbName == "" || username == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(testConnResponse{Success: false, Message: "Host, database name, and username are required."})
		return
	}

	if sslMode == "" {
		sslMode = "disable"
	}

	plainPassword := password
	if plainPassword == "" && idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(testConnResponse{Success: false, Message: "Invalid connection profile ID."})
			return
		}
		_, storedPass, err := h.repo.FindByIDWithPassword(r.Context(), id)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(testConnResponse{Success: false, Message: "Connection profile not found."})
			return
		}
		plainPassword = storedPass
	}

	if plainPassword == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(testConnResponse{Success: false, Message: "Password is required to test connection."})
		return
	}

	conn := &Connection{
		Engine:       engine,
		Host:         host,
		Port:         port,
		DatabaseName: dbName,
		Username:     username,
		SSLMode:      sslMode,
	}

	if err := TestConnection(r.Context(), "", conn, plainPassword); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(testConnResponse{
			Success: false,
			Message: "Connection failed: unable to reach database with provided parameters.",
		})
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(testConnResponse{
		Success: true,
		Message: "Connection established successfully.",
	})
}
