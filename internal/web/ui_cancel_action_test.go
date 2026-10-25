package web

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/admin"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/migration"
)

func TestGroupDeleteConfirmation_TemplateContract(t *testing.T) {
	templatesDir := filepath.Join("..", "..", "web", "templates")
	renderer := admin.NewTemplateRenderer(templatesDir)

	data := contract.PageData{
		Principal: contract.Principal{UserID: 1, Username: "admin", IsPlatformAdmin: true},
		CSRFToken: "test-csrf-token",
		Data: struct {
			Groups []admin.Group
		}{
			Groups: []admin.Group{
				{ID: 42, Name: "Engineering", Description: "Dev team"},
			},
		},
	}

	rec := httptest.NewRecorder()
	if err := renderer.Render(rec, "admin/groups.html", data); err != nil {
		t.Fatalf("render admin/groups.html: %v", err)
	}

	html := rec.Body.String()

	if strings.Contains(html, "onsubmit=") || strings.Contains(html, "onclick=") {
		t.Fatal("template must not contain inline event handlers forbidden by CSP")
	}

	expectedAction := `/admin/groups/42/delete`
	if !strings.Contains(html, expectedAction) {
		t.Fatalf("rendered HTML does not contain expected delete action %s", expectedAction)
	}

	if !strings.Contains(html, `data-confirm=`) {
		t.Fatal("delete form must contain data-confirm attribute for external JS handling")
	}

	if !strings.Contains(html, `Hapus group Engineering?`) {
		t.Fatal("data-confirm prompt must include group name for clarity")
	}
}

func TestMigrationExecutionConfirmation_TemplateContract(t *testing.T) {
	templatesDir := filepath.Join("..", "..", "web", "templates")
	renderer := migration.NewHTMLRenderer(templatesDir)

	title := "001_create_users_table"
	sqlText := "CREATE TABLE users (id SERIAL PRIMARY KEY);"
	draftMigration := migration.Migration{
		ID:        77,
		DBID:      10,
		Title:     title,
		SQLText:   sqlText,
		Status:    "draft",
		CreatedBy: 1,
	}

	data := contract.PageData{
		Principal: contract.Principal{UserID: 1, Username: "developer"},
		CSRFToken: "test-csrf-token",
		Data: struct {
			DBID      int64
			Migration *migration.Migration
		}{
			DBID:      10,
			Migration: &draftMigration,
		},
	}

	rec := httptest.NewRecorder()
	if err := renderer.Render(rec, "migration/detail.html", data); err != nil {
		t.Fatalf("render migration/detail.html: %v", err)
	}

	html := rec.Body.String()

	if strings.Contains(html, "onsubmit=") || strings.Contains(html, "onclick=") {
		t.Fatal("template must not contain inline event handlers forbidden by CSP")
	}

	expectedAction := `/databases/10/migrations/77/execute`
	if !strings.Contains(html, expectedAction) {
		t.Fatalf("rendered HTML does not contain expected execute action %s", expectedAction)
	}

	if !strings.Contains(html, `data-confirm=`) {
		t.Fatal("execute form must contain data-confirm attribute for external JS handling")
	}

	if !strings.Contains(html, `Jalankan migration ini pada database target?`) {
		t.Fatal("data-confirm prompt must state impact and confirm execution")
	}
}

func TestAppJS_CancellationBehavior(t *testing.T) {
	jsPath := filepath.Join("..", "..", "web", "static", "js", "app.js")
	raw, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}

	content := string(raw)

	if !strings.Contains(content, `document.addEventListener("submit"`) {
		t.Fatal("app.js must attach event listener for submit events")
	}

	if !strings.Contains(content, "window.confirm") {
		t.Fatal("app.js must call window.confirm for confirmation dialog")
	}

	if !strings.Contains(content, "event.preventDefault()") || !strings.Contains(content, "event.stopImmediatePropagation()") {
		t.Fatal("app.js must prevent default form submission when user cancels")
	}
}
