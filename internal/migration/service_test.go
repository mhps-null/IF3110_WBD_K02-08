package migration

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/example/if3110-milestone1/internal/contract"
)

type mockExecutor struct {
	execResult contract.ExecResult
	execErr    error
	planResult contract.Plan
	planErr    error
}

func (m *mockExecutor) Execute(ctx context.Context, db *sql.DB, sqlText string) (contract.ExecResult, error) {
	return m.execResult, m.execErr
}

func (m *mockExecutor) Explain(ctx context.Context, db *sql.DB, sqlText string, analyze bool) (contract.Plan, error) {
	return m.planResult, m.planErr
}

func TestServiceExecutionSuccess(t *testing.T) {
	repo := NewMemoryRepository()
	targetDB := &FakeTargetDB{
		DB:     nil,
		Engine: contract.EnginePostgres,
	}

	exec := &mockExecutor{
		execResult: contract.ExecResult{
			RowsAffected: 1,
			Duration:     12 * time.Millisecond,
		},
	}

	executors := map[contract.Engine]contract.Executor{
		contract.EnginePostgres: exec,
	}

	svc := NewCustomService(repo, targetDB, executors)
	ctx := context.Background()

	draft, err := svc.CreateDraft(ctx, 10, "Create products", "CREATE TABLE products (id int);", 1)
	if err != nil {
		t.Fatalf("failed to create draft: %v", err)
	}

	executed, res, err := svc.ExecuteMigration(ctx, draft.DBID, draft.ID, 1)
	if err != nil {
		t.Fatalf("unexpected error executing migration: %v", err)
	}

	if executed.Status != StatusSuccess {
		t.Errorf("expected status 'success', got '%s'", executed.Status)
	}
	if executed.Version == nil || *executed.Version != 1 {
		t.Errorf("expected version 1, got %v", executed.Version)
	}
	if res.RowsAffected != 1 {
		t.Errorf("expected 1 row affected, got %d", res.RowsAffected)
	}

	_, _, err = svc.ExecuteMigration(ctx, draft.DBID, draft.ID, 1)
	if err == nil {
		t.Errorf("expected error executing already executed migration")
	}
}

func TestServiceExecutionFailure(t *testing.T) {
	repo := NewMemoryRepository()
	targetDB := &FakeTargetDB{
		DB:     nil,
		Engine: contract.EnginePostgres,
	}

	exec := &mockExecutor{
		execErr: errors.New("syntax error at or near 'INVALID'"),
	}

	executors := map[contract.Engine]contract.Executor{
		contract.EnginePostgres: exec,
	}

	svc := NewCustomService(repo, targetDB, executors)
	ctx := context.Background()

	draft, _ := svc.CreateDraft(ctx, 10, "Broken SQL", "INVALID SQL;", 1)

	executed, _, err := svc.ExecuteMigration(ctx, draft.DBID, draft.ID, 1)
	if err == nil {
		t.Fatalf("expected execution error")
	}

	if executed.Status != StatusFailed {
		t.Errorf("expected status 'failed', got '%s'", executed.Status)
	}
	if executed.ErrorMessage == nil || *executed.ErrorMessage != "syntax error at or near 'INVALID'" {
		t.Errorf("expected error message recorded in metadata, got %v", executed.ErrorMessage)
	}
}

func TestServiceExplainMigration(t *testing.T) {
	repo := NewMemoryRepository()
	targetDB := &FakeTargetDB{
		DB:     nil,
		Engine: contract.EnginePostgres,
	}

	expectedPlan := contract.Plan{
		Root: contract.PlanNode{
			NodeType:  "Seq Scan",
			Relation:  "products",
			TotalCost: 35.5,
		},
		Analyzed: false,
		Raw:      `[{"Plan": {"Node Type": "Seq Scan"}}]`,
	}

	exec := &mockExecutor{
		planResult: expectedPlan,
	}

	executors := map[contract.Engine]contract.Executor{
		contract.EnginePostgres: exec,
	}

	svc := NewCustomService(repo, targetDB, executors)
	ctx := context.Background()

	draft, _ := svc.CreateDraft(ctx, 10, "Scan products", "SELECT * FROM products;", 1)

	plan, err := svc.ExplainMigration(ctx, draft.DBID, draft.ID, false)
	if err != nil {
		t.Fatalf("unexpected error running explain: %v", err)
	}

	if plan.Root.NodeType != "Seq Scan" {
		t.Errorf("expected plan root 'Seq Scan', got '%s'", plan.Root.NodeType)
	}
	if plan.Root.TotalCost != 35.5 {
		t.Errorf("expected total cost 35.5, got %f", plan.Root.TotalCost)
	}
}
