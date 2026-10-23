package schemadiff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/example/if3110-milestone1/internal/contract"
)

// mengubah daftar Change menjadi satu string SQL utuh yg siap dijalankan di database target
func GenerateSQL(changes []Change, engine contract.Engine) string {
	if len(changes) == 0 {
		return "-- No schema changes detected."
	}

	// Urutkan perubahan biar aman pas dieksekusi:
	// 1. Create Table
	// 2. Alter Column / Add Column
	// 3. Drop Column
	// 4. Drop Table
	sorted := sortChanges(changes)

	var sb strings.Builder
	for _, c := range sorted {
		if c.Destructive {
			sb.WriteString("-- WARNING: DESTRUCTIVE CHANGE\n")
		}
		sb.WriteString(generateSingle(c, engine))
		sb.WriteString("\n\n")
	}

	return strings.TrimSpace(sb.String())
}

func sortChanges(changes []Change) []Change {
	sorted := make([]Change, len(changes))
	copy(sorted, changes)

	weight := map[ChangeType]int{
		CreateTable: 1,
		AlterColumn: 2,
		AddColumn: 3,
		DropColumn: 4,
		DropTable: 5,
	}

	sort.SliceStable(sorted, func(i, j int) bool {
		return weight[sorted[i].Type] < weight[sorted[j].Type]
	})

	return sorted
}

func generateSingle(c Change, engine contract.Engine) string {
	switch c.Type {
	case CreateTable:
		return generateCreateTable(c.TableDef, engine)
	case DropTable:
		return fmt.Sprintf("DROP TABLE %s;", quote(c.TableName, engine))
	case AddColumn:
		return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;",
			quote(c.TableName, engine), formatColumnDef(c.ColumnName, c.NewColumn, engine))
	case DropColumn:
		return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;",
			quote(c.TableName, engine), quote(c.ColumnName, engine))
	case AlterColumn:
		return generateAlterColumn(c, engine)
	default:
		return "-- Unknown change type"
	}
}

func generateCreateTable(t *contract.Table, engine contract.Engine) string {
	var cols []string
	for _, col := range t.Columns {
		cols = append(cols, "  "+formatColumnDef(col.Name, &col, engine))
	}
	return fmt.Sprintf("CREATE TABLE %s (\n%s\n);", quote(t.Name, engine), strings.Join(cols, ",\n"))
}

func formatColumnDef(name string, col *contract.Column, engine contract.Engine) string {
	def := fmt.Sprintf("%s %s", quote(name, engine), col.DataType)
	if !col.Nullable {
		def += " NOT NULL"
	}
	if col.Default != nil {
		def += " DEFAULT " + *col.Default
	}
	return def
}

func generateAlterColumn(c Change, engine contract.Engine) string {
	table := quote(c.TableName, engine)
	col := quote(c.ColumnName, engine)
	old := c.OldColumn
	new := c.NewColumn

	// MySQL bisa melakukan perubahan tipe + nullability sekaligus via MODIFY COLUMN
	if engine == contract.EngineMySQL || engine == contract.EngineMariaDB {
		return fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s;", table, formatColumnDef(c.ColumnName, new, engine))
	}

	// PostgreSQL memisahkan ALTER TYPE dan SET/DROP NOT NULL
	var stmts []string
	if old.DataType != new.DataType {
		stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s;", table, col, new.DataType))
	}
	
	if old.Nullable != new.Nullable {
		if new.Nullable {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP NOT NULL;", table, col))
		} else {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET NOT NULL;", table, col))
		}
	}

	if !defaultsEqual(old.Default, new.Default) {
		if new.Default == nil {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", table, col))
		} else {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s;", table, col, *new.Default))
		}
	}

	return strings.Join(stmts, "\n")
}

// mengapit nama identifier dgn char yg sesuai per DBMS
func quote(name string, engine contract.Engine) string {
	if engine == contract.EngineMySQL || engine == contract.EngineMariaDB {
		return "`" + name + "`"
	}
	return `"` + name + `"` // Postgres
}
