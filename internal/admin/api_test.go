package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/audit"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

type fakeAdminStore struct {
	users       []User
	groups      map[int64]Group
	members     map[int64][]User
	dbs         []Database
	access      []GroupAccess
	setLeaderFn func(groupID int64, leaderUserID *int64) error
}

func newFakeAdminStore() *fakeAdminStore {
	return &fakeAdminStore{
		users: []User{
			{ID: 1, Username: "admin", IsAdmin: true, IsActive: true},
			{ID: 2, Username: "alice", IsAdmin: false, IsActive: true},
			{ID: 3, Username: "bob", IsAdmin: false, IsActive: true},
		},
		groups: map[int64]Group{
			10: {ID: 10, Name: "Backend", Description: "Core API"},
		},
		members: map[int64][]User{
			10: {{ID: 2, Username: "alice", IsActive: true}},
		},
		dbs: []Database{{ID: 100, Name: "prod_pg"}},
	}
}

func (f *fakeAdminStore) ListUsers(context.Context) ([]User, error) { return f.users, nil }
func (f *fakeAdminStore) CreateUser(_ context.Context, username, _ string) (int64, error) {
	for _, u := range f.users {
		if u.Username == username {
			return 0, ErrDuplicateUsername
		}
	}
	id := int64(len(f.users) + 1)
	f.users = append(f.users, User{ID: id, Username: username, IsActive: true})
	return id, nil
}
func (f *fakeAdminStore) DeactivateUser(_ context.Context, id int64) error {
	for i := range f.users {
		if f.users[i].ID == id {
			f.users[i].IsActive = false
			return nil
		}
	}
	return ErrUserNotFound
}
func (f *fakeAdminStore) ActivateUser(_ context.Context, id int64) error {
	for i := range f.users {
		if f.users[i].ID == id {
			f.users[i].IsActive = true
			return nil
		}
	}
	return ErrUserNotFound
}
func (f *fakeAdminStore) ResetPassword(_ context.Context, id int64, _ string) error {
	for _, u := range f.users {
		if u.ID == id {
			return nil
		}
	}
	return ErrUserNotFound
}
func (f *fakeAdminStore) TogglePlatformAdmin(_ context.Context, id int64, isAdmin bool) error {
	for i := range f.users {
		if f.users[i].ID == id {
			f.users[i].IsAdmin = isAdmin
			return nil
		}
	}
	return ErrUserNotFound
}
func (f *fakeAdminStore) ListGroups(context.Context) ([]Group, error) {
	out := make([]Group, 0, len(f.groups))
	for _, g := range f.groups {
		out = append(out, g)
	}
	return out, nil
}
func (f *fakeAdminStore) CreateGroup(_ context.Context, name, desc string) (int64, error) {
	for _, g := range f.groups {
		if g.Name == name {
			return 0, ErrDuplicateGroupName
		}
	}
	id := int64(len(f.groups) + 11)
	f.groups[id] = Group{ID: id, Name: name, Description: desc}
	return id, nil
}
func (f *fakeAdminStore) GetGroup(_ context.Context, id int64) (Group, error) {
	g, ok := f.groups[id]
	if !ok {
		return Group{}, ErrGroupNotFound
	}
	return g, nil
}
func (f *fakeAdminStore) UpdateGroup(_ context.Context, id int64, name, desc string) error {
	g, ok := f.groups[id]
	if !ok {
		return ErrGroupNotFound
	}
	g.Name, g.Description = name, desc
	f.groups[id] = g
	return nil
}
func (f *fakeAdminStore) DeleteGroup(_ context.Context, id int64) error {
	if _, ok := f.groups[id]; !ok {
		return ErrGroupNotFound
	}
	delete(f.groups, id)
	return nil
}
func (f *fakeAdminStore) SetGroupLeader(_ context.Context, groupID int64, leaderUserID *int64) error {
	if f.setLeaderFn != nil {
		return f.setLeaderFn(groupID, leaderUserID)
	}
	g, ok := f.groups[groupID]
	if !ok {
		return ErrGroupNotFound
	}
	if leaderUserID == nil {
		g.LeaderUserID, g.LeaderUsername, g.LeaderIsActive, g.HasLeader = nil, nil, false, false
		f.groups[groupID] = g
		return nil
	}
	var found *User
	for _, m := range f.members[groupID] {
		if m.ID == *leaderUserID {
			cp := m
			found = &cp
			break
		}
	}
	if found == nil {
		return ErrLeaderNotMember
	}
	if !found.IsActive {
		return ErrLeaderInactive
	}
	id := found.ID
	name := found.Username
	g.LeaderUserID, g.LeaderUsername, g.LeaderIsActive, g.HasLeader = &id, &name, true, true
	f.groups[groupID] = g
	return nil
}
func (f *fakeAdminStore) ListGroupMembers(_ context.Context, groupID int64) ([]User, error) {
	return f.members[groupID], nil
}
func (f *fakeAdminStore) AddMember(_ context.Context, groupID, userID int64) error {
	for _, u := range f.users {
		if u.ID == userID {
			f.members[groupID] = append(f.members[groupID], u)
			return nil
		}
	}
	return ErrUserNotFound
}
func (f *fakeAdminStore) RemoveMember(_ context.Context, groupID, userID int64) error {
	members := f.members[groupID]
	next := members[:0]
	for _, m := range members {
		if m.ID != userID {
			next = append(next, m)
		}
	}
	f.members[groupID] = next
	if g, ok := f.groups[groupID]; ok && g.LeaderUserID != nil && *g.LeaderUserID == userID {
		g.LeaderUserID, g.LeaderUsername, g.LeaderIsActive, g.HasLeader = nil, nil, false, false
		f.groups[groupID] = g
	}
	return nil
}
func (f *fakeAdminStore) ListDatabases(context.Context) ([]Database, error)      { return f.dbs, nil }
func (f *fakeAdminStore) ListGroupAccess(context.Context) ([]GroupAccess, error) { return f.access, nil }
func (f *fakeAdminStore) SetGroupAccessLevels(_ context.Context, groupID int64, entries map[int64]int) error {
	for dbID, lvl := range entries {
		f.access = append(f.access, GroupAccess{GroupID: groupID, DBID: dbID, Level: lvl})
	}
	return nil
}

type fakeInspector struct {
	groups []contract.DatabaseGroupAccess
}

func (f fakeInspector) ListGroupsForDatabase(context.Context, int64) ([]contract.DatabaseGroupAccess, error) {
	return f.groups, nil
}
func (f fakeInspector) ListUserGroups(context.Context, int64) ([]contract.UserGroupMembership, error) {
	return nil, nil
}

type fakeAccessChecker struct {
	level contract.AccessLevel
}

func (f fakeAccessChecker) EffectiveLevel(context.Context, int64, int64) (contract.AccessLevel, error) {
	return f.level, nil
}
func (f fakeAccessChecker) ListAccessible(context.Context, int64) ([]contract.AccessibleDB, error) {
	return nil, nil
}

func TestAPISetGroupLeaderValidationAndAudit(t *testing.T) {
	store := newFakeAdminStore()
	auditStore := audit.NewMemoryStore()
	h := NewAPIHandler(store, nil, nil)
	h.SetAuditStore(auditStore)

	passAuth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := contract.WithPrincipal(r.Context(), contract.Principal{UserID: 1, Username: "admin", IsPlatformAdmin: true})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	mux := http.NewServeMux()
	h.RegisterAPIRoutes(mux, passAuth, func(next http.Handler) http.Handler { return next })

	// Non-member cannot be assigned as leader.
	reqNonMember := httptest.NewRequest(http.MethodPut, "/api/admin/groups/10/leader", strings.NewReader(`{"leader_user_id":3}`))
	recNonMember := httptest.NewRecorder()
	mux.ServeHTTP(recNonMember, reqNonMember)
	if recNonMember.Code != http.StatusBadRequest {
		t.Fatalf("non-member leader status=%d want=400 body=%s", recNonMember.Code, recNonMember.Body.String())
	}

	// Active member can be assigned as leader.
	reqValid := httptest.NewRequest(http.MethodPut, "/api/admin/groups/10/leader", strings.NewReader(`{"leader_user_id":2}`))
	recValid := httptest.NewRecorder()
	mux.ServeHTTP(recValid, reqValid)
	if recValid.Code != http.StatusOK {
		t.Fatalf("valid leader status=%d want=200 body=%s", recValid.Code, recValid.Body.String())
	}
	var resp struct {
		Group Group `json:"group"`
	}
	if err := json.Unmarshal(recValid.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Group.HasLeader || resp.Group.LeaderUserID == nil || *resp.Group.LeaderUserID != 2 {
		t.Fatalf("expected leader user 2, got %+v", resp.Group)
	}

	// Clearing leader sets leader_user_id=nil.
	reqClear := httptest.NewRequest(http.MethodPut, "/api/admin/groups/10/leader", strings.NewReader(`{"leader_user_id":null}`))
	recClear := httptest.NewRecorder()
	mux.ServeHTTP(recClear, reqClear)
	if recClear.Code != http.StatusOK {
		t.Fatalf("clear leader status=%d want=200 body=%s", recClear.Code, recClear.Body.String())
	}

	page, err := auditStore.List(context.Background(), audit.Filter{Event: "admin.group.set_leader"})
	if err != nil || page.Total != 2 {
		t.Fatalf("expected 2 audit records for set_leader, got %+v err=%v", page, err)
	}
}

func TestAPIDatabaseAccessGroupsPendingWithoutLeader(t *testing.T) {
	leaderID := int64(2)
	leaderName := "alice"
	inspector := fakeInspector{
		groups: []contract.DatabaseGroupAccess{
			{
				GroupID: 10, GroupName: "Backend", DBID: 100,
				Level: contract.AccessViewEdit, LevelLabel: "View + Edit",
				LeaderUserID: &leaderID, LeaderUsername: &leaderName,
				LeaderIsActive: true, HasLeader: true,
			},
			{
				GroupID: 11, GroupName: "Analytics", DBID: 100,
				Level: contract.AccessViewOnly, LevelLabel: "View Only",
				HasLeader: false,
			},
		},
	}

	t.Run("user without db access denied 403", func(t *testing.T) {
		h := NewAPIHandler(newFakeAdminStore(), inspector, fakeAccessChecker{level: contract.AccessNone})
		mux := http.NewServeMux()
		h.RegisterAPIRoutes(mux, func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r.WithContext(contract.WithPrincipal(r.Context(), contract.Principal{UserID: 9, Username: "outsider"})))
			})
		}, func(next http.Handler) http.Handler { return next })

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/databases/100/access-groups", nil))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status=%d want=403", rec.Code)
		}
	})

	t.Run("user with view access sees pending leader status rather than rejection", func(t *testing.T) {
		h := NewAPIHandler(newFakeAdminStore(), inspector, fakeAccessChecker{level: contract.AccessViewOnly})
		mux := http.NewServeMux()
		h.RegisterAPIRoutes(mux, func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r.WithContext(contract.WithPrincipal(r.Context(), contract.Principal{UserID: 2, Username: "alice"})))
			})
		}, func(next http.Handler) http.Handler { return next })

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/databases/100/access-groups", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d want=200 body=%s", rec.Code, rec.Body.String())
		}
		var body struct {
			HasMissingLeaders   bool                         `json:"has_missing_leaders"`
			ApprovalStatus      string                       `json:"approval_status"`
			MissingLeaderGroups []missingLeaderGroup         `json:"missing_leader_groups"`
			Groups              []contract.DatabaseGroupAccess `json:"groups"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if !body.HasMissingLeaders || body.ApprovalStatus != "pending_leader_assignment" || len(body.MissingLeaderGroups) != 1 || body.MissingLeaderGroups[0].GroupName != "Analytics" {
			t.Fatalf("unexpected approver payload: %+v", body)
		}
		if len(body.Groups) != 2 {
			t.Fatalf("expected 2 groups, got %d", len(body.Groups))
		}
	})
}
