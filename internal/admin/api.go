package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/audit"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/password"
)

// AdminStore defines persistence operations required by the admin JSON API handlers.
type AdminStore interface {
	ListUsers(ctx context.Context) ([]User, error)
	CreateUser(ctx context.Context, username, passwordHash string) (int64, error)
	DeactivateUser(ctx context.Context, userID int64) error
	ActivateUser(ctx context.Context, userID int64) error
	ResetPassword(ctx context.Context, userID int64, passwordHash string) error
	TogglePlatformAdmin(ctx context.Context, userID int64, isAdmin bool) error

	ListGroups(ctx context.Context) ([]Group, error)
	CreateGroup(ctx context.Context, name, description string) (int64, error)
	GetGroup(ctx context.Context, id int64) (Group, error)
	UpdateGroup(ctx context.Context, id int64, name, description string) error
	DeleteGroup(ctx context.Context, id int64) error
	SetGroupLeader(ctx context.Context, groupID int64, leaderUserID *int64) error

	ListGroupMembers(ctx context.Context, groupID int64) ([]User, error)
	AddMember(ctx context.Context, groupID, userID int64) error
	RemoveMember(ctx context.Context, groupID, userID int64) error

	ListDatabases(ctx context.Context) ([]Database, error)
	ListGroupAccess(ctx context.Context) ([]GroupAccess, error)
	SetGroupAccessLevels(ctx context.Context, groupID int64, entries map[int64]int) error
}

// NewAPIHandler creates a Handler backed by an AdminStore (primarily for unit tests).
func NewAPIHandler(store AdminStore, inspector contract.GroupAccessInspector, checker contract.AccessChecker) *Handler {
	return &Handler{
		apiStore:       store,
		groupInspector: inspector,
		accessChecker:  checker,
		logger:         nil,
	}
}

// RegisterAPIRoutes registers all JSON admin endpoints and database approver inspection APIs.
func (h *Handler) RegisterAPIRoutes(mux *http.ServeMux, authenticate, adminGuard func(http.Handler) http.Handler) {
	wrapAdmin := func(fn http.HandlerFunc) http.Handler {
		return authenticate(adminGuard(fn))
	}

	// Users
	mux.Handle("GET /api/admin/users", wrapAdmin(h.APIListUsers))
	mux.Handle("POST /api/admin/users", wrapAdmin(h.APICreateUser))
	mux.Handle("POST /api/admin/users/{id}/deactivate", wrapAdmin(h.APIDeactivateUser))
	mux.Handle("POST /api/admin/users/{id}/activate", wrapAdmin(h.APIActivateUser))
	mux.Handle("POST /api/admin/users/{id}/reset-password", wrapAdmin(h.APIResetPassword))
	mux.Handle("POST /api/admin/users/{id}/toggle-admin", wrapAdmin(h.APIToggleAdmin))

	// Groups, membership, and leader management
	mux.Handle("GET /api/admin/groups", wrapAdmin(h.APIListGroups))
	mux.Handle("POST /api/admin/groups", wrapAdmin(h.APICreateGroup))
	mux.Handle("GET /api/admin/groups/{id}", wrapAdmin(h.APIGetGroup))
	mux.Handle("PUT /api/admin/groups/{id}", wrapAdmin(h.APIUpdateGroup))
	mux.Handle("DELETE /api/admin/groups/{id}", wrapAdmin(h.APIDeleteGroup))
	mux.Handle("POST /api/admin/groups/{id}/members", wrapAdmin(h.APIAddMember))
	mux.Handle("DELETE /api/admin/groups/{id}/members/{uid}", wrapAdmin(h.APIRemoveMember))
	mux.Handle("PUT /api/admin/groups/{id}/leader", wrapAdmin(h.APISetGroupLeader))

	// Access Matrix
	mux.Handle("GET /api/admin/access", wrapAdmin(h.APIGetAccessMatrix))
	mux.Handle("PUT /api/admin/access", wrapAdmin(h.APISetAccess))

	// Approver / group-access lookup per database (used by Orang 3 approval workflows & SPA)
	mux.Handle("GET /api/databases/{id}/access-groups", authenticate(http.HandlerFunc(h.APIDatabaseAccessGroups)))
}

func (h *Handler) APIListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.apiStore.ListUsers(r.Context())
	if err != nil {
		h.logErr("api list users failed")
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memuat daftar user."})
		return
	}
	if users == nil {
		users = []User{}
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (h *Handler) APICreateUser(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "Payload JSON tidak valid."})
		return
	}
	username := strings.TrimSpace(req.Username)
	if username == "" || len(username) > 100 {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "Username wajib diisi (maksimal 100 karakter)."})
		return
	}
	if len(req.Password) < 8 {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "Password minimal 8 karakter."})
		return
	}
	hash, err := password.Hash(req.Password)
	if err != nil {
		h.logErr("api hash password failed")
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memproses password."})
		return
	}
	id, err := h.apiStore.CreateUser(r.Context(), username, hash)
	if err != nil {
		if errors.Is(err, ErrDuplicateUsername) {
			writeAdminJSON(w, http.StatusConflict, map[string]string{"error": "Username sudah digunakan."})
			return
		}
		h.logErr("api create user failed")
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal membuat user."})
		return
	}
	h.recordAdminAudit(r.Context(), "admin.user.create", "", "success", map[string]any{
		"target_user_id":  id,
		"target_username": username,
	})
	writeAdminJSON(w, http.StatusCreated, map[string]any{
		"id":       id,
		"username": username,
		"message":  "User berhasil dibuat.",
	})
}

func (h *Handler) APIDeactivateUser(w http.ResponseWriter, r *http.Request) {
	targetID, ok := parseID(r, "id")
	if !ok {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "ID user tidak valid."})
		return
	}
	p, _ := contract.PrincipalFrom(r.Context())
	if p.UserID == targetID {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "Tidak dapat menonaktifkan akun sendiri."})
		return
	}
	if err := h.apiStore.DeactivateUser(r.Context(), targetID); err != nil {
		if errors.Is(err, ErrUserNotFound) {
			writeAdminJSON(w, http.StatusNotFound, map[string]string{"error": "User tidak ditemukan."})
			return
		}
		h.logErr("api deactivate user failed")
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal menonaktifkan user."})
		return
	}
	h.recordAdminAudit(r.Context(), "admin.user.deactivate", "", "success", map[string]any{"target_user_id": targetID})
	writeAdminJSON(w, http.StatusOK, map[string]string{"message": "User berhasil dinonaktifkan dan seluruh session-nya dicabut."})
}

func (h *Handler) APIActivateUser(w http.ResponseWriter, r *http.Request) {
	targetID, ok := parseID(r, "id")
	if !ok {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "ID user tidak valid."})
		return
	}
	if err := h.apiStore.ActivateUser(r.Context(), targetID); err != nil {
		if errors.Is(err, ErrUserNotFound) {
			writeAdminJSON(w, http.StatusNotFound, map[string]string{"error": "User tidak ditemukan."})
			return
		}
		h.logErr("api activate user failed")
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal mengaktifkan user."})
		return
	}
	h.recordAdminAudit(r.Context(), "admin.user.activate", "", "success", map[string]any{"target_user_id": targetID})
	writeAdminJSON(w, http.StatusOK, map[string]string{"message": "User berhasil diaktifkan."})
}

func (h *Handler) APIResetPassword(w http.ResponseWriter, r *http.Request) {
	targetID, ok := parseID(r, "id")
	if !ok {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "ID user tidak valid."})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "Payload JSON tidak valid."})
		return
	}
	if len(req.Password) < 8 {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "Password baru minimal 8 karakter."})
		return
	}
	hash, err := password.Hash(req.Password)
	if err != nil {
		h.logErr("api hash reset password failed")
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memproses password."})
		return
	}
	if err := h.apiStore.ResetPassword(r.Context(), targetID, hash); err != nil {
		if errors.Is(err, ErrUserNotFound) {
			writeAdminJSON(w, http.StatusNotFound, map[string]string{"error": "User tidak ditemukan."})
			return
		}
		h.logErr("api reset password failed")
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal mereset password."})
		return
	}
	h.recordAdminAudit(r.Context(), "admin.user.reset_password", "", "success", map[string]any{"target_user_id": targetID})
	writeAdminJSON(w, http.StatusOK, map[string]string{"message": "Password berhasil direset dan seluruh session dicabut."})
}

func (h *Handler) APIToggleAdmin(w http.ResponseWriter, r *http.Request) {
	targetID, ok := parseID(r, "id")
	if !ok {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "ID user tidak valid."})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var req struct {
		IsAdmin *bool `json:"is_admin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.IsAdmin == nil {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "Field is_admin (boolean) wajib diisi."})
		return
	}
	p, _ := contract.PrincipalFrom(r.Context())
	if p.UserID == targetID && !*req.IsAdmin {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "Tidak dapat mencabut hak Platform Admin diri sendiri."})
		return
	}
	if err := h.apiStore.TogglePlatformAdmin(r.Context(), targetID, *req.IsAdmin); err != nil {
		if errors.Is(err, ErrUserNotFound) {
			writeAdminJSON(w, http.StatusNotFound, map[string]string{"error": "User tidak ditemukan."})
			return
		}
		h.logErr("api toggle admin failed")
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memperbarui status Platform Admin."})
		return
	}
	h.recordAdminAudit(r.Context(), "admin.user.toggle_admin", "", "success", map[string]any{
		"target_user_id": targetID,
		"is_admin":       *req.IsAdmin,
	})
	writeAdminJSON(w, http.StatusOK, map[string]string{"message": "Hak Platform Admin berhasil diperbarui dan seluruh session dicabut."})
}

func (h *Handler) APIListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.apiStore.ListGroups(r.Context())
	if err != nil {
		h.logErr("api list groups failed")
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memuat daftar group."})
		return
	}
	if groups == nil {
		groups = []Group{}
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

func (h *Handler) APICreateGroup(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "Payload JSON tidak valid."})
		return
	}
	name := strings.TrimSpace(req.Name)
	desc := strings.TrimSpace(req.Description)
	if name == "" || len(name) > 100 {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "Nama group wajib diisi (maksimal 100 karakter)."})
		return
	}
	id, err := h.apiStore.CreateGroup(r.Context(), name, desc)
	if err != nil {
		if errors.Is(err, ErrDuplicateGroupName) {
			writeAdminJSON(w, http.StatusConflict, map[string]string{"error": "Nama group sudah dipakai."})
			return
		}
		h.logErr("api create group failed")
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal membuat group."})
		return
	}
	h.recordAdminAudit(r.Context(), "admin.group.create", "", "success", map[string]any{"group_id": id, "name": name})
	writeAdminJSON(w, http.StatusCreated, map[string]any{"id": id, "name": name, "description": desc, "message": "Group berhasil dibuat."})
}

func (h *Handler) APIGetGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r, "id")
	if !ok {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "ID group tidak valid."})
		return
	}
	g, err := h.apiStore.GetGroup(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrGroupNotFound) {
			writeAdminJSON(w, http.StatusNotFound, map[string]string{"error": "Group tidak ditemukan."})
			return
		}
		h.logErr("api get group failed")
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memuat detail group."})
		return
	}
	members, err := h.apiStore.ListGroupMembers(r.Context(), id)
	if err != nil {
		h.logErr("api list group members failed")
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memuat anggota group."})
		return
	}
	allUsers, err := h.apiStore.ListUsers(r.Context())
	if err != nil {
		h.logErr("api list users for group failed")
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memuat daftar user."})
		return
	}
	if members == nil {
		members = []User{}
	}
	isMember := make(map[int64]bool, len(members))
	for _, m := range members {
		isMember[m.ID] = true
	}
	available := make([]User, 0)
	for _, u := range allUsers {
		if !isMember[u.ID] {
			available = append(available, u)
		}
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"group":           g,
		"members":         members,
		"available_users": available,
	})
}

func (h *Handler) APIUpdateGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r, "id")
	if !ok {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "ID group tidak valid."})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "Payload JSON tidak valid."})
		return
	}
	name := strings.TrimSpace(req.Name)
	desc := strings.TrimSpace(req.Description)
	if name == "" || len(name) > 100 {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "Nama group wajib diisi (maksimal 100 karakter)."})
		return
	}
	if err := h.apiStore.UpdateGroup(r.Context(), id, name, desc); err != nil {
		if errors.Is(err, ErrGroupNotFound) {
			writeAdminJSON(w, http.StatusNotFound, map[string]string{"error": "Group tidak ditemukan."})
			return
		}
		if errors.Is(err, ErrDuplicateGroupName) {
			writeAdminJSON(w, http.StatusConflict, map[string]string{"error": "Nama group sudah dipakai."})
			return
		}
		h.logErr("api update group failed")
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memperbarui group."})
		return
	}
	h.recordAdminAudit(r.Context(), "admin.group.update", "", "success", map[string]any{"group_id": id, "name": name})
	writeAdminJSON(w, http.StatusOK, map[string]string{"message": "Group berhasil diperbarui."})
}

func (h *Handler) APIDeleteGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r, "id")
	if !ok {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "ID group tidak valid."})
		return
	}
	if err := h.apiStore.DeleteGroup(r.Context(), id); err != nil {
		if errors.Is(err, ErrGroupNotFound) {
			writeAdminJSON(w, http.StatusNotFound, map[string]string{"error": "Group tidak ditemukan."})
			return
		}
		h.logErr("api delete group failed")
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal menghapus group."})
		return
	}
	h.recordAdminAudit(r.Context(), "admin.group.delete", "", "success", map[string]any{"group_id": id})
	writeAdminJSON(w, http.StatusOK, map[string]string{"message": "Group berhasil dihapus."})
}

func (h *Handler) APIAddMember(w http.ResponseWriter, r *http.Request) {
	groupID, ok := parseID(r, "id")
	if !ok {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "ID group tidak valid."})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var req struct {
		UserID int64 `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID <= 0 {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id tidak valid."})
		return
	}
	if err := h.apiStore.AddMember(r.Context(), groupID, req.UserID); err != nil {
		h.logErr("api add member failed")
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal menambahkan anggota."})
		return
	}
	h.recordAdminAudit(r.Context(), "admin.group.add_member", "", "success", map[string]any{
		"group_id": groupID,
		"user_id":  req.UserID,
	})
	writeAdminJSON(w, http.StatusOK, map[string]string{"message": "Anggota berhasil ditambahkan."})
}

func (h *Handler) APIRemoveMember(w http.ResponseWriter, r *http.Request) {
	groupID, ok := parseID(r, "id")
	if !ok {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "ID group tidak valid."})
		return
	}
	userID, err := strconv.ParseInt(r.PathValue("uid"), 10, 64)
	if err != nil || userID <= 0 {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "ID user tidak valid."})
		return
	}
	if err := h.apiStore.RemoveMember(r.Context(), groupID, userID); err != nil {
		h.logErr("api remove member failed")
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal menghapus anggota."})
		return
	}
	h.recordAdminAudit(r.Context(), "admin.group.remove_member", "", "success", map[string]any{
		"group_id": groupID,
		"user_id":  userID,
	})
	writeAdminJSON(w, http.StatusOK, map[string]string{"message": "Anggota berhasil dihapus."})
}

func (h *Handler) APISetGroupLeader(w http.ResponseWriter, r *http.Request) {
	groupID, ok := parseID(r, "id")
	if !ok {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "ID group tidak valid."})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var req struct {
		LeaderUserID *int64 `json:"leader_user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "Payload JSON tidak valid."})
		return
	}
	if req.LeaderUserID != nil && *req.LeaderUserID <= 0 {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "leader_user_id tidak valid."})
		return
	}
	if err := h.apiStore.SetGroupLeader(r.Context(), groupID, req.LeaderUserID); err != nil {
		switch {
		case errors.Is(err, ErrGroupNotFound):
			writeAdminJSON(w, http.StatusNotFound, map[string]string{"error": "Group tidak ditemukan."})
		case errors.Is(err, ErrUserNotFound):
			writeAdminJSON(w, http.StatusNotFound, map[string]string{"error": "User tidak ditemukan."})
		case errors.Is(err, ErrLeaderNotMember):
			writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "Leader harus merupakan anggota group."})
		case errors.Is(err, ErrLeaderInactive):
			writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "Leader harus merupakan user yang aktif."})
		default:
			h.logErr("api set group leader failed")
			writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal menetapkan leader group."})
		}
		return
	}
	h.recordAdminAudit(r.Context(), "admin.group.set_leader", "", "success", map[string]any{
		"group_id":       groupID,
		"leader_user_id": req.LeaderUserID,
	})
	group, err := h.apiStore.GetGroup(r.Context(), groupID)
	if err != nil {
		writeAdminJSON(w, http.StatusOK, map[string]string{"message": "Leader group berhasil diperbarui."})
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"message": "Leader group berhasil diperbarui.",
		"group":   group,
	})
}

type apiAccessMatrixEntry struct {
	DBID           int64   `json:"db_id"`
	GroupID        int64   `json:"group_id"`
	DatabaseName   string  `json:"database_name"`
	GroupName      string  `json:"group_name"`
	Level          int     `json:"level"`
	LevelLabel     string  `json:"level_label"`
	LeaderUserID   *int64  `json:"leader_user_id"`
	LeaderUsername *string `json:"leader_username"`
	HasLeader      bool    `json:"has_leader"`
}

func (h *Handler) APIGetAccessMatrix(w http.ResponseWriter, r *http.Request) {
	dbs, err := h.apiStore.ListDatabases(r.Context())
	if err != nil {
		h.logErr("api access matrix list databases failed")
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memuat daftar database."})
		return
	}
	groups, err := h.apiStore.ListGroups(r.Context())
	if err != nil {
		h.logErr("api access matrix list groups failed")
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memuat daftar group."})
		return
	}
	accessList, err := h.apiStore.ListGroupAccess(r.Context())
	if err != nil {
		h.logErr("api access matrix list access failed")
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memuat matriks akses."})
		return
	}
	if dbs == nil {
		dbs = []Database{}
	}
	if groups == nil {
		groups = []Group{}
	}

	groupByID := make(map[int64]Group, len(groups))
	for _, g := range groups {
		groupByID[g.ID] = g
	}
	dbNameByID := make(map[int64]string, len(dbs))
	for _, d := range dbs {
		dbNameByID[d.ID] = d.Name
	}

	entries := make([]apiAccessMatrixEntry, 0, len(accessList))
	for _, a := range accessList {
		if a.Level == 0 {
			continue
		}
		g, gOK := groupByID[a.GroupID]
		dbName, dbOK := dbNameByID[a.DBID]
		if !gOK || !dbOK {
			continue
		}
		entries = append(entries, apiAccessMatrixEntry{
			DBID:           a.DBID,
			GroupID:        a.GroupID,
			DatabaseName:   dbName,
			GroupName:      g.Name,
			Level:          a.Level,
			LevelLabel:     contract.AccessLevel(a.Level).Label(),
			LeaderUserID:   g.LeaderUserID,
			LeaderUsername: g.LeaderUsername,
			HasLeader:      g.HasLeader,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].DatabaseName != entries[j].DatabaseName {
			return entries[i].DatabaseName < entries[j].DatabaseName
		}
		return entries[i].GroupName < entries[j].GroupName
	})

	writeAdminJSON(w, http.StatusOK, map[string]any{
		"databases":      dbs,
		"groups":         groups,
		"access_entries": entries,
	})
}

func (h *Handler) APISetAccess(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		GroupID int64 `json:"group_id"`
		DBID    int64 `json:"db_id"`
		Level   int   `json:"level"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "Payload JSON tidak valid."})
		return
	}
	if req.GroupID <= 0 || req.DBID <= 0 || req.Level < 0 || req.Level > 2 {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "group_id, db_id, atau level akses tidak valid (0, 1, atau 2)."})
		return
	}
	if err := h.apiStore.SetGroupAccessLevels(r.Context(), req.GroupID, map[int64]int{req.DBID: req.Level}); err != nil {
		if errors.Is(err, ErrGroupNotFound) {
			writeAdminJSON(w, http.StatusNotFound, map[string]string{"error": "Group tidak ditemukan."})
			return
		}
		h.logErr("api set access failed")
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "Gagal memperbarui hak akses database."})
		return
	}
	h.recordAdminAudit(r.Context(), "admin.access.update", "", "success", map[string]any{
		"group_id": req.GroupID,
		"db_id":    req.DBID,
		"level":    req.Level,
	})
	writeAdminJSON(w, http.StatusOK, map[string]string{"message": "Matriks akses berhasil disimpan."})
}

type missingLeaderGroup struct {
	GroupID   int64  `json:"group_id"`
	GroupName string `json:"group_name"`
}

// APIDatabaseAccessGroups returns all groups that have access to database {id} along
// with their access levels, leaders, and any groups missing an active leader.
// Accessible to Platform Admins or users who hold at least View Only access on {id}.
func (h *Handler) APIDatabaseAccessGroups(w http.ResponseWriter, r *http.Request) {
	dbID, ok := parseID(r, "id")
	if !ok {
		writeAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "ID database tidak valid."})
		return
	}
	p, ok := contract.PrincipalFrom(r.Context())
	if !ok {
		writeAdminJSON(w, http.StatusUnauthorized, map[string]string{"error": "Autentikasi diperlukan."})
		return
	}
	if !p.IsPlatformAdmin {
		if h.accessChecker == nil {
			writeAdminJSON(w, http.StatusForbidden, map[string]string{"error": contract.ErrAccessDenied.Error()})
			return
		}
		lvl, err := h.accessChecker.EffectiveLevel(r.Context(), p.UserID, dbID)
		if err != nil {
			h.logErr("api database access groups effective level failed")
			writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memeriksa hak akses."})
			return
		}
		if lvl < contract.AccessViewOnly {
			writeAdminJSON(w, http.StatusForbidden, map[string]string{"error": contract.ErrAccessDenied.Error()})
			return
		}
	}
	if h.groupInspector == nil {
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Inspektor akses group belum dikonfigurasi."})
		return
	}
	groups, err := h.groupInspector.ListGroupsForDatabase(r.Context(), dbID)
	if err != nil {
		h.logErr("api database access groups query failed")
		writeAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memuat daftar group akses database."})
		return
	}
	if groups == nil {
		groups = []contract.DatabaseGroupAccess{}
	}

	missing := make([]missingLeaderGroup, 0)
	for _, g := range groups {
		if !g.HasLeader {
			missing = append(missing, missingLeaderGroup{GroupID: g.GroupID, GroupName: g.GroupName})
		}
	}

	approvalStatus := "ready"
	pendingReason := ""
	if len(missing) > 0 {
		approvalStatus = "pending_leader_assignment"
		pendingReason = "Satu atau lebih group yang memiliki akses ke database ini belum memiliki leader aktif; workflow approval tetap Pending hingga leader ditetapkan."
	}

	writeAdminJSON(w, http.StatusOK, map[string]any{
		"db_id":                 dbID,
		"groups":                groups,
		"missing_leader_groups": missing,
		"has_missing_leaders":   len(missing) > 0,
		"approval_status":       approvalStatus,
		"pending_reason":        pendingReason,
	})
}

func (h *Handler) logErr(msg string) {
	if h.logger != nil {
		h.logger.Error(msg)
	}
}

func (h *Handler) recordAdminAudit(ctx context.Context, event, dbName, status string, details map[string]any) {
	actor := "admin"
	if p, ok := contract.PrincipalFrom(ctx); ok && p.Username != "" {
		actor = p.Username
	}
	if h.auditStore != nil {
		if err := h.auditStore.Record(ctx, audit.Entry{
			Actor:    actor,
			Event:    event,
			Database: dbName,
			Status:   status,
			Details:  details,
		}); err != nil {
			h.logErr("failed to persist admin audit event")
		}
		return
	}
	if h.obsLogger != nil {
		h.obsLogger.LogActivity(ctx, event, actor, dbName, status)
	}
}

func writeAdminJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
