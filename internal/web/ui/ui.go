// Package ui owns Siftail's embedded server-rendered templates and local
// browser assets.
package ui

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"path"
)

//go:embed templates/*.html assets/* licenses/*
var files embed.FS

type Renderer struct {
	templates *template.Template
}

type LoginView struct {
	AdministratorMissing bool
	ReturnPath           string
	Expired              bool
	Error                string
}

type ShellView struct {
	CSRFToken string
}

func New() *Renderer {
	return &Renderer{templates: template.Must(template.ParseFS(files, "templates/*.html"))}
}

func (r *Renderer) Login(w http.ResponseWriter, status int, view LoginView) error {
	return r.render(w, status, "login.html", view)
}

func (r *Renderer) Shell(w http.ResponseWriter, status int, view ShellView) error {
	return r.render(w, status, "shell.html", view)
}

func (r *Renderer) render(w http.ResponseWriter, status int, name string, view any) error {
	var output bytes.Buffer
	if err := r.templates.ExecuteTemplate(&output, name, view); err != nil {
		return fmt.Errorf("render %s: %w", name, err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err := output.WriteTo(w)
	return err
}

func (r *Renderer) Asset(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := path.Base(request.URL.Path)
	contentTypes := map[string]string{
		"app.css":            "text/css; charset=utf-8",
		"app.js":             "text/javascript; charset=utf-8",
		"favicon.svg":        "image/svg+xml",
		"htmx-2.0.10.min.js": "text/javascript; charset=utf-8",
	}
	contentType, ok := contentTypes[name]
	if !ok {
		http.NotFound(w, request)
		return
	}
	content, err := files.ReadFile("assets/" + name)
	if err != nil {
		http.NotFound(w, request)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
	w.WriteHeader(http.StatusOK)
	if request.Method == http.MethodGet {
		_, _ = w.Write(content)
	}
}
