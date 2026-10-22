package testutil

import (
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"

	"github.com/example/if3110-milestone1/internal/contract"
)

// ViewRenderer renders templates with the shared base layout.
type ViewRenderer struct {
	templatesDir string
}

func NewViewRenderer(templatesDir string) *ViewRenderer {
	return &ViewRenderer{templatesDir: templatesDir}
}

func (vr *ViewRenderer) Render(w http.ResponseWriter, templateRelativePath string, data contract.PageData) error {
	layoutPath := filepath.Join(vr.templatesDir, "base", "layout.html")
	pagePath := filepath.Join(vr.templatesDir, templateRelativePath)

	tmpl, err := template.New("layout.html").ParseFiles(layoutPath, pagePath)
	if err != nil {
		return fmt.Errorf("failed to parse template %s: %w", templateRelativePath, err)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return tmpl.ExecuteTemplate(w, "layout.html", data)
}
