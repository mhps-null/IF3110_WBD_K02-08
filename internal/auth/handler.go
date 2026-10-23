package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/example/if3110-milestone1/internal/contract"
	"github.com/example/if3110-milestone1/internal/password"
	"github.com/example/if3110-milestone1/internal/session"
)

const sessionCookieName = "session_id"

// Renderer sengaja sederhana agar auth tidak bergantung pada implementasi web/template.
type Renderer interface {
	Render(w http.ResponseWriter, name string, data contract.PageData) error
}

type Handler struct {
	users        UserRepository
	sessions     session.Store
	renderer     Renderer
	cookieSecure bool
	sessionCfg   session.Config
	logger       *slog.Logger
}

func NewHandler(users UserRepository, sessions session.Store, renderer Renderer, cookieSecure bool, sessionCfg session.Config, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{users: users, sessions: sessions, renderer: renderer, cookieSecure: cookieSecure, sessionCfg: sessionCfg, logger: logger}
}

func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "login", contract.PageData{CSRFToken: csrfToken(r.Context())})
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		h.renderStatus(w, r, http.StatusBadRequest, "login", contract.PageData{CSRFToken: csrfToken(r.Context()), Flash: "Permintaan tidak valid."})
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	plainPassword := r.FormValue("password")
	if username == "" || plainPassword == "" {
		h.loginFailed(w, r, http.StatusUnauthorized)
		return
	}

	user, err := h.users.FindByUsername(r.Context(), username)
	if err != nil {
		if !errors.Is(err, ErrUserNotFound) {
			h.logger.Error("login user lookup failed")
		}
		h.loginFailed(w, r, http.StatusUnauthorized)
		return
	}
	if !user.IsActive || !password.Verify(user.PasswordHash, plainPassword) {
		h.loginFailed(w, r, http.StatusUnauthorized)
		return
	}

	now := time.Now().UTC()
	rawID, info, err := h.sessions.Create(r.Context(), user.ID, now)
	if err != nil {
		h.logger.Error("create session failed")
		h.renderStatus(w, r, http.StatusInternalServerError, "login", contract.PageData{CSRFToken: csrfToken(r.Context()), Flash: "Terjadi kesalahan. Silakan coba lagi."})
		return
	}
	h.setSessionCookie(w, rawID, info.AbsoluteExpiresAt.Sub(now))
	h.redirect(w, r, "/")
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		if err := h.sessions.Revoke(r.Context(), c.Value); err != nil {
			h.logger.Error("revoke session failed")
			h.renderStatus(w, r, http.StatusInternalServerError, "home", contract.PageData{CSRFToken: csrfToken(r.Context()), Flash: "Logout gagal diproses."})
			return
		}
	}
	h.clearSessionCookie(w)
	h.redirect(w, r, "/login")
}

// Authenticate memvalidasi session pada backend setiap request fitur terlindungi.
func (h *Handler) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			h.clearSessionCookie(w)
			h.redirect(w, r, "/login")
			return
		}
		now := time.Now().UTC()
		info, err := h.sessions.Load(r.Context(), cookie.Value, now)
		if err != nil {
			h.clearSessionCookie(w)
			h.redirect(w, r, "/login")
			return
		}

		// Rotasi token tidak memperpanjang absolute expiration. Jika request lain
		// telah merotasi token yang sama, grace period tetap memungkinkan request ini.
		// Rotasi dilakukan ketika interval tercapai atau versi hak akses user berubah.
		// Rotasi tidak mengubah absolute_expires_at.
		if info.IsCurrentID && (info.PrivilegeChanged || now.Sub(info.RotatedAt) >= h.sessionCfg.RotateInterval) {
			newID, newInfo, rotateErr := h.sessions.Rotate(r.Context(), cookie.Value, now)
			if rotateErr == nil {
				info = newInfo
				h.setSessionCookie(w, newID, info.AbsoluteExpiresAt.Sub(now))
			} else {
				latest, loadErr := h.sessions.Load(r.Context(), cookie.Value, now)
				if loadErr != nil {
					h.clearSessionCookie(w)
					h.redirect(w, r, "/login")
					return
				}
				info = latest
			}
		}

		user, err := h.users.FindByID(r.Context(), info.UserID)
		if err != nil || !user.IsActive {
			if err != nil && !errors.Is(err, ErrUserNotFound) {
				h.logger.Error("load session user failed")
			}
			_ = h.sessions.Revoke(r.Context(), cookie.Value)
			h.clearSessionCookie(w)
			h.redirect(w, r, "/login")
			return
		}
		principal := contract.Principal{UserID: user.ID, Username: user.Username, IsPlatformAdmin: user.IsPlatformAdmin}
		r = r.WithContext(contract.WithPrincipal(r.Context(), principal))
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) RenderHome(w http.ResponseWriter, r *http.Request) {
	p, _ := contract.PrincipalFrom(r.Context())
	h.render(w, r, "home", contract.PageData{Principal: p, CSRFToken: csrfToken(r.Context())})
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, name string, data contract.PageData) {
	if err := h.renderer.Render(w, name, data); err != nil {
		h.logger.Error("render template failed", "template", name)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *Handler) renderStatus(w http.ResponseWriter, r *http.Request, status int, name string, data contract.PageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	h.render(w, r, name, data)
}

func (h *Handler) loginFailed(w http.ResponseWriter, r *http.Request, status int) {
	h.renderStatus(w, r, status, "login", contract.PageData{CSRFToken: csrfToken(r.Context()), Flash: "Username atau password salah."})
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, value string, remaining time.Duration) {
	seconds := int(remaining.Seconds())
	if seconds < 0 {
		seconds = 0
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: value, Path: "/", MaxAge: seconds,
		Expires: time.Now().Add(remaining), HttpOnly: true, Secure: h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		Expires: time.Unix(1, 0), HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteLaxMode})
}

func (h *Handler) redirect(w http.ResponseWriter, r *http.Request, location string) {
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func csrfToken(ctx context.Context) string {
	token, _ := contract.CSRFTokenFrom(ctx)
	return token
}
