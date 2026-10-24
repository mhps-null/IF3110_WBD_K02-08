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

// Authorization revision contract:
// users.privilege_version is a monotonically increasing per-user authorization
// revision. Any metadata transaction that changes effective authorization must
// increment it for each affected user in the same transaction. This includes
// changes to users.is_platform_admin, group_members, and group_db_access; a
// group_db_access change affects every current member of that group. P1 compares
// this revision with sessions.privilege_version and rotates the Session ID when
// they differ. This revision does not replace EffectiveLevel or route guards:
// authorization must still be checked against current metadata on each request.
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
	Schema string // Active database for MySQL/MariaDB, including an empty database.
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
	Name         string
	Type         string // PRIMARY KEY, FOREIGN KEY, UNIQUE, or CHECK
	Columns      []string
	RefTable     string
	RefSchema    string
	RefTableName string // Raw identifier; RefTable remains the display/legacy qualified name.
	RefColumns   []string
	OnDelete     string
	OnUpdate     string
	Definition   string
}

type Index struct {
	Name           string
	Columns        []string
	Unique         bool
	Method         string
	Predicate      string   // PostgreSQL partial-index WHERE expression from the catalog.
	KeyDefinitions []string // Catalog SQL for index keys; Columns contains names/display values.
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
	CostAvailable                           bool
	StartupCostAvailable                    bool
	PlanRows                                int64
	PlanRowsAvailable                       bool
	ActualTimeMs                            *float64
	ActualRows                              *int64
	ActualRowsPerLoop                       *float64
	ActualLoops                             *int64
	Children                                []PlanNode
}

type Plan struct {
	Root        PlanNode
	Analyzed    bool
	PlanningMs  *float64
	ExecutionMs *float64
	Raw         string
	Notice      string // Safe explanation of unsupported analysis or estimated fallback.
	MetricsNote string // Engine-specific meaning of timing, rows, and loops.
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
