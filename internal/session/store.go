package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/example/if3110-milestone1/internal/contract"
)

var ErrInvalid = errors.New("session tidak valid")

type Config struct {
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
	RotateInterval  time.Duration
	GracePeriod     time.Duration
}

func DefaultConfig() Config {
	return Config{
		IdleTimeout:     30 * time.Minute,
		AbsoluteTimeout: 8 * time.Hour,
		RotateInterval:  15 * time.Minute,
		GracePeriod:     30 * time.Second,
	}
}

type Store interface {
	Create(ctx context.Context, userID int64, now time.Time) (string, contract.SessionInfo, error)
	Load(ctx context.Context, rawID string, now time.Time) (contract.SessionInfo, error)
	Rotate(ctx context.Context, rawID string, now time.Time) (string, contract.SessionInfo, error)
	Revoke(ctx context.Context, rawID string) error
	contract.SessionRevoker
}

type SQLStore struct {
	db  *sql.DB
	cfg Config
}

func NewSQLStore(db *sql.DB, cfg Config) *SQLStore {
	return &SQLStore{db: db, cfg: cfg}
}

func (s *SQLStore) Create(ctx context.Context, userID int64, now time.Time) (string, contract.SessionInfo, error) {
	raw, err := newID()
	if err != nil {
		return "", contract.SessionInfo{}, err
	}
	info := contract.SessionInfo{UserID: userID, CreatedAt: now, LastSeenAt: now, RotatedAt: now, AbsoluteExpiresAt: now.Add(s.cfg.AbsoluteTimeout), IsCurrentID: true}
	err = s.db.QueryRowContext(ctx, `INSERT INTO sessions
		(user_id, id_hash, created_at, last_seen_at, rotated_at, absolute_expires_at, privilege_version)
		SELECT id, $2, $3, $3, $3, $4, privilege_version
		FROM users WHERE id = $1
		RETURNING privilege_version`, userID, hashID(raw), now, info.AbsoluteExpiresAt).Scan(&info.PrivilegeVersion)
	if err != nil {
		return "", contract.SessionInfo{}, fmt.Errorf("buat session: %w", err)
	}
	info.CurrentPrivilegeVersion = info.PrivilegeVersion
	return raw, info, nil
}

func (s *SQLStore) Load(ctx context.Context, rawID string, now time.Time) (contract.SessionInfo, error) {
	if rawID == "" {
		return contract.SessionInfo{}, ErrInvalid
	}
	var info contract.SessionInfo
	var revokedAt sql.NullTime
	var prevIDHash sql.NullString
	var prevValidUntil sql.NullTime
	var currentHash string
	err := s.db.QueryRowContext(ctx, `SELECT s.user_id, s.created_at, s.last_seen_at, s.rotated_at,
		s.absolute_expires_at, s.revoked_at, s.id_hash, s.prev_id_hash, s.prev_valid_until,
		s.privilege_version, u.privilege_version
		FROM sessions AS s
		JOIN users AS u ON u.id = s.user_id
		WHERE s.id_hash = $1 OR (s.prev_id_hash = $1 AND s.prev_valid_until > $2)
		LIMIT 1`, hashID(rawID), now).Scan(
		&info.UserID, &info.CreatedAt, &info.LastSeenAt, &info.RotatedAt,
		&info.AbsoluteExpiresAt, &revokedAt, &currentHash, &prevIDHash, &prevValidUntil,
		&info.PrivilegeVersion, &info.CurrentPrivilegeVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return contract.SessionInfo{}, ErrInvalid
	}
	if err != nil {
		return contract.SessionInfo{}, fmt.Errorf("baca session: %w", err)
	}
	info.IsCurrentID = currentHash == hashID(rawID)
	info.PrivilegeChanged = info.PrivilegeVersion != info.CurrentPrivilegeVersion
	if revokedAt.Valid || !now.Before(info.AbsoluteExpiresAt) || now.Sub(info.LastSeenAt) >= s.cfg.IdleTimeout {
		_, _ = s.db.ExecContext(ctx, `UPDATE sessions SET revoked_at = COALESCE(revoked_at, $1) WHERE id_hash = $2 OR prev_id_hash = $2`, now, hashID(rawID))
		return contract.SessionInfo{}, ErrInvalid
	}
	if prevIDHash.Valid && prevValidUntil.Valid && !now.Before(prevValidUntil.Time) && !info.IsCurrentID {
		return contract.SessionInfo{}, ErrInvalid
	}
	result, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = $1
		WHERE (id_hash = $2 OR (prev_id_hash = $2 AND prev_valid_until > $1))
		AND revoked_at IS NULL AND absolute_expires_at > $1`, now, hashID(rawID))
	if err != nil {
		return contract.SessionInfo{}, fmt.Errorf("perbarui aktivitas session: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return contract.SessionInfo{}, ErrInvalid
	}
	info.LastSeenAt = now
	return info, nil
}

func (s *SQLStore) Rotate(ctx context.Context, rawID string, now time.Time) (string, contract.SessionInfo, error) {
	if rawID == "" {
		return "", contract.SessionInfo{}, ErrInvalid
	}
	newRaw, err := newID()
	if err != nil {
		return "", contract.SessionInfo{}, err
	}
	var info contract.SessionInfo
	err = s.db.QueryRowContext(ctx, `UPDATE sessions AS s
		SET prev_id_hash = s.id_hash, prev_valid_until = $1, id_hash = $2,
			rotated_at = $3, privilege_version = u.privilege_version
		FROM users AS u
		WHERE s.user_id = u.id AND s.id_hash = $4
			AND s.revoked_at IS NULL AND s.absolute_expires_at > $3
		RETURNING s.user_id, s.created_at, s.last_seen_at, s.rotated_at,
			s.absolute_expires_at, s.privilege_version`,
		now.Add(s.cfg.GracePeriod), hashID(newRaw), now, hashID(rawID)).Scan(
		&info.UserID, &info.CreatedAt, &info.LastSeenAt, &info.RotatedAt,
		&info.AbsoluteExpiresAt, &info.PrivilegeVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return "", contract.SessionInfo{}, ErrInvalid
	}
	if err != nil {
		return "", contract.SessionInfo{}, fmt.Errorf("rotasi session: %w", err)
	}
	info.IsCurrentID = true
	info.CurrentPrivilegeVersion = info.PrivilegeVersion
	info.PrivilegeChanged = false
	return newRaw, info, nil
}

func (s *SQLStore) Revoke(ctx context.Context, rawID string) error {
	if rawID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET revoked_at = NOW()
		WHERE (id_hash = $1 OR prev_id_hash = $1) AND revoked_at IS NULL`, hashID(rawID))
	if err != nil {
		return fmt.Errorf("cabut session: %w", err)
	}
	return nil
}

func (s *SQLStore) RevokeAllForUser(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET revoked_at = NOW() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	if err != nil {
		return fmt.Errorf("cabut semua session user: %w", err)
	}
	return nil
}

func newID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("buat session ID: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashID(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
