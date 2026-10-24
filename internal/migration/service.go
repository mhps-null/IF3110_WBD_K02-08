package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/example/if3110-milestone1/internal/contract"
	"github.com/example/if3110-milestone1/internal/dbdriver/mysql"
	"github.com/example/if3110-milestone1/internal/dbdriver/postgres"
)

type Service interface {
	ListMigrations(ctx context.Context, dbID int64) ([]*Migration, error)
	GetMigration(ctx context.Context, dbID, id int64) (*Migration, error)
	CreateDraft(ctx context.Context, dbID int64, title, sqlText string, userID int64) (*Migration, error)
	UpdateDraft(ctx context.Context, dbID, id int64, title, sqlText string, userID int64) (*Migration, error)
	DeleteDraft(ctx context.Context, dbID, id int64) error
	ExecuteMigration(ctx context.Context, dbID, id, userID int64) (*Migration, contract.ExecResult, error)
	ExplainMigration(ctx context.Context, dbID, id int64, analyze bool) (contract.Plan, error)
}

type migrationService struct {
	repo      Repository
	targetDB  contract.TargetDB
	executors map[contract.Engine]contract.Executor
}

func NewService(repo Repository, targetDB contract.TargetDB) Service {
	executors := map[contract.Engine]contract.Executor{
		contract.EnginePostgres: postgres.NewExecutor(),
		contract.EngineMySQL:    mysql.NewExecutor(),
		contract.EngineMariaDB:  mysql.NewExecutor(),
	}
	return &migrationService{
		repo:      repo,
		targetDB:  targetDB,
		executors: executors,
	}
}

func NewCustomService(repo Repository, targetDB contract.TargetDB, executors map[contract.Engine]contract.Executor) Service {
	return &migrationService{
		repo:      repo,
		targetDB:  targetDB,
		executors: executors,
	}
}

func (s *migrationService) ListMigrations(ctx context.Context, dbID int64) ([]*Migration, error) {
	return s.repo.ListByDB(ctx, dbID)
}

func (s *migrationService) getMigrationForDatabase(ctx context.Context, dbID, id int64) (*Migration, error) {
	m, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if m.DBID != dbID {
		// Treat a migration from another database as not found, so callers do
		// not learn that a record exists outside the requested database.
		return nil, contract.ErrNotFound
	}
	return m, nil
}

func (s *migrationService) GetMigration(ctx context.Context, dbID, id int64) (*Migration, error) {
	return s.getMigrationForDatabase(ctx, dbID, id)
}

func (s *migrationService) CreateDraft(ctx context.Context, dbID int64, title, sqlText string, userID int64) (*Migration, error) {
	m := &Migration{
		DBID:      dbID,
		Title:     title,
		SQLText:   sqlText,
		CreatedBy: userID,
	}
	if err := s.repo.CreateDraft(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *migrationService) UpdateDraft(ctx context.Context, dbID, id int64, title, sqlText string, userID int64) (*Migration, error) {
	m, err := s.getMigrationForDatabase(ctx, dbID, id)
	if err != nil {
		return nil, err
	}
	if !m.IsEditable() {
		return nil, errors.New("migration ini sudah dieksekusi dan tidak dapat diubah")
	}

	m.Title = title
	m.SQLText = sqlText
	if err := s.repo.UpdateDraft(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *migrationService) DeleteDraft(ctx context.Context, dbID, id int64) error {
	m, err := s.getMigrationForDatabase(ctx, dbID, id)
	if err != nil {
		return err
	}
	if !m.IsEditable() {
		return errors.New("migration yang sudah dieksekusi tidak dapat dihapus")
	}
	return s.repo.DeleteDraft(ctx, id)
}

func (s *migrationService) ExecuteMigration(ctx context.Context, dbID, id, userID int64) (*Migration, contract.ExecResult, error) {
	m, err := s.getMigrationForDatabase(ctx, dbID, id)
	if err != nil {
		return nil, contract.ExecResult{}, err
	}
	if !m.IsEditable() {
		return nil, contract.ExecResult{}, errors.New("hanya migration berstatus draft yang dapat dieksekusi")
	}

	conn, engine, err := s.targetDB.Open(ctx, m.DBID)
	if err != nil {
		return nil, contract.ExecResult{}, fmt.Errorf("gagal membuka koneksi database target: %w", err)
	}

	executor, ok := s.executors[engine]
	if !ok || executor == nil {
		return nil, contract.ExecResult{}, fmt.Errorf("tidak ada executor yang terdaftar untuk engine: %s", engine)
	}

	checksum := ComputeChecksum(m.SQLText)

	res, execErr := executor.Execute(ctx, conn, m.SQLText)
	if execErr != nil {
		errMsg := execErr.Error()
		updatedM, recordErr := s.repo.RecordExecution(ctx, id, StatusFailed, userID, &errMsg, checksum)
		if recordErr != nil {
			return nil, res, fmt.Errorf("eksekusi gagal: %v; gagal mencatat riwayat metadata: %w", execErr, recordErr)
		}
		return updatedM, res, execErr
	}

	updatedM, recordErr := s.repo.RecordExecution(ctx, id, StatusSuccess, userID, nil, checksum)
	if recordErr != nil {
		return nil, res, fmt.Errorf("eksekusi di database target berhasil tetapi gagal mencatat status metadata: %w", recordErr)
	}

	return updatedM, res, nil
}

func (s *migrationService) ExplainMigration(ctx context.Context, dbID, id int64, analyze bool) (contract.Plan, error) {
	m, err := s.getMigrationForDatabase(ctx, dbID, id)
	if err != nil {
		return contract.Plan{}, err
	}

	conn, engine, err := s.targetDB.Open(ctx, m.DBID)
	if err != nil {
		return contract.Plan{}, fmt.Errorf("gagal membuka koneksi database target: %w", err)
	}

	executor, ok := s.executors[engine]
	if !ok || executor == nil {
		return contract.Plan{}, fmt.Errorf("tidak ada executor untuk engine: %s", engine)
	}

	return executor.Explain(ctx, conn, m.SQLText, analyze)
}

type FakeTargetDB struct {
	DB     *sql.DB
	Engine contract.Engine
	Err    error
}

func (f *FakeTargetDB) Open(ctx context.Context, dbID int64) (*sql.DB, contract.Engine, error) {
	if f.Err != nil {
		return nil, "", f.Err
	}
	return f.DB, f.Engine, nil
}
