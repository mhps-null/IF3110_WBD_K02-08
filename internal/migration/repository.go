package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/example/if3110-milestone1/internal/contract"
)

type Repository interface {
	GetByID(ctx context.Context, id int64) (*Migration, error)
	ListByDB(ctx context.Context, dbID int64) ([]*Migration, error)
	CreateDraft(ctx context.Context, m *Migration) error
	UpdateDraft(ctx context.Context, m *Migration) error
	DeleteDraft(ctx context.Context, id int64) error
	RecordExecution(ctx context.Context, id int64, status string, executedBy int64, errMsg *string, checksum string) (*Migration, error)
}

type SQLRepository struct {
	db *sql.DB
}

func NewSQLRepository(db *sql.DB) *SQLRepository {
	return &SQLRepository{db: db}
}

func (r *SQLRepository) GetByID(ctx context.Context, id int64) (*Migration, error) {
	query := `
		SELECT 
			m.id, m.db_id, m.version, m.title, m.sql_text, m.status, 
			m.created_by, COALESCE(uc.username, ''), m.created_at,
			m.executed_by, ue.username, m.executed_at,
			m.error_message, m.checksum
		FROM migrations m
		LEFT JOIN users uc ON m.created_by = uc.id
		LEFT JOIN users ue ON m.executed_by = ue.id
		WHERE m.id = $1
	`
	row := r.db.QueryRowContext(ctx, query, id)

	var m Migration
	var execUser sql.NullString
	var err = row.Scan(
		&m.ID, &m.DBID, &m.Version, &m.Title, &m.SQLText, &m.Status,
		&m.CreatedBy, &m.CreatedByName, &m.CreatedAt,
		&m.ExecutedBy, &execUser, &m.ExecutedAt,
		&m.ErrorMessage, &m.Checksum,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, contract.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query migration: %w", err)
	}
	if execUser.Valid {
		m.ExecutedByName = &execUser.String
	}
	return &m, nil
}

func (r *SQLRepository) ListByDB(ctx context.Context, dbID int64) ([]*Migration, error) {
	query := `
		SELECT 
			m.id, m.db_id, m.version, m.title, m.sql_text, m.status, 
			m.created_by, COALESCE(uc.username, ''), m.created_at,
			m.executed_by, ue.username, m.executed_at,
			m.error_message, m.checksum
		FROM migrations m
		LEFT JOIN users uc ON m.created_by = uc.id
		LEFT JOIN users ue ON m.executed_by = ue.id
		WHERE m.db_id = $1
		ORDER BY 
			CASE WHEN m.version IS NOT NULL THEN m.version ELSE 999999999 END ASC,
			m.created_at ASC
	`
	rows, err := r.db.QueryContext(ctx, query, dbID)
	if err != nil {
		return nil, fmt.Errorf("failed to list migrations: %w", err)
	}
	defer rows.Close()

	var result []*Migration
	for rows.Next() {
		var m Migration
		var execUser sql.NullString
		if err := rows.Scan(
			&m.ID, &m.DBID, &m.Version, &m.Title, &m.SQLText, &m.Status,
			&m.CreatedBy, &m.CreatedByName, &m.CreatedAt,
			&m.ExecutedBy, &execUser, &m.ExecutedAt,
			&m.ErrorMessage, &m.Checksum,
		); err != nil {
			return nil, fmt.Errorf("scan migration row: %w", err)
		}
		if execUser.Valid {
			m.ExecutedByName = &execUser.String
		}
		result = append(result, &m)
	}
	return result, rows.Err()
}

func (r *SQLRepository) CreateDraft(ctx context.Context, m *Migration) error {
	m.Status = StatusDraft
	if err := m.Validate(); err != nil {
		return err
	}
	query := `
		INSERT INTO migrations (db_id, version, title, sql_text, status, created_by, created_at)
		VALUES ($1, NULL, $2, $3, $4, $5, NOW())
		RETURNING id, created_at
	`
	return r.db.QueryRowContext(ctx, query, m.DBID, m.Title, m.SQLText, StatusDraft, m.CreatedBy).
		Scan(&m.ID, &m.CreatedAt)
}

func (r *SQLRepository) UpdateDraft(ctx context.Context, m *Migration) error {
	if err := m.Validate(); err != nil {
		return err
	}
	query := `
		UPDATE migrations 
		SET title = $1, sql_text = $2
		WHERE id = $3 AND status = $4
	`
	res, err := r.db.ExecContext(ctx, query, m.Title, m.SQLText, m.ID, StatusDraft)
	if err != nil {
		return fmt.Errorf("failed to update draft migration: %w", err)
	}
	ra, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if ra == 0 {
		return errors.New("migration not found or is no longer a draft")
	}
	return nil
}

func (r *SQLRepository) DeleteDraft(ctx context.Context, id int64) error {
	query := `DELETE FROM migrations WHERE id = $1 AND status = $2`
	res, err := r.db.ExecContext(ctx, query, id, StatusDraft)
	if err != nil {
		return fmt.Errorf("failed to delete draft migration: %w", err)
	}
	ra, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if ra == 0 {
		return errors.New("migration not found or cannot be deleted")
	}
	return nil
}

func (r *SQLRepository) RecordExecution(ctx context.Context, id int64, status string, executedBy int64, errMsg *string, checksum string) (*Migration, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin metadata transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var dbID int64
	var currentStatus string
	checkQuery := `SELECT db_id, status FROM migrations WHERE id = $1 FOR UPDATE`
	if err := tx.QueryRowContext(ctx, checkQuery, id).Scan(&dbID, &currentStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, contract.ErrNotFound
		}
		return nil, fmt.Errorf("failed to check migration: %w", err)
	}

	if currentStatus != StatusDraft {
		return nil, errors.New("migration has already been executed")
	}

	lockQuery := `SELECT pg_advisory_xact_lock(hashtext('db_migration_' || $1::text))`
	if _, err := tx.ExecContext(ctx, lockQuery, dbID); err != nil {
		return nil, fmt.Errorf("failed to acquire advisory lock for database: %w", err)
	}

	var nextVersion int64
	versionQuery := `SELECT COALESCE(MAX(version), 0) + 1 FROM migrations WHERE db_id = $1 AND version IS NOT NULL`
	if err := tx.QueryRowContext(ctx, versionQuery, dbID).Scan(&nextVersion); err != nil {
		return nil, fmt.Errorf("failed to calculate next migration version: %w", err)
	}

	now := time.Now()
	updateQuery := `
		UPDATE migrations
		SET version = $1, status = $2, executed_by = $3, executed_at = $4, error_message = $5, checksum = $6
		WHERE id = $7
	`
	if _, err := tx.ExecContext(ctx, updateQuery, nextVersion, status, executedBy, now, errMsg, checksum, id); err != nil {
		return nil, fmt.Errorf("failed to record migration execution: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit metadata transaction: %w", err)
	}

	return r.GetByID(ctx, id)
}

type MemoryRepository struct {
	mu         sync.RWMutex
	migrations map[int64]*Migration
	nextID     int64
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		migrations: make(map[int64]*Migration),
		nextID:     1,
	}
}

func (m *MemoryRepository) GetByID(ctx context.Context, id int64) (*Migration, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	item, exists := m.migrations[id]
	if !exists {
		return nil, contract.ErrNotFound
	}
	cp := *item
	return &cp, nil
}

func (m *MemoryRepository) ListByDB(ctx context.Context, dbID int64) ([]*Migration, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*Migration
	for _, item := range m.migrations {
		if item.DBID == dbID {
			cp := *item
			result = append(result, &cp)
		}
	}
	return result, nil
}

func (m *MemoryRepository) CreateDraft(ctx context.Context, mig *Migration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	mig.Status = StatusDraft
	if err := mig.Validate(); err != nil {
		return err
	}
	mig.ID = m.nextID
	m.nextID++
	mig.CreatedAt = time.Now()

	cp := *mig
	m.migrations[mig.ID] = &cp
	return nil
}

func (m *MemoryRepository) UpdateDraft(ctx context.Context, mig *Migration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	item, exists := m.migrations[mig.ID]
	if !exists {
		return contract.ErrNotFound
	}
	if item.Status != StatusDraft {
		return errors.New("migration not in draft status is immutable")
	}

	item.Title = mig.Title
	item.SQLText = mig.SQLText
	return nil
}

func (m *MemoryRepository) DeleteDraft(ctx context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	item, exists := m.migrations[id]
	if !exists {
		return contract.ErrNotFound
	}
	if item.Status != StatusDraft {
		return errors.New("cannot delete executed migration")
	}
	delete(m.migrations, id)
	return nil
}

func (m *MemoryRepository) RecordExecution(ctx context.Context, id int64, status string, executedBy int64, errMsg *string, checksum string) (*Migration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	item, exists := m.migrations[id]
	if !exists {
		return nil, contract.ErrNotFound
	}
	if item.Status != StatusDraft {
		return nil, errors.New("migration has already been executed")
	}

	var maxVersion int64 = 0
	for _, other := range m.migrations {
		if other.DBID == item.DBID && other.Version != nil && *other.Version > maxVersion {
			maxVersion = *other.Version
		}
	}
	nextVersion := maxVersion + 1

	now := time.Now()
	item.Version = &nextVersion
	item.Status = status
	item.ExecutedBy = &executedBy
	item.ExecutedAt = &now
	item.ErrorMessage = errMsg
	item.Checksum = &checksum

	cp := *item
	return &cp, nil
}
