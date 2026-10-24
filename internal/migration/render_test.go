package migration

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/example/if3110-milestone1/internal/contract"
	"github.com/example/if3110-milestone1/internal/explain"
)

func TestExplainTemplateRendersPlan(t *testing.T) {
	renderer := NewHTMLRenderer("../../web/templates")
	for _, tc := range []struct {
		name      string
		analyzed  bool
		requested bool
	}{
		{"estimated", false, false},
		{"actual with zero metrics", true, true},
		{"fallback after ANALYZE requested", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := contract.Plan{
				Root: contract.PlanNode{
					NodeType: "Result", StartupCost: 0.25, StartupCostAvailable: true, TotalCost: 12.5, CostAvailable: true, PlanRows: 3, PlanRowsAvailable: true,
					Children: []contract.PlanNode{{NodeType: "Index Scan", TotalCost: 1.75, CostAvailable: true}},
				},
				Analyzed: tc.analyzed,
				Raw:      "<script>unsafe</script>",
			}
			if tc.analyzed {
				zeroTime, zeroRows := 0.0, int64(0)
				plan.PlanningMs = &zeroTime
				plan.ExecutionMs = &zeroTime
				plan.Root.ActualTimeMs = &zeroTime
				plan.Root.ActualRows = &zeroRows
			}
			if tc.requested && !tc.analyzed {
				plan.Notice = "Analisis aktual tidak didukung; hasil berikut adalah estimasi."
			}
			// Match the handler: Plan is a struct value inside map[string]any.
			rec := httptest.NewRecorder()
			err := renderer.Render(rec, "migration/explain.html", contract.PageData{
				Data: map[string]any{
					"DBID": int64(1), "Migration": &Migration{ID: 1, Title: "Explain test"},
					"Plan": plan, "Analyzed": tc.requested,
				},
			})
			if err != nil {
				t.Fatalf("render real EXPLAIN template: %v", err)
			}
			body := rec.Body.String()
			if strings.Contains(body, "Analisis aktual</span>") != tc.analyzed {
				t.Error("label must describe the returned plan, not the requested checkbox")
			}
			if plan.Notice != "" && !strings.Contains(body, plan.Notice) {
				t.Error("fallback explanation missing")
			}
			for _, want := range []string{"Execution plan", "0.25", "12.5", "1.75", "Index Scan", "&lt;script&gt;unsafe&lt;/script&gt;"} {
				if !strings.Contains(body, want) {
					t.Errorf("rendered page is missing %q", want)
				}
			}
			if tc.analyzed {
				for _, want := range []string{"Planning time", "Execution time", ">0.00</strong>", ">0</td>", "0.00 ms"} {
					if !strings.Contains(body, want) {
						t.Errorf("zero-valued actual metric is missing %q", want)
					}
				}
			} else if strings.Contains(body, "Execution time</span>") {
				t.Error("missing optional execution time must not be displayed as an actual metric")
			}
		})
	}
}

func TestExplainTemplateMissingMariaDBMetrics(t *testing.T) {
	plan, err := explain.ParseMySQLPlan(`{"query_block":{"cost":0.00301265,"nested_loop":[{"table":{"table_name":"demo_explain","rows":0}}]}}`, false)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	err = NewHTMLRenderer("../../web/templates").Render(rec, "migration/explain.html", contract.PageData{
		Data: map[string]any{"DBID": int64(2), "Migration": &Migration{ID: 4}, "Plan": plan},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	for _, want := range []string{`class="metric-value text-brand">0.00301265</strong>`, `class="metric-value">—</strong>`, `class="mono">— → 0.00301265</td><td>—</td>`, `class="mono">— → —</td><td>0</td>`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing rendered metric %q", want)
		}
	}
}
