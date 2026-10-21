package web

import (
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"

	"github.com/example/if3110-milestone1/internal/contract"
)

type TemplateRenderer struct {
	root string
}

func NewRenderer(root string) (*TemplateRenderer, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve template directory: %w", err)
	}
	return &TemplateRenderer{root: abs}, nil
}

func (r *TemplateRenderer) Render(w http.ResponseWriter, name string, data contract.PageData) error {
	// Nama template dipetakan secara eksplisit agar input pengguna tidak dapat
	// dipakai untuk memilih path file.
	pages := map[string]string{
		"login": filepath.Join("auth", "login.html"),
		"home":  filepath.Join("home", "index.html"),
		"error": filepath.Join("errors", "error.html"),
	}
	page, ok := pages[name]
	if !ok {
		return fmt.Errorf("unknown template: %s", name)
	}
	layoutPath := filepath.Join(r.root, "base", "layout.html")
	pagePath := filepath.Join(r.root, page)
	t, err := template.New("layout.html").ParseFiles(layoutPath, pagePath)
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return t.ExecuteTemplate(w, "layout.html", data)
}
