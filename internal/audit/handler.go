package audit

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

// Handler serves JSON API endpoints for viewing platform audit logs in the SPA.
type Handler struct {
	store  Store
	guards contract.Guards
}

func NewHandler(store Store, guards contract.Guards) *Handler {
	return &Handler{store: store, guards: guards}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	h.RegisterRoutesWithAuth(mux, func(next http.Handler) http.Handler { return next })
}

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
		admin = func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p, ok := contract.PrincipalFrom(r.Context())
				if !ok || !p.IsPlatformAdmin {
					http.Error(w, "Access denied: Platform Admin privileges required", http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
			})
		}
	}
	mux.Handle("GET /api/audit-logs", authenticate(admin(http.HandlerFunc(h.List))))
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	pageSize, _ := strconv.Atoi(q.Get("page_size"))

	result, err := h.store.List(r.Context(), Filter{
		Actor:    q.Get("actor"),
		Event:    q.Get("event"),
		Database: q.Get("database"),
		Status:   q.Get("status"),
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to fetch audit logs"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(result)
}
