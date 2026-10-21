package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var ErrUserNotFound = errors.New("user tidak ditemukan")

type User struct {
	ID              int64
	Username        string
	PasswordHash    string
	IsPlatformAdmin bool
	IsActive        bool
}

type UserRepository interface {
	FindByUsername(ctx context.Context, username string) (User, error)
	FindByID(ctx context.Context, userID int64) (User, error)
}

type SQLUsers struct{ db *sql.DB }

func NewSQLUsers(db *sql.DB) *SQLUsers { return &SQLUsers{db: db} }

func (r *SQLUsers) FindByUsername(ctx context.Context, username string) (User, error) {
	var u User
	err := r.db.QueryRowContext(ctx, `SELECT id, username, password_hash, is_platform_admin, is_active
		FROM users WHERE username = $1 LIMIT 1`, username).Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.IsPlatformAdmin, &u.IsActive)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("cari user berdasarkan username: %w", err)
	}
	return u, nil
}

func (r *SQLUsers) FindByID(ctx context.Context, userID int64) (User, error) {
	var u User
	err := r.db.QueryRowContext(ctx, `SELECT id, username, password_hash, is_platform_admin, is_active
		FROM users WHERE id = $1 LIMIT 1`, userID).Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.IsPlatformAdmin, &u.IsActive)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("cari user berdasarkan ID: %w", err)
	}
	return u, nil
}
