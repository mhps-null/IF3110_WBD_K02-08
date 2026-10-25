package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/password"
)

type usersPageData struct {
	Users []User
}

func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.ListUsers(r.Context())
	if err != nil {
		h.logger.Error("list users failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	flash := ""
	switch r.URL.Query().Get("flash") {
	case "created":
		flash = "User berhasil dibuat."
	case "deactivated":
		flash = "User berhasil dinonaktifkan dan seluruh session-nya dicabut."
	case "activated":
		flash = "User berhasil diaktifkan. Session lama tetap tidak berlaku; user harus login ulang."
	case "password-reset":
		flash = "Password berhasil direset dan seluruh session user dicabut."
	case "admin-updated":
		flash = "Hak Platform Admin berhasil diperbarui dan seluruh session user dicabut."
	case "error":
		flash = "Operasi gagal. Silakan coba lagi."
	}

	h.render(w, r, "admin/users.html", contract.PageData{
		Flash: flash,
		Data:  usersPageData{Users: users},
	})
}

func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/users?flash=error", http.StatusSeeOther)
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	plain := r.FormValue("password")
	if username == "" || len(plain) < 8 || len(username) > 100 {
		http.Redirect(w, r, "/admin/users?flash=error", http.StatusSeeOther)
		return
	}

	hash, err := password.Hash(plain)
	if err != nil {
		h.logger.Error("hash password failed")
		http.Redirect(w, r, "/admin/users?flash=error", http.StatusSeeOther)
		return
	}

	if _, err := h.store.CreateUser(r.Context(), username, hash); err != nil {
		if errors.Is(err, ErrDuplicateUsername) {
			http.Redirect(w, r, "/admin/users?flash=error", http.StatusSeeOther)
			return
		}

		h.logger.Error("create user failed")
		http.Redirect(w, r, "/admin/users?flash=error", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/users?flash=created", http.StatusSeeOther)
}

func (h *Handler) DeactivateUser(w http.ResponseWriter, r *http.Request) {
	targetID, ok := parseID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}

	p, _ := contract.PrincipalFrom(r.Context())
	if p.UserID == targetID {
		http.Redirect(w, r, "/admin/users?flash=error", http.StatusSeeOther)
		return
	}

	if err := h.store.DeactivateUser(r.Context(), targetID); err != nil {
		if errors.Is(err, ErrUserNotFound) {
			http.NotFound(w, r)
			return
		}

		h.logger.Error("deactivate user failed", "user_id", targetID)
		http.Redirect(w, r, "/admin/users?flash=error", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/users?flash=deactivated", http.StatusSeeOther)
}

func (h *Handler) ActivateUser(w http.ResponseWriter, r *http.Request) {
	targetID, ok := parseID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}

	if err := h.store.ActivateUser(r.Context(), targetID); err != nil {
		if errors.Is(err, ErrUserNotFound) {
			http.NotFound(w, r)
			return
		}

		h.logger.Error("activate user failed")
		redirect(w, r, "/admin/users?flash=error")
		return
	}

	redirect(w, r, "/admin/users?flash=activated")
}

func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	targetID, ok := parseID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/users?flash=error", http.StatusSeeOther)
		return
	}

	plain := r.FormValue("password")
	if len(plain) < 8 {
		http.Redirect(w, r, "/admin/users?flash=error", http.StatusSeeOther)
		return
	}

	hash, err := password.Hash(plain)
	if err != nil {
		h.logger.Error("hash password failed")
		http.Redirect(w, r, "/admin/users?flash=error", http.StatusSeeOther)
		return
	}

	if err := h.store.ResetPassword(r.Context(), targetID, hash); err != nil {
		if errors.Is(err, ErrUserNotFound) {
			http.NotFound(w, r)
			return
		}

		h.logger.Error("reset password failed", "user_id", targetID)
		http.Redirect(w, r, "/admin/users?flash=error", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/users?flash=password-reset", http.StatusSeeOther)
}

func parseID(r *http.Request, name string) (int64, bool) {
	v := r.PathValue(name)
	id, err := strconv.ParseInt(v, 10, 64)
	return id, err == nil && id > 0
}

func (h *Handler) ToggleAdmin(w http.ResponseWriter, r *http.Request) {
	targetID, ok := parseID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		redirect(w, r, "/admin/users?flash=error")
		return
	}

	values := r.PostForm["is_admin"]
	if len(values) != 1 || (values[0] != "true" && values[0] != "false") {
		redirect(w, r, "/admin/users?flash=error")
		return
	}
	isAdmin := values[0] == "true"

	p, _ := contract.PrincipalFrom(r.Context())
	if p.UserID == targetID && !isAdmin {
		redirect(w, r, "/admin/users?flash=error")
		return
	}

	if err := h.store.TogglePlatformAdmin(r.Context(), targetID, isAdmin); err != nil {
		if errors.Is(err, ErrUserNotFound) {
			http.NotFound(w, r)
			return
		}
		h.logger.Error("toggle platform admin failed", "user_id", targetID, "error", err)
		redirect(w, r, "/admin/users?flash=error")
		return
	}
	redirect(w, r, "/admin/users?flash=admin-updated")
}
