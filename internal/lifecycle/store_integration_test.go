//go:build integration

package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func setupLifecycleDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_METADATA_DSN")
	if dsn == "" {
		t.Skip("TEST_METADATA_DSN is not set")
	}

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test metadata DSN: %v", err)
	}
	adminDB := stdlib.OpenDB(*cfg)
	if err := adminDB.Ping(); err != nil {
		adminDB.Close()
		t.Fatalf("connect to test metadata server: %v", err)
	}

	dbName := fmt.Sprintf("test_lifecycle_%d", time.Now().UnixNano())
	if _, err := adminDB.Exec(fmt.Sprintf(`CREATE DATABASE "%s"`, dbName)); err != nil {
		adminDB.Close()
		t.Fatalf("create lifecycle test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adminDB.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS "%s" WITH (FORCE)`, dbName))
		_ = adminDB.Close()
	})

	testCfg := *cfg
	testCfg.Database = dbName
	db := stdlib.OpenDB(testCfg)
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, name := range []string{
		"001_initial_schema.sql",
		"002_session_privilege_version.sql",
		"003_audit_logs.sql",
		"004_group_leader.sql",
		"005_migration_lifecycle.sql",
		"006_lifecycle_execution_runs.sql",
	} {
		script, err := os.ReadFile(filepath.Join("..", "..", "migrations_meta", name))
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := db.ExecContext(ctx, string(script)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
	}
	return db
}

func lifecycleUser(t *testing.T, db *sql.DB, username string) int64 {
	t.Helper()
	var id int64
	err := db.QueryRow(`INSERT INTO users (username,password_hash) VALUES ($1,'x') RETURNING id`, username).Scan(&id)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func lifecycleDatabase(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var id int64
	err := db.QueryRow(`INSERT INTO db_connections (name,engine,host,port,database_name,username,password_enc)
		VALUES ('lifecycle-test','postgres','localhost',5432,'target','user','encrypted') RETURNING id`).Scan(&id)
	if err != nil {
		t.Fatalf("insert database connection: %v", err)
	}
	return id
}

func lifecycleGroup(t *testing.T, db *sql.DB, name string, databaseID int64, level int, leaderID *int64, members ...int64) int64 {
	t.Helper()
	var groupID int64
	if err := db.QueryRow(`INSERT INTO groups(name) VALUES($1) RETURNING id`, name).Scan(&groupID); err != nil {
		t.Fatalf("insert group: %v", err)
	}
	for _, memberID := range members {
		if _, err := db.Exec(`INSERT INTO group_members(group_id,user_id) VALUES($1,$2)`, groupID, memberID); err != nil {
			t.Fatalf("insert group member: %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO group_db_access(group_id,db_id,level) VALUES($1,$2,$3)`, groupID, databaseID, level); err != nil {
		t.Fatalf("insert group database access: %v", err)
	}
	if leaderID != nil {
		if _, err := db.Exec(`UPDATE groups SET leader_user_id=$2 WHERE id=$1`, groupID, *leaderID); err != nil {
			t.Fatalf("set group leader: %v", err)
		}
	}
	return groupID
}

func lifecycleRequest(t *testing.T, store *Store, ctx context.Context, actor, dbID int64, title string) Request {
	t.Helper()
	req, err := store.Create(ctx, actor, DefinitionInput{
		DBID: dbID, Title: title, SQLUp: "SELECT 1", SQLDown: "SELECT 1",
	})
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	return req
}

func TestRefreshReviewAfterLeaderAssignmentAndRecordRejection(t *testing.T) {
	db := setupLifecycleDB(t)
	ctx := context.Background()
	store := NewStore(db)
	author := lifecycleUser(t, db, "review-author")
	newLeader := lifecycleUser(t, db, "review-new-leader")
	dbID := lifecycleDatabase(t, db)
	lifecycleGroup(t, db, "edit-group", dbID, 2, &author, author)
	lifecycleGroup(t, db, "shared-leader-group", dbID, 1, &author, author)
	viewGroup := lifecycleGroup(t, db, "view-group", dbID, 1, nil, newLeader)

	req := lifecycleRequest(t, store, ctx, author, dbID, "wait for leader")
	if _, err := store.Submit(ctx, req.ID, author); err != nil {
		t.Fatalf("submit request: %v", err)
	}
	pending, err := store.Approvals(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 3 {
		t.Fatalf("expected both access groups as stakeholders, got %+v", pending)
	}

	if _, err := store.Approve(ctx, req.ID, author, true); err != nil {
		t.Fatalf("approve author's group: %v", err)
	}
	firstDecisionHistory, err := store.ApprovalHistory(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstDecisionHistory) != 2 {
		t.Fatalf("one approval must cover all groups led by the same user: %+v", firstDecisionHistory)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM migration_requests WHERE id=$1`, req.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != PendingReview {
		t.Fatalf("request with a missing leader must remain pending review, got %q", status)
	}

	if _, err := db.Exec(`UPDATE groups SET leader_user_id=$2 WHERE id=$1`, viewGroup, newLeader); err != nil {
		t.Fatal(err)
	}
	refreshed, err := store.RefreshReview(ctx, req.ID, author)
	if err != nil {
		t.Fatalf("refresh review after leader assignment: %v", err)
	}
	if refreshed.Status != PendingReview {
		t.Fatalf("expected pending review after refresh, got %q", refreshed.Status)
	}
	decisionHistory, err := store.ApprovalHistory(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisionHistory) != 1 || decisionHistory[0].Decision != "approved" || decisionHistory[0].ActorID != author {
		t.Fatalf("refresh must preserve prior decision history: %+v", decisionHistory)
	}
	requirements, err := store.Approvals(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, requirement := range requirements {
		if requirement.Decision != nil || requirement.ApprovedBy != nil {
			t.Fatalf("refresh must require fresh decisions: %+v", requirement)
		}
	}
	if _, err := store.Approve(ctx, req.ID, author, true); err != nil {
		t.Fatalf("re-approve author's group: %v", err)
	}
	if _, err := store.Approve(ctx, req.ID, newLeader, true); err != nil {
		t.Fatalf("approve newly assigned leader's group: %v", err)
	}

	rejectReq := lifecycleRequest(t, store, ctx, author, dbID, "reject audit")
	if _, err := store.Submit(ctx, rejectReq.ID, author); err != nil {
		t.Fatalf("submit reject test request: %v", err)
	}
	rejected, err := store.Approve(ctx, rejectReq.ID, newLeader, false)
	if err != nil {
		t.Fatalf("reject request: %v", err)
	}
	if rejected.Status != Rejected {
		t.Fatalf("expected rejected request, got %q", rejected.Status)
	}
	decisions, err := store.Approvals(ctx, rejectReq.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decision := range decisions {
		if decision.GroupID == viewGroup {
			found = decision.Decision != nil && *decision.Decision == "rejected" &&
				decision.DecidedBy != nil && *decision.DecidedBy == newLeader &&
				decision.DecidedAt != nil
		}
	}
	if !found {
		t.Fatalf("rejection decision must retain actor and timestamp: %+v", decisions)
	}
	rejectionHistory, err := store.ApprovalHistory(ctx, rejectReq.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rejectionHistory) != 1 || rejectionHistory[0].Decision != "rejected" || rejectionHistory[0].ActorID != newLeader {
		t.Fatalf("rejection must persist as an immutable decision event: %+v", rejectionHistory)
	}
}

func TestRecordAttemptReusesExecutionAndPersistsAttemptActor(t *testing.T) {
	db := setupLifecycleDB(t)
	ctx := context.Background()
	store := NewStore(db)
	author := lifecycleUser(t, db, "execution-author")
	workerActor := lifecycleUser(t, db, "execution-actor")
	dbID := lifecycleDatabase(t, db)
	lifecycleGroup(t, db, "execution-group", dbID, 2, &author, author)

	req := lifecycleRequest(t, store, ctx, author, dbID, "retry execution")
	if _, err := store.Submit(ctx, req.ID, author); err != nil {
		t.Fatalf("submit request: %v", err)
	}
	if _, err := store.Approve(ctx, req.ID, author, true); err != nil {
		t.Fatalf("approve request: %v", err)
	}
	if _, err := store.Queue(ctx, req.ID, author); err != nil {
		t.Fatalf("queue request: %v", err)
	}
	if claimed, err := store.Claim(ctx, req.ID); err != nil || !claimed {
		t.Fatalf("claim request: claimed=%v err=%v", claimed, err)
	}

	firstID, err := store.RecordAttempt(ctx, req.ID, workerActor, Failed, 1, "temporary failure")
	if err != nil {
		t.Fatalf("record failed attempt: %v", err)
	}
	if _, err := store.Revise(ctx, req.ID, author, DefinitionInput{
		DBID: dbID, Title: "changed", SQLUp: "SELECT 2", SQLDown: "SELECT 1",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("failed attempt must freeze the revision, got %v", err)
	}
	if _, err := store.Retry(ctx, req.ID); err != nil {
		t.Fatalf("retry failed request: %v", err)
	}
	if claimed, err := store.Claim(ctx, req.ID); err != nil || !claimed {
		t.Fatalf("claim retry: claimed=%v err=%v", claimed, err)
	}
	secondID, err := store.RecordAttempt(ctx, req.ID, author, Success, 2, "")
	if err != nil {
		t.Fatalf("record successful attempt: %v", err)
	}
	if firstID != secondID {
		t.Fatalf("retry must retain one execution ID, got %d and %d", firstID, secondID)
	}
	if _, err := store.Revise(ctx, req.ID, author, DefinitionInput{
		DBID: dbID, Title: "changed", SQLUp: "SELECT 2", SQLDown: "SELECT 1",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("successful attempt must freeze the revision, got %v", err)
	}

	history, err := store.ListHistory(ctx, dbID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].ID != history[1].ID ||
		history[0].AttemptID == history[1].AttemptID ||
		history[0].ActorID != workerActor || history[1].ActorID != author {
		t.Fatalf("expected one execution with two immutable attempts and their actors: %+v", history)
	}
	if strings.TrimSpace(history[0].Status) != Failed || history[1].Status != Success {
		t.Fatalf("attempt outcomes were not preserved: %+v", history)
	}
}
