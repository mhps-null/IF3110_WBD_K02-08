package explain

import (
	"testing"
)

func TestParsePostgresPlan(t *testing.T) {
	pgJSON := `[
  {
    "Plan": {
      "Node Type": "Index Scan",
      "Scan Direction": "Forward",
      "Index Name": "idx_users_username",
      "Relation Name": "users",
      "Alias": "u",
      "Startup Cost": 0.28,
      "Total Cost": 8.29,
      "Plan Rows": 1,
      "Plan Width": 142,
      "Actual Startup Time": 0.015,
      "Actual Total Time": 0.017,
      "Actual Rows": 1,
      "Actual Loops": 1
    },
    "Planning Time": 0.095,
    "Execution Time": 0.038
  }
]`

	plan, err := ParsePostgresPlan(pgJSON, true)
	if err != nil {
		t.Fatalf("unexpected error parsing postgres plan: %v", err)
	}

	if !plan.Analyzed {
		t.Errorf("expected plan.Analyzed to be true")
	}
	if plan.Root.NodeType != "Index Scan" {
		t.Errorf("expected Node Type 'Index Scan', got '%s'", plan.Root.NodeType)
	}
	if plan.Root.Relation != "users" {
		t.Errorf("expected Relation 'users', got '%s'", plan.Root.Relation)
	}
	if plan.Root.IndexName != "idx_users_username" {
		t.Errorf("expected IndexName 'idx_users_username', got '%s'", plan.Root.IndexName)
	}
	if plan.Root.StartupCost != 0.28 || plan.Root.TotalCost != 8.29 {
		t.Errorf("unexpected cost: %f, %f", plan.Root.StartupCost, plan.Root.TotalCost)
	}
	if plan.Root.PlanRows != 1 {
		t.Errorf("expected 1 plan row, got %d", plan.Root.PlanRows)
	}
	if plan.Root.ActualTimeMs == nil || *plan.Root.ActualTimeMs != 0.017 {
		t.Errorf("expected actual time 0.017, got %v", plan.Root.ActualTimeMs)
	}
	if plan.Root.ActualRows == nil || *plan.Root.ActualRows != 1 {
		t.Errorf("expected actual rows 1, got %v", plan.Root.ActualRows)
	}
	if plan.PlanningMs == nil || *plan.PlanningMs != 0.095 {
		t.Errorf("expected planning time 0.095, got %v", plan.PlanningMs)
	}
	if plan.ExecutionMs == nil || *plan.ExecutionMs != 0.038 {
		t.Errorf("expected execution time 0.038, got %v", plan.ExecutionMs)
	}
}

func TestParsePostgresNestedPlan(t *testing.T) {
	pgNested := `[
  {
    "Plan": {
      "Node Type": "Nested Loop",
      "Join Type": "Inner",
      "Startup Cost": 0.56,
      "Total Cost": 16.58,
      "Plan Rows": 1,
      "Plans": [
        {
          "Node Type": "Seq Scan",
          "Relation Name": "departments",
          "Startup Cost": 0.00,
          "Total Cost": 1.20,
          "Plan Rows": 1
        },
        {
          "Node Type": "Index Scan",
          "Relation Name": "employees",
          "Index Name": "idx_emp_dept",
          "Startup Cost": 0.28,
          "Total Cost": 8.29,
          "Plan Rows": 5
        }
      ]
    }
  }
]`

	plan, err := ParsePostgresPlan(pgNested, false)
	if err != nil {
		t.Fatalf("unexpected error parsing nested plan: %v", err)
	}

	if plan.Root.NodeType != "Nested Loop" {
		t.Errorf("expected root 'Nested Loop', got '%s'", plan.Root.NodeType)
	}
	if len(plan.Root.Children) != 2 {
		t.Fatalf("expected 2 child nodes, got %d", len(plan.Root.Children))
	}
	if plan.Root.Children[0].NodeType != "Seq Scan" {
		t.Errorf("expected child 0 to be 'Seq Scan', got '%s'", plan.Root.Children[0].NodeType)
	}
	if plan.Root.Children[1].NodeType != "Index Scan" {
		t.Errorf("expected child 1 to be 'Index Scan', got '%s'", plan.Root.Children[1].NodeType)
	}
}

func TestParseMySQLPlan(t *testing.T) {
	mySQLJSON := `{
  "query_block": {
    "select_id": 1,
    "cost_info": {
      "query_cost": "2.40"
    },
    "table": {
      "table_name": "orders",
      "access_type": "ref",
      "possible_keys": ["idx_cust"],
      "key": "idx_cust",
      "used_key_parts": ["customer_id"],
      "key_length": "8",
      "ref": ["const"],
      "rows_examined_per_scan": 2,
      "rows_produced_per_join": 2
    }
  }
}`

	plan, err := ParseMySQLPlan(mySQLJSON, false)
	if err != nil {
		t.Fatalf("unexpected error parsing mysql plan: %v", err)
	}

	if plan.Root.NodeType != "Table Scan (ref)" {
		t.Errorf("expected 'Table Scan (ref)', got '%s'", plan.Root.NodeType)
	}
	if plan.Root.Relation != "orders" {
		t.Errorf("expected 'orders', got '%s'", plan.Root.Relation)
	}
	if plan.Root.IndexName != "idx_cust" {
		t.Errorf("expected 'idx_cust', got '%s'", plan.Root.IndexName)
	}
	if plan.Root.TotalCost != 2.40 {
		t.Errorf("expected total cost 2.40, got %f", plan.Root.TotalCost)
	}
	if plan.Root.PlanRows != 2 {
		t.Errorf("expected 2 plan rows, got %d", plan.Root.PlanRows)
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := ParsePostgresPlan("", false); err == nil {
		t.Errorf("expected error on empty postgres json")
	}
	if _, err := ParsePostgresPlan("invalid-json", false); err == nil {
		t.Errorf("expected error on invalid postgres json")
	}
	if _, err := ParsePostgresPlan("[]", false); err == nil {
		t.Errorf("expected error on empty array")
	}

	if _, err := ParseMySQLPlan("", false); err == nil {
		t.Errorf("expected error on empty mysql json")
	}
}
