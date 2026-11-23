package connections

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/audit"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

type connectionJSONRequest struct {
	ID           *int64 `json:"id,omitempty"`
	Name         string `json:"name"`
	Engine       string `json:"engine"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	DatabaseName string `json:"database_name"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	SSLMode      string `json:"ssl_mode"`
}

// RegisterAPIRoutes registers JSON endpoints for Platform Admin connection management.
func (h *Handler) RegisterAPIRoutes(mux *http.ServeMux, authenticate func(http.Handler) http.Handler) {
	if authenticate == nil {
		authenticate = func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeConnJSON(w, http.StatusForbidden, map[string]string{"error": "authentication middleware not configured"})
			})
		}
	}
	admin := h.guards.Admin
	if admin == nil {
		admin = func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeConnJSON(w, http.StatusForbidden, map[string]string{"error": "security guard not configured"})
			})
		}
	}
	wrap := func(fn http.HandlerFunc) http.Handler {
		return authenticate(admin(fn))
	}

	mux.Handle("GET /api/admin/connections", wrap(h.APIList))
	mux.Handle("POST /api/admin/connections", wrap(h.APICreate))
	mux.Handle("GET /api/admin/connections/pool-stats", wrap(h.APIPoolStats))
	mux.Handle("POST /api/admin/connections/test", wrap(h.APITestConnection))
	mux.Handle("GET /api/admin/connections/{id}", wrap(h.APIGet))
	mux.Handle("PUT /api/admin/connections/{id}", wrap(h.APIUpdate))
	mux.Handle("DELETE /api/admin/connections/{id}", wrap(h.APIDelete))
}

func (h *Handler) APIList(w http.ResponseWriter, r *http.Request) {
	conns, err := h.repo.List(r.Context())
	if err != nil {
		writeConnJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memuat daftar koneksi database."})
		return
	}
	if conns == nil {
		conns = []Connection{}
	}
	writeConnJSON(w, http.StatusOK, map[string]any{"connections": conns})
}

func (h *Handler) APIGet(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeConnJSON(w, http.StatusBadRequest, map[string]string{"error": "ID koneksi tidak valid."})
		return
	}
	conn, err := h.repo.FindByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, contract.ErrNotFound) {
			writeConnJSON(w, http.StatusNotFound, map[string]string{"error": "Koneksi database tidak ditemukan."})
			return
		}
		writeConnJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memuat koneksi database."})
		return
	}
	writeConnJSON(w, http.StatusOK, map[string]any{"connection": conn})
}

func (h *Handler) APICreate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var req connectionJSONRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeConnJSON(w, http.StatusBadRequest, map[string]string{"error": "Payload JSON tidak valid."})
		return
	}

	name := strings.TrimSpace(req.Name)
	engine := contract.Engine(strings.ToLower(strings.TrimSpace(req.Engine)))
	if engine == "" {
		engine = contract.EnginePostgres
	}
	host := strings.TrimSpace(req.Host)
	dbName := strings.TrimSpace(req.DatabaseName)
	username := strings.TrimSpace(req.Username)
	sslMode := strings.TrimSpace(req.SSLMode)
	if sslMode == "" {
		sslMode = "disable"
	}

	if name == "" || host == "" || dbName == "" || username == "" || req.Password == "" {
		writeConnJSON(w, http.StatusBadRequest, map[string]string{"error": "Nama, host, nama database, username, dan password wajib diisi."})
		return
	}
	if engine != contract.EnginePostgres && engine != contract.EngineMySQL && engine != contract.EngineMariaDB {
		writeConnJSON(w, http.StatusBadRequest, map[string]string{"error": "Engine database tidak valid (gunakan postgres, mysql, atau mariadb)."})
		return
	}
	if req.Port < 1 || req.Port > 65535 {
		writeConnJSON(w, http.StatusBadRequest, map[string]string{"error": "Port harus berada di antara 1 dan 65535."})
		return
	}

	conn := &Connection{
		Name:         name,
		Engine:       engine,
		Host:         host,
		Port:         req.Port,
		DatabaseName: dbName,
		Username:     username,
		SSLMode:      sslMode,
	}
	if err := h.repo.Create(r.Context(), conn, req.Password); err != nil {
		writeConnJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal menyimpan koneksi database."})
		return
	}
	h.recordConnAudit(r.Context(), "admin.connection.create", conn.Name, "success", map[string]any{
		"connection_id": conn.ID,
		"engine":        conn.Engine,
		"host":          conn.Host,
		"port":          conn.Port,
		"database_name": conn.DatabaseName,
	})
	writeConnJSON(w, http.StatusCreated, map[string]any{
		"connection": conn,
		"message":    "Koneksi database berhasil dibuat.",
	})
}

func (h *Handler) APIUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeConnJSON(w, http.StatusBadRequest, map[string]string{"error": "ID koneksi tidak valid."})
		return
	}
	existing, err := h.repo.FindByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, contract.ErrNotFound) {
			writeConnJSON(w, http.StatusNotFound, map[string]string{"error": "Koneksi database tidak ditemukan."})
			return
		}
		writeConnJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memuat koneksi database."})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var req connectionJSONRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeConnJSON(w, http.StatusBadRequest, map[string]string{"error": "Payload JSON tidak valid."})
		return
	}

	name := strings.TrimSpace(req.Name)
	engine := contract.Engine(strings.ToLower(strings.TrimSpace(req.Engine)))
	if engine == "" {
		engine = existing.Engine
	}
	host := strings.TrimSpace(req.Host)
	dbName := strings.TrimSpace(req.DatabaseName)
	username := strings.TrimSpace(req.Username)
	sslMode := strings.TrimSpace(req.SSLMode)
	if sslMode == "" {
		sslMode = "disable"
	}

	if name == "" || host == "" || dbName == "" || username == "" {
		writeConnJSON(w, http.StatusBadRequest, map[string]string{"error": "Nama, host, nama database, dan username wajib diisi."})
		return
	}
	if engine != contract.EnginePostgres && engine != contract.EngineMySQL && engine != contract.EngineMariaDB {
		writeConnJSON(w, http.StatusBadRequest, map[string]string{"error": "Engine database tidak valid."})
		return
	}
	if req.Port < 1 || req.Port > 65535 {
		writeConnJSON(w, http.StatusBadRequest, map[string]string{"error": "Port harus berada di antara 1 dan 65535."})
		return
	}

	existing.Name = name
	existing.Engine = engine
	existing.Host = host
	existing.Port = req.Port
	existing.DatabaseName = dbName
	existing.Username = username
	existing.SSLMode = sslMode

	var newPasswordPtr *string
	if req.Password != "" {
		newPasswordPtr = &req.Password
	}
	if err := h.repo.Update(r.Context(), existing, newPasswordPtr); err != nil {
		writeConnJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memperbarui koneksi database."})
		return
	}
	if h.poolMgr != nil {
		_ = h.poolMgr.Invalidate(id)
	}
	h.recordConnAudit(r.Context(), "admin.connection.update", existing.Name, "success", map[string]any{
		"connection_id": existing.ID,
		"engine":        existing.Engine,
		"host":          existing.Host,
		"port":          existing.Port,
		"database_name": existing.DatabaseName,
	})
	writeConnJSON(w, http.StatusOK, map[string]any{
		"connection": existing,
		"message":    "Koneksi database berhasil diperbarui.",
	})
}

func (h *Handler) APIDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeConnJSON(w, http.StatusBadRequest, map[string]string{"error": "ID koneksi tidak valid."})
		return
	}
	connName := ""
	if existing, err := h.repo.FindByID(r.Context(), id); err == nil && existing != nil {
		connName = existing.Name
	}
	if err := h.repo.Delete(r.Context(), id); err != nil {
		if errors.Is(err, ErrHasMigrationHistory) {
			writeConnJSON(w, http.StatusConflict, map[string]string{
				"error": "Koneksi tidak dapat dihapus karena masih memiliki riwayat migration.",
			})
			return
		}
		if errors.Is(err, contract.ErrNotFound) {
			writeConnJSON(w, http.StatusNotFound, map[string]string{"error": "Koneksi database tidak ditemukan."})
			return
		}
		writeConnJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal menghapus koneksi database."})
		return
	}
	if h.poolMgr != nil {
		_ = h.poolMgr.Invalidate(id)
	}
	h.recordConnAudit(r.Context(), "admin.connection.delete", connName, "success", map[string]any{"connection_id": id})
	writeConnJSON(w, http.StatusOK, map[string]string{"message": "Koneksi database berhasil dihapus."})
}

func (h *Handler) APITestConnection(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var req connectionJSONRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeConnJSON(w, http.StatusBadRequest, testConnResponse{Success: false, Message: "Payload JSON tidak valid."})
		return
	}

	engine := contract.Engine(strings.ToLower(strings.TrimSpace(req.Engine)))
	if engine == "" {
		engine = contract.EnginePostgres
	}
	if engine != contract.EnginePostgres && engine != contract.EngineMySQL && engine != contract.EngineMariaDB {
		writeConnJSON(w, http.StatusBadRequest, testConnResponse{Success: false, Message: "Engine database tidak valid."})
		return
	}
	if req.Port < 1 || req.Port > 65535 {
		writeConnJSON(w, http.StatusBadRequest, testConnResponse{Success: false, Message: "Port harus antara 1 dan 65535."})
		return
	}
	host := strings.TrimSpace(req.Host)
	dbName := strings.TrimSpace(req.DatabaseName)
	username := strings.TrimSpace(req.Username)
	if host == "" || dbName == "" || username == "" {
		writeConnJSON(w, http.StatusBadRequest, testConnResponse{Success: false, Message: "Host, nama database, dan username wajib diisi."})
		return
	}
	sslMode := strings.TrimSpace(req.SSLMode)
	if sslMode == "" {
		sslMode = "disable"
	}

	plainPassword := req.Password
	if plainPassword == "" && req.ID != nil && *req.ID > 0 {
		_, storedPass, err := h.repo.FindByIDWithPassword(r.Context(), *req.ID)
		if err != nil {
			writeConnJSON(w, http.StatusNotFound, testConnResponse{Success: false, Message: "Profil koneksi tidak ditemukan."})
			return
		}
		plainPassword = storedPass
	}
	if plainPassword == "" {
		writeConnJSON(w, http.StatusBadRequest, testConnResponse{Success: false, Message: "Password wajib diisi untuk menguji koneksi."})
		return
	}

	conn := &Connection{
		Engine:       engine,
		Host:         host,
		Port:         req.Port,
		DatabaseName: dbName,
		Username:     username,
		SSLMode:      sslMode,
	}
	fn := h.testConnFn
	if fn == nil {
		fn = TestConnection
	}
	if err := fn(r.Context(), "", conn, plainPassword); err != nil {
		writeConnJSON(w, http.StatusBadRequest, testConnResponse{
			Success: false,
			Message: "Koneksi gagal: tidak dapat terhubung ke database dengan parameter yang diberikan.",
		})
		return
	}
	writeConnJSON(w, http.StatusOK, testConnResponse{
		Success: true,
		Message: "Koneksi berhasil dibuat.",
	})
}

func (h *Handler) APIPoolStats(w http.ResponseWriter, _ *http.Request) {
	pools := make([]contract.PoolStat, 0)
	if h.poolMgr != nil {
		pools = h.poolMgr.PoolStats()
	}
	writeConnJSON(w, http.StatusOK, map[string]any{"pools": pools})
}

func (h *Handler) recordConnAudit(ctx context.Context, event, dbName, status string, details map[string]any) {
	actor := "admin"
	if p, ok := contract.PrincipalFrom(ctx); ok && p.Username != "" {
		actor = p.Username
	}
	if h.auditStore != nil {
		_ = h.auditStore.Record(ctx, audit.Entry{
			Actor:    actor,
			Event:    event,
			Database: dbName,
			Status:   status,
			Details:  details,
		})
		return
	}
	if h.obsLogger != nil {
		h.obsLogger.LogActivity(ctx, event, actor, dbName, status)
	}
}

func writeConnJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
