package testutil

import (
	"context"
	"net/http"
	"strconv"
	"sync"

	"github.com/example/if3110-milestone1/internal/contract"
)

// FakeAccessChecker is an in-memory implementation of contract.AccessChecker for testing.
type FakeAccessChecker struct {
	mu          sync.RWMutex
	userGroups  map[int64][]int64
	groupAccess map[int64]map[int64]contract.AccessLevel
	dbNames     map[int64]string
}

func NewFakeAccessChecker() *FakeAccessChecker {
	return &FakeAccessChecker{
		userGroups:  make(map[int64][]int64),
		groupAccess: make(map[int64]map[int64]contract.AccessLevel),
		dbNames:     make(map[int64]string),
	}
}

func (f *FakeAccessChecker) RegisterDB(dbID int64, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dbNames[dbID] = name
}

func (f *FakeAccessChecker) AssignUserToGroup(userID int64, groupID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userGroups[userID] = append(f.userGroups[userID], groupID)
}

func (f *FakeAccessChecker) GrantGroupAccess(groupID int64, dbID int64, level contract.AccessLevel) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.groupAccess[groupID]; !ok {
		f.groupAccess[groupID] = make(map[int64]contract.AccessLevel)
	}
	f.groupAccess[groupID][dbID] = level
}

func (f *FakeAccessChecker) EffectiveLevel(ctx context.Context, userID, dbID int64) (contract.AccessLevel, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.effectiveLevelLocked(userID, dbID), nil
}

// effectiveLevelLocked computes effective level without acquiring locks to prevent RLock re-entrancy deadlocks.
func (f *FakeAccessChecker) effectiveLevelLocked(userID, dbID int64) contract.AccessLevel {
	groups := f.userGroups[userID]
	highest := contract.AccessNone

	for _, gID := range groups {
		if dbMap, ok := f.groupAccess[gID]; ok {
			if lvl, hasAccess := dbMap[dbID]; hasAccess {
				if lvl > highest {
					highest = lvl
				}
			}
		}
	}

	return highest
}

func (f *FakeAccessChecker) ListAccessible(ctx context.Context, userID int64) ([]contract.AccessibleDB, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	result := make([]contract.AccessibleDB, 0)
	for dbID, name := range f.dbNames {
		lvl := f.effectiveLevelLocked(userID, dbID)
		if lvl > contract.AccessNone {
			result = append(result, contract.AccessibleDB{
				DBID:  dbID,
				Name:  name,
				Level: lvl,
			})
		}
	}
	return result, nil
}

// NewFakeGuards creates contract.Guards using the provided AccessChecker.
func NewFakeGuards(checker contract.AccessChecker) contract.Guards {
	return contract.Guards{
		Admin: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p, ok := contract.PrincipalFrom(r.Context())
				if !ok || !p.IsPlatformAdmin {
					http.Error(w, "Access denied: Platform Admin privileges required.", http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
			})
		},
		ViewDB: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p, ok := contract.PrincipalFrom(r.Context())
				if !ok {
					http.Error(w, "Access denied: Unauthorized.", http.StatusUnauthorized)
					return
				}
				dbIDStr := r.PathValue("id")
				dbID, err := strconv.ParseInt(dbIDStr, 10, 64)
				if err != nil || dbID <= 0 {
					http.Error(w, "Invalid database ID.", http.StatusBadRequest)
					return
				}

				level, err := checker.EffectiveLevel(r.Context(), p.UserID, dbID)
				if err != nil || level < contract.AccessViewOnly {
					http.Error(w, "Access denied: You do not have permission to view this database.", http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
			})
		},
		EditDB: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p, ok := contract.PrincipalFrom(r.Context())
				if !ok {
					http.Error(w, "Access denied: Unauthorized.", http.StatusUnauthorized)
					return
				}
				dbIDStr := r.PathValue("id")
				dbID, err := strconv.ParseInt(dbIDStr, 10, 64)
				if err != nil || dbID <= 0 {
					http.Error(w, "Invalid database ID.", http.StatusBadRequest)
					return
				}

				level, err := checker.EffectiveLevel(r.Context(), p.UserID, dbID)
				if err != nil || level < contract.AccessViewEdit {
					http.Error(w, "Access denied: View + Edit permissions required.", http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
			})
		},
	}
}
