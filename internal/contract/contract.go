package contract

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"
)

type AccessLevel int

const (
	AccessNone AccessLevel = iota
	AccessViewOnly
	AccessViewEdit
)

type AccessibleDB struct {
	DBID  int64
	Name  string
	Level AccessLevel
}

type AccessChecker interface {
	EffectiveLevel(ctx context.Context, userID, dbID int64) (AccessLevel, error)
	ListAccessible(ctx context.Context, userID int64) ([]AccessibleDB, error)
}

// Guards contains route authorization middleware functions.
type Guards struct {
	Admin  func(http.Handler) http.Handler
	ViewDB func(http.Handler) http.Handler
	EditDB func(http.Handler) http.Handler
}

type Engine string

const (
	EnginePostgres Engine = "postgres"
	EngineMySQL    Engine = "mysql"
	EngineMariaDB  Engine = "mariadb"
)

type TargetDB interface {
	Open(ctx context.Context, dbID int64) (*sql.DB, Engine, error)
}

type SchemaSnapshot struct {
	Engine Engine
	Tables []Table
}

type Table struct {
	Schema, Name string
	Columns      []Column
	Constraints  []Constraint
	Indexes      []Index
}

type Column struct {
	Name, DataType string
	Nullable       bool
	Default        *string
	IsPrimaryKey   bool
	Ordinal        int // 1-based column position
}

type Constraint struct {
	Name       string
	Type       string // PRIMARY KEY, FOREIGN KEY, UNIQUE, or CHECK
	Columns    []string
	RefTable   string
	RefColumns []string
	Definition string
}

type Index struct {
	Name    string
	Columns []string
	Unique  bool
	Method  string
}

type PageRequest struct {
	Page, PageSize int
	OrderBy        string
	Desc           bool
}

type RowPage struct {
	Columns []string
	Rows    [][]any
	Total   int64
}

// Introspector inspects database metadata and retrieves paginated table rows.
type Introspector interface {
	Snapshot(ctx context.Context, db *sql.DB) (SchemaSnapshot, error)
	Rows(ctx context.Context, db *sql.DB, schema, table string, p PageRequest) (RowPage, error)
}

type ExecResult struct {
	RowsAffected int64
	Duration     time.Duration
}

type PlanNode struct {
	NodeType, Relation, IndexName, JoinType string
	StartupCost, TotalCost                  float64
	PlanRows                                int64
	ActualTimeMs                            *float64
	ActualRows                              *int64
	Children                                []PlanNode
}

type Plan struct {
	Root        PlanNode
	Analyzed    bool
	PlanningMs  *float64
	ExecutionMs *float64
	Raw         string
}

type Executor interface {
	Execute(ctx context.Context, db *sql.DB, sqlText string) (ExecResult, error)
	Explain(ctx context.Context, db *sql.DB, sqlText string, analyze bool) (Plan, error)
}

var (
	ErrConnectionFailed = errors.New("database connection failed")
	ErrAccessDenied     = errors.New("access denied")
	ErrNotFound         = errors.New("not found")
)
