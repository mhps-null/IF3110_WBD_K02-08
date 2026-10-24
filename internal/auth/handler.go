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

type FlashStore interface {
	Set(http.ResponseWriter, string, string)
	Consume(http.ResponseWriter, *http.Request) (message, kind string)
}

type Handler struct {
	users        UserRepository
	sessions     session.Store
	renderer     Renderer
	cookieSecure bool
	sessionCfg   session.Config
	logger       *slog.Logger
	flash        FlashStore
}

func NewHandler(users UserRepository, sessions session.Store, renderer Renderer, cookieSecure bool, sessionCfg session.Config, logger *slog.Logger, flashStores ...FlashStore) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	var flash FlashStore
	if len(flashStores) > 0 {
		flash = flashStores[0]
	}
	return &Handler{users: users, sessions: sessions, renderer: renderer, cookieSecure: cookieSecure, sessionCfg: sessionCfg, logger: logger, flash: flash}
}

func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
	message, kind := h.consumeFlash(w, r)
	h.render(w, r, "login", contract.PageData{CSRFToken: csrfToken(r.Context()), Flash: message, FlashKind: kind})
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		h.renderStatus(w, r, http.StatusBadRequest, "login", contract.PageData{CSRFToken: csrfToken(r.Context()), Flash: "Permintaan tidak valid.", FlashKind: "error"})
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
		h.renderStatus(w, r, http.StatusInternalServerError, "login", contract.PageData{CSRFToken: csrfToken(r.Context()), Flash: "Terjadi kesalahan. Silakan coba lagi.", FlashKind: "error"})
		return
	}
	h.setSessionCookie(w, rawID, info.AbsoluteExpiresAt.Sub(now))
	h.redirectWithFlash(w, r, "/", "success", "Berhasil masuk.")
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		if err := h.sessions.Revoke(r.Context(), c.Value); err != nil {
			h.logger.Error("revoke session failed")
			h.renderStatus(w, r, http.StatusInternalServerError, "error", contract.PageData{CSRFToken: csrfToken(r.Context()), Flash: "Logout gagal diproses.", FlashKind: "error"})
			return
		}
	}
	h.clearSessionCookie(w)
	h.redirectWithFlash(w, r, "/login", "success", "Berhasil keluar.")
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
			if errors.Is(err, session.ErrInvalid) {
				h.clearSessionCookie(w)
				h.redirectWithFlash(w, r, "/login", "error", "Sesi tidak valid atau telah berakhir. Silakan masuk kembali.")
			} else {
				// A metadata database failure is not proof that the session is invalid.
				// Preserve the cookie and fail temporarily instead of forcing re-login.
				h.logger.Error("load session failed")
				h.serviceUnavailable(w, r, "Layanan sementara tidak tersedia. Silakan coba lagi.")
			}
			return
		}

		// Rotasi token tidak memperpanjang absolute expiration. Jika request lain
		// telah merotasi token yang sama, grace period tetap memungkinkan request ini.
		// Rotasi dilakukan ketika interval tercapai atau versi hak akses user berubah.
		// Rotasi tidak mengubah absolute_expires_at.
		needsRotation := info.PrivilegeChanged || now.Sub(info.RotatedAt) >= h.sessionCfg.RotateInterval
		if info.PrivilegeChanged && !info.IsCurrentID {
			// The presented token is the previous token from a concurrent rotation,
			// but its privilege snapshot is stale again. The raw current token cannot
			// be reconstructed from its stored hash, so do not serve this request.
			h.logger.Warn("stale session privilege version on previous token")
			h.serviceUnavailable(w, r, "Sesi perlu diperbarui. Silakan coba lagi.")
			return
		}
		if info.IsCurrentID && needsRotation {
			newID, newInfo, rotateErr := h.sessions.Rotate(r.Context(), cookie.Value, now)
			if rotateErr == nil {
				info = newInfo
				h.setSessionCookie(w, newID, info.AbsoluteExpiresAt.Sub(now))
			} else {
				// Another concurrent request may already have rotated this ID. In
				// that case the old ID remains usable only during the configured grace
				// period and Load reports IsCurrentID=false. If this ID is still current
				// and rotation remains necessary, fail closed instead of serving a
				// request with a session that should have been rotated.
				latest, loadErr := h.sessions.Load(r.Context(), cookie.Value, now)
				if loadErr != nil {
					if errors.Is(loadErr, session.ErrInvalid) {
						h.clearSessionCookie(w)
						h.redirectWithFlash(w, r, "/login", "error", "Sesi tidak valid atau telah berakhir. Silakan masuk kembali.")
					} else {
						h.logger.Error("reload session after failed rotation")
						h.serviceUnavailable(w, r, "Layanan sementara tidak tersedia. Silakan coba lagi.")
					}
					return
				}
				if latest.PrivilegeChanged || (latest.IsCurrentID && now.Sub(latest.RotatedAt) >= h.sessionCfg.RotateInterval) {
					h.logger.Error("required session rotation failed")
					h.serviceUnavailable(w, r, "Layanan sementara tidak tersedia. Silakan coba lagi.")
					return
				}
				info = latest
			}
		}

		user, err := h.users.FindByID(r.Context(), info.UserID)
		if err != nil && !errors.Is(err, ErrUserNotFound) {
			// A transient metadata failure must not revoke an otherwise valid session.
			h.logger.Error("load session user failed")
			h.serviceUnavailable(w, r, "Layanan sementara tidak tersedia. Silakan coba lagi.")
			return
		}
		if errors.Is(err, ErrUserNotFound) || !user.IsActive {
			_ = h.sessions.Revoke(r.Context(), cookie.Value)
			h.clearSessionCookie(w)
			h.redirectWithFlash(w, r, "/login", "error", "Sesi tidak valid atau telah berakhir. Silakan masuk kembali.")
			return
		}
		principal := contract.Principal{UserID: user.ID, Username: user.Username, IsPlatformAdmin: user.IsPlatformAdmin}
		r = r.WithContext(contract.WithPrincipal(r.Context(), principal))
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) RenderHome(w http.ResponseWriter, r *http.Request) {
	p, _ := contract.PrincipalFrom(r.Context())
	message, kind := h.consumeFlash(w, r)
	h.render(w, r, "home", contract.PageData{Principal: p, CSRFToken: csrfToken(r.Context()), Flash: message, FlashKind: kind})
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, name string, data contract.PageData) {
	h.renderStatus(w, r, http.StatusOK, name, data)
}

func (h *Handler) renderStatus(w http.ResponseWriter, _ *http.Request, status int, name string, data contract.PageData) {
	// Render into a buffer first so a template error cannot leave a partial page
	// or commit the intended status before a safe fallback response is ready.
	buffered := newBufferedResponseWriter()
	if err := h.renderer.Render(buffered, name, data); err != nil {
		h.logger.Error("render template failed", "template", name)
		if name != "error" {
			fallback := newBufferedResponseWriter()
			fallbackData := contract.PageData{
				CSRFToken: data.CSRFToken,
				Flash:     "Terjadi kesalahan. Silakan coba lagi.",
				FlashKind: "error",
			}
			if fallbackErr := h.renderer.Render(fallback, "error", fallbackData); fallbackErr == nil {
				writeBufferedResponse(w, http.StatusInternalServerError, fallback)
				return
			}
		}
		// Last-resort response is generic and does not expose the underlying error.
		http.Error(w, "Terjadi kesalahan pada server.", http.StatusInternalServerError)
		return
	}
	writeBufferedResponse(w, status, buffered)
}

func (h *Handler) serviceUnavailable(w http.ResponseWriter, r *http.Request, message string) {
	h.renderStatus(w, r, http.StatusServiceUnavailable, "error", contract.PageData{
		CSRFToken: csrfToken(r.Context()),
		Flash:     message,
		FlashKind: "error",
	})
}

func (h *Handler) loginFailed(w http.ResponseWriter, r *http.Request, status int) {
	h.renderStatus(w, r, status, "login", contract.PageData{CSRFToken: csrfToken(r.Context()), Flash: "Username atau password salah.", FlashKind: "error"})
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

func (h *Handler) redirectWithFlash(w http.ResponseWriter, r *http.Request, location, kind, message string) {
	if h.flash != nil {
		h.flash.Set(w, kind, message)
	}
	h.redirect(w, r, location)
}

func (h *Handler) consumeFlash(w http.ResponseWriter, r *http.Request) (message, kind string) {
	if h.flash == nil {
		return "", ""
	}
	return h.flash.Consume(w, r)
}

func csrfToken(ctx context.Context) string {
	token, _ := contract.CSRFTokenFrom(ctx)
	return token
}
