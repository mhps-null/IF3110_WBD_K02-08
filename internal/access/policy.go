package access

import "github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"

type Action string

const (
	// read only actions
	ActionViewSchema     Action = "view_schema"
	ActionViewData       Action = "view_data"
	ActionViewHistory    Action = "view_history"
	ActionViewSchemaDiff Action = "view_schema_diff"

	// write actions
	ActionCreateMigration  Action = "create_migration"
	ActionEditMigration    Action = "edit_migration"
	ActionExecuteMigration Action = "execute_migration"
)

// klo ada action baru tp belum dimasukin ke sini, otomatis dianggap ga boleh
var minimumLevel = map[Action]contract.AccessLevel{
	ActionViewSchema:     contract.AccessViewOnly,
	ActionViewData:       contract.AccessViewOnly,
	ActionViewHistory:    contract.AccessViewOnly,
	ActionViewSchemaDiff: contract.AccessViewOnly,

	ActionCreateMigration:  contract.AccessViewEdit,
	ActionEditMigration:    contract.AccessViewEdit,
	ActionExecuteMigration: contract.AccessViewEdit,
}

func MinLevel(a Action) (contract.AccessLevel, bool) {
	lvl, ok := minimumLevel[a]
	return lvl, ok
}

func Allows(level contract.AccessLevel, a Action) bool {
	min, ok := minimumLevel[a]
	if !ok {
		return false
	}
	return level >= min
}
