package access

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

type GuardsConfig struct {
	Checker         contract.AccessChecker
	DBIDFromRequest func(*http.Request) (int64, bool) // kalo nil, ambil {id} dr Go ServeMux
}

func NewGuards(cfg GuardsConfig) contract.Guards {
	if cfg.Checker == nil {
		panic("access: NewGuards requires a non-nil AccessChecker")
	}

	extract := cfg.DBIDFromRequest
	if extract == nil {
		extract = defaultDBIDFromRequest
	}

	g := &guards{checker: cfg.Checker, dbID: extract}
	return contract.Guards{
		Admin:  g.admin,
		ViewDB: g.viewDB,
		EditDB: g.editDB,
	}
}

type guards struct {
	checker contract.AccessChecker
	dbID    func(*http.Request) (int64, bool)
}

func defaultDBIDFromRequest(r *http.Request) (int64, bool) {
	v := r.PathValue("id")
	if v == "" {
		return 0, false
	}

	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}

	return id, true
}

// akses platform, bukan akses db tertentu
func (g *guards) admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := contract.PrincipalFrom(r.Context())

		if !ok {
			denyUnauthenticated(w, r)
			return
		}

		if !p.IsPlatformAdmin {
			denyForbidden(w, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (g *guards) viewDB(next http.Handler) http.Handler {
	return g.requireAction(next, ActionViewSchema)
}

func (g *guards) editDB(next http.Handler) http.Handler {
	return g.requireAction(next, ActionCreateMigration)
}

func (g *guards) requireAction(next http.Handler, action Action) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := contract.PrincipalFrom(r.Context())
		if !ok {
			denyUnauthenticated(w, r)
			return
		}

		dbID, ok := g.dbID(r)
		if !ok {
			writeGuardError(w, r, http.StatusBadRequest, "invalid database id")
			return
		}

		level, err := g.checker.EffectiveLevel(r.Context(), p.UserID, dbID)
		if err != nil {
			log.Printf("access: guard: effective level user=%d db=%d: %v", p.UserID, dbID, err)
			writeGuardError(w, r, http.StatusInternalServerError, "internal error")
			return
		}

		if !Allows(level, action) {
			denyForbidden(w, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// access dicek tiap request supaya perubahan/revoke akses langsung berlaku
func (g *guards) requireDBLevel(next http.Handler, min contract.AccessLevel) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := contract.PrincipalFrom(r.Context())
		if !ok {
			denyUnauthenticated(w, r)
			return
		}

		dbID, ok := g.dbID(r)
		if !ok {
			writeGuardError(w, r, http.StatusBadRequest, "invalid database id")
			return
		}

		level, err := g.checker.EffectiveLevel(r.Context(), p.UserID, dbID)
		if err != nil {
			log.Printf("access: guard: effective level user=%d db=%d: %v", p.UserID, dbID, err)
			writeGuardError(w, r, http.StatusInternalServerError, "internal error")
			return
		}
		if level < min {
			denyForbidden(w, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func denyUnauthenticated(w http.ResponseWriter, r *http.Request) {
	writeGuardError(w, r, http.StatusUnauthorized, "authentication required")
}

func denyForbidden(w http.ResponseWriter, r *http.Request) {
	writeGuardError(w, r, http.StatusForbidden, contract.ErrAccessDenied.Error())
}

func writeGuardError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if r != nil && strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
		return
	}
	http.Error(w, msg, status)
}
