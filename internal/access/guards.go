package access

import (
	"log"
	"net/http"
	"strconv"

	"github.com/example/if3110-milestone1/internal/contract"
)

type GuardsConfig struct {
	Checker contract.AccessChecker
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
		Admin: g.admin,
		ViewDB: g.viewDB,
		EditDB: g.editDB,
	}
}

type guards struct {
	checker contract.AccessChecker
	dbID func(*http.Request) (int64, bool)
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
			denyUnauthenticated(w)
			return
		}

		if !p.IsPlatformAdmin {
			denyForbidden(w)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (g *guards) viewDB(next http.Handler) http.Handler {
	return g.requireDBLevel(next, contract.AccessViewOnly)
}

func (g *guards) editDB(next http.Handler) http.Handler {
	return g.requireDBLevel(next, contract.AccessViewEdit)
}

// access dicek tiap request supaya perubahan/revoke akses langsung berlaku
func (g *guards) requireDBLevel(next http.Handler, min contract.AccessLevel) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := contract.PrincipalFrom(r.Context())
		if !ok {
			denyUnauthenticated(w)
			return
		}

		dbID, ok := g.dbID(r)
		if !ok {
			http.Error(w, "invalid database id", http.StatusBadRequest)
			return
		}

		level, err := g.checker.EffectiveLevel(r.Context(), p.UserID, dbID)
		if err != nil {
			log.Printf("access: guard: effective level user=%d db=%d: %v", p.UserID, dbID, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if level < min {
			denyForbidden(w)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func denyUnauthenticated(w http.ResponseWriter) {
	http.Error(w, "authentication required", http.StatusUnauthorized)
}

func denyForbidden(w http.ResponseWriter) {
	http.Error(w, contract.ErrAccessDenied.Error(), http.StatusForbidden)
}
