package viewer

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/example/if3110-milestone1/internal/connections"
	"github.com/example/if3110-milestone1/internal/contract"
)

func TestHTMLRendererTemplates(t *testing.T) {
	renderer := NewHTMLRenderer("../../web/templates")
	conn := connections.Connection{ID: 1, Name: "<script>unsafe</script>", Engine: contract.EnginePostgres, PasswordEnc: "hidden-ciphertext"}
	table := contract.Table{Schema: "public", Name: "users", Columns: []contract.Column{{Name: "id", DataType: "integer", IsPrimaryKey: true}}}
	data := map[string]any{
		"Connections": []connections.Connection{conn},
		"Databases":   []contract.AccessibleDB{{DBID: 1, Name: "app", Level: contract.AccessViewOnly}},
		"DBID":        int64(1), "Engine": contract.EnginePostgres, "Table": &table, "Schema": "public", "TableName": "users",
		"Snapshot":    contract.SchemaSnapshot{Engine: contract.EnginePostgres, Tables: []contract.Table{table}},
		"RowPage":     contract.RowPage{Columns: []string{"id"}, Rows: [][]any{{int64(1)}}, Total: 1},
		"CurrentPage": 1, "PageSize": 20, "TotalPages": 1, "OrderBy": "id", "Desc": false,
	}
	for _, tc := range []struct{ page, contains string }{
		{"connections/index.html", "&lt;script&gt;"},
		{"connections/edit.html", "&lt;script&gt;"},
		{"viewer/databases.html", "app"},
		{"viewer/schema.html", "users"},
		{"viewer/table.html", "integer"},
		{"viewer/data.html", "id"},
	} {
		t.Run(tc.page, func(t *testing.T) {
			payload := any(data)
			if tc.page == "connections/edit.html" {
				payload = &conn
			}
			rec := httptest.NewRecorder()
			err := renderer.Render(rec, tc.page, contract.PageData{CSRFToken: "test-token", Data: payload})
			body := rec.Body.String()
			if err != nil || !strings.Contains(body, tc.contains) {
				t.Fatalf("render: err=%v body=%s", err, body)
			}
			if strings.Contains(body, "<script>unsafe") || strings.Contains(body, conn.PasswordEnc) {
				t.Fatal("unsafe content or credentials rendered")
			}
		})
	}
	rec := httptest.NewRecorder()
	if err := renderer.Render(rec, "missing.html", contract.PageData{}); err == nil || rec.Body.Len() != 0 {
		t.Fatal("missing template must fail without partial output")
	}
}
