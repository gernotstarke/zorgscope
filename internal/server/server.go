package server

import (
	"context"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gernotstarke/zorgscope/internal/app"
	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/web"
)

// Refresher is what the refresh button needs from the scheduler.
type Refresher interface {
	TriggerAll() int
	InFlight() int
}

// Deps are the server's collaborators.
type Deps struct {
	Dashboard *app.Dashboard
	Refresher Refresher
	Store     ports.Store
	Clock     ports.Clock
	Cfg       *config.Config
	Log       *slog.Logger
	Ready     func() bool
}

// Server is the HTTP delivery layer.
type Server struct {
	deps Deps
	tmpl *template.Template
}

// New parses templates and returns a server.
func New(d Deps) (*Server, error) {
	tmpl, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Server{deps: d, tmpl: tmpl}, nil
}

type ctxKey int

const csrfKey ctxKey = 1

func withCSRF(r *http.Request, token string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), csrfKey, token))
}

func csrfToken(r *http.Request) string {
	t, _ := r.Context().Value(csrfKey).(string)
	return t
}

// Handler builds the routing tree with middleware (arc42 §5, §8.6).
func (s *Server) Handler() http.Handler {
	https := strings.HasPrefix(s.deps.Cfg.Server.BaseURL, "https://")

	static, _ := fs.Sub(web.FS, "static")
	staticHandler := http.StripPrefix("/static/", cacheControl(http.FileServerFS(static)))

	protected := http.NewServeMux()
	protected.HandleFunc("GET /{$}", s.handlePage)
	protected.HandleFunc("GET /tiles/{name}", s.handleTile)
	protected.HandleFunc("POST /dismiss", s.handleDismiss)
	protected.HandleFunc("POST /dismiss-all", s.handleDismissAll)
	protected.HandleFunc("POST /refresh", s.handleRefresh)
	protected.HandleFunc("GET /status", s.handleStatus)

	root := http.NewServeMux()
	root.Handle("GET /healthz", HealthHandler())
	root.Handle("GET /readyz", ReadyHandler(s.deps.Ready))
	root.Handle("GET /static/", staticHandler)
	root.Handle("/", chain(protected, devAuth(s.deps.Cfg.AuthMode, s), csrf(https)))

	return chain(root, recoverer(s.deps.Log), requestLog(s.deps.Log), securityHeaders(https))
}

func cacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}
