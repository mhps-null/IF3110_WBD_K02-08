package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

type ExecutionReservation interface {
	Migration() *Migration
	Complete(ctx context.Context, status string, executedBy int64, errMsg *string, checksum string) (*Migration, error)
	Abort()
}

type Repository interface {
	GetByID(ctx context.Context, id int64) (*Migration, error)
	ListByDB(ctx context.Context, dbID int64) ([]*Migration, error)
	CreateDraft(ctx context.Context, m *Migration) error
	UpdateDraft(ctx context.Context, m *Migration) error
	DeleteDraft(ctx context.Context, id int64) error
	BeginExecution(ctx context.Context, dbID, id int64) (ExecutionReservation, error)
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

type sqlExecutionReservation struct {
	repo      *SQLRepository
	tx        *sql.Tx
	migration *Migration
	done      bool
}

// BeginExecution acquires a per-database transaction-scoped advisory lock and
// locks the draft row before the target database is touched. The transaction
// must remain open until Complete or Abort so concurrent executions serialize.
func (r *SQLRepository) BeginExecution(ctx context.Context, dbID, id int64) (ExecutionReservation, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin migration execution reservation: %w", err)
	}
	abort := func(err error) (ExecutionReservation, error) {
		_ = tx.Rollback()
		return nil, err
	}

	lockQuery := `SELECT pg_advisory_xact_lock(hashtext('db_migration_' || ($1::bigint)::text))`
	if _, err := tx.ExecContext(ctx, lockQuery, dbID); err != nil {
		return abort(fmt.Errorf("failed to acquire advisory lock for database: %w", err))
	}

	var m Migration
	query := `SELECT id, db_id, version, title, sql_text, status, created_by, created_at
		FROM migrations WHERE db_id = $1 AND id = $2 FOR UPDATE`
	err = tx.QueryRowContext(ctx, query, dbID, id).Scan(
		&m.ID, &m.DBID, &m.Version, &m.Title, &m.SQLText, &m.Status, &m.CreatedBy, &m.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return abort(contract.ErrNotFound)
	}
	if err != nil {
		return abort(fmt.Errorf("failed to load migration for execution: %w", err))
	}
	if m.Status != StatusDraft {
		return abort(errors.New("migration has already been executed or is not a draft"))
	}

	return &sqlExecutionReservation{repo: r, tx: tx, migration: &m}, nil
}

func (e *sqlExecutionReservation) Migration() *Migration {
	cp := *e.migration
	return &cp
}

func (e *sqlExecutionReservation) Abort() {
	if e == nil || e.done {
		return
	}
	e.done = true
	_ = e.tx.Rollback()
}

func (e *sqlExecutionReservation) Complete(ctx context.Context, status string, executedBy int64, errMsg *string, checksum string) (*Migration, error) {
	if e == nil || e.done {
		return nil, errors.New("migration execution reservation is already closed")
	}
	abortWith := func(err error) (*Migration, error) {
		e.Abort()
		return nil, err
	}
	if status != StatusSuccess && status != StatusFailed {
		return abortWith(fmt.Errorf("invalid execution result status %q", status))
	}

	var nextVersion int64
	versionQuery := `SELECT COALESCE(MAX(version), 0) + 1 FROM migrations WHERE db_id = $1 AND version IS NOT NULL`
	if err := e.tx.QueryRowContext(ctx, versionQuery, e.migration.DBID).Scan(&nextVersion); err != nil {
		return abortWith(fmt.Errorf("failed to calculate next migration version: %w", err))
	}

	now := time.Now().UTC()
	updateQuery := `UPDATE migrations
		SET version = $1, status = $2, executed_by = $3, executed_at = $4,
			error_message = $5, checksum = $6
		WHERE id = $7 AND db_id = $8 AND status = $9`
	res, err := e.tx.ExecContext(ctx, updateQuery, nextVersion, status, executedBy, now, errMsg, checksum,
		e.migration.ID, e.migration.DBID, StatusDraft)
	if err != nil {
		return abortWith(fmt.Errorf("failed to record migration execution: %w", err))
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return abortWith(fmt.Errorf("failed to verify migration execution update: %w", err))
	}
	if rows != 1 {
		return abortWith(errors.New("migration was no longer an executable draft"))
	}

	if err := e.tx.Commit(); err != nil {
		e.done = true
		return nil, fmt.Errorf("commit metadata transaction: %w", err)
	}
	e.done = true
	return e.repo.GetByID(ctx, e.migration.ID)
}

func (r *SQLRepository) RecordExecution(ctx context.Context, id int64, status string, executedBy int64, errMsg *string, checksum string) (*Migration, error) {
	m, err := r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	reservation, err := r.BeginExecution(ctx, m.DBID, id)
	if err != nil {
		return nil, err
	}
	defer reservation.Abort()
	return reservation.Complete(ctx, status, executedBy, errMsg, checksum)
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

type memoryExecutionReservation struct {
	repo      *MemoryRepository
	migration *Migration // actual record; repo.mu remains locked until Complete/Abort
	done      bool
}

func (m *MemoryRepository) BeginExecution(_ context.Context, dbID, id int64) (ExecutionReservation, error) {
	m.mu.Lock()
	item, exists := m.migrations[id]
	if !exists || item.DBID != dbID {
		m.mu.Unlock()
		return nil, contract.ErrNotFound
	}
	if item.Status != StatusDraft {
		m.mu.Unlock()
		return nil, errors.New("migration has already been executed or is not a draft")
	}
	return &memoryExecutionReservation{repo: m, migration: item}, nil
}

func (e *memoryExecutionReservation) Migration() *Migration {
	cp := *e.migration
	return &cp
}

func (e *memoryExecutionReservation) Abort() {
	if e == nil || e.done {
		return
	}
	e.done = true
	e.repo.mu.Unlock()
}

func (e *memoryExecutionReservation) Complete(_ context.Context, status string, executedBy int64, errMsg *string, checksum string) (*Migration, error) {
	if e == nil || e.done {
		return nil, errors.New("migration execution reservation is already closed")
	}
	if status != StatusSuccess && status != StatusFailed {
		e.Abort()
		return nil, fmt.Errorf("invalid execution result status %q", status)
	}

	var maxVersion int64
	for _, other := range e.repo.migrations {
		if other.DBID == e.migration.DBID && other.Version != nil && *other.Version > maxVersion {
			maxVersion = *other.Version
		}
	}
	nextVersion := maxVersion + 1
	now := time.Now().UTC()
	e.migration.Version = &nextVersion
	e.migration.Status = status
	e.migration.ExecutedBy = &executedBy
	e.migration.ExecutedAt = &now
	e.migration.ErrorMessage = errMsg
	e.migration.Checksum = &checksum
	cp := *e.migration
	e.done = true
	e.repo.mu.Unlock()
	return &cp, nil
}

func (m *MemoryRepository) RecordExecution(ctx context.Context, id int64, status string, executedBy int64, errMsg *string, checksum string) (*Migration, error) {
	item, err := m.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	reservation, err := m.BeginExecution(ctx, item.DBID, id)
	if err != nil {
		return nil, err
	}
	defer reservation.Abort()
	return reservation.Complete(ctx, status, executedBy, errMsg, checksum)
}
