package access

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

type Checker struct {
	db *sql.DB
}

func NewChecker(db *sql.DB) *Checker {
	return &Checker{db: db}
}

var _ contract.AccessChecker = (*Checker)(nil)

func (c *Checker) EffectiveLevel(ctx context.Context, userID, dbID int64) (contract.AccessLevel, error) {
	const q = `
		SELECT COALESCE(MAX(gda.level), 0)
		FROM group_members gm
		JOIN group_db_access gda ON gda.group_id = gm.group_id
		JOIN users u ON u.id = gm.user_id
		WHERE gm.user_id = $1
		  AND gda.db_id = $2
		  AND u.is_active = TRUE
	`
	var level int64
	if err := c.db.QueryRowContext(ctx, q, userID, dbID).Scan(&level); err != nil {
		return contract.AccessNone, fmt.Errorf("access: effective level: %w", err)
	}
	return levelFromDB(level), nil
}

func (c *Checker) ListAccessible(ctx context.Context, userID int64) ([]contract.AccessibleDB, error) {
	const q = `
		SELECT dc.id, dc.name, MAX(gda.level) AS level
		FROM group_members gm
		JOIN group_db_access gda ON gda.group_id = gm.group_id
		JOIN db_connections dc ON dc.id = gda.db_id
		JOIN users u ON u.id = gm.user_id
		WHERE gm.user_id = $1
		  AND u.is_active = TRUE
		GROUP BY dc.id, dc.name
		ORDER BY dc.name
	`
	rows, err := c.db.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("access: list accessible: %w", err)
	}
	defer rows.Close()

	var out []contract.AccessibleDB
	for rows.Next() {
		var (
			id    int64
			name  string
			level int64
		)
		if err := rows.Scan(&id, &name, &level); err != nil {
			return nil, fmt.Errorf("access: scan accessible row: %w", err)
		}
		lvl := levelFromDB(level)
		if lvl == contract.AccessNone {
			continue
		}
		out = append(out, contract.AccessibleDB{DBID: id, Name: name, Level: lvl})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("access: iterate accessible rows: %w", err)
	}
	return out, nil
}

// kalo user punya beberapa group, ambil access tertinggi
func levelFromDB(v int64) contract.AccessLevel {
	switch v {
	case 1:
		return contract.AccessViewOnly
	case 2:
		return contract.AccessViewEdit
	default:
		return contract.AccessNone
	}
}
