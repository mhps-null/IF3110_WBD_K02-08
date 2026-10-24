package schemadiff

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/example/if3110-milestone1/internal/admin"
	"github.com/example/if3110-milestone1/internal/contract"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type schemaDiffAccessChecker struct {
	levels map[int64]contract.AccessLevel
	listed []contract.AccessibleDB
	err    error
	checks []int64
}

func (f *schemaDiffAccessChecker) EffectiveLevel(_ context.Context, _ int64, dbID int64) (contract.AccessLevel, error) {
	f.checks = append(f.checks, dbID)
	if f.err != nil {
		return contract.AccessNone, f.err
	}
	return f.levels[dbID], nil
}

func (f *schemaDiffAccessChecker) ListAccessible(context.Context, int64) ([]contract.AccessibleDB, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]contract.AccessibleDB(nil), f.listed...), nil
}

type schemaDiffTargetDB struct {
	opens     []int64
	engines   map[int64]contract.Engine
	allowOpen bool
}

func (f *schemaDiffTargetDB) Open(_ context.Context, dbID int64) (*sql.DB, contract.Engine, error) {
	f.opens = append(f.opens, dbID)
	if !f.allowOpen {
		return nil, contract.EnginePostgres, errors.New("unexpected Open call in authorization test")
	}
	engine, ok := f.engines[dbID]
	if !ok {
		return nil, "", errors.New("missing fake engine")
	}
	db, err := sql.Open("pgx", "postgres://user:password@localhost:5432/test")
	if err != nil {
		return nil, "", err
	}
	return db, engine, nil
}

type schemaDiffIntrospector struct {
	snapshotCalls int
}

func (f *schemaDiffIntrospector) Snapshot(context.Context, *sql.DB) (contract.SchemaSnapshot, error) {
	f.snapshotCalls++
	return contract.SchemaSnapshot{}, errors.New("unexpected Snapshot call in authorization test")
}

func (schemaDiffIntrospector) Rows(context.Context, *sql.DB, string, string, contract.PageRequest) (contract.RowPage, error) {
	return contract.RowPage{}, errors.New("unexpected Rows call in authorization test")
}

type schemaDiffRenderer struct {
	name string
	data contract.PageData
}

func (f *schemaDiffRenderer) Render(_ http.ResponseWriter, name string, data contract.PageData) error {
	f.name = name
	f.data = data
	return nil
}

func newSchemaDiffTestHandler(checker contract.AccessChecker, target *schemaDiffTargetDB, renderer *schemaDiffRenderer) *Handler {
	return NewHandler(checker, &schemaDiffIntrospector{}, target, renderer, nil)
}

func requestWithPrincipal(method, path string, body string, principal contract.Principal) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.SetPathValue("id", "1")
	return req.WithContext(contract.WithPrincipal(req.Context(), principal))
}

func TestDoDiffRejectsUnauthorizedTargetBeforeOpeningDatabases(t *testing.T) {
	checker := &schemaDiffAccessChecker{
		levels: map[int64]contract.AccessLevel{
			1: contract.AccessViewEdit,
			2: contract.AccessNone,
		},
		listed: []contract.AccessibleDB{{DBID: 1, Name: "allowed-source", Level: contract.AccessViewEdit}},
	}
	target := &schemaDiffTargetDB{}
	renderer := &schemaDiffRenderer{}
	h := newSchemaDiffTestHandler(checker, target, renderer)

	r := requestWithPrincipal(http.MethodPost, "/databases/1/schema-diff", url.Values{"target_id": {"2"}}.Encode(), contract.Principal{UserID: 7, Username: "viewer"})
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.DoDiff(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want %d; body=%q", w.Code, http.StatusForbidden, w.Body.String())
	}
	if len(target.opens) != 0 {
		t.Fatalf("unauthorized request opened databases %v", target.opens)
	}
	if len(checker.checks) != 2 || checker.checks[0] != 1 || checker.checks[1] != 2 {
		t.Fatalf("authorization checks=%v, want source 1 and target 2", checker.checks)
	}
}

func TestShowDiffFormOnlyListsAccessibleDatabases(t *testing.T) {
	checker := &schemaDiffAccessChecker{
		levels: map[int64]contract.AccessLevel{
			1: contract.AccessViewOnly,
		},
		listed: []contract.AccessibleDB{
			{DBID: 1, Name: "source", Level: contract.AccessViewOnly},
			{DBID: 3, Name: "visible-target", Level: contract.AccessViewEdit},
		},
	}
	target := &schemaDiffTargetDB{}
	renderer := &schemaDiffRenderer{}
	h := newSchemaDiffTestHandler(checker, target, renderer)

	r := requestWithPrincipal(http.MethodGet, "/databases/1/schema-diff", "", contract.Principal{UserID: 7, Username: "viewer"})
	w := httptest.NewRecorder()
	h.ShowDiffForm(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d", w.Code, http.StatusOK)
	}
	if renderer.name != "schemadiff/form.html" {
		t.Fatalf("rendered template=%q", renderer.name)
	}
	data, ok := renderer.data.Data.(diffFormData)
	if !ok {
		t.Fatalf("renderer data type=%T, want diffFormData", renderer.data.Data)
	}
	if len(data.Databases) != 2 || data.Databases[0].DBID != 1 || data.Databases[1].DBID != 3 {
		t.Fatalf("form database list=%+v, want only the two accessible databases", data.Databases)
	}
}

func TestDoDiffRejectsDifferentEnginesBeforeSnapshot(t *testing.T) {
	tests := []struct {
		name         string
		sourceEngine contract.Engine
		targetEngine contract.Engine
	}{
		{"postgres to mysql", contract.EnginePostgres, contract.EngineMySQL},
		{"postgres to mariadb", contract.EnginePostgres, contract.EngineMariaDB},
		{"mysql to mariadb", contract.EngineMySQL, contract.EngineMariaDB},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checker := &schemaDiffAccessChecker{
				levels: map[int64]contract.AccessLevel{
					1: contract.AccessViewEdit,
					2: contract.AccessViewEdit,
				},
				listed: []contract.AccessibleDB{
					{DBID: 1, Name: "source", Level: contract.AccessViewEdit},
					{DBID: 2, Name: "target", Level: contract.AccessViewEdit},
				},
			}
			target := &schemaDiffTargetDB{
				allowOpen: true,
				engines: map[int64]contract.Engine{
					1: tc.sourceEngine,
					2: tc.targetEngine,
				},
			}
			introspector := &schemaDiffIntrospector{}
			h := NewHandler(checker, introspector, target, &schemaDiffRenderer{}, nil)

			r := requestWithPrincipal(http.MethodPost, "/databases/1/schema-diff", url.Values{"target_id": {"2"}}.Encode(), contract.Principal{UserID: 7, Username: "viewer"})
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			h.DoDiff(w, r)

			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status=%d, want %d; body=%q", w.Code, http.StatusUnprocessableEntity, w.Body.String())
			}
			if len(target.opens) != 2 {
				t.Fatalf("database opens=%v, want source and target opened", target.opens)
			}
			if introspector.snapshotCalls != 0 {
				t.Fatalf("snapshot called %d times for cross-engine diff", introspector.snapshotCalls)
			}
		})
	}
}

func TestSupportedDiffEngine(t *testing.T) {
	for _, engine := range []contract.Engine{contract.EnginePostgres, contract.EngineMySQL, contract.EngineMariaDB} {
		if !supportedDiffEngine(engine) {
			t.Errorf("engine %q should be supported", engine)
		}
	}
	if supportedDiffEngine(contract.Engine("oracle")) {
		t.Fatal("unknown engine must not be treated as a supported schema diff dialect")
	}
}

func TestDoDiffFailsClosedWhenAuthorizationCheckErrors(t *testing.T) {
	checker := &schemaDiffAccessChecker{
		levels: map[int64]contract.AccessLevel{1: contract.AccessViewEdit, 2: contract.AccessViewEdit},
		err:    errors.New("simulated metadata outage"),
	}
	target := &schemaDiffTargetDB{}
	h := newSchemaDiffTestHandler(checker, target, &schemaDiffRenderer{})

	r := requestWithPrincipal(http.MethodPost, "/databases/1/schema-diff", url.Values{"target_id": {"2"}}.Encode(), contract.Principal{UserID: 7, Username: "viewer"})
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.DoDiff(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want %d", w.Code, http.StatusInternalServerError)
	}
	if len(target.opens) != 0 {
		t.Fatalf("databases opened while authorization backend failed: %v", target.opens)
	}
}

var _ contract.AccessChecker = (*schemaDiffAccessChecker)(nil)
var _ contract.TargetDB = (*schemaDiffTargetDB)(nil)
var _ contract.Introspector = (*schemaDiffIntrospector)(nil)
var _ admin.Renderer = (*schemaDiffRenderer)(nil)
