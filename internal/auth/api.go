package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/audit"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/password"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/session"
)

type loginJSONRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type apiAccessibleDB struct {
	DBID       int64                `json:"db_id"`
	Name       string               `json:"name"`
	Engine     contract.Engine      `json:"engine,omitempty"`
	Level      contract.AccessLevel `json:"level"`
	LevelLabel string               `json:"level_label"`
}

type apiAuthResponse struct {
	User      contract.Principal `json:"user"`
	CSRFToken string             `json:"csrf_token,omitempty"`
}

type apiMeResponse struct {
	User                contract.Principal             `json:"user"`
	Groups              []contract.UserGroupMembership `json:"groups"`
	AccessibleDatabases []apiAccessibleDB              `json:"accessible_databases"`
	CSRFToken           string                         `json:"csrf_token,omitempty"`
}

// RegisterAPIRoutes mounts JSON authentication, session, CSRF, and profile endpoints for the SPA.
func (h *Handler) RegisterAPIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/csrf", h.APICSRF)
	mux.HandleFunc("POST /api/auth/login", h.APILogin)
	mux.Handle("POST /api/auth/logout", h.Authenticate(http.HandlerFunc(h.APILogout)))
	mux.Handle("GET /api/auth/me", h.Authenticate(http.HandlerFunc(h.APIMe)))
}

// APICSRF returns the current CSRF token associated with the HttpOnly CSRF cookie.
func (h *Handler) APICSRF(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"csrf_token": csrfToken(r.Context()),
	})
}

// APILogin authenticates a user via JSON and sets an opaque HttpOnly session cookie.
// Session identifiers, passwords, and hashes are never returned in response bodies, URLs, or logs.
func (h *Handler) APILogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var req loginJSONRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Payload JSON tidak valid."})
		return
	}

	username := strings.TrimSpace(req.Username)
	plainPassword := req.Password
	if username == "" || plainPassword == "" {
		h.recordAuthAudit(r.Context(), "auth.login", username, "failed", map[string]any{"reason": "missing_credentials"})
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Username atau password salah."})
		return
	}

	user, err := h.users.FindByUsername(r.Context(), username)
	if err != nil {
		if !errors.Is(err, ErrUserNotFound) {
			h.logger.Error("api login user lookup failed")
		}
		h.recordAuthAudit(r.Context(), "auth.login", username, "failed", map[string]any{"reason": "invalid_credentials"})
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Username atau password salah."})
		return
	}
	if !user.IsActive || !password.Verify(user.PasswordHash, plainPassword) {
		h.recordAuthAudit(r.Context(), "auth.login", user.Username, "failed", map[string]any{"reason": "invalid_or_inactive"})
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Username atau password salah."})
		return
	}

	now := time.Now().UTC()
	rawID, info, err := h.sessions.CreateVerified(r.Context(), user.ID, user.PasswordHash, now)
	if err != nil {
		if errors.Is(err, session.ErrCredentialsChanged) {
			h.recordAuthAudit(r.Context(), "auth.login", user.Username, "failed", map[string]any{"reason": "credentials_changed"})
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Username atau password salah."})
			return
		}
		h.logger.Error("api create session failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Terjadi kesalahan. Silakan coba lagi."})
		return
	}

	h.setSessionCookie(w, rawID, info.AbsoluteExpiresAt.Sub(now))
	h.recordAuthAudit(r.Context(), "auth.login", user.Username, "success", map[string]any{
		"user_id":           user.ID,
		"is_platform_admin": user.IsPlatformAdmin,
	})
	writeJSON(w, http.StatusOK, apiAuthResponse{
		User: contract.Principal{
			UserID:          user.ID,
			Username:        user.Username,
			IsPlatformAdmin: user.IsPlatformAdmin,
		},
		CSRFToken: csrfToken(r.Context()),
	})
}

// APILogout revokes the current backend session and clears the session cookie.
func (h *Handler) APILogout(w http.ResponseWriter, r *http.Request) {
	actor := ""
	if p, ok := contract.PrincipalFrom(r.Context()); ok {
		actor = p.Username
	}
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		if err := h.sessions.Revoke(r.Context(), c.Value); err != nil {
			h.logger.Error("api revoke session failed")
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Logout gagal diproses."})
			return
		}
	}
	h.clearSessionCookie(w)
	h.recordAuthAudit(r.Context(), "auth.logout", actor, "success", nil)
	writeJSON(w, http.StatusOK, map[string]string{"message": "Berhasil keluar."})
}

// APIMe returns the authenticated principal, group memberships (with leader roles),
// accessible databases, and the current CSRF token for the SPA.
func (h *Handler) APIMe(w http.ResponseWriter, r *http.Request) {
	principal, ok := contract.PrincipalFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Autentikasi diperlukan."})
		return
	}

	groups := make([]contract.UserGroupMembership, 0)
	if h.groupInspector != nil {
		loaded, err := h.groupInspector.ListUserGroups(r.Context(), principal.UserID)
		if err != nil {
			h.logger.Error("api me list user groups failed")
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memuat keanggotaan group."})
			return
		}
		if loaded != nil {
			groups = loaded
		}
	}

	accessible := make([]apiAccessibleDB, 0)
	if h.accessChecker != nil {
		dbs, err := h.accessChecker.ListAccessible(r.Context(), principal.UserID)
		if err != nil {
			h.logger.Error("api me list accessible databases failed")
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Gagal memuat daftar database."})
			return
		}
		for _, db := range dbs {
			accessible = append(accessible, apiAccessibleDB{
				DBID:       db.DBID,
				Name:       db.Name,
				Engine:     db.Engine,
				Level:      db.Level,
				LevelLabel: db.Level.Label(),
			})
		}
	}

	writeJSON(w, http.StatusOK, apiMeResponse{
		User:                principal,
		Groups:              groups,
		AccessibleDatabases: accessible,
		CSRFToken:           csrfToken(r.Context()),
	})
}

func (h *Handler) recordAuthAudit(ctx context.Context, event, actor, status string, details map[string]any) {
	if actor == "" {
		actor = "anonymous"
	}
	if h.auditStore != nil {
		if err := h.auditStore.Record(ctx, audit.Entry{
			Actor:   actor,
			Event:   event,
			Status:  status,
			Details: details,
		}); err != nil {
			h.logger.Warn("failed to persist auth audit event", "event", event)
		}
		return
	}
	if h.obsLogger != nil {
		h.obsLogger.LogActivity(ctx, event, actor, "", status)
	}
}
