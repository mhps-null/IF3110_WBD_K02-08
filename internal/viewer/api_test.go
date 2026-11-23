package viewer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/observability"
)

type apiStubTargetDB struct {
	err error
}

func (s apiStubTargetDB) Open(context.Context, int64) (*sql.DB, contract.Engine, error) {
	if s.err != nil {
		return nil, "", s.err
	}
	return nil, contract.EnginePostgres, nil
}

type apiStubIntrospector struct {
	snap contract.SchemaSnapshot
	rows contract.RowPage
	err  error
}

func (s apiStubIntrospector) Snapshot(context.Context, *sql.DB) (contract.SchemaSnapshot, error) {
	return s.snap, s.err
}

func (s apiStubIntrospector) Rows(context.Context, *sql.DB, string, string, contract.PageRequest) (contract.RowPage, error) {
	return s.rows, s.err
}

type apiStubAccess struct {
	dbs   []contract.AccessibleDB
	level contract.AccessLevel
}

func (s apiStubAccess) EffectiveLevel(context.Context, int64, int64) (contract.AccessLevel, error) {
	return s.level, nil
}
func (s apiStubAccess) ListAccessible(context.Context, int64) ([]contract.AccessibleDB, error) {
	return s.dbs, nil
}

func TestViewerAPIRoutesAuthzAndReadOnlyLogging(t *testing.T) {
	var logBuf bytes.Buffer
	obsLogger := observability.NewLogger(&logBuf, slog.LevelDebug)
	tracer := observability.NewTracer("api-test", nil)

	intro := apiStubIntrospector{
		snap: contract.SchemaSnapshot{
			Engine: contract.EnginePostgres,
			Tables: []contract.Table{
				{
					Schema: "public",
					Name:   "users",
					Columns: []contract.Column{
						{Name: "id", DataType: "bigint", IsPrimaryKey: true, Ordinal: 1},
					},
				},
			},
		},
		rows: contract.RowPage{
			Columns: []string{"id"},
			Rows:    [][]any{{1}, {2}},
			Total:   2,
		},
	}

	viewGuard := func(allow bool) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !allow {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "access denied"})
					return
				}
				next.ServeHTTP(w, r)
			})
		}
	}
	asUser := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(contract.WithPrincipal(r.Context(), contract.Principal{UserID: 2, Username: "viewer1"})))
		})
	}

	t.Run("unauthorized user blocked on schema and rows", func(t *testing.T) {
		h := NewHandler(apiStubTargetDB{}, intro, apiStubAccess{}, nil, contract.Guards{ViewDB: viewGuard(false)})
		mux := http.NewServeMux()
		h.RegisterAPIRoutes(mux, asUser)

		for _, path := range []string{"/api/databases/1/schema", "/api/databases/1/tables/users", "/api/databases/1/tables/users/data"} {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("path=%s status=%d want=403", path, rec.Code)
			}
		}
	})

	t.Run("authorized user reads databases, schema, table, and rows with SQL logging", func(t *testing.T) {
		h := NewHandler(
			apiStubTargetDB{},
			intro,
			apiStubAccess{
				dbs:   []contract.AccessibleDB{{DBID: 1, Name: "app_pg", Engine: contract.EnginePostgres, Level: contract.AccessViewOnly}},
				level: contract.AccessViewOnly,
			},
			nil,
			contract.Guards{ViewDB: viewGuard(true)},
		)
		h.SetObservability(obsLogger, tracer)
		mux := http.NewServeMux()
		h.RegisterAPIRoutes(mux, asUser)

		// List databases
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/databases", nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "View Only") {
			t.Fatalf("list databases failed: %d %s", rec.Code, rec.Body.String())
		}

		// Schema
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/databases/1/schema", nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"users"`) {
			t.Fatalf("schema failed: %d %s", rec.Code, rec.Body.String())
		}

		// Table data
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/databases/1/tables/users/data?page=1&page_size=20", nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total":2`) {
			t.Fatalf("table data failed: %d %s", rec.Code, rec.Body.String())
		}

		// Verify read-only SQL activity was logged as structured JSON
		logs := logBuf.String()
		if !strings.Contains(logs, `"event":"sql.start"`) || !strings.Contains(logs, `"event":"sql.result"`) || !strings.Contains(logs, `"actor":"viewer1"`) {
			t.Fatalf("expected structured read-only SQL logs, got: %s", logs)
		}
	})

	t.Run("connection failure returns 502 JSON", func(t *testing.T) {
		h := NewHandler(apiStubTargetDB{err: errors.New("dial tcp refused")}, intro, apiStubAccess{}, nil, contract.Guards{ViewDB: viewGuard(true)})
		mux := http.NewServeMux()
		h.RegisterAPIRoutes(mux, asUser)

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/databases/1/schema", nil))
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status=%d want=502", rec.Code)
		}
	})
}
