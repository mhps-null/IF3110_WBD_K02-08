package access

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

type fakeChecker struct {
	levels map[int64]map[int64]contract.AccessLevel
	err    error
}

func (f *fakeChecker) EffectiveLevel(_ context.Context, userID, dbID int64) (contract.AccessLevel, error) {
	if f.err != nil {
		return contract.AccessNone, f.err
	}
	return f.levels[userID][dbID], nil
}

func (f *fakeChecker) ListAccessible(_ context.Context, _ int64) ([]contract.AccessibleDB, error) {
	return nil, nil
}

func okHandler(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

func requestWith(principal *contract.Principal, dbID string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/databases/"+dbID, nil)
	if dbID != "" {
		req.SetPathValue("id", dbID)
	}
	if principal != nil {
		req = req.WithContext(contract.WithPrincipal(req.Context(), *principal))
	}
	return req
}

// Admin
func TestGuardsAdmin(t *testing.T) {
	guards := NewGuards(GuardsConfig{Checker: &fakeChecker{}})

	t.Run("admin allowed", func(t *testing.T) {
		rr := httptest.NewRecorder()
		guards.Admin(http.HandlerFunc(okHandler)).ServeHTTP(rr,
			requestWith(&contract.Principal{UserID: 1, IsPlatformAdmin: true}, ""))
		if rr.Code != http.StatusOK {
			t.Fatalf("got %d, want 200", rr.Code)
		}
	})

	t.Run("non-admin denied", func(t *testing.T) {
		rr := httptest.NewRecorder()
		guards.Admin(http.HandlerFunc(okHandler)).ServeHTTP(rr,
			requestWith(&contract.Principal{UserID: 2}, ""))
		if rr.Code != http.StatusForbidden {
			t.Fatalf("got %d, want 403", rr.Code)
		}
	})

	t.Run("no principal", func(t *testing.T) {
		rr := httptest.NewRecorder()
		guards.Admin(http.HandlerFunc(okHandler)).ServeHTTP(rr, requestWith(nil, ""))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("got %d, want 401", rr.Code)
		}
	})
}

// ViewDB
func TestGuardsViewDB(t *testing.T) {
	checker := &fakeChecker{levels: map[int64]map[int64]contract.AccessLevel{
		1: {10: contract.AccessViewOnly},
		2: {10: contract.AccessViewEdit},
		3: {10: contract.AccessNone},
	}}
	guards := NewGuards(GuardsConfig{Checker: checker})

	cases := []struct {
		name string
		user int64
		want int
	}{
		{"view only allowed", 1, http.StatusOK},
		{"view edit allowed (higher level)", 2, http.StatusOK},
		{"none denied", 3, http.StatusForbidden},
		{"no access for db", 1, http.StatusOK}, // placeholder replaced below
	}
	cases = cases[:3] // keep only meaningful cases

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			guards.ViewDB(http.HandlerFunc(okHandler)).ServeHTTP(rr,
				requestWith(&contract.Principal{UserID: c.user}, "10"))
			if rr.Code != c.want {
				t.Fatalf("got %d, want %d", rr.Code, c.want)
			}
		})
	}

	t.Run("unknown database denied", func(t *testing.T) {
		rr := httptest.NewRecorder()
		guards.ViewDB(http.HandlerFunc(okHandler)).ServeHTTP(rr,
			requestWith(&contract.Principal{UserID: 1}, "999"))
		if rr.Code != http.StatusForbidden {
			t.Fatalf("got %d, want 403", rr.Code)
		}
	})
}

// EditDB
func TestGuardsEditDB(t *testing.T) {
	checker := &fakeChecker{levels: map[int64]map[int64]contract.AccessLevel{
		1: {10: contract.AccessViewOnly},
		2: {10: contract.AccessViewEdit},
	}}
	guards := NewGuards(GuardsConfig{Checker: checker})

	t.Run("view only denied", func(t *testing.T) {
		rr := httptest.NewRecorder()
		guards.EditDB(http.HandlerFunc(okHandler)).ServeHTTP(rr,
			requestWith(&contract.Principal{UserID: 1}, "10"))
		if rr.Code != http.StatusForbidden {
			t.Fatalf("got %d, want 403", rr.Code)
		}
	})

	t.Run("view edit allowed", func(t *testing.T) {
		rr := httptest.NewRecorder()
		guards.EditDB(http.HandlerFunc(okHandler)).ServeHTTP(rr,
			requestWith(&contract.Principal{UserID: 2}, "10"))
		if rr.Code != http.StatusOK {
			t.Fatalf("got %d, want 200", rr.Code)
		}
	})
}

// Boundary cases
func TestGuardsInvalidDBID(t *testing.T) {
	guards := NewGuards(GuardsConfig{Checker: &fakeChecker{}})

	for _, bad := range []string{"", "abc", "0", "-5"} {
		t.Run("bad id: "+bad, func(t *testing.T) {
			rr := httptest.NewRecorder()
			guards.ViewDB(http.HandlerFunc(okHandler)).ServeHTTP(rr,
				requestWith(&contract.Principal{UserID: 1}, bad))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400", rr.Code)
			}
		})
	}
}

func TestGuardsCheckerError(t *testing.T) {
	guards := NewGuards(GuardsConfig{Checker: &fakeChecker{err: errors.New("boom")}})
	rr := httptest.NewRecorder()
	guards.ViewDB(http.HandlerFunc(okHandler)).ServeHTTP(rr,
		requestWith(&contract.Principal{UserID: 1}, "10"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500", rr.Code)
	}
}
