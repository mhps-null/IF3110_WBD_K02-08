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

func (l AccessLevel) Label() string {
	switch l {
	case AccessViewOnly:
		return "View Only"
	case AccessViewEdit:
		return "View + Edit"
	default:
		return "None"
	}
}

type AccessibleDB struct {
	DBID   int64       `json:"db_id"`
	Name   string      `json:"name"`
	Engine Engine      `json:"engine,omitempty"`
	Level  AccessLevel `json:"level"`
}

// DatabaseGroupAccess describes a group that has access to a target database,
// along with its assigned leader (if any). Approval workflows use this to
// compute required approvers and detect groups missing an active leader.
type DatabaseGroupAccess struct {
	GroupID        int64       `json:"group_id"`
	GroupName      string      `json:"group_name"`
	DBID           int64       `json:"db_id"`
	Level          AccessLevel `json:"level"`
	LevelLabel     string      `json:"level_label"`
	LeaderUserID   *int64      `json:"leader_user_id"`
	LeaderUsername *string     `json:"leader_username"`
	LeaderIsActive bool        `json:"leader_is_active"`
	HasLeader      bool        `json:"has_leader"`
}

// UserGroupMembership describes a group a user belongs to and whether the user leads it.
type UserGroupMembership struct {
	GroupID     int64  `json:"group_id"`
	GroupName   string `json:"group_name"`
	Description string `json:"description"`
	IsLeader    bool   `json:"is_leader"`
}

// GroupAccessInspector exposes live group access and leader metadata for
// approval calculation (Orang 3) and user identity context.
type GroupAccessInspector interface {
	ListGroupsForDatabase(ctx context.Context, dbID int64) ([]DatabaseGroupAccess, error)
	ListUserGroups(ctx context.Context, userID int64) ([]UserGroupMembership, error)
}

// PoolStat captures live connection pool telemetry for a managed target database.
type PoolStat struct {
	DBID         int64         `json:"db_id"`
	DBName       string        `json:"db_name"`
	Engine       Engine        `json:"engine"`
	OpenConns    int           `json:"open_conns"`
	InUse        int           `json:"in_use"`
	Idle         int           `json:"idle"`
	WaitCount    int64         `json:"wait_count"`
	WaitDuration time.Duration `json:"wait_duration"`
}

// PoolStatsProvider exposes live connection pool stats for observability metrics.
type PoolStatsProvider interface {
	PoolStats() []PoolStat
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
	Engine Engine  `json:"engine"`
	Schema string  `json:"schema"` // Active database for MySQL/MariaDB, including an empty database.
	Tables []Table `json:"tables"`
}

type Table struct {
	Schema      string       `json:"schema"`
	Name        string       `json:"name"`
	Columns     []Column     `json:"columns"`
	Constraints []Constraint `json:"constraints"`
	Indexes     []Index      `json:"indexes"`
}

type Column struct {
	Name         string  `json:"name"`
	DataType     string  `json:"data_type"`
	Nullable     bool    `json:"nullable"`
	Default      *string `json:"default"`
	IsPrimaryKey bool    `json:"is_primary_key"`
	Ordinal      int     `json:"ordinal"` // 1-based column position
}

type Constraint struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"` // PRIMARY KEY, FOREIGN KEY, UNIQUE, or CHECK
	Columns      []string `json:"columns"`
	RefTable     string   `json:"ref_table,omitempty"`
	RefSchema    string   `json:"ref_schema,omitempty"`
	RefTableName string   `json:"ref_table_name,omitempty"` // Raw identifier; RefTable remains the display/legacy qualified name.
	RefColumns   []string `json:"ref_columns,omitempty"`
	OnDelete     string   `json:"on_delete,omitempty"`
	OnUpdate     string   `json:"on_update,omitempty"`
	Definition   string   `json:"definition,omitempty"`
}

type Index struct {
	Name           string   `json:"name"`
	Columns        []string `json:"columns"`
	Unique         bool     `json:"unique"`
	Method         string   `json:"method"`
	Predicate      string   `json:"predicate,omitempty"`       // PostgreSQL partial-index WHERE expression from the catalog.
	KeyDefinitions []string `json:"key_definitions,omitempty"` // Catalog SQL for index keys; Columns contains names/display values.
}

type PageRequest struct {
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
	OrderBy  string `json:"order_by"`
	Desc     bool   `json:"desc"`
}

type RowPage struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
	Total   int64    `json:"total"`
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
