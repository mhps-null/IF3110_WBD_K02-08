//go:build integration

package access

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

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

const schemaPath = "../../migrations_meta/001_initial_schema.sql"

func TestCheckerIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_METADATA_DSN")
	if dsn == "" {
		t.Skip("TEST_METADATA_DSN tidak diset; lewati integration test")
	}

	ctx := context.Background()

	// koneksi admin
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TEST_METADATA_DSN: %v", err)
	}
	adminDB := stdlib.OpenDB(*cfg)
	if err := adminDB.PingContext(ctx); err != nil {
		adminDB.Close()
		t.Fatalf("ping admin db: %v", err)
	}

	dbName := fmt.Sprintf("test_access_%d_%d", time.Now().UnixNano(), rand.Int63())
	if _, err := adminDB.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %q`, dbName)); err != nil {
		adminDB.Close()
		t.Fatalf("create test db: %v", err)
	}

	// cleanup terdaftar lebih dulu
	t.Cleanup(func() {
		if _, err := adminDB.ExecContext(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, dbName)); err != nil {
			t.Logf("cleanup: drop test db: %v", err)
		}
		adminDB.Close()
	})

	// koneksi ke database sementara
	testCfg := *cfg
	testCfg.Database = dbName
	testDB := stdlib.OpenDB(testCfg)
	t.Cleanup(func() { testDB.Close() })

	if err := testDB.PingContext(ctx); err != nil {
		t.Fatalf("ping test db: %v", err)
	}

	applySchema(t, testDB, schemaPath)
	data := seed(t, testDB)
	checker := NewChecker(testDB)

	// EffectiveLevel
	t.Run("EffectiveLevel", func(t *testing.T) {
		cases := []struct {
			name     string
			user, db int64
			want     contract.AccessLevel
		}{
			{"alice / zeta-db -> view only (grup A)", data.Alice, data.DB1, contract.AccessViewOnly},
			{"alice / alpha-db -> view edit (grup A)", data.Alice, data.DB2, contract.AccessViewEdit},
			{"admin tanpa grup -> none (admin ≠ akses db)", data.Admin, data.DB1, contract.AccessNone},
			{"user non-aktif -> none (defense-in-depth)", data.Inactive, data.DB1, contract.AccessNone},
			{"both / zeta-db -> view edit (MAX(A,B))", data.Both, data.DB1, contract.AccessViewEdit},
			{"both / alpha-db -> view edit (grup A)", data.Both, data.DB2, contract.AccessViewEdit},
			{"alice / db tidak ada -> none", data.Alice, 999999, contract.AccessNone},
		}
		for _, c := range cases {
			c := c
			t.Run(c.name, func(t *testing.T) {
				got, err := checker.EffectiveLevel(ctx, c.user, c.db)
				if err != nil {
					t.Fatalf("EffectiveLevel: %v", err)
				}
				if got != c.want {
					t.Errorf("got %v, want %v", got, c.want)
				}
			})
		}
	})

	// ListAccessible
	t.Run("ListAccessible", func(t *testing.T) {
		assertAccessible(t, ctx, checker, data.Alice, []contract.AccessibleDB{
			{DBID: data.DB2, Name: "alpha-db", Level: contract.AccessViewEdit},
			{DBID: data.DB1, Name: "zeta-db", Level: contract.AccessViewOnly},
		})
		assertAccessible(t, ctx, checker, data.Admin, nil)
		assertAccessible(t, ctx, checker, data.Inactive, nil)
		assertAccessible(t, ctx, checker, data.Both, []contract.AccessibleDB{
			{DBID: data.DB2, Name: "alpha-db", Level: contract.AccessViewEdit},
			{DBID: data.DB1, Name: "zeta-db", Level: contract.AccessViewEdit},
		})
	})
}

// helpers

func applySchema(t *testing.T, db *sql.DB, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for _, stmt := range strings.Split(string(raw), ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("apply schema stmt: %v\nstmt: %s", err, stmt)
		}
	}
}

type seedData struct {
	Alice, Admin, Inactive, Both int64
	DB1, DB2                     int64 // DB1 = "zeta-db", DB2 = "alpha-db"
}

func seed(t *testing.T, db *sql.DB) seedData {
	t.Helper()

	var d seedData

	// Users
	d.Alice = mustQueryID(t, db,
		`INSERT INTO users (username, password_hash, is_platform_admin, is_active)
		 VALUES ($1, $2, FALSE, TRUE) RETURNING id`, "alice", "x")
	d.Admin = mustQueryID(t, db,
		`INSERT INTO users (username, password_hash, is_platform_admin, is_active)
		 VALUES ($1, $2, TRUE, TRUE) RETURNING id`, "admin", "x")
	d.Inactive = mustQueryID(t, db,
		`INSERT INTO users (username, password_hash, is_platform_admin, is_active)
		 VALUES ($1, $2, FALSE, FALSE) RETURNING id`, "inactive", "x")
	d.Both = mustQueryID(t, db,
		`INSERT INTO users (username, password_hash, is_platform_admin, is_active)
		 VALUES ($1, $2, FALSE, TRUE) RETURNING id`, "both", "x")

	// DB connections
	d.DB1 = mustQueryID(t, db,
		`INSERT INTO db_connections (name, engine, host, port, database_name, username, password_enc, ssl_mode)
		 VALUES ($1, 'postgres', 'localhost', 5432, 'x', 'u', 'enc', 'disable') RETURNING id`, "zeta-db")
	d.DB2 = mustQueryID(t, db,
		`INSERT INTO db_connections (name, engine, host, port, database_name, username, password_enc, ssl_mode)
		 VALUES ($1, 'postgres', 'localhost', 5432, 'x', 'u', 'enc', 'disable') RETURNING id`, "alpha-db")

	// Groups
	gA := mustQueryID(t, db, `INSERT INTO groups (name) VALUES ($1) RETURNING id`, "group-a")
	gB := mustQueryID(t, db, `INSERT INTO groups (name) VALUES ($1) RETURNING id`, "group-b")

	// Access matrix
	mustExec(t, db, `INSERT INTO group_db_access (group_id, db_id, level) VALUES ($1, $2, 1)`, gA, d.DB1) // A: zeta = View Only
	mustExec(t, db, `INSERT INTO group_db_access (group_id, db_id, level) VALUES ($1, $2, 2)`, gA, d.DB2) // A: alpha = View Edit
	mustExec(t, db, `INSERT INTO group_db_access (group_id, db_id, level) VALUES ($1, $2, 2)`, gB, d.DB1) // B: zeta = View Edit

	// Membership
	mustExec(t, db, `INSERT INTO group_members (group_id, user_id) VALUES ($1, $2)`, gA, d.Alice)
	mustExec(t, db, `INSERT INTO group_members (group_id, user_id) VALUES ($1, $2)`, gA, d.Inactive)
	mustExec(t, db, `INSERT INTO group_members (group_id, user_id) VALUES ($1, $2)`, gA, d.Both)
	mustExec(t, db, `INSERT INTO group_members (group_id, user_id) VALUES ($1, $2)`, gB, d.Both)

	return d
}

func mustQueryID(t *testing.T, db *sql.DB, q string, args ...any) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(q, args...).Scan(&id); err != nil {
		t.Fatalf("insert returning id: %v", err)
	}
	return id
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

func assertAccessible(t *testing.T, ctx context.Context, c contract.AccessChecker, userID int64, want []contract.AccessibleDB) {
	t.Helper()
	got, err := c.ListAccessible(ctx, userID)
	if err != nil {
		t.Fatalf("ListAccessible: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("len(got)=%d, len(want)=%d\ngot=%+v\nwant=%+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}
