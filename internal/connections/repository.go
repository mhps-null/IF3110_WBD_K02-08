package connections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/example/if3110-milestone1/internal/contract"
)

var (
	ErrHasMigrationHistory = errors.New("cannot delete database connection with active migration history")
)

// Repository provides persistence operations for database connection profiles.
type Repository interface {
	Create(ctx context.Context, conn *Connection, plainPassword string) error
	FindByID(ctx context.Context, id int64) (*Connection, error)
	FindByIDWithPassword(ctx context.Context, id int64) (*Connection, string, error)
	List(ctx context.Context) ([]Connection, error)
	Update(ctx context.Context, conn *Connection, newPlainPassword *string) error
	Delete(ctx context.Context, id int64) error
}

// SQLRepository implements Repository backed by PostgreSQL with encrypted passwords.
type SQLRepository struct {
	db            *sql.DB
	encryptionKey []byte
}

func NewSQLRepository(db *sql.DB, encryptionKey []byte) (*SQLRepository, error) {
	if len(encryptionKey) != 32 {
		return nil, ErrInvalidKey
	}
	return &SQLRepository{db: db, encryptionKey: encryptionKey}, nil
}

func (r *SQLRepository) Create(ctx context.Context, conn *Connection, plainPassword string) error {
	encPassword, err := Encrypt(plainPassword, r.encryptionKey)
	if err != nil {
		return fmt.Errorf("encrypt password: %w", err)
	}

	query := `INSERT INTO db_connections (name, engine, host, port, database_name, username, password_enc, ssl_mode, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW())
		RETURNING id, created_at, updated_at`

	err = r.db.QueryRowContext(ctx, query,
		conn.Name,
		string(conn.Engine),
		conn.Host,
		conn.Port,
		conn.DatabaseName,
		conn.Username,
		encPassword,
		conn.SSLMode,
	).Scan(&conn.ID, &conn.CreatedAt, &conn.UpdatedAt)

	if err != nil {
		return fmt.Errorf("create connection: %w", err)
	}

	conn.PasswordEnc = encPassword
	return nil
}

func (r *SQLRepository) FindByID(ctx context.Context, id int64) (*Connection, error) {
	query := `SELECT id, name, engine, host, port, database_name, username, password_enc, ssl_mode, created_at, updated_at
		FROM db_connections WHERE id = $1`

	var conn Connection
	var engineStr string
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&conn.ID,
		&conn.Name,
		&engineStr,
		&conn.Host,
		&conn.Port,
		&conn.DatabaseName,
		&conn.Username,
		&conn.PasswordEnc,
		&conn.SSLMode,
		&conn.CreatedAt,
		&conn.UpdatedAt,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, contract.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find connection by id: %w", err)
	}

	conn.Engine = contract.Engine(engineStr)
	return &conn, nil
}

func (r *SQLRepository) FindByIDWithPassword(ctx context.Context, id int64) (*Connection, string, error) {
	conn, err := r.FindByID(ctx, id)
	if err != nil {
		return nil, "", err
	}

	plainPassword, err := Decrypt(conn.PasswordEnc, r.encryptionKey)
	if err != nil {
		return nil, "", fmt.Errorf("decrypt connection password: %w", err)
	}

	return conn, plainPassword, nil
}

func (r *SQLRepository) List(ctx context.Context) ([]Connection, error) {
	query := `SELECT id, name, engine, host, port, database_name, username, password_enc, ssl_mode, created_at, updated_at
		FROM db_connections ORDER BY name ASC, id ASC`

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	defer rows.Close()

	var result []Connection
	for rows.Next() {
		var conn Connection
		var engineStr string
		err := rows.Scan(
			&conn.ID,
			&conn.Name,
			&engineStr,
			&conn.Host,
			&conn.Port,
			&conn.DatabaseName,
			&conn.Username,
			&conn.PasswordEnc,
			&conn.SSLMode,
			&conn.CreatedAt,
			&conn.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan connection row: %w", err)
		}
		conn.Engine = contract.Engine(engineStr)
		result = append(result, conn)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate connection rows: %w", err)
	}

	return result, nil
}

func (r *SQLRepository) Update(ctx context.Context, conn *Connection, newPlainPassword *string) error {
	var updatedAt time.Time

	if newPlainPassword != nil && *newPlainPassword != "" {
		encPassword, err := Encrypt(*newPlainPassword, r.encryptionKey)
		if err != nil {
			return fmt.Errorf("encrypt new password: %w", err)
		}

		query := `UPDATE db_connections
			SET name = $1, engine = $2, host = $3, port = $4, database_name = $5, username = $6, password_enc = $7, ssl_mode = $8, updated_at = NOW()
			WHERE id = $9 RETURNING updated_at`

		err = r.db.QueryRowContext(ctx, query,
			conn.Name,
			string(conn.Engine),
			conn.Host,
			conn.Port,
			conn.DatabaseName,
			conn.Username,
			encPassword,
			conn.SSLMode,
			conn.ID,
		).Scan(&updatedAt)

		if errors.Is(err, sql.ErrNoRows) {
			return contract.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("update connection with password: %w", err)
		}
		conn.PasswordEnc = encPassword
	} else {
		// Update connection without touching password_enc
		query := `UPDATE db_connections
			SET name = $1, engine = $2, host = $3, port = $4, database_name = $5, username = $6, ssl_mode = $7, updated_at = NOW()
			WHERE id = $8 RETURNING updated_at, password_enc`

		var currentPasswordEnc string
		err := r.db.QueryRowContext(ctx, query,
			conn.Name,
			string(conn.Engine),
			conn.Host,
			conn.Port,
			conn.DatabaseName,
			conn.Username,
			conn.SSLMode,
			conn.ID,
		).Scan(&updatedAt, &currentPasswordEnc)

		if errors.Is(err, sql.ErrNoRows) {
			return contract.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("update connection: %w", err)
		}
		conn.PasswordEnc = currentPasswordEnc
	}

	conn.UpdatedAt = updatedAt
	return nil
}

func (r *SQLRepository) Delete(ctx context.Context, id int64) error {
	var migrationCount int64
	checkQuery := `SELECT COUNT(1) FROM migrations WHERE db_id = $1`
	err := r.db.QueryRowContext(ctx, checkQuery, id).Scan(&migrationCount)
	if err != nil {
		return fmt.Errorf("check migration history: %w", err)
	}
	if migrationCount > 0 {
		return ErrHasMigrationHistory
	}

	deleteQuery := `DELETE FROM db_connections WHERE id = $1`
	res, err := r.db.ExecContext(ctx, deleteQuery, id)
	if err != nil {
		return fmt.Errorf("delete connection: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("check delete result: %w", err)
	}
	if affected == 0 {
		return contract.ErrNotFound
	}

	return nil
}
