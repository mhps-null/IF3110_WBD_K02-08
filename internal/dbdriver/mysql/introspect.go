package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/example/if3110-milestone1/internal/contract"
)

// Introspector implements schema snapshotting and bounded row queries for MySQL 8.0+ and MariaDB 10.3+.
type Introspector struct{}

func NewIntrospector() *Introspector {
	return &Introspector{}
}

// QuoteIdentifier safely quotes a MySQL or MariaDB identifier using backticks.
func QuoteIdentifier(name string) string {
	escaped := strings.ReplaceAll(name, "`", "``")
	return "`" + escaped + "`"
}

// QualifyTable returns a qualified schema.table or simple table identifier for MySQL.
func QualifyTable(schema, table string) string {
	if schema == "" {
		return QuoteIdentifier(table)
	}
	return QuoteIdentifier(schema) + "." + QuoteIdentifier(table)
}

func (i *Introspector) Snapshot(ctx context.Context, db *sql.DB) (contract.SchemaSnapshot, error) {
	snapshot := contract.SchemaSnapshot{
		Engine: contract.EngineMySQL,
		Tables: make([]contract.Table, 0),
	}

	var activeDBSql sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&activeDBSql); err != nil {
		return snapshot, fmt.Errorf("determine active database: %w", err)
	}
	if !activeDBSql.Valid || strings.TrimSpace(activeDBSql.String) == "" {
		return snapshot, errors.New("no active database selected on connection")
	}
	activeDB := strings.TrimSpace(activeDBSql.String)

	tableMap := make(map[string]*contract.Table)
	tableOrder := make([]string, 0)

	tablesQuery := `SELECT table_schema, table_name
		FROM information_schema.tables
		WHERE table_schema = ?
		  AND table_type IN ('BASE TABLE', 'SYSTEM VERSIONED')
		ORDER BY table_schema, table_name`

	rows, err := db.QueryContext(ctx, tablesQuery, activeDB)
	if err != nil {
		return snapshot, fmt.Errorf("query tables: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var schema, name string
		if err := rows.Scan(&schema, &name); err != nil {
			return snapshot, fmt.Errorf("scan table: %w", err)
		}
		key := schema + "." + name
		t := &contract.Table{
			Schema:      schema,
			Name:        name,
			Columns:     make([]contract.Column, 0),
			Constraints: make([]contract.Constraint, 0),
			Indexes:     make([]contract.Index, 0),
		}
		tableMap[key] = t
		tableOrder = append(tableOrder, key)
	}
	if err := rows.Err(); err != nil {
		return snapshot, fmt.Errorf("iterate tables: %w", err)
	}

	colsQuery := `SELECT
			table_schema,
			table_name,
			column_name,
			column_type,
			is_nullable,
			column_default,
			column_key,
			ordinal_position
		FROM information_schema.columns
		WHERE table_schema = ?
		ORDER BY table_schema, table_name, ordinal_position`

	colRows, err := db.QueryContext(ctx, colsQuery, activeDB)
	if err != nil {
		return snapshot, fmt.Errorf("query columns: %w", err)
	}
	defer colRows.Close()

	for colRows.Next() {
		var schema, table, colName, colType, isNullable, colKey string
		var colDefault sql.NullString
		var ordPos int
		if err := colRows.Scan(&schema, &table, &colName, &colType, &isNullable, &colDefault, &colKey, &ordPos); err != nil {
			return snapshot, fmt.Errorf("scan column: %w", err)
		}

		key := schema + "." + table
		t, ok := tableMap[key]
		if !ok {
			continue
		}

		var defaultVal *string
		if colDefault.Valid {
			v := colDefault.String
			defaultVal = &v
		}

		isPK := strings.EqualFold(colKey, "PRI")
		col := contract.Column{
			Name:         colName,
			DataType:     colType,
			Nullable:     strings.EqualFold(isNullable, "YES"),
			Default:      defaultVal,
			IsPrimaryKey: isPK,
			Ordinal:      ordPos,
		}
		t.Columns = append(t.Columns, col)
	}
	if err := colRows.Err(); err != nil {
		return snapshot, fmt.Errorf("iterate columns: %w", err)
	}

	type rawConstraint struct {
		schema string
		table  string
		name   string
		cType  string
	}
	constraintMap := make(map[string]*rawConstraint)
	constraintOrder := make([]string, 0)

	tcQuery := `SELECT
			constraint_schema,
			table_name,
			constraint_name,
			constraint_type
		FROM information_schema.table_constraints
		WHERE constraint_schema = ?
		ORDER BY constraint_schema, table_name, constraint_name`

	tcRows, err := db.QueryContext(ctx, tcQuery, activeDB)
	if err != nil {
		return snapshot, fmt.Errorf("query table constraints: %w", err)
	}
	defer tcRows.Close()

	for tcRows.Next() {
		var schema, table, name, cTypeStr string
		if err := tcRows.Scan(&schema, &table, &name, &cTypeStr); err != nil {
			return snapshot, fmt.Errorf("scan table constraint: %w", err)
		}

		var cType string
		switch strings.ToUpper(cTypeStr) {
		case "PRIMARY KEY":
			cType = "PRIMARY KEY"
		case "FOREIGN KEY":
			cType = "FOREIGN KEY"
		case "UNIQUE":
			cType = "UNIQUE"
		case "CHECK":
			cType = "CHECK"
		default:
			continue
		}

		ckey := schema + "." + table + "." + name
		rawC := &rawConstraint{
			schema: schema,
			table:  table,
			name:   name,
			cType:  cType,
		}
		constraintMap[ckey] = rawC
		constraintOrder = append(constraintOrder, ckey)
	}
	if err := tcRows.Err(); err != nil {
		return snapshot, fmt.Errorf("iterate table constraints: %w", err)
	}

	type keyUsageCol struct {
		column    string
		refSchema string
		refTable  string
		refColumn string
	}
	keyUsageMap := make(map[string][]keyUsageCol)

	kcuQuery := `SELECT
			constraint_schema,
			table_name,
			constraint_name,
			column_name,
			ordinal_position,
			referenced_table_schema,
			referenced_table_name,
			referenced_column_name
		FROM information_schema.key_column_usage
		WHERE constraint_schema = ?
		ORDER BY constraint_schema, table_name, constraint_name, ordinal_position`

	kcuRows, err := db.QueryContext(ctx, kcuQuery, activeDB)
	if err != nil {
		return snapshot, fmt.Errorf("query key column usage: %w", err)
	}
	defer kcuRows.Close()

	for kcuRows.Next() {
		var schema, table, cName, colName string
		var ordPos int
		var refSchema, refTable, refCol sql.NullString
		if err := kcuRows.Scan(&schema, &table, &cName, &colName, &ordPos, &refSchema, &refTable, &refCol); err != nil {
			return snapshot, fmt.Errorf("scan key column usage: %w", err)
		}

		ckey := schema + "." + table + "." + cName
		var refS, refT, refC string
		if refSchema.Valid {
			refS = refSchema.String
		}
		if refTable.Valid {
			refT = refTable.String
		}
		if refCol.Valid {
			refC = refCol.String
		}

		keyUsageMap[ckey] = append(keyUsageMap[ckey], keyUsageCol{
			column:    colName,
			refSchema: refS,
			refTable:  refT,
			refColumn: refC,
		})
	}
	if err := kcuRows.Err(); err != nil {
		return snapshot, fmt.Errorf("iterate key column usage: %w", err)
	}

	checkClauseMap := make(map[string]string)
	var checkTableExists int
	checkTableProbe := `SELECT COUNT(1)
		FROM information_schema.tables
		WHERE table_schema = 'information_schema'
		  AND table_name = 'check_constraints'`
	if err := db.QueryRowContext(ctx, checkTableProbe).Scan(&checkTableExists); err != nil {
		return snapshot, fmt.Errorf("check check_constraints availability: %w", err)
	}

	if checkTableExists > 0 {
		var hasCheckTableCol int
		checkColProbe := `SELECT COUNT(1)
			FROM information_schema.columns
			WHERE table_schema = 'information_schema'
			  AND table_name = 'check_constraints'
			  AND column_name = 'table_name'`
		if err := db.QueryRowContext(ctx, checkColProbe).Scan(&hasCheckTableCol); err != nil {
			return snapshot, fmt.Errorf("check check_constraints table_name availability: %w", err)
		}

		if hasCheckTableCol > 0 {
			checkQuery := `SELECT
					constraint_schema,
					table_name,
					constraint_name,
					check_clause
				FROM information_schema.check_constraints
				WHERE constraint_schema = ?`

			checkRows, err := db.QueryContext(ctx, checkQuery, activeDB)
			if err != nil {
				return snapshot, fmt.Errorf("query check constraints: %w", err)
			}
			defer checkRows.Close()

			for checkRows.Next() {
				var schema, table, cName, clause string
				if err := checkRows.Scan(&schema, &table, &cName, &clause); err != nil {
					return snapshot, fmt.Errorf("scan check constraint: %w", err)
				}
				checkClauseMap[schema+"."+table+"."+cName] = clause
				if _, exists := checkClauseMap[schema+"."+cName]; !exists {
					checkClauseMap[schema+"."+cName] = clause
				}
			}
			if err := checkRows.Err(); err != nil {
				return snapshot, fmt.Errorf("iterate check constraints: %w", err)
			}
		} else {
			checkQuery := `SELECT
					constraint_schema,
					constraint_name,
					check_clause
				FROM information_schema.check_constraints
				WHERE constraint_schema = ?`

			checkRows, err := db.QueryContext(ctx, checkQuery, activeDB)
			if err != nil {
				return snapshot, fmt.Errorf("query check constraints: %w", err)
			}
			defer checkRows.Close()

			for checkRows.Next() {
				var schema, cName, clause string
				if err := checkRows.Scan(&schema, &cName, &clause); err != nil {
					return snapshot, fmt.Errorf("scan check constraint: %w", err)
				}
				checkClauseMap[schema+"."+cName] = clause
			}
			if err := checkRows.Err(); err != nil {
				return snapshot, fmt.Errorf("iterate check constraints: %w", err)
			}
		}
	}

	for _, ckey := range constraintOrder {
		rawC := constraintMap[ckey]
		tKey := rawC.schema + "." + rawC.table
		t, ok := tableMap[tKey]
		if !ok {
			continue
		}

		c := contract.Constraint{
			Name:       rawC.name,
			Type:       rawC.cType,
			Columns:    make([]string, 0),
			RefColumns: make([]string, 0),
		}

		if rawC.cType == "CHECK" {
			tableKey := rawC.schema + "." + rawC.table + "." + rawC.name
			if def, ok := checkClauseMap[tableKey]; ok {
				c.Definition = def
			} else {
				c.Definition = checkClauseMap[rawC.schema+"."+rawC.name]
			}
		} else {
			usages := keyUsageMap[ckey]
			for _, u := range usages {
				c.Columns = append(c.Columns, u.column)
				if rawC.cType == "FOREIGN KEY" {
					if u.refSchema != "" && u.refTable != "" {
						c.RefTable = u.refSchema + "." + u.refTable
					} else if u.refTable != "" {
						c.RefTable = u.refTable
					}
					c.RefColumns = append(c.RefColumns, u.refColumn)
				}
			}

			if rawC.cType == "PRIMARY KEY" {
				pkSet := make(map[string]bool)
				for _, pkCol := range c.Columns {
					pkSet[pkCol] = true
				}
				for idx := range t.Columns {
					if pkSet[t.Columns[idx].Name] {
						t.Columns[idx].IsPrimaryKey = true
					}
				}
			}
		}

		t.Constraints = append(t.Constraints, c)
	}

	type indexKey struct {
		schema string
		table  string
		name   string
	}
	type rawIndex struct {
		unique  bool
		method  string
		columns []string
	}
	indexMap := make(map[indexKey]*rawIndex)
	indexOrder := make([]indexKey, 0)

	var hasExprCol int
	exprProbe := `SELECT COUNT(1)
		FROM information_schema.columns
		WHERE table_schema = 'information_schema'
		  AND table_name = 'statistics'
		  AND column_name = 'expression'`
	if err := db.QueryRowContext(ctx, exprProbe).Scan(&hasExprCol); err != nil {
		return snapshot, fmt.Errorf("check statistics expression column availability: %w", err)
	}

	var statsRows *sql.Rows
	if hasExprCol > 0 {
		statsQuery := `SELECT
				table_schema,
				table_name,
				index_name,
				non_unique,
				seq_in_index,
				column_name,
				index_type,
				expression
			FROM information_schema.statistics
			WHERE table_schema = ?
			ORDER BY table_schema, table_name, index_name, seq_in_index`
		statsRows, err = db.QueryContext(ctx, statsQuery, activeDB)
	} else {
		statsQuery := `SELECT
				table_schema,
				table_name,
				index_name,
				non_unique,
				seq_in_index,
				column_name,
				index_type
			FROM information_schema.statistics
			WHERE table_schema = ?
			ORDER BY table_schema, table_name, index_name, seq_in_index`
		statsRows, err = db.QueryContext(ctx, statsQuery, activeDB)
	}
	if err != nil {
		return snapshot, fmt.Errorf("query statistics: %w", err)
	}
	defer statsRows.Close()

	for statsRows.Next() {
		var schema, table, idxName, idxType string
		var nonUnique, seqInIndex int
		var colNameNull, exprNull sql.NullString

		if hasExprCol > 0 {
			if err := statsRows.Scan(&schema, &table, &idxName, &nonUnique, &seqInIndex, &colNameNull, &idxType, &exprNull); err != nil {
				return snapshot, fmt.Errorf("scan statistics: %w", err)
			}
		} else {
			if err := statsRows.Scan(&schema, &table, &idxName, &nonUnique, &seqInIndex, &colNameNull, &idxType); err != nil {
				return snapshot, fmt.Errorf("scan statistics: %w", err)
			}
		}

		keyPart := ""
		if colNameNull.Valid && colNameNull.String != "" {
			keyPart = colNameNull.String
		} else if exprNull.Valid && exprNull.String != "" {
			keyPart = exprNull.String
		} else {
			keyPart = "(expression)"
		}

		k := indexKey{
			schema: schema,
			table:  table,
			name:   idxName,
		}
		idx, exists := indexMap[k]
		if !exists {
			idx = &rawIndex{
				unique:  nonUnique == 0,
				method:  idxType,
				columns: make([]string, 0),
			}
			indexMap[k] = idx
			indexOrder = append(indexOrder, k)
		}
		idx.columns = append(idx.columns, keyPart)
	}
	if err := statsRows.Err(); err != nil {
		return snapshot, fmt.Errorf("iterate statistics: %w", err)
	}

	for _, k := range indexOrder {
		tKey := k.schema + "." + k.table
		t, ok := tableMap[tKey]
		if !ok {
			continue
		}

		rawIdx := indexMap[k]
		t.Indexes = append(t.Indexes, contract.Index{
			Name:    k.name,
			Unique:  rawIdx.unique,
			Columns: rawIdx.columns,
			Method:  rawIdx.method,
		})
	}

	for _, key := range tableOrder {
		snapshot.Tables = append(snapshot.Tables, *tableMap[key])
	}

	return snapshot, nil
}

// Rows queries paginated table rows in a read-only transaction with engine-specific execution limits.
func (i *Introspector) Rows(ctx context.Context, db *sql.DB, schema, table string, p contract.PageRequest) (contract.RowPage, error) {
	if table == "" {
		return contract.RowPage{}, fmt.Errorf("table name cannot be empty")
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	page := p.Page
	if page < 1 {
		page = 1
	}
	pageSize := p.PageSize
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	offset := (page - 1) * pageSize

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return contract.RowPage{}, fmt.Errorf("begin read-only transaction: %w", err)
	}
	defer tx.Rollback()

	var activeDBSql sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&activeDBSql); err != nil {
		return contract.RowPage{}, fmt.Errorf("determine active database: %w", err)
	}
	if !activeDBSql.Valid || strings.TrimSpace(activeDBSql.String) == "" {
		return contract.RowPage{}, errors.New("no active database selected on connection")
	}
	activeSchema := strings.TrimSpace(activeDBSql.String)

	requestedSchema := strings.TrimSpace(schema)
	if requestedSchema == "" {
		requestedSchema = activeSchema
	} else if !strings.EqualFold(requestedSchema, activeSchema) {
		return contract.RowPage{}, fmt.Errorf("access denied: cross-database access to schema %q is prohibited; connection is bound to %q: %w", requestedSchema, activeSchema, contract.ErrAccessDenied)
	}

	var versionStr string
	if err := tx.QueryRowContext(ctx, "SELECT VERSION()").Scan(&versionStr); err != nil {
		return contract.RowPage{}, fmt.Errorf("detect server version: %w", err)
	}
	isMariaDB := strings.Contains(strings.ToLower(versionStr), "mariadb")

	colQuery := `SELECT column_name
		FROM information_schema.columns
		WHERE table_name = ?
		  AND table_schema = ?
		ORDER BY ordinal_position`

	colRows, err := tx.QueryContext(ctx, colQuery, table, activeSchema)
	if err != nil {
		return contract.RowPage{}, fmt.Errorf("query table columns: %w", err)
	}
	defer colRows.Close()

	validColumns := make([]string, 0)
	for colRows.Next() {
		var col string
		if err := colRows.Scan(&col); err != nil {
			return contract.RowPage{}, fmt.Errorf("scan column name: %w", err)
		}
		validColumns = append(validColumns, col)
	}
	if err := colRows.Err(); err != nil {
		return contract.RowPage{}, fmt.Errorf("iterate columns: %w", err)
	}
	if len(validColumns) == 0 {
		return contract.RowPage{}, contract.ErrNotFound
	}

	orderCol := validColumns[0]
	if p.OrderBy != "" {
		matched := false
		for _, c := range validColumns {
			if strings.EqualFold(c, p.OrderBy) {
				orderCol = c
				matched = true
				break
			}
		}
		if !matched {
			return contract.RowPage{}, fmt.Errorf("invalid order column: %s", p.OrderBy)
		}
	}

	orderDir := "ASC"
	if p.Desc {
		orderDir = "DESC"
	}

	qualifiedTable := QualifyTable(activeSchema, table)

	var countQuery string
	if isMariaDB {
		countQuery = fmt.Sprintf("SET STATEMENT max_statement_time = 5 FOR SELECT COUNT(1) FROM %s", qualifiedTable)
	} else {
		countQuery = fmt.Sprintf("SELECT /*+ MAX_EXECUTION_TIME(5000) */ COUNT(1) FROM %s", qualifiedTable)
	}

	var totalRows int64
	if err := tx.QueryRowContext(ctx, countQuery).Scan(&totalRows); err != nil {
		return contract.RowPage{}, fmt.Errorf("count rows: %w", err)
	}

	quotedCols := make([]string, len(validColumns))
	for idx, col := range validColumns {
		quotedCols[idx] = QuoteIdentifier(col)
	}

	var dataQuery string
	if isMariaDB {
		dataQuery = fmt.Sprintf("SET STATEMENT max_statement_time = 5 FOR SELECT %s FROM %s ORDER BY %s %s LIMIT %d OFFSET %d",
			strings.Join(quotedCols, ", "),
			qualifiedTable,
			QuoteIdentifier(orderCol),
			orderDir,
			pageSize,
			offset,
		)
	} else {
		dataQuery = fmt.Sprintf("SELECT /*+ MAX_EXECUTION_TIME(5000) */ %s FROM %s ORDER BY %s %s LIMIT %d OFFSET %d",
			strings.Join(quotedCols, ", "),
			qualifiedTable,
			QuoteIdentifier(orderCol),
			orderDir,
			pageSize,
			offset,
		)
	}

	rows, err := tx.QueryContext(ctx, dataQuery)
	if err != nil {
		return contract.RowPage{}, fmt.Errorf("query rows: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return contract.RowPage{}, fmt.Errorf("read columns: %w", err)
	}

	resultRows := make([][]any, 0)
	for rows.Next() {
		scanPointers := make([]any, len(cols))
		scanValues := make([]any, len(cols))
		for idx := range scanValues {
			scanPointers[idx] = &scanValues[idx]
		}
		if err := rows.Scan(scanPointers...); err != nil {
			return contract.RowPage{}, fmt.Errorf("scan row data: %w", err)
		}

		formattedRow := make([]any, len(cols))
		for idx, val := range scanValues {
			switch v := val.(type) {
			case []byte:
				formattedRow[idx] = string(v)
			case time.Time:
				formattedRow[idx] = v.Format("2006-01-02 15:04:05")
			default:
				formattedRow[idx] = v
			}
		}
		resultRows = append(resultRows, formattedRow)
	}
	if err := rows.Err(); err != nil {
		return contract.RowPage{}, fmt.Errorf("iterate row data: %w", err)
	}

	return contract.RowPage{
		Columns: cols,
		Rows:    resultRows,
		Total:   totalRows,
	}, nil
}
