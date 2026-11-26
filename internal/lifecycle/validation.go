package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

type sqlValidationError struct {
	Field    string `json:"field"`
	SQLState string `json:"sqlstate,omitempty"`
	Position int32  `json:"position,omitempty"`
	Reason   string `json:"reason"`
}

func (e *sqlValidationError) Error() string {
	label := "SQL Up"
	if e.Field == "sql_down" {
		label = "SQL Down"
	}
	if e.Position > 0 {
		label = fmt.Sprintf("%s (karakter %d)", label, e.Position)
	}
	return label + ". " + e.Reason
}

func (e *sqlValidationError) Unwrap() error { return ErrInvalid }

func sqlValidationFailure(field string, err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}

	// Use stable diagnostics without returning raw messages from the target database.
	reason := "PostgreSQL menolak SQL. Periksa sintaks dan skema database target (SQLSTATE " + pgErr.Code + ")."
	switch pgErr.Code {
	case "42601":
		reason = "Sintaks tidak valid. Gunakan satu statement SQL per Up atau Down."
	case "42P01", "42703":
		reason = "Tabel atau kolom tidak ditemukan. Periksa nama dan skema pada database target."
	case "42501":
		reason = "Koneksi database target tidak memiliki izin yang diperlukan."
	}
	return &sqlValidationError{Field: field, SQLState: pgErr.Code, Position: pgErr.Position, Reason: reason}
}

// validateSQL uses PostgreSQL's extended protocol Parse phase
// Parse does NOT execute SQL, unlike EXPLAIN ANALYZE or a transaction-based dry run
// PostgreSQL accepts one statement per Parse message; multi-statement scripts must be split into separately reviewed migrations.
func validateSQL(ctx context.Context, db *sql.DB, field, statement string) error {
	if strings.TrimSpace(statement) == "" {
		return ErrInvalid
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}

	defer conn.Close()
	err = conn.Raw(func(driverConn any) error {
		raw, ok := driverConn.(interface{ Conn() *pgx.Conn })
		if !ok {
			return errors.New("PostgreSQL protocol parser is unavailable")
		}
		_, err := raw.Conn().PgConn().Prepare(ctx, "", statement, nil)
		return err
	})

	return sqlValidationFailure(field, err)
}

func (h Handler) validateRequest(ctx context.Context, requestID int64) (int64, error) {
	if h.TargetPools == nil {
		return 0, errors.New("target connection manager is unavailable")
	}

	definition, revisionID, err := h.Store.revisionDetails(ctx, requestID)
	if err != nil {
		return 0, err
	}
	if err = ValidateDefinition(definition); err != nil {
		return 0, err
	}

	target, engine, err := h.TargetPools.Open(ctx, definition.DBID)
	if err != nil {
		return 0, err
	}
	if engine != contract.EnginePostgres {
		return 0, ErrInvalid
	}
	if err = validateSQL(ctx, target, "sql_up", definition.SQLUp); err != nil {
		return 0, err
	}
	if definition.SQLDown != "" {
		if err = validateSQL(ctx, target, "sql_down", definition.SQLDown); err != nil {
			return 0, err
		}
	}
	return revisionID, nil
}
