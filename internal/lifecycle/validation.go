package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

// validateSQL uses PostgreSQL's extended protocol Parse phase
// Parse does NOT execute SQL, unlike EXPLAIN ANALYZE or a transaction-based dry run
// PostgreSQL accepts one statement per Parse message; multi-statement scripts must be split into separately reviewed migrations.
func validateSQL(ctx context.Context, db *sql.DB, statement string) error {
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

	if err != nil {
		return fmt.Errorf("SQL syntax validation failed: %w", ErrInvalid)
	}
	return nil
}

func (h Handler) validateRequest(ctx context.Context, requestID int64) error {
	if h.TargetPools == nil {
		return ErrInvalid
	}

	definition, err := h.Store.RevisionDetails(ctx, requestID)
	if err != nil {
		return err
	}
	if err = ValidateDefinition(definition); err != nil {
		return err
	}

	target, engine, err := h.TargetPools.Open(ctx, definition.DBID)
	if err != nil {
		return err
	}
	if engine != contract.EnginePostgres {
		return ErrInvalid
	}
	if err = validateSQL(ctx, target, definition.SQLUp); err != nil {
		return err
	}
	if definition.SQLDown != "" {
		if err = validateSQL(ctx, target, definition.SQLDown); err != nil {
			return err
		}
	}
	
	return nil
}
