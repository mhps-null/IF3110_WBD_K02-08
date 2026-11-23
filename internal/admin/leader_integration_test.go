//go:build integration

package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/access"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

func TestStore_GroupLeaderAndApproverInspector(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)
	checker := access.NewChecker(db)
	ctx := context.Background()

	alice := insertUser(t, db, "alice-lead", false)
	bob := insertUser(t, db, "bob-member", false)
	inactive := insertUser(t, db, "inactive-user", false)
	if err := s.DeactivateUser(ctx, inactive); err != nil {
		t.Fatal(err)
	}

	gBackend, err := s.CreateGroup(ctx, "backend", "Backend team")
	if err != nil {
		t.Fatal(err)
	}
	gData, err := s.CreateGroup(ctx, "data", "Data team")
	if err != nil {
		t.Fatal(err)
	}
	gNoLeader, err := s.CreateGroup(ctx, "qa-no-leader", "QA without leader")
	if err != nil {
		t.Fatal(err)
	}

	dbID := insertDB(t, db, "shared-pg")
	if err := s.SetGroupAccess(ctx, gBackend, dbID, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupAccess(ctx, gData, dbID, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupAccess(ctx, gNoLeader, dbID, 1); err != nil {
		t.Fatal(err)
	}

	// Non-member cannot be assigned as leader.
	if err := s.SetGroupLeader(ctx, gBackend, &alice); !errors.Is(err, ErrLeaderNotMember) {
		t.Fatalf("want ErrLeaderNotMember, got %v", err)
	}

	// Inactive user cannot be assigned as leader even if member.
	if err := s.AddMember(ctx, gBackend, inactive); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupLeader(ctx, gBackend, &inactive); !errors.Is(err, ErrLeaderInactive) {
		t.Fatalf("want ErrLeaderInactive, got %v", err)
	}

	// Add alice to both backend & data; one user can lead multiple groups simultaneously.
	if err := s.AddMember(ctx, gBackend, alice); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(ctx, gData, alice); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(ctx, gBackend, bob); err != nil {
		t.Fatal(err)
	}

	vBefore := privilegeVersion(t, db, alice)
	if err := s.SetGroupLeader(ctx, gBackend, &alice); err != nil {
		t.Fatalf("assign alice backend leader: %v", err)
	}
	if err := s.SetGroupLeader(ctx, gData, &alice); err != nil {
		t.Fatalf("assign alice data leader: %v", err)
	}
	if vAfter := privilegeVersion(t, db, alice); vAfter != vBefore+2 {
		t.Fatalf("expected 2 privilege bumps for alice, got %d -> %d", vBefore, vAfter)
	}

	// Verify approver inspector reports all 3 groups, including qa-no-leader with HasLeader=false.
	groups, err := checker.ListGroupsForDatabase(ctx, dbID)
	if err != nil {
		t.Fatalf("ListGroupsForDatabase: %v", err)
	}
	if len(groups) != 3 {
		t.Fatalf("want 3 groups, got %d", len(groups))
	}
	byName := map[string]contract.DatabaseGroupAccess{}
	for _, g := range groups {
		byName[g.GroupName] = g
	}
	if !byName["backend"].HasLeader || *byName["backend"].LeaderUserID != alice || byName["backend"].Level != contract.AccessViewEdit {
		t.Fatalf("unexpected backend access group: %+v", byName["backend"])
	}
	if !byName["data"].HasLeader || *byName["data"].LeaderUserID != alice || byName["data"].Level != contract.AccessViewOnly {
		t.Fatalf("unexpected data access group: %+v", byName["data"])
	}
	if byName["qa-no-leader"].HasLeader || byName["qa-no-leader"].LeaderUserID != nil {
		t.Fatalf("qa-no-leader should have no leader: %+v", byName["qa-no-leader"])
	}

	// Removing the leader from group_members clears leader_user_id automatically.
	if err := s.RemoveMember(ctx, gBackend, alice); err != nil {
		t.Fatal(err)
	}
	gReloaded, err := s.GetGroup(ctx, gBackend)
	if err != nil {
		t.Fatal(err)
	}
	if gReloaded.HasLeader || gReloaded.LeaderUserID != nil {
		t.Fatalf("removing leader from group must clear leader_user_id, got %+v", gReloaded)
	}

	// Reassigning to bob works.
	if err := s.SetGroupLeader(ctx, gBackend, &bob); err != nil {
		t.Fatalf("assign bob as new leader: %v", err)
	}
	bobGroups, err := checker.ListUserGroups(ctx, bob)
	if err != nil || len(bobGroups) != 1 || !bobGroups[0].IsLeader {
		t.Fatalf("expected bob to lead backend, got %+v err=%v", bobGroups, err)
	}
}
