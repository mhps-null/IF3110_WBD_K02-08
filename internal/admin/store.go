package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrUserNotFound = errors.New("user tidak ditemukan")
	ErrDuplicateUsername = errors.New("username sudah dipakai")
)

type User struct {
	ID int64
	Username string
	IsAdmin bool
	IsActive bool
	CreatedAt time.Time
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// mereturn semua user urut username
// password hash ga di SELECT biar ga pernah masuk memori handler
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, username, is_platform_admin, is_active, created_at
		FROM users ORDER BY username`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.IsAdmin, &u.IsActive, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// menyisipkan user baru dgn hak platform-admin FALSE
// user jadi Platform Admin hanya lewat seed
func (s *Store) CreateUser(ctx context.Context, username, passwordHash string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO users (username, password_hash, is_platform_admin, is_active)
		VALUES ($1, $2, FALSE, TRUE)
		RETURNING id`, username, passwordHash).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrDuplicateUsername
		}
		return 0, fmt.Errorf("create user: %w", err)
	}
	return id, nil
}

// DeactivateUser disables the account and revokes all of its sessions atomically.
func (s *Store) DeactivateUser(ctx context.Context, userID int64) error {
	return s.updateUserAndRevokeSessions(ctx, userID, "deactivate user",
		`UPDATE users SET is_active = FALSE, privilege_version = privilege_version + 1 WHERE id = $1`)
}

func (s *Store) ActivateUser(ctx context.Context, userID int64) error {
	return s.updateUserAndRevokeSessions(ctx, userID, "activate user",
		`UPDATE users SET is_active = TRUE, privilege_version = privilege_version + 1 WHERE id = $1`)
}

// ResetPassword replaces the password hash and revokes every session of the user.
func (s *Store) ResetPassword(ctx context.Context, userID int64, passwordHash string) error {
	return s.updateUserAndRevokeSessions(ctx, userID, "reset password",
		`UPDATE users SET password_hash = $2, privilege_version = privilege_version + 1 WHERE id = $1`, passwordHash)
}

// updateUserAndRevokeSessions runs the user update and session revocation in one
func (s *Store) updateUserAndRevokeSessions(ctx context.Context, userID int64, op, userQuery string, extraArgs ...any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%s: begin tx: %w", op, err)
	}
	defer tx.Rollback()

	args := append([]any{userID}, extraArgs...)
	res, err := tx.ExecContext(ctx, userQuery, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUserNotFound
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE sessions SET revoked_at = NOW() WHERE user_id = $1 AND revoked_at IS NULL`, userID); err != nil {
		return fmt.Errorf("%s: revoke sessions: %w", op, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%s: commit: %w", op, err)
	}
	return nil
}

func (s *Store) BumpPrivilegeVersion(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET privilege_version = privilege_version + 1 WHERE id = $1`, userID)

	if err != nil {
		return fmt.Errorf("bump privilege version: %w", err)
	}

	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// Errors
var (
	ErrGroupNotFound = errors.New("group tidak ditemukan")
	ErrDuplicateGroupName = errors.New("nama group sudah dipakai")
)

// Tipe 
type Group struct {
	ID int64
	Name string
	Description string
}

type Database struct {
	ID int64
	Name string
}

type GroupAccess struct {
	GroupID int64
	DBID int64
	Level int // 1 = view only, 2 = view + edit
}

// Groups 
func (s *Store) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, description FROM groups ORDER BY name`)

	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}

	defer rows.Close()

	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Description); err != nil {
			return nil, fmt.Errorf("scan group: %w", err)
		}

		out = append(out, g)
	}

	return out, rows.Err()
}

func (s *Store) CreateGroup(ctx context.Context, name, description string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO groups (name, description) VALUES ($1, $2) RETURNING id`,
		name, description).Scan(&id)

	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrDuplicateGroupName
		}
		return 0, fmt.Errorf("create group: %w", err)
	}

	return id, nil
}

func (s *Store) GetGroup(ctx context.Context, id int64) (Group, error) {
	var g Group
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, description FROM groups WHERE id = $1`, id).
		Scan(&g.ID, &g.Name, &g.Description)

	if errors.Is(err, sql.ErrNoRows) {
		return Group{}, ErrGroupNotFound
	}

	if err != nil {
		return Group{}, fmt.Errorf("get group: %w", err)
	}

	return g, nil
}

// membership dan akses ikut kehapus lewat ON DELETE CASCADE.
// Sebelum dihapus, semua anggota dibump privilege_versionnya biar session mereka dirotasi pas request berikutnya
func (s *Store) DeleteGroup(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE users SET privilege_version = privilege_version + 1
		WHERE id IN (SELECT user_id FROM group_members WHERE group_id = $1)`, id); err != nil {
		return fmt.Errorf("bump members: %w", err)
	}

	res, err := tx.ExecContext(ctx, `DELETE FROM groups WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete group: %w", err)
	}

	if n, _ := res.RowsAffected(); n == 0 {
		return ErrGroupNotFound
	}

	return tx.Commit()
}

// Membership
func (s *Store) ListGroupMembers(ctx context.Context, groupID int64) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT u.id, u.username, u.is_platform_admin, u.is_active, u.created_at
		FROM group_members gm
		JOIN users u ON u.id = gm.user_id
		WHERE gm.group_id = $1
		ORDER BY u.username`, groupID)

	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}

	defer rows.Close()

	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.IsAdmin, &u.IsActive, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		out = append(out, u)
	}

	return out, rows.Err()
}

// kalo udh anggota, no-op (ga bump).
// kalo anggota baru, privilege_version user dibump supaya P1 merotasi session.
func (s *Store) AddMember(ctx context.Context, groupID, userID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO group_members (group_id, user_id) VALUES ($1, $2)
		ON CONFLICT (group_id, user_id) DO NOTHING`, groupID, userID)
	if err != nil {
		return fmt.Errorf("insert membership: %w", err)
	}

	if n, _ := res.RowsAffected(); n == 0 {
		return nil // sudah anggota, ga ada perubahan
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET privilege_version = privilege_version + 1 WHERE id = $1`,
		userID); err != nil {
		return fmt.Errorf("bump privilege: %w", err)
	}

	return tx.Commit()
}

func (s *Store) RemoveMember(ctx context.Context, groupID, userID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`DELETE FROM group_members WHERE group_id = $1 AND user_id = $2`,
		groupID, userID)

	if err != nil {
		return fmt.Errorf("delete membership: %w", err)
	}

	if n, _ := res.RowsAffected(); n == 0 {
		return nil // sudah bukan anggota
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET privilege_version = privilege_version + 1 WHERE id = $1`,
		userID); err != nil {
		return fmt.Errorf("bump privilege: %w", err)
	}

	return tx.Commit()
}

// Databases (baca dari db_connections) 

// return id + name dari db_connections.
func (s *Store) ListDatabases(ctx context.Context) ([]Database, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name FROM db_connections ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list databases: %w", err)
	}
	defer rows.Close()

	var out []Database
	for rows.Next() {
		var d Database
		if err := rows.Scan(&d.ID, &d.Name); err != nil {
			return nil, fmt.Errorf("scan database: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Access matrix 
func (s *Store) ListGroupAccess(ctx context.Context) ([]GroupAccess, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT group_id, db_id, level FROM group_db_access`)
	if err != nil {
		return nil, fmt.Errorf("list group access: %w", err)
	}
	defer rows.Close()

	var out []GroupAccess
	for rows.Next() {
		var a GroupAccess
		if err := rows.Scan(&a.GroupID, &a.DBID, &a.Level); err != nil {
			return nil, fmt.Errorf("scan group access: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetGroupAccessLevels updates all database access levels for a group in a single transaction.
// entries maps dbID -> level (0: remove, 1: View Only, 2: View + Edit).
// If any database ID is invalid/not found, or an invalid level is provided, the transaction is rolled back.
// If any level actually changed, privilege_version is incremented once for all group members.
func (s *Store) SetGroupAccessLevels(ctx context.Context, groupID int64, entries map[int64]int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM groups WHERE id = $1)`, groupID).Scan(&exists); err != nil {
		return fmt.Errorf("check group: %w", err)
	}
	if !exists {
		return ErrGroupNotFound
	}

	var anyChanged bool

	for dbID, level := range entries {
		if dbID <= 0 {
			return fmt.Errorf("id database tidak valid: %d", dbID)
		}
		var dbExists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM db_connections WHERE id = $1)`, dbID).Scan(&dbExists); err != nil {
			return fmt.Errorf("check db: %w", err)
		}
		if !dbExists {
			return fmt.Errorf("database #%d tidak ditemukan", dbID)
		}

		switch level {
		case 0:
			res, err := tx.ExecContext(ctx,
				`DELETE FROM group_db_access WHERE group_id = $1 AND db_id = $2`,
				groupID, dbID)
			if err != nil {
				return fmt.Errorf("delete group access: %w", err)
			}
			if n, _ := res.RowsAffected(); n > 0 {
				anyChanged = true
			}
		case 1, 2:
			res, err := tx.ExecContext(ctx, `
				INSERT INTO group_db_access (group_id, db_id, level) VALUES ($1, $2, $3)
				ON CONFLICT (group_id, db_id) DO UPDATE SET level = EXCLUDED.level
				WHERE group_db_access.level != EXCLUDED.level`,
				groupID, dbID, level)
			if err != nil {
				return fmt.Errorf("upsert group access: %w", err)
			}
			if n, _ := res.RowsAffected(); n > 0 {
				anyChanged = true
			}
		default:
			return fmt.Errorf("level tidak valid: %d", level)
		}
	}

	if anyChanged {
		if _, err := tx.ExecContext(ctx, `
			UPDATE users SET privilege_version = privilege_version + 1
			WHERE id IN (SELECT user_id FROM group_members WHERE group_id = $1)`,
			groupID); err != nil {
			return fmt.Errorf("bump members: %w", err)
		}
	}

	return tx.Commit()
}

func (s *Store) SetGroupAccess(ctx context.Context, groupID, dbID int64, level int) error {
	if level != 1 && level != 2 {
		return fmt.Errorf("level tidak valid: %d", level)
	}
	return s.SetGroupAccessLevels(ctx, groupID, map[int64]int{dbID: level})
}

func (s *Store) RemoveGroupAccess(ctx context.Context, groupID, dbID int64) error {
	return s.SetGroupAccessLevels(ctx, groupID, map[int64]int{dbID: 0})
}
