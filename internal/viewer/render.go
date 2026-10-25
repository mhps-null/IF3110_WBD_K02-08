package viewer

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

// Renderer renders HTML templates for database management and viewer views.
type Renderer interface {
	Render(w http.ResponseWriter, templateRelativePath string, data contract.PageData) error
}

// HTMLRenderer renders HTML templates using buffered execution to prevent partial responses.
type HTMLRenderer struct {
	templatesDir string
}

func NewHTMLRenderer(templatesDir string) *HTMLRenderer {
	return &HTMLRenderer{templatesDir: templatesDir}
}

func (r *HTMLRenderer) Render(w http.ResponseWriter, templateRelativePath string, data contract.PageData) error {
	layoutPath := filepath.Join(r.templatesDir, "base", "layout.html")
	pagePath := filepath.Join(r.templatesDir, templateRelativePath)

	funcMap := template.FuncMap{
		"deref": func(s *string) string {
			if s == nil {
				return "-"
			}
			return *s
		},
		"add": func(a, b int) int {
			return a + b
		},
		"sub": func(a, b int) int {
			return a - b
		},
	}

	tmpl, err := template.New("layout.html").Funcs(funcMap).ParseFiles(layoutPath, pagePath)
	if err != nil {
		return fmt.Errorf("parse template %s: %w", templateRelativePath, err)
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		return fmt.Errorf("execute template %s: %w", templateRelativePath, err)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err = buf.WriteTo(w)
	return err
}
