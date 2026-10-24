package explain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/example/if3110-milestone1/internal/contract"
)

type postgresExplainWrapper struct {
	Plan         postgresPlanNode `json:"Plan"`
	PlanningTime *float64         `json:"Planning Time"`
	ExecutionTime *float64        `json:"Execution Time"`
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
		NodeType:    node.NodeType,
		Relation:    node.RelationName,
		IndexName:   node.IndexName,
		JoinType:    node.JoinType,
		StartupCost: node.StartupCost,
		TotalCost:   node.TotalCost,
		PlanRows:    int64(node.PlanRows),
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

func ParseMySQLPlan(raw string, analyzed bool) (contract.Plan, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return contract.Plan{}, errors.New("empty explain output")
	}

	var rootMap map[string]any
	if err := json.Unmarshal([]byte(trimmed), &rootMap); err != nil {
		return contract.Plan{
			Root: contract.PlanNode{
				NodeType: "Execution Plan",
			},
			Analyzed: analyzed,
			Raw:      trimmed,
		}, nil
	}

	queryBlock, _ := rootMap["query_block"].(map[string]any)
	rootNode := extractMySQLNode(queryBlock)

	return contract.Plan{
		Root:     rootNode,
		Analyzed: analyzed,
		Raw:      trimmed,
	}, nil
}

func extractMySQLNode(block map[string]any) contract.PlanNode {
	if block == nil {
		return contract.PlanNode{NodeType: "Query"}
	}

	node := contract.PlanNode{
		NodeType: "Query Block",
	}

	if costInfo, ok := block["cost_info"].(map[string]any); ok {
		if queryCostStr, ok := costInfo["query_cost"].(string); ok {
			if cost, err := strconv.ParseFloat(queryCostStr, 64); err == nil {
				node.TotalCost = cost
			}
		}
	}

	if tableInfo, ok := block["table"].(map[string]any); ok {
		if tblName, ok := tableInfo["table_name"].(string); ok {
			node.Relation = tblName
		}
		if accessType, ok := tableInfo["access_type"].(string); ok {
			node.NodeType = fmt.Sprintf("Table Scan (%s)", accessType)
		}
		if keyName, ok := tableInfo["key"].(string); ok {
			node.IndexName = keyName
		}
		if rowsExamined, ok := tableInfo["rows_examined_per_scan"].(float64); ok {
			node.PlanRows = int64(rowsExamined)
		}
	}

	if nestedLoop, ok := block["nested_loop"].([]any); ok {
		node.JoinType = "Nested Loop"
		for _, item := range nestedLoop {
			if itemMap, ok := item.(map[string]any); ok {
				if tbl, ok := itemMap["table"].(map[string]any); ok {
					subBlock := map[string]any{"table": tbl}
					node.Children = append(node.Children, extractMySQLNode(subBlock))
				}
			}
		}
	}

	return node
}
