//go:build integration

package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/access"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/connections"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/session"
)

const schemaPath = "../../migrations_meta/001_initial_schema.sql"
const schemaPath2 = "../../migrations_meta/002_session_privilege_version.sql"

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_METADATA_DSN")
	if dsn == "" {
		t.Skip("TEST_METADATA_DSN tidak diset")
	}

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}

	adminDB := stdlib.OpenDB(*cfg)
	if err := adminDB.Ping(); err != nil {
		adminDB.Close()
		t.Fatalf("ping admin: %v", err)
	}

	dbName := fmt.Sprintf("test_admin_%d_%d", time.Now().UnixNano(), rand.Int63())
	if _, err := adminDB.Exec(fmt.Sprintf(`CREATE DATABASE %q`, dbName)); err != nil {
		adminDB.Close()
		t.Fatalf("create test db: %v", err)
	}

	t.Cleanup(func() {
		adminDB.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, dbName))
		adminDB.Close()
	})

	testCfg := *cfg
	testCfg.Database = dbName
	testDB := stdlib.OpenDB(testCfg)
	t.Cleanup(func() { testDB.Close() })

	applySQL(t, testDB, schemaPath)
	applySQL(t, testDB, schemaPath2)
	return testDB
}

func applySQL(t *testing.T, db *sql.DB, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	for _, stmt := range strings.Split(string(raw), ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" || strings.HasPrefix(stmt, "--") {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("apply %s: %v\nstmt: %s", path, err, stmt)
		}
	}
}

func insertUser(t *testing.T, db *sql.DB, name string, admin bool) int64 {
	t.Helper()
	var id int64
	err := db.QueryRow(
		`INSERT INTO users (username, password_hash, is_platform_admin, is_active)
		 VALUES ($1, 'x', $2, TRUE) RETURNING id`, name, admin).Scan(&id)

	if err != nil {
		t.Fatalf("insert user %s: %v", name, err)
	}
	return id
}

func insertDB(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	var id int64
	err := db.QueryRow(`
		INSERT INTO db_connections (name, engine, host, port, database_name, username, password_enc, ssl_mode)
		VALUES ($1, 'postgres', 'h', 5432, 'd', 'u', 'e', 'disable') RETURNING id`, name).Scan(&id)

	if err != nil {
		t.Fatalf("insert db %s: %v", name, err)
	}
	return id
}

func privilegeVersion(t *testing.T, db *sql.DB, userID int64) int64 {
	t.Helper()
	var v int64
	if err := db.QueryRow(`SELECT privilege_version FROM users WHERE id = $1`, userID).Scan(&v); err != nil {
		t.Fatalf("privilege_version: %v", err)
	}
	return v
}

// Tests

func TestStore_Groups(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	t.Run("empty list", func(t *testing.T) {
		gs, err := s.ListGroups(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(gs) != 0 {
			t.Fatalf("want 0 groups, got %d", len(gs))
		}
	})

	t.Run("create", func(t *testing.T) {
		id, err := s.CreateGroup(ctx, "dev", "developer group")
		if err != nil {
			t.Fatal(err)
		}
		if id == 0 {
			t.Fatal("want non-zero id")
		}
	})

	t.Run("duplicate rejected", func(t *testing.T) {
		_, err := s.CreateGroup(ctx, "dev", "again")
		if !errorsIs(err, ErrDuplicateGroupName) {
			t.Fatalf("want ErrDuplicateGroupName, got %v", err)
		}
	})

	t.Run("update", func(t *testing.T) {
		id, err := s.CreateGroup(ctx, "qa", "qa team")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateGroup(ctx, id, "qa-lead", "qa leadership"); err != nil {
			t.Fatal(err)
		}
		g, err := s.GetGroup(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if g.Name != "qa-lead" || g.Description != "qa leadership" {
			t.Fatalf("unexpected group: %+v", g)
		}
	})

	t.Run("update duplicate rejected", func(t *testing.T) {
		_, err := s.CreateGroup(ctx, "marketing", "")
		if err != nil {
			t.Fatal(err)
		}
		g2, err := s.CreateGroup(ctx, "sales", "")
		if err != nil {
			t.Fatal(err)
		}
		err = s.UpdateGroup(ctx, g2, "marketing", "try duplicate")
		if !errorsIs(err, ErrDuplicateGroupName) {
			t.Fatalf("want ErrDuplicateGroupName, got %v", err)
		}
	})

	t.Run("list sorted", func(t *testing.T) {
		s.CreateGroup(ctx, "ops", "operations")
		gs, _ := s.ListGroups(ctx)
		if len(gs) != 2 {
			t.Fatalf("want 2 groups, got %d", len(gs))
		}
		if gs[0].Name != "dev" || gs[1].Name != "ops" {
			t.Fatalf("want [dev, ops], got [%s, %s]", gs[0].Name, gs[1].Name)
		}
	})

	t.Run("delete cascades and bumps members", func(t *testing.T) {
		gs, _ := s.ListGroups(ctx)
		gid := gs[0].ID // "dev"
		uid := insertUser(t, db, "alice", false)

		if err := s.AddMember(ctx, gid, uid); err != nil {
			t.Fatal(err)
		}
		v1 := privilegeVersion(t, db, uid)

		if err := s.DeleteGroup(ctx, gid); err != nil {
			t.Fatal(err)
		}
		v2 := privilegeVersion(t, db, uid)
		if v2 != v1+1 {
			t.Fatalf("want bump from %d to %d, got %d", v1, v1+1, v2)
		}

		// membership hrs hilang krn cascade
		var count int
		db.QueryRow(`SELECT COUNT(*) FROM group_members WHERE group_id = $1`, gid).Scan(&count)
		if count != 0 {
			t.Fatalf("want 0 memberships, got %d", count)
		}
	})
}

func TestStore_Membership(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	gid, _ := s.CreateGroup(ctx, "g", "")
	uid := insertUser(t, db, "alice", false)
	v0 := privilegeVersion(t, db, uid)

	t.Run("add bumps", func(t *testing.T) {
		if err := s.AddMember(ctx, gid, uid); err != nil {
			t.Fatal(err)
		}
		if v := privilegeVersion(t, db, uid); v != v0+1 {
			t.Fatalf("want %d, got %d", v0+1, v)
		}
	})

	t.Run("add again is no-op", func(t *testing.T) {
		before := privilegeVersion(t, db, uid)
		if err := s.AddMember(ctx, gid, uid); err != nil {
			t.Fatal(err)
		}
		if after := privilegeVersion(t, db, uid); after != before {
			t.Fatalf("bump on duplicate add: %d -> %d", before, after)
		}
	})

	t.Run("remove bumps", func(t *testing.T) {
		before := privilegeVersion(t, db, uid)
		if err := s.RemoveMember(ctx, gid, uid); err != nil {
			t.Fatal(err)
		}
		if after := privilegeVersion(t, db, uid); after != before+1 {
			t.Fatalf("want %d, got %d", before+1, after)
		}
	})

	t.Run("remove non-member is no-op", func(t *testing.T) {
		before := privilegeVersion(t, db, uid)
		if err := s.RemoveMember(ctx, gid, uid); err != nil {
			t.Fatal(err)
		}
		if after := privilegeVersion(t, db, uid); after != before {
			t.Fatalf("bump on remove non-member: %d -> %d", before, after)
		}
	})
}

func TestStore_AccessMatrix(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	gid, _ := s.CreateGroup(ctx, "g", "")
	uid := insertUser(t, db, "alice", false)
	dbID := insertDB(t, db, "alpha-db")

	if err := s.AddMember(ctx, gid, uid); err != nil {
		t.Fatal(err)
	}

	t.Run("set level bumps members", func(t *testing.T) {
		before := privilegeVersion(t, db, uid)
		if err := s.SetGroupAccess(ctx, gid, dbID, 1); err != nil {
			t.Fatal(err)
		}
		if after := privilegeVersion(t, db, uid); after != before+1 {
			t.Fatalf("want %d, got %d", before+1, after)
		}
	})

	t.Run("same level does not bump", func(t *testing.T) {
		before := privilegeVersion(t, db, uid)
		if err := s.SetGroupAccess(ctx, gid, dbID, 1); err != nil {
			t.Fatal(err)
		}
		if after := privilegeVersion(t, db, uid); after != before {
			t.Fatalf("bump on same level: %d -> %d", before, after)
		}
	})

	t.Run("change level bumps members", func(t *testing.T) {
		before := privilegeVersion(t, db, uid)
		if err := s.SetGroupAccess(ctx, gid, dbID, 2); err != nil {
			t.Fatal(err)
		}
		if after := privilegeVersion(t, db, uid); after != before+1 {
			t.Fatalf("want %d, got %d", before+1, after)
		}
	})

	t.Run("invalid level rejected", func(t *testing.T) {
		if err := s.SetGroupAccess(ctx, gid, dbID, 3); err == nil {
			t.Fatal("want error for level=3")
		}
	})

	t.Run("remove bumps members", func(t *testing.T) {
		before := privilegeVersion(t, db, uid)
		if err := s.RemoveGroupAccess(ctx, gid, dbID); err != nil {
			t.Fatal(err)
		}
		if after := privilegeVersion(t, db, uid); after != before+1 {
			t.Fatalf("want %d, got %d", before+1, after)
		}
	})
}

func TestAccessFlow_SelectDatabaseGroupAndLevel(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)
	ctx := context.Background()
	groupID, err := s.CreateGroup(ctx, "flow-group", "")
	if err != nil {
		t.Fatal(err)
	}
	dbID := insertDB(t, db, "flow-db")
	h := NewHandler(s, nil, NewTemplateRenderer("../../web/templates"), nil)

	getSelectedGroup := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("/admin/access?db_id=%d&group_id=%d", dbID, groupID), nil)
		rr := httptest.NewRecorder()
		h.ShowMatrix(rr, req)
		return rr
	}

	beforeSave := getSelectedGroup()
	if beforeSave.Code != http.StatusOK ||
		!strings.Contains(beforeSave.Body.String(), `<select id="database" name="db_id" required>`) ||
		!strings.Contains(beforeSave.Body.String(), `<select id="group" name="group_id" required>`) ||
		!strings.Contains(beforeSave.Body.String(), `<select id="level" name="level" required>`) {
		t.Fatalf("expected all required access fields immediately, got status %d", beforeSave.Code)
	}

	form := url.Values{
		"csrf_token": {"test-token"},
		"db_id":      {fmt.Sprint(dbID)},
		"group_id":   {fmt.Sprint(groupID)},
		"level":      {"2"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/access", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.SetAccess(rr, req)

	wantLocation := fmt.Sprintf("/admin/access?db_id=%d&group_id=%d&flash=saved", dbID, groupID)
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != wantLocation {
		t.Fatalf("got status %d and location %q, want redirect to %q", rr.Code, rr.Header().Get("Location"), wantLocation)
	}

	var level int
	if err := db.QueryRow(`SELECT level FROM group_db_access WHERE group_id = $1 AND db_id = $2`,
		groupID, dbID).Scan(&level); err != nil {
		t.Fatal(err)
	}
	if level != 2 {
		t.Fatalf("saved level = %d, want 2", level)
	}
	afterSave := getSelectedGroup()
	if !strings.Contains(afterSave.Body.String(), `<option value="2" selected>View + Edit</option>`) ||
		!strings.Contains(afterSave.Body.String(), "flow-db") ||
		!strings.Contains(afterSave.Body.String(), "flow-group") ||
		!strings.Contains(afterSave.Body.String(), "View + Edit") {
		t.Fatal("expected the saved access level and matrix entry to appear after redirect")
	}
}

// TestStore_AccessIntegration adalah test end-to-end P2:
// Setelah SetGroupAccess, AccessChecker.EffectiveLevel langsung mencerminkan
// level terbaru pada request berikutnya — sesuai spec "hitung per request tanpa cache".
func TestStore_AccessIntegration(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)
	checker := access.NewChecker(db)
	ctx := context.Background()

	gid, _ := s.CreateGroup(ctx, "g", "")
	uid := insertUser(t, db, "alice", false)
	dbID := insertDB(t, db, "alpha-db")

	if err := s.AddMember(ctx, gid, uid); err != nil {
		t.Fatal(err)
	}

	// Sebelum akses diberikan -> AccessNone
	lvl, err := checker.EffectiveLevel(ctx, uid, dbID)
	if err != nil {
		t.Fatal(err)
	}
	if lvl != contract.AccessNone {
		t.Fatalf("want AccessNone, got %v", lvl)
	}

	// Beri View Only -> langsung efektif
	if err := s.SetGroupAccess(ctx, gid, dbID, 1); err != nil {
		t.Fatal(err)
	}
	lvl, _ = checker.EffectiveLevel(ctx, uid, dbID)
	if lvl != contract.AccessViewOnly {
		t.Fatalf("want AccessViewOnly, got %v", lvl)
	}

	// Naikkan ke View Edit -> langsung efektif
	if err := s.SetGroupAccess(ctx, gid, dbID, 2); err != nil {
		t.Fatal(err)
	}
	lvl, _ = checker.EffectiveLevel(ctx, uid, dbID)
	if lvl != contract.AccessViewEdit {
		t.Fatalf("want AccessViewEdit, got %v", lvl)
	}

	// Cabut akses -> langsung hilang
	if err := s.RemoveGroupAccess(ctx, gid, dbID); err != nil {
		t.Fatal(err)
	}
	lvl, _ = checker.EffectiveLevel(ctx, uid, dbID)
	if lvl != contract.AccessNone {
		t.Fatalf("want AccessNone, got %v", lvl)
	}
}

func errorsIs(err, target error) bool {
	return errors.Is(err, target)
}

func insertSession(t *testing.T, db *sql.DB, userID int64, idHash string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO sessions (user_id, id_hash, absolute_expires_at)
		VALUES ($1, $2, NOW() + INTERVAL '1 hour')`, userID, idHash); err != nil {
		t.Fatalf("insert session: %v", err)
	}
}

func activeSessions(t *testing.T, db *sql.DB, userID int64) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = $1 AND revoked_at IS NULL`, userID).Scan(&n); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	return n
}

func isActive(t *testing.T, db *sql.DB, userID int64) bool {
	t.Helper()
	var active bool
	if err := db.QueryRow(`SELECT is_active FROM users WHERE id = $1`, userID).Scan(&active); err != nil {
		t.Fatalf("is_active: %v", err)
	}
	return active
}

func TestStore_AccountStatusSessionLifecycle(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	t.Run("deactivate revokes sessions and bumps privilege", func(t *testing.T) {
		uid := insertUser(t, db, "deact", false)
		insertSession(t, db, uid, "deact-1")
		insertSession(t, db, uid, "deact-2")
		before := privilegeVersion(t, db, uid)

		if err := s.DeactivateUser(ctx, uid); err != nil {
			t.Fatal(err)
		}
		if isActive(t, db, uid) || activeSessions(t, db, uid) != 0 || privilegeVersion(t, db, uid) != before+1 {
			t.Fatal("deactivation must disable the user, revoke every session, and bump privilege_version")
		}
	})

	t.Run("reactivation does not resurrect old sessions", func(t *testing.T) {
		uid := insertUser(t, db, "react", false)
		insertSession(t, db, uid, "react-old")
		if err := s.DeactivateUser(ctx, uid); err != nil {
			t.Fatal(err)
		}

		insertSession(t, db, uid, "react-leftover")
		before := privilegeVersion(t, db, uid)

		if err := s.ActivateUser(ctx, uid); err != nil {
			t.Fatal(err)
		}
		if !isActive(t, db, uid) || activeSessions(t, db, uid) != 0 || privilegeVersion(t, db, uid) != before+1 {
			t.Fatal("reactivation must enable the user without reviving any earlier session")
		}
	})

	t.Run("reset password revokes sessions", func(t *testing.T) {
		uid := insertUser(t, db, "reset", false)
		insertSession(t, db, uid, "reset-1")
		before := privilegeVersion(t, db, uid)

		if err := s.ResetPassword(ctx, uid, "new-hash"); err != nil {
			t.Fatal(err)
		}
		var hash string
		if err := db.QueryRow(`SELECT password_hash FROM users WHERE id = $1`, uid).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		if hash != "new-hash" || activeSessions(t, db, uid) != 0 || privilegeVersion(t, db, uid) != before+1 {
			t.Fatal("password reset must store the new hash, revoke sessions, and bump privilege_version")
		}
	})

	t.Run("unknown user reports not found", func(t *testing.T) {
		for name, op := range map[string]func() error{
			"deactivate": func() error { return s.DeactivateUser(ctx, 999999) },
			"activate":   func() error { return s.ActivateUser(ctx, 999999) },
			"reset":      func() error { return s.ResetPassword(ctx, 999999, "x") },
		} {
			if err := op(); !errorsIs(err, ErrUserNotFound) {
				t.Fatalf("%s: want ErrUserNotFound, got %v", name, err)
			}
		}
	})

	t.Run("revocation failure rolls back the account change", func(t *testing.T) {
		uid := insertUser(t, db, "rollback", false)
		insertSession(t, db, uid, "rollback-1")
		before := privilegeVersion(t, db, uid)

		// Force the session UPDATE to fail inside the transaction.
		for _, stmt := range []string{
			`CREATE FUNCTION fail_session_update() RETURNS trigger AS $$
			BEGIN RAISE EXCEPTION 'simulated revocation failure'; END; $$ LANGUAGE plpgsql`,
			`CREATE TRIGGER fail_session_update BEFORE UPDATE ON sessions
			FOR EACH ROW EXECUTE FUNCTION fail_session_update()`,
		} {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("install failure trigger: %v", err)
			}
		}
		t.Cleanup(func() {
			_, _ = db.Exec(`DROP TRIGGER IF EXISTS fail_session_update ON sessions`)
			_, _ = db.Exec(`DROP FUNCTION IF EXISTS fail_session_update()`)
		})

		for name, op := range map[string]func() error{
			"deactivate": func() error { return s.DeactivateUser(ctx, uid) },
			"reset":      func() error { return s.ResetPassword(ctx, uid, "should-not-persist") },
		} {
			if err := op(); err == nil {
				t.Fatalf("%s: want error when session revocation fails", name)
			}
		}
		var hash string
		if err := db.QueryRow(`SELECT password_hash FROM users WHERE id = $1`, uid).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		if !isActive(t, db, uid) || hash != "x" || privilegeVersion(t, db, uid) != before || activeSessions(t, db, uid) != 1 {
			t.Fatal("a failed revocation must leave the account and sessions unchanged")
		}
	})
}

func TestStore_TogglePlatformAdminSessionLifecycle(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)
	ctx := context.Background()
	userID := insertUser(t, db, "platform-admin-toggle", false)
	insertSession(t, db, userID, "platform-admin-toggle-1")
	before := privilegeVersion(t, db, userID)

	if err := s.TogglePlatformAdmin(ctx, userID, true); err != nil {
		t.Fatal(err)
	}

	var isAdmin bool
	if err := db.QueryRow(`SELECT is_platform_admin FROM users WHERE id = $1`, userID).Scan(&isAdmin); err != nil {
		t.Fatal(err)
	}
	if !isAdmin || activeSessions(t, db, userID) != 0 || privilegeVersion(t, db, userID) != before+1 {
		t.Fatal("granting Platform Admin must update role, revoke sessions, and bump privilege_version")
	}

	insertSession(t, db, userID, "platform-admin-toggle-2")
	before = privilegeVersion(t, db, userID)
	if err := s.TogglePlatformAdmin(ctx, userID, true); err != nil {
		t.Fatal(err)
	}
	if activeSessions(t, db, userID) != 1 || privilegeVersion(t, db, userID) != before {
		t.Fatal("setting the existing Platform Admin role must not change privilege or sessions")
	}

	if err := s.TogglePlatformAdmin(ctx, userID, false); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT is_platform_admin FROM users WHERE id = $1`, userID).Scan(&isAdmin); err != nil {
		t.Fatal(err)
	}
	if isAdmin || activeSessions(t, db, userID) != 0 || privilegeVersion(t, db, userID) != before+1 {
		t.Fatal("revoking Platform Admin must update role, revoke sessions, and bump privilege_version")
	}

	if err := s.TogglePlatformAdmin(ctx, 999999, true); !errorsIs(err, ErrUserNotFound) {
		t.Fatalf("want ErrUserNotFound, got %v", err)
	}
}

func TestStore_DeleteConnectionCascadesAndBumpsPrivileges(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)
	checker := access.NewChecker(db)
	ctx := context.Background()

	connRepo, err := connections.NewSQLRepository(db, make([]byte, 32))
	if err != nil {
		t.Fatalf("new sql repository: %v", err)
	}

	t.Run("successful connection deletion cascades access and bumps affected user privileges", func(t *testing.T) {
		gidAffected, err := s.CreateGroup(ctx, "grp-affected", "affected group")
		if err != nil {
			t.Fatalf("create affected group: %v", err)
		}
		gidUnaffected, err := s.CreateGroup(ctx, "grp-unaffected", "unaffected group")
		if err != nil {
			t.Fatalf("create unaffected group: %v", err)
		}

		uidAffected1 := insertUser(t, db, "u-affected-1", false)
		uidAffected2 := insertUser(t, db, "u-affected-2", false)
		uidUnaffected := insertUser(t, db, "u-unaffected", false)

		if err := s.AddMember(ctx, gidAffected, uidAffected1); err != nil {
			t.Fatalf("add member 1: %v", err)
		}
		if err := s.AddMember(ctx, gidAffected, uidAffected2); err != nil {
			t.Fatalf("add member 2: %v", err)
		}
		if err := s.AddMember(ctx, gidUnaffected, uidUnaffected); err != nil {
			t.Fatalf("add member 3: %v", err)
		}

		targetDBID := insertDB(t, db, "target-db-to-delete")
		otherDBID := insertDB(t, db, "other-active-db")

		if err := s.SetGroupAccess(ctx, gidAffected, targetDBID, 2); err != nil {
			t.Fatalf("set group access target: %v", err)
		}
		if err := s.SetGroupAccess(ctx, gidUnaffected, otherDBID, 1); err != nil {
			t.Fatalf("set group access other: %v", err)
		}

		lvl1, err := checker.EffectiveLevel(ctx, uidAffected1, targetDBID)
		if err != nil || lvl1 != contract.AccessViewEdit {
			t.Fatalf("want AccessViewEdit for u-affected-1, got %v (err: %v)", lvl1, err)
		}
		lvlOther, err := checker.EffectiveLevel(ctx, uidUnaffected, otherDBID)
		if err != nil || lvlOther != contract.AccessViewOnly {
			t.Fatalf("want AccessViewOnly for u-unaffected on other db, got %v (err: %v)", lvlOther, err)
		}

		vAffected1Before := privilegeVersion(t, db, uidAffected1)
		vAffected2Before := privilegeVersion(t, db, uidAffected2)
		vUnaffectedBefore := privilegeVersion(t, db, uidUnaffected)

		if err := connRepo.Delete(ctx, targetDBID); err != nil {
			t.Fatalf("delete connection: %v", err)
		}

		var connCount int
		if err := db.QueryRow(`SELECT COUNT(1) FROM db_connections WHERE id = $1`, targetDBID).Scan(&connCount); err != nil {
			t.Fatalf("query conn count: %v", err)
		}
		if connCount != 0 {
			t.Fatalf("expected db_connection to be deleted, found %d", connCount)
		}

		var gdaCount int
		if err := db.QueryRow(`SELECT COUNT(1) FROM group_db_access WHERE db_id = $1`, targetDBID).Scan(&gdaCount); err != nil {
			t.Fatalf("query gda count: %v", err)
		}
		if gdaCount != 0 {
			t.Fatalf("expected group_db_access to be cascade deleted, found %d", gdaCount)
		}

		if v := privilegeVersion(t, db, uidAffected1); v != vAffected1Before+1 {
			t.Fatalf("u-affected-1 privilege_version: want %d, got %d", vAffected1Before+1, v)
		}
		if v := privilegeVersion(t, db, uidAffected2); v != vAffected2Before+1 {
			t.Fatalf("u-affected-2 privilege_version: want %d, got %d", vAffected2Before+1, v)
		}
		if v := privilegeVersion(t, db, uidUnaffected); v != vUnaffectedBefore {
			t.Fatalf("u-unaffected privilege_version should not change: want %d, got %d", vUnaffectedBefore, v)
		}

		postLevel, err := checker.EffectiveLevel(ctx, uidAffected1, targetDBID)
		if err != nil || postLevel != contract.AccessNone {
			t.Fatalf("post-delete effective level: want AccessNone, got %v (err: %v)", postLevel, err)
		}
	})

	t.Run("deletion aborted on migration history rolls back privilege bump and cascade", func(t *testing.T) {
		gid, err := s.CreateGroup(ctx, "grp-with-migration", "grp")
		if err != nil {
			t.Fatalf("create group: %v", err)
		}
		uid := insertUser(t, db, "u-with-migration", false)
		if err := s.AddMember(ctx, gid, uid); err != nil {
			t.Fatalf("add member: %v", err)
		}
		dbID := insertDB(t, db, "db-with-migration")
		if err := s.SetGroupAccess(ctx, gid, dbID, 2); err != nil {
			t.Fatalf("set group access: %v", err)
		}

		_, err = db.Exec(`
			INSERT INTO migrations (db_id, title, sql_text, status, created_by)
			VALUES ($1, 'mig 1', 'SELECT 1;', 'success', $2)`, dbID, uid)
		if err != nil {
			t.Fatalf("insert migration: %v", err)
		}

		vBefore := privilegeVersion(t, db, uid)

		err = connRepo.Delete(ctx, dbID)
		if !errors.Is(err, connections.ErrHasMigrationHistory) {
			t.Fatalf("want ErrHasMigrationHistory, got %v", err)
		}

		if v := privilegeVersion(t, db, uid); v != vBefore {
			t.Fatalf("privilege_version should NOT change on failed delete: want %d, got %d", vBefore, v)
		}

		var connCount int
		_ = db.QueryRow(`SELECT COUNT(1) FROM db_connections WHERE id = $1`, dbID).Scan(&connCount)
		if connCount != 1 {
			t.Fatalf("db_connection should still exist, got count %d", connCount)
		}

		var gdaCount int
		_ = db.QueryRow(`SELECT COUNT(1) FROM group_db_access WHERE db_id = $1`, dbID).Scan(&gdaCount)
		if gdaCount != 1 {
			t.Fatalf("group_db_access should still exist, got count %d", gdaCount)
		}
	})
}

func TestStore_SetGroupAccessLevelsAtomic(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	gid, err := s.CreateGroup(ctx, "grp-matrix", "test group")
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	uid := insertUser(t, db, "u-matrix", false)
	if err := s.AddMember(ctx, gid, uid); err != nil {
		t.Fatalf("add member: %v", err)
	}

	db1 := insertDB(t, db, "db-matrix-1")
	db2 := insertDB(t, db, "db-matrix-2")

	// Set initial baseline
	if err := s.SetGroupAccessLevels(ctx, gid, map[int64]int{db1: 1, db2: 1}); err != nil {
		t.Fatalf("initial set: %v", err)
	}
	vBefore := privilegeVersion(t, db, uid)

	for _, invalidDBLevel := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("non-existent database level %d rolls back all changes", invalidDBLevel), func(t *testing.T) {
			nonExistentDB := int64(999999)
			err := s.SetGroupAccessLevels(ctx, gid, map[int64]int{
				db1:           2,
				nonExistentDB: invalidDBLevel,
			})
			if err == nil {
				t.Fatal("expected error when setting access for non-existent database")
			}

			var lvl int
			if err := db.QueryRow(`SELECT level FROM group_db_access WHERE group_id = $1 AND db_id = $2`, gid, db1).Scan(&lvl); err != nil {
				t.Fatalf("query level: %v", err)
			}
			if lvl != 1 {
				t.Fatalf("db1 level should remain 1 on rollback, got %d", lvl)
			}

			if v := privilegeVersion(t, db, uid); v != vBefore {
				t.Fatalf("privilege_version should not change on rollback: want %d, got %d", vBefore, v)
			}
		})
	}

	t.Run("batch update succeeds and bumps privilege_version exactly once", func(t *testing.T) {
		err := s.SetGroupAccessLevels(ctx, gid, map[int64]int{
			db1: 2,
			db2: 0,
		})
		if err != nil {
			t.Fatalf("batch update failed: %v", err)
		}

		var lvl1 int
		if err := db.QueryRow(`SELECT level FROM group_db_access WHERE group_id = $1 AND db_id = $2`, gid, db1).Scan(&lvl1); err != nil {
			t.Fatalf("query db1: %v", err)
		}
		if lvl1 != 2 {
			t.Fatalf("want db1 level 2, got %d", lvl1)
		}

		var count2 int
		if err := db.QueryRow(`SELECT COUNT(1) FROM group_db_access WHERE group_id = $1 AND db_id = $2`, gid, db2).Scan(&count2); err != nil {
			t.Fatalf("query db2 count: %v", err)
		}
		if count2 != 0 {
			t.Fatalf("want db2 removed, got count %d", count2)
		}

		if v := privilegeVersion(t, db, uid); v != vBefore+1 {
			t.Fatalf("privilege_version want %d, got %d", vBefore+1, v)
		}
	})
}

func TestStore_ResetPasswordAndVerifiedSessionOrdering(t *testing.T) {
	db := setupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	users := NewStore(db)
	sessions := session.NewSQLStore(db, session.DefaultConfig())
	uid := insertUser(t, db, "login-reset-order", false)
	now := time.Now().UTC()

	// The login verified the original hash "x", but reset finished first.
	if err := users.ResetPassword(ctx, uid, "new-hash"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sessions.CreateVerified(ctx, uid, "x", now); !errors.Is(err, session.ErrCredentialsChanged) {
		t.Fatalf("login using the pre-reset hash must be rejected: %v", err)
	}
	if activeSessions(t, db, uid) != 0 {
		t.Fatal("stale login created an active session")
	}

	// In the opposite order, reset must revoke the session created first.
	token, _, err := sessions.CreateVerified(ctx, uid, "new-hash", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := users.ResetPassword(ctx, uid, "latest-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Load(ctx, token, now); !errors.Is(err, session.ErrInvalid) {
		t.Fatalf("session created before reset must be revoked: %v", err)
	}
}
