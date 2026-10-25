package schemadiff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

type ChangeType string

const (
	CreateTable    ChangeType = "CREATE_TABLE"
	DropTable      ChangeType = "DROP_TABLE"
	AddColumn      ChangeType = "ADD_COLUMN"
	DropColumn     ChangeType = "DROP_COLUMN"
	AlterColumn    ChangeType = "ALTER_COLUMN"
	AddConstraint  ChangeType = "ADD_CONSTRAINT"
	DropConstraint ChangeType = "DROP_CONSTRAINT"
	AddIndex       ChangeType = "ADD_INDEX"
	DropIndex      ChangeType = "DROP_INDEX"
)

// Change represents one DDL operation required to transform snapshot a into b.
type Change struct {
	Type        ChangeType
	TableName   string
	TableSchema string
	ColumnName  string
	OldColumn   *contract.Column
	NewColumn   *contract.Column
	TableDef    *contract.Table
	Constraint  *contract.Constraint
	Index       *contract.Index
	Destructive bool
}

// Diff compares two snapshots and returns the operations needed to change a into b.
// The result is deterministic so generated migration SQL is stable between runs.
func Diff(a, b contract.SchemaSnapshot) []Change {
	b = targetInSourceNamespace(a, b)
	var changes []Change
	tablesA := make(map[tableIdentity]contract.Table, len(a.Tables))
	tablesB := make(map[tableIdentity]contract.Table, len(b.Tables))
	for _, table := range a.Tables {
		tablesA[tableIdentity{table.Schema, table.Name}] = table
	}
	for _, table := range b.Tables {
		tablesB[tableIdentity{table.Schema, table.Name}] = table
	}

	for _, name := range sortedTableKeys(tablesA) {
		tA := tablesA[name]
		tB, exists := tablesB[name]
		if !exists {
			changes = append(changes, Change{Type: DropTable, TableSchema: name.schema, TableName: name.name, Destructive: true})
			continue
		}
		tableChanges := append(diffColumns(tA, tB), diffConstraints(tA, tB)...)
		tableChanges = append(tableChanges, diffIndexes(tA, tB)...)
		for i := range tableChanges {
			tableChanges[i].TableSchema = name.schema
		}
		changes = append(changes, tableChanges...)
	}

	for _, name := range sortedTableKeys(tablesB) {
		if _, exists := tablesA[name]; exists {
			continue
		}
		tableCopy := tablesB[name]
		changes = append(changes, Change{Type: CreateTable, TableSchema: name.schema, TableName: name.name, TableDef: &tableCopy})
		// Constraints and indexes are separate operations so foreign keys can be
		// created only after every target table exists.
		for _, constraint := range sortedConstraints(tableCopy.Constraints) {
			constraintCopy := constraint
			changes = append(changes, Change{Type: AddConstraint, TableSchema: name.schema, TableName: name.name, Constraint: &constraintCopy})
		}
		for _, index := range sortedIndexes(indexesWithoutConstraintBacking(tableCopy)) {
			indexCopy := index
			changes = append(changes, Change{Type: AddIndex, TableSchema: name.schema, TableName: name.name, Index: &indexCopy})
		}
	}
	return changes
}

func diffColumns(tA, tB contract.Table) []Change {
	var changes []Change
	colsA := make(map[string]contract.Column, len(tA.Columns))
	colsB := make(map[string]contract.Column, len(tB.Columns))
	for _, column := range tA.Columns {
		colsA[column.Name] = column
	}
	for _, column := range tB.Columns {
		colsB[column.Name] = column
	}

	for _, name := range sortedKeys(colsA) {
		oldColumn := colsA[name]
		newColumn, exists := colsB[name]
		if !exists {
			oldCopy := oldColumn
			changes = append(changes, Change{Type: DropColumn, TableName: tA.Name, ColumnName: name, OldColumn: &oldCopy, Destructive: true})
			continue
		}
		if oldColumn.DataType != newColumn.DataType || oldColumn.Nullable != newColumn.Nullable || !defaultsEqual(oldColumn.Default, newColumn.Default) {
			oldCopy, newCopy := oldColumn, newColumn
			changes = append(changes, Change{
				Type:        AlterColumn,
				TableName:   tA.Name,
				ColumnName:  name,
				OldColumn:   &oldCopy,
				NewColumn:   &newCopy,
				Destructive: oldColumn.DataType != newColumn.DataType || (oldColumn.Nullable && !newColumn.Nullable),
			})
		}
	}
	for _, name := range sortedKeys(colsB) {
		if _, exists := colsA[name]; exists {
			continue
		}
		newCopy := colsB[name]
		changes = append(changes, Change{Type: AddColumn, TableName: tB.Name, ColumnName: name, NewColumn: &newCopy})
	}
	return changes
}

func diffConstraints(tA, tB contract.Table) []Change {
	var changes []Change
	constraintsA := make(map[string]contract.Constraint, len(tA.Constraints))
	constraintsB := make(map[string]contract.Constraint, len(tB.Constraints))
	for _, constraint := range tA.Constraints {
		constraintsA[constraint.Name] = constraint
	}
	for _, constraint := range tB.Constraints {
		constraintsB[constraint.Name] = constraint
	}
	for _, name := range sortedKeys(constraintsA) {
		oldConstraint := constraintsA[name]
		newConstraint, exists := constraintsB[name]
		if !exists || !sameConstraint(oldConstraint, newConstraint) {
			oldCopy := oldConstraint
			changes = append(changes, Change{Type: DropConstraint, TableName: tA.Name, Constraint: &oldCopy, Destructive: true})
		}
	}
	for _, name := range sortedKeys(constraintsB) {
		newConstraint := constraintsB[name]
		oldConstraint, exists := constraintsA[name]
		if !exists || !sameConstraint(oldConstraint, newConstraint) {
			newCopy := newConstraint
			changes = append(changes, Change{Type: AddConstraint, TableName: tB.Name, Constraint: &newCopy})
		}
	}
	return changes
}

func diffIndexes(tA, tB contract.Table) []Change {
	var changes []Change
	indexesA := indexesWithoutConstraintBacking(tA)
	indexesB := indexesWithoutConstraintBacking(tB)
	indexMapA := make(map[string]contract.Index, len(indexesA))
	indexMapB := make(map[string]contract.Index, len(indexesB))
	for _, index := range indexesA {
		indexMapA[index.Name] = index
	}
	for _, index := range indexesB {
		indexMapB[index.Name] = index
	}
	for _, name := range sortedKeys(indexMapA) {
		oldIndex := indexMapA[name]
		newIndex, exists := indexMapB[name]
		if !exists || !sameIndex(oldIndex, newIndex) {
			oldCopy := oldIndex
			changes = append(changes, Change{Type: DropIndex, TableName: tA.Name, Index: &oldCopy})
		}
	}
	for _, name := range sortedKeys(indexMapB) {
		newIndex := indexMapB[name]
		oldIndex, exists := indexMapA[name]
		if !exists || !sameIndex(oldIndex, newIndex) {
			newCopy := newIndex
			changes = append(changes, Change{Type: AddIndex, TableName: tB.Name, Index: &newCopy})
		}
	}
	return changes
}

func sameConstraint(a, b contract.Constraint) bool {
	return a.Name == b.Name && a.Type == b.Type && sameStrings(a.Columns, b.Columns) &&
		a.RefTable == b.RefTable && a.RefSchema == b.RefSchema && a.RefTableName == b.RefTableName &&
		sameStrings(a.RefColumns, b.RefColumns) && a.Definition == b.Definition &&
		strings.EqualFold(a.OnDelete, b.OnDelete) && strings.EqualFold(a.OnUpdate, b.OnUpdate)
}

func sameIndex(a, b contract.Index) bool {
	return a.Name == b.Name && sameStrings(a.Columns, b.Columns) && a.Unique == b.Unique &&
		a.Predicate == b.Predicate && sameStrings(a.KeyDefinitions, b.KeyDefinitions) &&
		strings.EqualFold(strings.TrimSpace(a.Method), strings.TrimSpace(b.Method))
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// PostgreSQL and MySQL/MariaDB report backing indexes for PRIMARY KEY and
// UNIQUE constraints. Those indexes are created/dropped with the constraint.
func indexesWithoutConstraintBacking(table contract.Table) []contract.Index {
	var result []contract.Index
	for _, index := range table.Indexes {
		backing := false
		for _, constraint := range table.Constraints {
			constraintType := strings.ToUpper(strings.TrimSpace(constraint.Type))
			if (constraintType != "PRIMARY KEY" && constraintType != "UNIQUE") || constraint.Name != index.Name {
				continue
			}
			if sameStrings(constraint.Columns, index.Columns) && index.Unique {
				backing = true
				break
			}
		}
		if !backing {
			result = append(result, index)
		}
	}
	return result
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedConstraints(values []contract.Constraint) []contract.Constraint {
	result := append([]contract.Constraint(nil), values...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].Type < result[j].Type
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func sortedIndexes(values []contract.Index) []contract.Index {
	result := append([]contract.Index(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func defaultsEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// FormatChange provides a human-readable description of a schema operation.
func FormatChange(change Change) string {
	if change.TableSchema != "" {
		change.TableName = change.TableSchema + "." + change.TableName
	}
	switch change.Type {
	case CreateTable:
		return fmt.Sprintf("Create table %s", change.TableName)
	case DropTable:
		return fmt.Sprintf("Drop table %s", change.TableName)
	case AddColumn:
		return fmt.Sprintf("Add column %s.%s (%s)", change.TableName, change.ColumnName, change.NewColumn.DataType)
	case DropColumn:
		return fmt.Sprintf("Drop column %s.%s", change.TableName, change.ColumnName)
	case AlterColumn:
		return fmt.Sprintf("Alter column %s.%s (from %s to %s)", change.TableName, change.ColumnName, change.OldColumn.DataType, change.NewColumn.DataType)
	case AddConstraint:
		if change.Constraint != nil {
			return fmt.Sprintf("Add %s constraint %s on %s", strings.ToLower(change.Constraint.Type), change.Constraint.Name, change.TableName)
		}
	case DropConstraint:
		if change.Constraint != nil {
			return fmt.Sprintf("Drop %s constraint %s on %s", strings.ToLower(change.Constraint.Type), change.Constraint.Name, change.TableName)
		}
	case AddIndex:
		if change.Index != nil {
			return fmt.Sprintf("Add index %s on %s", change.Index.Name, change.TableName)
		}
	case DropIndex:
		if change.Index != nil {
			return fmt.Sprintf("Drop index %s on %s", change.Index.Name, change.TableName)
		}
	}
	return string(change.Type)
}
