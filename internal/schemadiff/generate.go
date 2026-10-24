package schemadiff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/example/if3110-milestone1/internal/contract"
)

// GenerateSQL converts schema changes to reviewable DDL for the selected engine.
func GenerateSQL(changes []Change, engine contract.Engine) string {
	if len(changes) == 0 {
		return "-- No schema changes detected."
	}
	sorted := sortChanges(changes)
	var builder strings.Builder
	for _, change := range sorted {
		if change.Destructive {
			builder.WriteString("-- WARNING: DESTRUCTIVE CHANGE\n")
		}
		builder.WriteString(generateSingle(change, engine))
		builder.WriteString("\n\n")
	}
	return strings.TrimSpace(builder.String())
}

func sortChanges(changes []Change) []Change {
	sorted := append([]Change(nil), changes...)
	weight := map[ChangeType]int{
		CreateTable: 1, DropConstraint: 2, DropIndex: 3,
		AddColumn: 4, AlterColumn: 5, DropColumn: 6, DropTable: 7,
		AddConstraint: 8, AddIndex: 9,
	}
	operationWeight := func(change Change) int {
		// Referenced unique keys and supporting indexes must exist before FKs.
		// Creating a MySQL FK first can auto-create an index that collides with
		// the explicit index from the reference snapshot.
		if change.Type == AddConstraint && change.Constraint != nil && strings.EqualFold(change.Constraint.Type, "FOREIGN KEY") {
			return 10
		}
		return weight[change.Type]
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		left, right := sorted[i], sorted[j]
		if operationWeight(left) != operationWeight(right) {
			return operationWeight(left) < operationWeight(right)
		}
		if left.TableSchema != right.TableSchema {
			return left.TableSchema < right.TableSchema
		}
		if left.TableName != right.TableName {
			return left.TableName < right.TableName
		}
		return changeObjectName(left) < changeObjectName(right)
	})
	return sorted
}

func changeObjectName(change Change) string {
	switch change.Type {
	case AddConstraint, DropConstraint:
		if change.Constraint != nil {
			return change.Constraint.Name
		}
	case AddIndex, DropIndex:
		if change.Index != nil {
			return change.Index.Name
		}
	case AddColumn, DropColumn, AlterColumn:
		return change.ColumnName
	}
	return ""
}

func generateSingle(change Change, engine contract.Engine) string {
	switch change.Type {
	case CreateTable:
		return generateCreateTable(change.TableDef, engine)
	case DropTable:
		return fmt.Sprintf("DROP TABLE %s;", qualify(change.TableSchema, change.TableName, engine))
	case AddColumn:
		return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;", qualify(change.TableSchema, change.TableName, engine), formatColumnDef(change.ColumnName, change.NewColumn, engine))
	case DropColumn:
		return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", qualify(change.TableSchema, change.TableName, engine), quote(change.ColumnName, engine))
	case AlterColumn:
		return generateAlterColumn(change, engine)
	case AddConstraint:
		return generateAddConstraint(change, engine)
	case DropConstraint:
		return generateDropConstraint(change, engine)
	case AddIndex:
		return generateAddIndex(change, engine)
	case DropIndex:
		return generateDropIndex(change, engine)
	default:
		return "-- Unknown change type: " + string(change.Type)
	}
}

func generateCreateTable(table *contract.Table, engine contract.Engine) string {
	if table == nil {
		return "-- Cannot generate CREATE TABLE: table definition is missing."
	}
	columns := make([]string, 0, len(table.Columns))
	for _, column := range table.Columns {
		columns = append(columns, "  "+formatColumnDef(column.Name, &column, engine))
	}
	return fmt.Sprintf("CREATE TABLE %s (\n%s\n);", qualify(table.Schema, table.Name, engine), strings.Join(columns, ",\n"))
}

func formatColumnDef(name string, column *contract.Column, engine contract.Engine) string {
	if column == nil {
		return quote(name, engine) + " /* missing column definition */"
	}
	definition := fmt.Sprintf("%s %s", quote(name, engine), column.DataType)
	if !column.Nullable {
		definition += " NOT NULL"
	}
	if column.Default != nil {
		definition += " DEFAULT " + *column.Default
	}
	return definition
}

func generateAlterColumn(change Change, engine contract.Engine) string {
	if change.OldColumn == nil || change.NewColumn == nil {
		return "-- Cannot generate ALTER COLUMN: column definition is missing."
	}
	table := qualify(change.TableSchema, change.TableName, engine)
	columnName := quote(change.ColumnName, engine)
	oldColumn, newColumn := change.OldColumn, change.NewColumn
	if engine == contract.EngineMySQL || engine == contract.EngineMariaDB {
		return fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s;", table, formatColumnDef(change.ColumnName, newColumn, engine))
	}
	var statements []string
	if oldColumn.DataType != newColumn.DataType {
		statements = append(statements, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s;", table, columnName, newColumn.DataType))
	}
	if oldColumn.Nullable != newColumn.Nullable {
		if newColumn.Nullable {
			statements = append(statements, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP NOT NULL;", table, columnName))
		} else {
			statements = append(statements, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET NOT NULL;", table, columnName))
		}
	}
	if !defaultsEqual(oldColumn.Default, newColumn.Default) {
		if newColumn.Default == nil {
			statements = append(statements, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", table, columnName))
		} else {
			statements = append(statements, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s;", table, columnName, *newColumn.Default))
		}
	}
	return strings.Join(statements, "\n")
}

func generateAddConstraint(change Change, engine contract.Engine) string {
	if change.Constraint == nil {
		return "-- Cannot generate ADD CONSTRAINT: constraint definition is missing."
	}
	definition, err := formatConstraintDefinition(*change.Constraint, engine)
	if err != nil {
		return "-- Cannot generate ADD CONSTRAINT: " + err.Error()
	}
	if engine != contract.EnginePostgres && strings.EqualFold(strings.TrimSpace(change.Constraint.Type), "PRIMARY KEY") {
		// MySQL reserves PRIMARY as the primary index name; ADD CONSTRAINT
		// `PRIMARY` PRIMARY KEY is rejected by MariaDB.
		return fmt.Sprintf("ALTER TABLE %s ADD %s;", qualify(change.TableSchema, change.TableName, engine), definition)
	}
	return fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s %s;", qualify(change.TableSchema, change.TableName, engine), quote(change.Constraint.Name, engine), definition)
}

func formatConstraintDefinition(constraint contract.Constraint, engine contract.Engine) (string, error) {
	constraintType := strings.ToUpper(strings.TrimSpace(constraint.Type))
	if engine == contract.EnginePostgres && strings.TrimSpace(constraint.Definition) != "" {
		return strings.TrimSpace(constraint.Definition), nil
	}
	switch constraintType {
	case "PRIMARY KEY":
		return "PRIMARY KEY (" + quoteNames(constraint.Columns, engine) + ")", nil
	case "UNIQUE":
		return "UNIQUE (" + quoteNames(constraint.Columns, engine) + ")", nil
	case "FOREIGN KEY":
		if len(constraint.Columns) == 0 || len(constraint.RefColumns) == 0 || len(constraint.Columns) != len(constraint.RefColumns) || (constraint.RefTable == "" && constraint.RefTableName == "") {
			return "", fmt.Errorf("foreign key %q is missing columns or referenced table metadata", constraint.Name)
		}
		reference := quoteQualified(constraint.RefTable, engine)
		if constraint.RefTableName != "" {
			reference = qualify(constraint.RefSchema, constraint.RefTableName, engine)
		}
		definition := "FOREIGN KEY (" + quoteNames(constraint.Columns, engine) + ") REFERENCES " + reference + " (" + quoteNames(constraint.RefColumns, engine) + ")"
		for _, rule := range []struct{ clause, action string }{{"ON DELETE", constraint.OnDelete}, {"ON UPDATE", constraint.OnUpdate}} {
			action := strings.ToUpper(strings.TrimSpace(rule.action))
			switch action {
			case "":
			case "NO ACTION", "RESTRICT", "CASCADE", "SET NULL", "SET DEFAULT":
				definition += " " + rule.clause + " " + action
			default:
				return "", fmt.Errorf("invalid foreign key action %q", rule.action)
			}
		}
		return definition, nil
	case "CHECK":
		definition := strings.TrimSpace(constraint.Definition)
		if definition == "" {
			return "", fmt.Errorf("check constraint %q is missing its expression", constraint.Name)
		}
		if strings.HasPrefix(strings.ToUpper(definition), "CHECK ") {
			definition = strings.TrimSpace(definition[len("CHECK "):])
		}
		return "CHECK (" + definition + ")", nil
	default:
		return "", fmt.Errorf("unsupported constraint type %q", constraint.Type)
	}
}

func generateDropConstraint(change Change, engine contract.Engine) string {
	if change.Constraint == nil {
		return "-- Cannot generate DROP CONSTRAINT: constraint definition is missing."
	}
	table, name := qualify(change.TableSchema, change.TableName, engine), quote(change.Constraint.Name, engine)
	if engine == contract.EnginePostgres {
		return fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;", table, name)
	}
	switch strings.ToUpper(strings.TrimSpace(change.Constraint.Type)) {
	case "PRIMARY KEY":
		return fmt.Sprintf("ALTER TABLE %s DROP PRIMARY KEY;", table)
	case "FOREIGN KEY":
		return fmt.Sprintf("ALTER TABLE %s DROP FOREIGN KEY %s;", table, name)
	case "UNIQUE":
		return fmt.Sprintf("ALTER TABLE %s DROP INDEX %s;", table, name)
	case "CHECK":
		if engine == contract.EngineMariaDB {
			return fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;", table, name)
		}
		return fmt.Sprintf("ALTER TABLE %s DROP CHECK %s;", table, name)
	default:
		return fmt.Sprintf("-- Cannot generate DROP for unsupported constraint type %q.", change.Constraint.Type)
	}
}

func generateAddIndex(change Change, engine contract.Engine) string {
	if change.Index == nil || len(change.Index.Columns) == 0 {
		return "-- Cannot generate CREATE INDEX: index definition is missing or has no columns."
	}
	index := change.Index
	unique := ""
	if index.Unique {
		unique = "UNIQUE "
	}
	columns := quoteNames(index.Columns, engine)
	if len(index.KeyDefinitions) > 0 {
		columns = strings.Join(index.KeyDefinitions, ", ")
	}
	table, indexName := qualify(change.TableSchema, change.TableName, engine), quote(index.Name, engine)
	method := strings.TrimSpace(index.Method)
	if engine == contract.EnginePostgres {
		using := ""
		if method != "" {
			using = " USING " + method
		}
		predicate := ""
		if index.Predicate != "" {
			predicate = " WHERE " + index.Predicate
		}
		return fmt.Sprintf("CREATE %sINDEX %s ON %s%s (%s)%s;", unique, indexName, table, using, columns, predicate)
	}
	if index.Predicate != "" {
		return "-- Cannot generate partial index: this engine does not support WHERE on indexes."
	}

	indexKind, using := "INDEX", ""
	switch strings.ToUpper(method) {
	case "FULLTEXT":
		indexKind = "FULLTEXT INDEX"
	case "RTREE", "SPATIAL":
		indexKind = "SPATIAL INDEX"
	case "BTREE", "HASH":
		using = " USING " + strings.ToUpper(method)
	case "":
		// Let the engine choose its default index type.
	default:
		return fmt.Sprintf("-- Cannot generate index %s: unsupported MySQL index method %q.", index.Name, method)
	}
	if indexKind != "INDEX" && index.Unique {
		return fmt.Sprintf("-- Cannot generate %s %s: this index kind cannot be unique.", indexKind, index.Name)
	}
	return fmt.Sprintf("CREATE %s%s %s%s ON %s (%s);", unique, indexKind, indexName, using, table, columns)
}

func generateDropIndex(change Change, engine contract.Engine) string {
	if change.Index == nil {
		return "-- Cannot generate DROP INDEX: index definition is missing."
	}
	indexName := quote(change.Index.Name, engine)
	if engine == contract.EnginePostgres {
		return fmt.Sprintf("DROP INDEX %s;", qualify(change.TableSchema, change.Index.Name, engine))
	}
	return fmt.Sprintf("DROP INDEX %s ON %s;", indexName, qualify(change.TableSchema, change.TableName, engine))
}

func quoteNames(names []string, engine contract.Engine) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, quote(name, engine))
	}
	return strings.Join(quoted, ", ")
}

func quoteQualified(name string, engine contract.Engine) string {
	parts := strings.Split(name, ".")
	for index, part := range parts {
		parts[index] = quote(part, engine)
	}
	return strings.Join(parts, ".")
}

func qualify(schema, name string, engine contract.Engine) string {
	if schema == "" {
		return quote(name, engine)
	}
	return quote(schema, engine) + "." + quote(name, engine)
}

func quote(name string, engine contract.Engine) string {
	if engine == contract.EngineMySQL || engine == contract.EngineMariaDB {
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
