package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/example/if3110-milestone1/internal/contract"
)

// Introspector implements schema inspection and row querying for PostgreSQL databases.
type Introspector struct{}

func NewIntrospector() *Introspector {
	return &Introspector{}
}

// QuoteIdentifier safely quotes a PostgreSQL identifier using double quotes.
func QuoteIdentifier(name string) string {
	escaped := strings.ReplaceAll(name, `"`, `""`)
	return `"` + escaped + `"`
}

// QualifyTable returns a quoted schema.table identifier, defaulting to public schema.
func QualifyTable(schema, table string) string {
	if schema == "" {
		schema = "public"
	}
	return QuoteIdentifier(schema) + "." + QuoteIdentifier(table)
}


func (i *Introspector) Snapshot(ctx context.Context, db *sql.DB) (contract.SchemaSnapshot, error) {
	snapshot := contract.SchemaSnapshot{
		Engine: contract.EnginePostgres,
		Tables: make([]contract.Table, 0),
	}

	tableMap := make(map[string]*contract.Table)
	tableOrder := make([]string, 0)

	tablesQuery := `SELECT ns.nspname AS table_schema, cls.relname AS table_name
		FROM pg_class cls
		JOIN pg_namespace ns ON ns.oid = cls.relnamespace
		WHERE ns.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND cls.relkind IN ('r', 'p')
		ORDER BY ns.nspname, cls.relname`

	rows, err := db.QueryContext(ctx, tablesQuery)
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
			ns.nspname AS table_schema,
			cls.relname AS table_name,
			att.attname AS column_name,
			pg_catalog.format_type(att.atttypid, att.atttypmod) AS full_data_type,
			NOT att.attnotnull AS is_nullable,
			pg_get_expr(def.adbin, def.adrelid) AS column_default,
			att.attnum AS ordinal_position
		FROM pg_attribute att
		JOIN pg_class cls ON cls.oid = att.attrelid
		JOIN pg_namespace ns ON ns.oid = cls.relnamespace
		LEFT JOIN pg_attrdef def ON def.adrelid = att.attrelid AND def.adnum = att.attnum
		WHERE ns.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND cls.relkind IN ('r', 'p')
		  AND att.attnum > 0
		  AND NOT att.attisdropped
		ORDER BY ns.nspname, cls.relname, att.attnum`

	colRows, err := db.QueryContext(ctx, colsQuery)
	if err != nil {
		return snapshot, fmt.Errorf("query columns: %w", err)
	}
	defer colRows.Close()

	for colRows.Next() {
		var schema, table, colName, dataType string
		var isNullable bool
		var colDefault sql.NullString
		var ordinal int

		if err := colRows.Scan(&schema, &table, &colName, &dataType, &isNullable, &colDefault, &ordinal); err != nil {
			return snapshot, fmt.Errorf("scan column: %w", err)
		}

		key := schema + "." + table
		t, ok := tableMap[key]
		if !ok {
			continue
		}

		var defaultPtr *string
		if colDefault.Valid {
			val := colDefault.String
			defaultPtr = &val
		}

		t.Columns = append(t.Columns, contract.Column{
			Name:         colName,
			DataType:     dataType,
			Nullable:     isNullable,
			Default:      defaultPtr,
			IsPrimaryKey: false,
			Ordinal:      ordinal,
		})
	}
	if err := colRows.Err(); err != nil {
		return snapshot, fmt.Errorf("iterate columns: %w", err)
	}

	constraintsQuery := `SELECT
			c.oid,
			ns.nspname AS table_schema,
			cls.relname AS table_name,
			c.conname,
			c.contype::text,
			COALESCE(ref_ns.nspname, '') AS ref_schema,
			COALESCE(ref_cls.relname, '') AS ref_table,
			pg_get_constraintdef(c.oid, true) AS def
		FROM pg_constraint c
		JOIN pg_class cls ON cls.oid = c.conrelid
		JOIN pg_namespace ns ON ns.oid = cls.relnamespace
		LEFT JOIN pg_class ref_cls ON ref_cls.oid = c.confrelid
		LEFT JOIN pg_namespace ref_ns ON ref_ns.oid = ref_cls.relnamespace
		WHERE ns.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND cls.relkind IN ('r', 'p')
		ORDER BY ns.nspname, cls.relname, c.conname`

	consRows, err := db.QueryContext(ctx, constraintsQuery)
	if err != nil {
		return snapshot, fmt.Errorf("query constraints: %w", err)
	}
	defer consRows.Close()

	type consMeta struct {
		tableKey string
		cons     *contract.Constraint
	}
	consByOID := make(map[uint32]*consMeta)
	consOrder := make([]uint32, 0)

	for consRows.Next() {
		var oid uint32
		var schema, table, name, conType, refSchema, refTable, def string

		if err := consRows.Scan(&oid, &schema, &table, &name, &conType, &refSchema, &refTable, &def); err != nil {
			return snapshot, fmt.Errorf("scan constraint: %w", err)
		}

		var cType string
		switch conType {
		case "p":
			cType = "PRIMARY KEY"
		case "f":
			cType = "FOREIGN KEY"
		case "u":
			cType = "UNIQUE"
		case "c":
			cType = "CHECK"
		default:
			continue
		}

		c := &contract.Constraint{
			Name:       name,
			Type:       cType,
			Columns:    make([]string, 0),
			Definition: def,
		}

		if cType == "FOREIGN KEY" && refTable != "" {
			if refSchema != "" {
				c.RefTable = refSchema + "." + refTable
			} else {
				c.RefTable = refTable
			}
			c.RefColumns = make([]string, 0)
		}

		meta := &consMeta{
			tableKey: schema + "." + table,
			cons:     c,
		}
		consByOID[oid] = meta
		consOrder = append(consOrder, oid)
	}
	if err := consRows.Err(); err != nil {
		return snapshot, fmt.Errorf("iterate constraints: %w", err)
	}

	consColsQuery := `SELECT
			c.oid AS con_oid,
			u.ord AS position,
			att.attname AS col_name,
			COALESCE(ref_att.attname, '') AS ref_col_name
		FROM pg_constraint c
		JOIN pg_class cls ON cls.oid = c.conrelid
		JOIN pg_namespace ns ON ns.oid = cls.relnamespace
		JOIN unnest(c.conkey) WITH ORDINALITY AS u(attnum, ord) ON true
		LEFT JOIN pg_attribute att ON att.attrelid = c.conrelid AND att.attnum = u.attnum
		LEFT JOIN LATERAL (
			SELECT ref_u.attnum
			FROM unnest(c.confkey) WITH ORDINALITY AS ref_u(attnum, ord)
			WHERE ref_u.ord = u.ord
		) ref_k ON true
		LEFT JOIN pg_attribute ref_att ON ref_att.attrelid = c.confrelid AND ref_att.attnum = ref_k.attnum
		WHERE ns.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND cls.relkind IN ('r', 'p')
		ORDER BY c.oid, u.ord`

	consColsRows, err := db.QueryContext(ctx, consColsQuery)
	if err != nil {
		return snapshot, fmt.Errorf("query constraint columns: %w", err)
	}
	defer consColsRows.Close()

	for consColsRows.Next() {
		var oid uint32
		var pos int
		var colName, refColName string

		if err := consColsRows.Scan(&oid, &pos, &colName, &refColName); err != nil {
			return snapshot, fmt.Errorf("scan constraint column: %w", err)
		}

		meta, ok := consByOID[oid]
		if !ok {
			continue
		}
		if colName != "" {
			meta.cons.Columns = append(meta.cons.Columns, colName)
		}
		if meta.cons.Type == "FOREIGN KEY" && refColName != "" {
			meta.cons.RefColumns = append(meta.cons.RefColumns, refColName)
		}
	}
	if err := consColsRows.Err(); err != nil {
		return snapshot, fmt.Errorf("iterate constraint columns: %w", err)
	}

	pkMap := make(map[string]map[string]bool)
	for _, oid := range consOrder {
		meta := consByOID[oid]
		t, ok := tableMap[meta.tableKey]
		if !ok {
			continue
		}
		t.Constraints = append(t.Constraints, *meta.cons)
		if meta.cons.Type == "PRIMARY KEY" {
			if pkMap[meta.tableKey] == nil {
				pkMap[meta.tableKey] = make(map[string]bool)
			}
			for _, col := range meta.cons.Columns {
				pkMap[meta.tableKey][col] = true
			}
		}
	}

	for key, t := range tableMap {
		if pks, exists := pkMap[key]; exists {
			for idx := range t.Columns {
				if pks[t.Columns[idx].Name] {
					t.Columns[idx].IsPrimaryKey = true
				}
			}
		}
	}

	indexesQuery := `SELECT
			idx.indexrelid AS index_oid,
			ns.nspname AS table_schema,
			tbl.relname AS table_name,
			idx_cls.relname AS index_name,
			am.amname AS index_method,
			idx.indisunique AS is_unique
		FROM pg_index idx
		JOIN pg_class idx_cls ON idx_cls.oid = idx.indexrelid
		JOIN pg_class tbl ON tbl.oid = idx.indrelid
		JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
		JOIN pg_am am ON am.oid = idx_cls.relam
		WHERE ns.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND tbl.relkind IN ('r', 'p')
		ORDER BY ns.nspname, tbl.relname, idx_cls.relname`

	idxRows, err := db.QueryContext(ctx, indexesQuery)
	if err != nil {
		return snapshot, fmt.Errorf("query indexes: %w", err)
	}
	defer idxRows.Close()

	type idxMeta struct {
		tableKey string
		idx      *contract.Index
	}
	idxByOID := make(map[uint32]*idxMeta)
	idxOrder := make([]uint32, 0)

	for idxRows.Next() {
		var oid uint32
		var schema, table, idxName, method string
		var isUnique bool

		if err := idxRows.Scan(&oid, &schema, &table, &idxName, &method, &isUnique); err != nil {
			return snapshot, fmt.Errorf("scan index: %w", err)
		}

		i := &contract.Index{
			Name:    idxName,
			Columns: make([]string, 0),
			Unique:  isUnique,
			Method:  method,
		}

		meta := &idxMeta{
			tableKey: schema + "." + table,
			idx:      i,
		}
		idxByOID[oid] = meta
		idxOrder = append(idxOrder, oid)
	}
	if err := idxRows.Err(); err != nil {
		return snapshot, fmt.Errorf("iterate indexes: %w", err)
	}

	idxColsQuery := `SELECT
			idx.indexrelid AS index_oid,
			k.pos AS key_pos,
			pg_get_indexdef(idx.indexrelid, k.pos, true) AS col_def
		FROM pg_index idx
		JOIN pg_class tbl ON tbl.oid = idx.indrelid
		JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
		CROSS JOIN LATERAL generate_series(1, idx.indnkeyatts) AS k(pos)
		WHERE ns.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND tbl.relkind IN ('r', 'p')
		ORDER BY idx.indexrelid, k.pos`

	idxColsRows, err := db.QueryContext(ctx, idxColsQuery)
	if err != nil {
		return snapshot, fmt.Errorf("query index columns: %w", err)
	}
	defer idxColsRows.Close()

	for idxColsRows.Next() {
		var oid uint32
		var pos int
		var colDef string

		if err := idxColsRows.Scan(&oid, &pos, &colDef); err != nil {
			return snapshot, fmt.Errorf("scan index column: %w", err)
		}

		meta, ok := idxByOID[oid]
		if !ok {
			continue
		}
		meta.idx.Columns = append(meta.idx.Columns, colDef)
	}
	if err := idxColsRows.Err(); err != nil {
		return snapshot, fmt.Errorf("iterate index columns: %w", err)
	}

	for _, oid := range idxOrder {
		meta := idxByOID[oid]
		t, ok := tableMap[meta.tableKey]
		if !ok {
			continue
		}
		t.Indexes = append(t.Indexes, *meta.idx)
	}

	for _, key := range tableOrder {
		snapshot.Tables = append(snapshot.Tables, *tableMap[key])
	}

	return snapshot, nil
}

func parseIndexColumns(idxDef string) []string {
	upper := strings.ToUpper(idxDef)

	if whereIdx := findTopLevelKeyword(idxDef, upper, " WHERE "); whereIdx != -1 {
		idxDef = idxDef[:whereIdx]
		upper = upper[:whereIdx]
	}

	if includeIdx := findTopLevelKeyword(idxDef, upper, " INCLUDE "); includeIdx != -1 {
		idxDef = idxDef[:includeIdx]
		upper = upper[:includeIdx]
	}

	open := strings.Index(idxDef, "(")
	close := strings.LastIndex(idxDef, ")")
	if open == -1 || close == -1 || close <= open {
		return nil
	}

	body := idxDef[open+1 : close]
	return splitIndexKeys(body)
}

func findTopLevelKeyword(s, upper, keyword string) int {
	var inQuote, inSingleQuote bool
	var parenDepth int
	kLen := len(keyword)

	for i := 0; i <= len(s)-kLen; i++ {
		ch := s[i]
		if ch == '"' && !inSingleQuote {
			inQuote = !inQuote
		} else if ch == '\'' && !inQuote {
			inSingleQuote = !inSingleQuote
		} else if !inQuote && !inSingleQuote {
			if ch == '(' {
				parenDepth++
			} else if ch == ')' {
				if parenDepth > 0 {
					parenDepth--
				}
			} else if parenDepth == 0 && upper[i:i+kLen] == keyword {
				return i
			}
		}
	}
	return -1
}

func splitIndexKeys(body string) []string {
	var parts []string
	var current strings.Builder
	var inQuote, inSingleQuote bool
	var parenDepth int

	for i := 0; i < len(body); i++ {
		ch := body[i]
		if ch == '"' && !inSingleQuote {
			inQuote = !inQuote
			current.WriteByte(ch)
		} else if ch == '\'' && !inQuote {
			inSingleQuote = !inSingleQuote
			current.WriteByte(ch)
		} else if !inQuote && !inSingleQuote {
			if ch == '(' {
				parenDepth++
				current.WriteByte(ch)
			} else if ch == ')' {
				if parenDepth > 0 {
					parenDepth--
				}
				current.WriteByte(ch)
			} else if ch == ',' && parenDepth == 0 {
				part := strings.TrimSpace(current.String())
				if part != "" {
					parts = append(parts, cleanIndexCol(part))
				}
				current.Reset()
			} else {
				current.WriteByte(ch)
			}
		} else {
			current.WriteByte(ch)
		}
	}

	part := strings.TrimSpace(current.String())
	if part != "" {
		parts = append(parts, cleanIndexCol(part))
	}

	return parts
}

func cleanIndexCol(col string) string {
	col = strings.TrimSpace(col)
	if strings.HasPrefix(col, `"`) && strings.HasSuffix(col, `"`) && len(col) >= 2 {
		unquoted := col[1 : len(col)-1]
		return strings.ReplaceAll(unquoted, `""`, `"`)
	}
	return col
}

// Rows queries paginated table rows within a read-only transaction and statement timeout.
func (i *Introspector) Rows(ctx context.Context, db *sql.DB, schema, table string, p contract.PageRequest) (contract.RowPage, error) {
	if schema == "" {
		schema = "public"
	}
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

	if _, err := tx.ExecContext(ctx, "SET LOCAL statement_timeout = 5000"); err != nil {
		return contract.RowPage{}, fmt.Errorf("set statement timeout: %w", err)
	}

	colQuery := `SELECT att.attname
		FROM pg_attribute att
		JOIN pg_class cls ON cls.oid = att.attrelid
		JOIN pg_namespace ns ON ns.oid = cls.relnamespace
		WHERE ns.nspname = $1 AND cls.relname = $2
		  AND att.attnum > 0
		  AND NOT att.attisdropped
		ORDER BY att.attnum`

	colRows, err := tx.QueryContext(ctx, colQuery, schema, table)
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
			if c == p.OrderBy {
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

	qualifiedTable := QualifyTable(schema, table)

	var totalRows int64
	countQuery := fmt.Sprintf(`SELECT COUNT(1) FROM %s`, qualifiedTable)
	if err := tx.QueryRowContext(ctx, countQuery).Scan(&totalRows); err != nil {
		return contract.RowPage{}, fmt.Errorf("count rows: %w", err)
	}

	quotedCols := make([]string, len(validColumns))
	for idx, col := range validColumns {
		quotedCols[idx] = QuoteIdentifier(col)
	}

	dataQuery := fmt.Sprintf(`SELECT %s FROM %s ORDER BY %s %s LIMIT %d OFFSET %d`,
		strings.Join(quotedCols, ", "),
		qualifiedTable,
		QuoteIdentifier(orderCol),
		orderDir,
		pageSize,
		offset,
	)

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
