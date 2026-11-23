package connections

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/audit"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

func TestConnectionsAPICRUDNoPasswordLeakAndTestConn(t *testing.T) {
	key := make([]byte, 32)
	repo, err := NewMemoryRepository(key)
	if err != nil {
		t.Fatal(err)
	}
	poolMgr := NewPoolManager(repo, "")
	auditStore := audit.NewMemoryStore()

	adminGuard := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := contract.PrincipalFrom(r.Context())
			if !ok || !p.IsPlatformAdmin {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	h := NewHandler(repo, poolMgr, nil, contract.Guards{Admin: adminGuard})
	h.SetAuditStore(auditStore)

	var testErr error
	h.testConnFn = func(context.Context, string, *Connection, string) error {
		return testErr
	}

	asAdmin := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(contract.WithPrincipal(r.Context(), contract.Principal{UserID: 1, Username: "admin", IsPlatformAdmin: true})))
		})
	}
	mux := http.NewServeMux()
	h.RegisterAPIRoutes(mux, asAdmin)

	// 1. Create PostgreSQL connection
	secretPass := "DbSuperSecret!99"
	createBody := `{"name":"ecommerce_pg","engine":"postgres","host":"localhost","port":5433,"database_name":"test_ecommerce_pg","username":"test_user","password":"` + secretPass + `","ssl_mode":"disable"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/connections", strings.NewReader(createBody)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d want=201 body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secretPass) || strings.Contains(rec.Body.String(), "PasswordEnc") {
		t.Fatalf("create response leaked password material: %s", rec.Body.String())
	}

	// 2. List connections (never exposes password)
	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/api/admin/connections", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status=%d want=200", listRec.Code)
	}
	var listResp struct {
		Connections []Connection `json:"connections"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil || len(listResp.Connections) != 1 {
		t.Fatalf("unexpected list response: %s err=%v", listRec.Body.String(), err)
	}
	if listResp.Connections[0].PasswordEnc != "" || strings.Contains(listRec.Body.String(), secretPass) {
		t.Fatal("list response must never include encrypted or raw password")
	}

	// 3. Test connection success & failure
	testPayload := `{"id":1,"engine":"postgres","host":"localhost","port":5433,"database_name":"test_ecommerce_pg","username":"test_user"}`
	okRec := httptest.NewRecorder()
	mux.ServeHTTP(okRec, httptest.NewRequest(http.MethodPost, "/api/admin/connections/test", strings.NewReader(testPayload)))
	if okRec.Code != http.StatusOK {
		t.Fatalf("test connection success status=%d body=%s", okRec.Code, okRec.Body.String())
	}

	testErr = errors.New("connection refused")
	failRec := httptest.NewRecorder()
	mux.ServeHTTP(failRec, httptest.NewRequest(http.MethodPost, "/api/admin/connections/test", strings.NewReader(testPayload)))
	if failRec.Code != http.StatusBadRequest {
		t.Fatalf("test connection failure status=%d want=400", failRec.Code)
	}

	// 4. Delete blocked with 409 when migration history exists
	repo.SetMigrationCount(1, 2)
	conflictRec := httptest.NewRecorder()
	mux.ServeHTTP(conflictRec, httptest.NewRequest(http.MethodDelete, "/api/admin/connections/1", nil))
	if conflictRec.Code != http.StatusConflict {
		t.Fatalf("delete with migration history status=%d want=409", conflictRec.Code)
	}

	// 5. Pool stats endpoint returns empty list cleanly
	statsRec := httptest.NewRecorder()
	mux.ServeHTTP(statsRec, httptest.NewRequest(http.MethodGet, "/api/admin/connections/pool-stats", nil))
	if statsRec.Code != http.StatusOK {
		t.Fatalf("pool-stats status=%d want=200", statsRec.Code)
	}

	// 6. Audit record written
	page, err := auditStore.List(context.Background(), audit.Filter{Event: "admin.connection.create"})
	if err != nil || page.Total != 1 {
		t.Fatalf("expected audit event for connection create, got %+v err=%v", page, err)
	}
}
