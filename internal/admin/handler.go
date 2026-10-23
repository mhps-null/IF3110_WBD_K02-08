package admin

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/example/if3110-milestone1/internal/contract"
)

type Handler struct {
	store *Store
	revoker contract.SessionRevoker
	renderer Renderer
	logger *slog.Logger
}

func NewHandler(store *Store, revoker contract.SessionRevoker, renderer Renderer, logger *slog.Logger) *Handler {
	if renderer == nil {
		renderer = NewTemplateRenderer("web/templates")
	}

	if logger == nil {
		logger = slog.Default()
	}

	return &Handler{store: store, revoker: revoker, renderer: renderer, logger: logger}
}

// routes mendaftarkan route admin
func (h *Handler) Routes(mux *http.ServeMux, authenticate, adminGuard func(http.Handler) http.Handler) {
	w := func(handler http.HandlerFunc) http.Handler {
		return authenticate(adminGuard(handler))
	}

	// Users
	mux.Handle("GET /admin/users", w(h.ListUsers))
	mux.Handle("POST /admin/users", w(h.CreateUser))
	mux.Handle("POST /admin/users/{id}/deactivate", w(h.DeactivateUser))
	mux.Handle("POST /admin/users/{id}/activate", w(h.ActivateUser))
	mux.Handle("POST /admin/users/{id}/reset-password", w(h.ResetPassword))

	// Groups
	mux.Handle("GET /admin/groups", w(h.ListGroups))
	mux.Handle("POST /admin/groups", w(h.CreateGroup))
	mux.Handle("GET /admin/groups/{id}", w(h.ShowGroup))
	mux.Handle("POST /admin/groups/{id}/delete", w(h.DeleteGroup))
	mux.Handle("POST /admin/groups/{id}/members", w(h.AddMember))
	mux.Handle("POST /admin/groups/{id}/members/{uid}/remove", w(h.RemoveMember))

	// Access Matrix
	mux.Handle("GET /admin/access", w(h.ShowMatrix))
	mux.Handle("POST /admin/access", w(h.SetAccess))
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, templatePath string, data contract.PageData) {
	p, _ := contract.PrincipalFrom(r.Context())
	data.Principal = p
	data.CSRFToken = csrfToken(r.Context())
	
	if err := h.renderer.Render(w, templatePath, data); err != nil {
		h.logger.Error("render admin template failed", "template", templatePath)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func csrfToken(ctx context.Context) string {
	token, _ := contract.CSRFTokenFrom(ctx)
	return token
}
