package explain

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

// ParseMySQLPlan handles estimated JSON from both engines and MariaDB ANALYZE JSON.
func ParseMySQLPlan(raw string, analyzed bool) (contract.Plan, error) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return contract.Plan{}, fmt.Errorf("invalid EXPLAIN JSON: %w", err)
	}
	block, ok := doc["query_block"].(map[string]any)
	if !ok {
		return contract.Plan{}, errors.New("EXPLAIN JSON has no query_block")
	}
	plan := contract.Plan{Root: jsonPlanNode(block, "Query Block", analyzed), Analyzed: analyzed, Raw: raw}
	if analyzed {
		plan.ExecutionMs = planNumber(block, "r_total_time_ms")
		if optimization, ok := doc["query_optimization"].(map[string]any); ok {
			plan.PlanningMs = planNumber(optimization, "r_total_time_ms")
		}
	}
	return plan, nil
}

func planNumber(block map[string]any, key string) *float64 {
	var value float64
	switch n := block[key].(type) {
	case float64:
		value = n
	case string:
		parsed, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return nil
		}
		value = parsed
	default:
		return nil
	}
	return &value
}

func jsonPlanNode(block map[string]any, label string, analyzed bool) contract.PlanNode {
	node := contract.PlanNode{NodeType: label}
	if name, ok := block["table_name"].(string); ok {
		node.Relation = name
		access, _ := block["access_type"].(string)
		node.NodeType = "Table Scan (" + access + ")"
		node.IndexName, _ = block["key"].(string)
	}
	if message, ok := block["message"].(string); ok {
		node.NodeType = message
	}
	if value := planNumber(block, "cost"); value != nil {
		node.TotalCost, node.CostAvailable = *value, true
	}
	if cost, ok := block["cost_info"].(map[string]any); ok {
		for _, key := range []string{"query_cost", "prefix_cost"} {
			if value := planNumber(cost, key); value != nil {
				node.TotalCost, node.CostAvailable = *value, true
				break
			}
		}
	}
	for _, key := range []string{"rows_examined_per_scan", "rows"} {
		if value := planNumber(block, key); value != nil {
			node.PlanRows, node.PlanRowsAvailable = int64(*value), true
			break
		}
	}
	if analyzed {
		node.ActualTimeMs = planNumber(block, "r_total_time_ms")
		// MariaDB table nodes report table and other time separately.
		if node.ActualTimeMs == nil {
			tableTime, otherTime := planNumber(block, "r_table_time_ms"), planNumber(block, "r_other_time_ms")
			if tableTime != nil && otherTime != nil {
				total := *tableTime + *otherTime
				node.ActualTimeMs = &total
			}
		}
		node.ActualRowsPerLoop = planNumber(block, "r_rows")
		if loops := planNumber(block, "r_loops"); loops != nil {
			value := int64(*loops)
			node.ActualLoops = &value
		}
	}
	if table, ok := block["table"].(map[string]any); ok {
		child := jsonPlanNode(table, "Table", analyzed)
		if node.ActualTimeMs == nil && node.ActualLoops == nil {
			// Preserve the existing flattened shape for a simple estimated plan.
			if node.CostAvailable {
				child.TotalCost, child.CostAvailable = node.TotalCost, true
			}
			node = child
		} else {
			node.Children = append(node.Children, child)
		}
	}
	if loop, ok := block["nested_loop"].([]any); ok {
		node.JoinType = "Nested Loop"
		for _, entry := range loop {
			if child, ok := entry.(map[string]any); ok {
				node.Children = append(node.Children, jsonPlanNode(child, "Nested Loop", analyzed))
			}
		}
	}
	// Retain plan nodes nested under sort, materialization, and subquery wrappers.
	for _, key := range []string{"read_sorted_file", "filesort", "ordering_operation", "grouping_operation", "duplicates_removal", "temporary_table", "materialized", "query_block", "union_result", "block-nl-join"} {
		if child, ok := block[key].(map[string]any); ok {
			node.Children = append(node.Children, jsonPlanNode(child, key, analyzed))
		}
	}
	for _, key := range []string{"subqueries", "query_specifications"} {
		if entries, ok := block[key].([]any); ok {
			for _, entry := range entries {
				if child, ok := entry.(map[string]any); ok {
					node.Children = append(node.Children, jsonPlanNode(child, key, analyzed))
				}
			}
		}
	}
	return node
}

const planNumericPattern = `[-+]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][-+]?\d+)?`

var (
	treeCost     = regexp.MustCompile(`\(cost=(` + planNumericPattern + `)(?:\.\.(` + planNumericPattern + `))? rows=(` + planNumericPattern + `)\)`)
	treeActual   = regexp.MustCompile(`\(actual time=(` + planNumericPattern + `)\.\.(` + planNumericPattern + `) rows=(` + planNumericPattern + `) loops=(\d+)\)`)
	treeRelation = regexp.MustCompile(`\bon\s+([^\s(]+)`)
	treeIndex    = regexp.MustCompile(`\busing\s+([^\s(]+)`)
)

type treeEntry struct {
	depth    int
	line     string
	node     contract.PlanNode
	children []*treeEntry
}

// ParseMySQLAnalyze parses TREE output, retaining fractional average rows and loops.
func ParseMySQLAnalyze(raw string) (contract.Plan, error) {
	var entries []*treeEntry
	for _, line := range strings.Split(raw, "\n") {
		if position := strings.Index(line, "->"); position >= 0 {
			entries = append(entries, &treeEntry{depth: position, line: strings.TrimSpace(line[position+2:])})
		} else if len(entries) > 0 {
			entries[len(entries)-1].line += " " + strings.TrimSpace(line)
		}
	}
	if len(entries) == 0 {
		return contract.Plan{}, errors.New("EXPLAIN ANALYZE has no TREE nodes")
	}
	var root *treeEntry
	var stack []*treeEntry
	metricsFound := false
	for _, entry := range entries {
		description := entry.line
		for _, marker := range []string{"(cost=", "(actual time=", "(never executed)"} {
			if position := strings.Index(description, marker); position >= 0 {
				description = description[:position]
			}
		}
		entry.node.NodeType = strings.TrimSpace(description)
		if match := treeCost.FindStringSubmatch(entry.line); match != nil {
			total := match[1]
			if match[2] != "" {
				entry.node.StartupCost, _ = strconv.ParseFloat(match[1], 64)
				entry.node.StartupCostAvailable = true
				total = match[2]
			}
			entry.node.TotalCost, _ = strconv.ParseFloat(total, 64)
			entry.node.CostAvailable = true
			rows, _ := strconv.ParseFloat(match[3], 64)
			entry.node.PlanRows = int64(rows)
			entry.node.PlanRowsAvailable = true
		}
		if match := treeActual.FindStringSubmatch(entry.line); match != nil {
			elapsed, _ := strconv.ParseFloat(match[2], 64)
			rows, _ := strconv.ParseFloat(match[3], 64)
			loops, err := strconv.ParseInt(match[4], 10, 64)
			if err != nil {
				return contract.Plan{}, errors.New("invalid EXPLAIN ANALYZE loop count")
			}
			entry.node.ActualTimeMs, entry.node.ActualRowsPerLoop, entry.node.ActualLoops = &elapsed, &rows, &loops
			metricsFound = true
		} else if strings.Contains(entry.line, "(never executed)") {
			zero := int64(0)
			entry.node.ActualLoops = &zero
			metricsFound = true
		}
		if match := treeRelation.FindStringSubmatch(description); match != nil {
			entry.node.Relation = strings.Trim(match[1], "`")
		}
		if match := treeIndex.FindStringSubmatch(description); match != nil {
			entry.node.IndexName = strings.Trim(match[1], "`")
		}
		if strings.Contains(strings.ToLower(description), "join") {
			entry.node.JoinType = entry.node.NodeType
		}
		for len(stack) > 0 && stack[len(stack)-1].depth >= entry.depth {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			if root != nil {
				return contract.Plan{}, errors.New("EXPLAIN ANALYZE has multiple roots")
			}
			root = entry
		} else {
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, entry)
		}
		stack = append(stack, entry)
	}
	if !metricsFound {
		return contract.Plan{}, errors.New("EXPLAIN ANALYZE TREE has no actual execution metrics")
	}
	var convert func(*treeEntry) contract.PlanNode
	convert = func(entry *treeEntry) contract.PlanNode {
		node := entry.node
		for _, child := range entry.children {
			node.Children = append(node.Children, convert(child))
		}
		return node
	}
	plan := contract.Plan{Root: convert(root), Analyzed: true, Raw: raw}
	if plan.Root.ActualLoops != nil && *plan.Root.ActualLoops == 1 {
		plan.ExecutionMs = plan.Root.ActualTimeMs
	}
	return plan, nil
}
