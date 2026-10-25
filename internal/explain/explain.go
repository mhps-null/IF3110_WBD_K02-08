package explain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

type postgresExplainWrapper struct {
	Plan          postgresPlanNode `json:"Plan"`
	PlanningTime  *float64         `json:"Planning Time"`
	ExecutionTime *float64         `json:"Execution Time"`
}

type postgresPlanNode struct {
	NodeType     string             `json:"Node Type"`
	RelationName string             `json:"Relation Name"`
	IndexName    string             `json:"Index Name"`
	JoinType     string             `json:"Join Type"`
	StartupCost  float64            `json:"Startup Cost"`
	TotalCost    float64            `json:"Total Cost"`
	PlanRows     float64            `json:"Plan Rows"`
	ActualTime   *float64           `json:"Actual Total Time"`
	ActualRows   *float64           `json:"Actual Rows"`
	ActualLoops  *int64             `json:"Actual Loops"`
	Plans        []postgresPlanNode `json:"Plans"`
}

func ParsePostgresPlan(raw string, analyzed bool) (contract.Plan, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return contract.Plan{}, errors.New("empty explain output")
	}

	var wrappers []postgresExplainWrapper
	if err := json.Unmarshal([]byte(trimmed), &wrappers); err != nil {
		return contract.Plan{}, fmt.Errorf("failed to unmarshal postgres explain json: %w", err)
	}

	if len(wrappers) == 0 {
		return contract.Plan{}, errors.New("no plan nodes found in explain output")
	}

	wrapper := wrappers[0]
	rootNode := convertPostgresNode(wrapper.Plan)

	return contract.Plan{
		Root:        rootNode,
		Analyzed:    analyzed,
		PlanningMs:  wrapper.PlanningTime,
		ExecutionMs: wrapper.ExecutionTime,
		Raw:         trimmed,
	}, nil
}

func convertPostgresNode(node postgresPlanNode) contract.PlanNode {
	planNode := contract.PlanNode{
		NodeType:             node.NodeType,
		Relation:             node.RelationName,
		IndexName:            node.IndexName,
		JoinType:             node.JoinType,
		StartupCost:          node.StartupCost,
		TotalCost:            node.TotalCost,
		CostAvailable:        true,
		StartupCostAvailable: true,
		PlanRows:             int64(node.PlanRows),
		PlanRowsAvailable:    true,
		ActualLoops:          node.ActualLoops,
	}

	if node.ActualTime != nil {
		actualTime := *node.ActualTime
		planNode.ActualTimeMs = &actualTime
	}
	if node.ActualRows != nil {
		actualRows := int64(*node.ActualRows)
		planNode.ActualRows = &actualRows
	}

	for _, child := range node.Plans {
		planNode.Children = append(planNode.Children, convertPostgresNode(child))
	}

	return planNode
}
