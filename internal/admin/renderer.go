package admin

import (
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

type Renderer interface {
	Render(w http.ResponseWriter, templatePath string, data contract.PageData) error
}

type TemplateRenderer struct {
	root string
}

func NewTemplateRenderer(root string) *TemplateRenderer {
	abs, err := filepath.Abs(root)
	if err != nil {
		return &TemplateRenderer{root: root}
	}

	return &TemplateRenderer{root: abs}
}

func (r *TemplateRenderer) Render(w http.ResponseWriter, templatePath string, data contract.PageData) error {
	layoutPath := filepath.Join(r.root, "base", "layout.html")
	pagePath := filepath.Join(r.root, templatePath)
	tmpl, err := template.New("layout.html").ParseFiles(layoutPath, pagePath)

	if err != nil {
		return fmt.Errorf("parse template %s: %w", templatePath, err)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return tmpl.ExecuteTemplate(w, "layout.html", data)
}
