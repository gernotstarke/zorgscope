package server

import (
	"bytes"
	"html/template"
	"net/http"

	"github.com/gernotstarke/zorgscope/internal/app"
	"github.com/gernotstarke/zorgscope/web"
)

// pageData is what every template receives.
type pageData struct {
	View        app.View
	CSRF        string
	PollSeconds int
}

func parseTemplates() (*template.Template, error) {
	return template.ParseFS(web.FS, "templates/*.html")
}

// render executes a template into a buffer first so errors never produce half pages.
func (s *Server) render(w http.ResponseWriter, status int, name string, data pageData) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.deps.Log.Error("render failed", "component", componentServer, "template", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}
