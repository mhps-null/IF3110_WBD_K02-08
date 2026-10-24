package migration

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
	"time"

	"github.com/example/if3110-milestone1/internal/contract"
)

type Renderer interface {
	Render(w http.ResponseWriter, templateRelativePath string, data contract.PageData) error
}

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
			if s == nil || *s == "" {
				return "-"
			}
			return *s
		},
		"derefInt": func(i *int64) string {
			if i == nil {
				return "-"
			}
			return fmt.Sprintf("%d", *i)
		},
		"derefFloat": func(f *float64) string {
			if f == nil {
				return "-"
			}
			return fmt.Sprintf("%.2f", *f)
		},
		"actualRows": func(node contract.PlanNode) string {
			if node.ActualRowsPerLoop != nil {
				return fmt.Sprintf("%g", *node.ActualRowsPerLoop)
			}
			if node.ActualRows != nil {
				return fmt.Sprintf("%d", *node.ActualRows)
			}
			return "—"
		},
		"formatTime": func(t *time.Time) string {
			if t == nil || t.IsZero() {
				return "-"
			}
			return t.Format("02 Jan 2006 15:04:05 WIB")
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
