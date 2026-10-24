package explain

import (
	"strings"
	"testing"
)

func TestParseMySQLAnalyze(t *testing.T) {
	raw := `-> Nested loop inner join  (cost=1.2e+3 rows=2.5) (actual time=0.1..0.2 rows=1.5 loops=2)
    -> Table scan on users  (cost=2 rows=4) (never executed)
    -> Index lookup on orders using idx_user (cost=1 rows=2)
       (actual time=0..0 rows=0 loops=1)`
	plan, err := ParseMySQLAnalyze(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Analyzed || plan.Root.TotalCost != 1200 || !plan.Root.CostAvailable || plan.Root.StartupCostAvailable || !plan.Root.PlanRowsAvailable || plan.ExecutionMs != nil {
		t.Fatalf("incorrect root metrics: %+v", plan)
	}
	if plan.Root.ActualRowsPerLoop == nil || *plan.Root.ActualRowsPerLoop != 1.5 || *plan.Root.ActualLoops != 2 {
		t.Fatalf("fractional rows/loops lost: %+v", plan.Root)
	}
	if len(plan.Root.Children) != 2 || *plan.Root.Children[0].ActualLoops != 0 || plan.Root.Children[0].ActualTimeMs != nil {
		t.Fatal("never-executed node must preserve missing metrics")
	}
	child := plan.Root.Children[1]
	if child.IndexName != "idx_user" || child.Relation != "orders" || *child.ActualRowsPerLoop != 0 || *child.ActualTimeMs != 0 {
		t.Fatalf("zero metrics or identifiers lost: %+v", child)
	}
	zero, err := ParseMySQLAnalyze("-> Rows fetched before execution (actual time=0..0 rows=1 loops=1)")
	if err != nil || zero.ExecutionMs == nil || *zero.ExecutionMs != 0 {
		t.Fatalf("root execution time: %+v, %v", zero, err)
	}
	for _, invalid := range []string{"unexpected output", "-> Result (cost=1 rows=1)"} {
		if _, err := ParseMySQLAnalyze(invalid); err == nil {
			t.Fatal("output without actual analysis must fail")
		}
	}
}

func TestParseMariaDBEstimatedMetrics(t *testing.T) {
	plan, err := ParseMySQLPlan(`{"query_block":{"cost":0.00301265,"nested_loop":[{"table":{"table_name":"demo_explain","access_type":"range","rows":0}}]}}`, false)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Root.CostAvailable || plan.Root.TotalCost != 0.00301265 || plan.Root.StartupCostAvailable || plan.Root.PlanRowsAvailable {
		t.Fatalf("root must retain direct cost without inventing missing metrics: %+v", plan.Root)
	}
	child := plan.Root.Children[0]
	if !child.PlanRowsAvailable || child.PlanRows != 0 || child.CostAvailable {
		t.Fatalf("explicit zero rows must remain distinguishable from missing metrics: %+v", child)
	}
}

func TestParseMariaDBAnalyzeJSON(t *testing.T) {
	raw := `{"query_optimization":{"r_total_time_ms":0},"query_block":{
		"select_id":1,"r_total_time_ms":0,"r_loops":1,
		"nested_loop":[{"read_sorted_file":{"filesort":{"r_total_time_ms":0,
		"table":{"table_name":"items","access_type":"ALL","rows":2,"r_rows":0.5,
		"r_loops":2,"r_table_time_ms":0,"r_other_time_ms":0}}}}]}}`
	plan, err := ParseMySQLPlan(raw, true)
	if err != nil || !plan.Analyzed || plan.ExecutionMs == nil || *plan.ExecutionMs != 0 || plan.PlanningMs == nil {
		t.Fatalf("analysis: %+v err=%v", plan, err)
	}
	node := plan.Root
	for len(node.Children) > 0 {
		node = node.Children[0]
	}
	if node.Relation != "items" || node.ActualRowsPerLoop == nil || *node.ActualRowsPerLoop != 0.5 ||
		node.ActualTimeMs == nil || *node.ActualTimeMs != 0 || node.ActualLoops == nil || *node.ActualLoops != 2 {
		t.Fatalf("nested table metrics: %+v", node)
	}
	if node.CostAvailable {
		t.Fatal("missing cost must not be reported as measured zero")
	}
	for _, invalid := range []string{"", "invalid JSON", "{}"} {
		if _, err := ParseMySQLPlan(invalid, true); err == nil {
			t.Fatal("invalid JSON must fail instead of claiming successful analysis")
		}
	}
	if !strings.Contains(plan.Raw, "r_rows") {
		t.Fatal("raw output missing")
	}
}
