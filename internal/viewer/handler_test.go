package viewer

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/example/if3110-milestone1/internal/contract"
	"github.com/example/if3110-milestone1/internal/testutil"
)

type fakeTargetDB struct{ engine contract.Engine }

func (f *fakeTargetDB) Open(context.Context, int64) (*sql.DB, contract.Engine, error) {
	return nil, f.engine, nil
}

type fakeIntrospector struct {
	err    error
	page   contract.PageRequest
	schema string
	calls  int
}

func (f *fakeIntrospector) Snapshot(context.Context, *sql.DB) (contract.SchemaSnapshot, error) {
	f.calls++
	return contract.SchemaSnapshot{Tables: []contract.Table{{Schema: "public", Name: "users"}}}, f.err
}
func (f *fakeIntrospector) Rows(_ context.Context, _ *sql.DB, schema, _ string, p contract.PageRequest) (contract.RowPage, error) {
	f.calls++
	f.page, f.schema = p, schema
	return contract.RowPage{Columns: []string{"id"}, Rows: [][]any{{int64(1)}}, Total: 3}, f.err
}

type dummyViewerRenderer struct {
	data contract.PageData
	err  error
}

func (r *dummyViewerRenderer) Render(w http.ResponseWriter, _ string, data contract.PageData) error {
	r.data = data
	if r.err != nil {
		return r.err
	}
	_, err := w.Write([]byte("rendered"))
	return err
}
func viewerSetup() (*Handler, *fakeIntrospector, *dummyViewerRenderer) {
	checker := testutil.NewFakeAccessChecker()
	checker.RegisterDB(1, "allowed")
	checker.RegisterDB(2, "hidden")
	checker.AssignUserToGroup(1, 10)
	checker.GrantGroupAccess(10, 1, contract.AccessViewOnly)
	intro := &fakeIntrospector{}
	renderer := &dummyViewerRenderer{}
	return NewHandler(&fakeTargetDB{contract.EnginePostgres}, intro, checker, renderer, testutil.NewFakeGuards(checker)), intro, renderer
}
func viewerRequest(h http.Handler, path string, user int64) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	if user != 0 {
		req = req.WithContext(contract.WithPrincipal(req.Context(), contract.Principal{UserID: user, IsPlatformAdmin: user == 3}))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestListDatabases(t *testing.T) {
	h, _, renderer := viewerSetup()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	if rec := viewerRequest(mux, "/databases", 0); rec.Code != 303 || rec.Header().Get("Location") != "/login" {
		t.Fatal("anonymous request accepted")
	}
	if rec := viewerRequest(mux, "/databases", 1); rec.Code != 200 {
		t.Fatalf("list: %d", rec.Code)
	}
	data := renderer.data.Data.(struct{ Databases []contract.AccessibleDB })
	if len(data.Databases) != 1 || data.Databases[0].DBID != 1 {
		t.Fatalf("database filtering: %v", data.Databases)
	}
}

func TestViewerRoutesAndAccess(t *testing.T) {
	h, intro, _ := viewerSetup()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	for _, path := range []string{"/databases/1/schema", "/databases/1/tables/users", "/databases/1/tables/users/data"} {
		for _, user := range []int64{1, 2, 3} {
			want := 403
			if user == 1 {
				want = 200
			}
			if rec := viewerRequest(mux, path, user); rec.Code != want {
				t.Fatalf("user=%d %s: status=%d want=%d", user, path, rec.Code, want)
			}
		}
	}
	if rec := viewerRequest(mux, "/databases/1/tables/missing", 1); rec.Code != 404 {
		t.Fatal("missing table accepted")
	}
	viewerRequest(mux, "/databases/1/tables/users/data?page=2&page_size=2&order_by=id&desc=true", 1)
	if intro.page != (contract.PageRequest{Page: 2, PageSize: 2, OrderBy: "id", Desc: true}) {
		t.Fatalf("pagination/sorting: %+v", intro.page)
	}
	viewerRequest(mux, "/databases/1/tables/users/data?page=-1&page_size=999", 1)
	if intro.page.Page != 1 || intro.page.PageSize != 20 {
		t.Fatalf("pagination bounds: %+v", intro.page)
	}
	// Missing policy must fail closed.
	h.guards = contract.Guards{}
	blocked := http.NewServeMux()
	h.RegisterRoutes(blocked)
	if rec := viewerRequest(blocked, "/databases/1/schema", 1); rec.Code != 403 {
		t.Fatal("missing guard accepted")
	}
}

func TestViewerErrorsAndEngine(t *testing.T) {
	h, intro, renderer := viewerSetup()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	intro.err = errors.New("secret driver details")
	rec := viewerRequest(mux, "/databases/1/tables/users/data", 1)
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret") {
		t.Fatal("database error leaked")
	}
	intro.err = contract.ErrAccessDenied
	if rec := viewerRequest(mux, "/databases/1/tables/users/data", 1); rec.Code != 403 {
		t.Fatal("access denied not mapped to 403")
	}
	intro.err = nil
	renderer.err = errors.New("secret template details")
	for _, path := range []string{"/databases", "/databases/1/schema", "/databases/1/tables/users", "/databases/1/tables/users/data"} {
		rec := viewerRequest(mux, path, 1)
		if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret") {
			t.Fatalf("render error at %s: %d", path, rec.Code)
		}
	}
	renderer.err = nil
	h.targetDB = &fakeTargetDB{contract.EngineMySQL}
	calls := intro.calls
	if rec := viewerRequest(mux, "/databases/1/schema", 1); rec.Code != 500 || intro.calls != calls {
		t.Fatal("wrong-engine introspector used")
	}
	h.introspectors[contract.EngineMySQL] = intro
	if rec := viewerRequest(mux, "/databases/1/tables/users/data?schema=public", 1); rec.Code != 200 || intro.schema != "" {
		t.Fatal("MySQL engine/default schema routing failed")
	}
}
