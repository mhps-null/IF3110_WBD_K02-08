//go:build integration

package admin

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/example/if3110-milestone1/internal/access"
	"github.com/example/if3110-milestone1/internal/contract"
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

// helper lokal untuk test ini (menghindari import errors tambahan)
func errorsIs(err, target error) bool {
	return err != nil && target != nil && err.Error() == target.Error()
}
