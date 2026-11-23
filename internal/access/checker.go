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

var (
	_ contract.AccessChecker        = (*Checker)(nil)
	_ contract.GroupAccessInspector = (*Checker)(nil)
)

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

// ListAccessibleWithEngine returns accessible databases including engine metadata for SPA views.
func (c *Checker) ListAccessibleWithEngine(ctx context.Context, userID int64) ([]contract.AccessibleDB, error) {
	const q = `
		SELECT dc.id, dc.name, dc.engine, MAX(gda.level) AS level
		FROM group_members gm
		JOIN group_db_access gda ON gda.group_id = gm.group_id
		JOIN db_connections dc ON dc.id = gda.db_id
		JOIN users u ON u.id = gm.user_id
		WHERE gm.user_id = $1
		  AND u.is_active = TRUE
		GROUP BY dc.id, dc.name, dc.engine
		ORDER BY dc.name
	`
	rows, err := c.db.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("access: list accessible with engine: %w", err)
	}
	defer rows.Close()

	out := make([]contract.AccessibleDB, 0)
	for rows.Next() {
		var (
			id     int64
			name   string
			engine string
			level  int64
		)
		if err := rows.Scan(&id, &name, &engine, &level); err != nil {
			return nil, fmt.Errorf("access: scan accessible with engine row: %w", err)
		}
		lvl := levelFromDB(level)
		if lvl == contract.AccessNone {
			continue
		}
		out = append(out, contract.AccessibleDB{
			DBID:   id,
			Name:   name,
			Engine: contract.Engine(engine),
			Level:  lvl,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("access: iterate accessible with engine rows: %w", err)
	}
	return out, nil
}

// ListGroupsForDatabase returns every group that has access (View Only or View + Edit)
// to the target database along with its leader details. Groups without an active leader
// are returned with HasLeader=false so approval workflows remain Pending rather than rejected.
func (c *Checker) ListGroupsForDatabase(ctx context.Context, dbID int64) ([]contract.DatabaseGroupAccess, error) {
	const q = `
		SELECT g.id, g.name, gda.db_id, gda.level,
		       g.leader_user_id, u.username, COALESCE(u.is_active, FALSE)
		FROM group_db_access gda
		JOIN groups g ON g.id = gda.group_id
		LEFT JOIN users u ON u.id = g.leader_user_id
		WHERE gda.db_id = $1
		  AND gda.level IN (1, 2)
		ORDER BY g.name ASC, g.id ASC
	`
	rows, err := c.db.QueryContext(ctx, q, dbID)
	if err != nil {
		return nil, fmt.Errorf("access: list groups for database: %w", err)
	}
	defer rows.Close()

	out := make([]contract.DatabaseGroupAccess, 0)
	for rows.Next() {
		var (
			item           contract.DatabaseGroupAccess
			rawLevel       int64
			leaderID       sql.NullInt64
			leaderUsername sql.NullString
			leaderActive   bool
		)
		if err := rows.Scan(&item.GroupID, &item.GroupName, &item.DBID, &rawLevel, &leaderID, &leaderUsername, &leaderActive); err != nil {
			return nil, fmt.Errorf("access: scan database group access: %w", err)
		}
		item.Level = levelFromDB(rawLevel)
		item.LevelLabel = item.Level.Label()
		if leaderID.Valid {
			v := leaderID.Int64
			item.LeaderUserID = &v
		}
		if leaderUsername.Valid {
			v := leaderUsername.String
			item.LeaderUsername = &v
		}
		item.LeaderIsActive = leaderActive
		item.HasLeader = leaderID.Valid && leaderActive
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("access: iterate database group access: %w", err)
	}
	return out, nil
}

// ListUserGroups returns the groups a user belongs to and whether they are the group leader.
func (c *Checker) ListUserGroups(ctx context.Context, userID int64) ([]contract.UserGroupMembership, error) {
	const q = `
		SELECT g.id, g.name, g.description, (g.leader_user_id IS NOT NULL AND g.leader_user_id = gm.user_id) AS is_leader
		FROM group_members gm
		JOIN groups g ON g.id = gm.group_id
		JOIN users u ON u.id = gm.user_id
		WHERE gm.user_id = $1
		  AND u.is_active = TRUE
		ORDER BY g.name ASC, g.id ASC
	`
	rows, err := c.db.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("access: list user groups: %w", err)
	}
	defer rows.Close()

	out := make([]contract.UserGroupMembership, 0)
	for rows.Next() {
		var m contract.UserGroupMembership
		if err := rows.Scan(&m.GroupID, &m.GroupName, &m.Description, &m.IsLeader); err != nil {
			return nil, fmt.Errorf("access: scan user group: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("access: iterate user groups: %w", err)
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
