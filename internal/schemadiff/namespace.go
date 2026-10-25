package schemadiff

import (
	"sort"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

type tableIdentity struct{ schema, name string }

func sortedTableKeys(tables map[tableIdentity]contract.Table) []tableIdentity {
	keys := make([]tableIdentity, 0, len(tables))
	for key := range tables {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].schema != keys[j].schema {
			return keys[i].schema < keys[j].schema
		}
		return keys[i].name < keys[j].name
	})
	return keys
}

// A MySQL snapshot covers one active database. Compare its local objects in
// the source database's namespace, rather than writing to the reference DB.
// PostgreSQL schemas remain distinct and are never remapped.
func targetInSourceNamespace(source, target contract.SchemaSnapshot) contract.SchemaSnapshot {
	mySQL := func(engine contract.Engine) bool {
		return engine == contract.EngineMySQL || engine == contract.EngineMariaDB
	}
	if !mySQL(source.Engine) || !mySQL(target.Engine) || target.Schema == "" || source.Schema == target.Schema {
		return target
	}
	result := target
	result.Schema = source.Schema
	result.Tables = append([]contract.Table(nil), target.Tables...)
	for i := range result.Tables {
		table := &result.Tables[i]
		if table.Schema == target.Schema {
			table.Schema = source.Schema
		}
		table.Constraints = append([]contract.Constraint(nil), table.Constraints...)
		for j := range table.Constraints {
			fk := &table.Constraints[j]
			if fk.RefSchema == target.Schema && fk.RefTableName != "" {
				fk.RefSchema = source.Schema
				fk.RefTable = fk.RefTableName
				if source.Schema != "" {
					fk.RefTable = source.Schema + "." + fk.RefTableName
				}
			}
		}
	}
	return result
}
