package testutil

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

func TestFakeAccessCheckerAndGuards(t *testing.T) {
	checker := NewFakeAccessChecker()
	checker.RegisterDB(1, "app")
	checker.GrantGroupAccess(10, 1, contract.AccessViewOnly)
	checker.GrantGroupAccess(20, 1, contract.AccessViewEdit)
	checker.AssignUserToGroup(1, 10)
	checker.AssignUserToGroup(2, 10)
	checker.AssignUserToGroup(2, 20)
	for user, want := range map[int64]contract.AccessLevel{1: contract.AccessViewOnly, 2: contract.AccessViewEdit, 3: contract.AccessNone} {
		got, err := checker.EffectiveLevel(context.Background(), user, 1)
		if err != nil || got != want {
			t.Fatalf("user %d: level=%v err=%v", user, got, err)
		}
	}
	dbs, err := checker.ListAccessible(context.Background(), 1)
	if err != nil || len(dbs) != 1 || dbs[0].DBID != 1 {
		t.Fatalf("accessible=%v err=%v", dbs, err)
	}
	guards := NewFakeGuards(checker)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, tc := range []struct {
		name  string
		guard func(http.Handler) http.Handler
		user  int64
		admin bool
		want  int
	}{
		{"admin", guards.Admin, 3, true, 204},
		{"non-admin", guards.Admin, 1, false, 403},
		{"viewer", guards.ViewDB, 1, false, 204},
		{"admin without group", guards.ViewDB, 3, true, 403},
		{"view-only cannot edit", guards.EditDB, 1, false, 403},
		{"editor", guards.EditDB, 2, false, 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/databases/1/schema", nil)
			req.SetPathValue("id", "1")
			req = req.WithContext(contract.WithPrincipal(req.Context(), contract.Principal{UserID: tc.user, IsPlatformAdmin: tc.admin}))
			rec := httptest.NewRecorder()
			tc.guard(next).ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d", rec.Code, tc.want)
			}
		})
	}
}
