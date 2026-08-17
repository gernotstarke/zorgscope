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
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/web"
)

// Refresher is what the refresh button needs from the scheduler.
type Refresher interface {
	TriggerAll() int
	InFlight() int
}

// DashboardQuery is the cache-only read side used by both HTML and JSON delivery.
type DashboardQuery interface {
	Build(context.Context) (app.View, error)
	EvaluateAll(context.Context) ([]domain.Evaluated, error)
}

// ReloadableRuntime is implemented by app.Runtime. Keeping the delivery boundary as an interface
// lets handler tests use the smaller M1 fakes while production swaps whole scheduler generations.
type ReloadableRuntime interface {
	DashboardQuery
	Refresher
	Apply(*config.Config) error
	Config() (*config.Config, error)
}

// SecretRedactor receives the effective secret set after an API rotation.
type SecretRedactor interface{ Replace([]string) }

// Deps are the server's collaborators.
type Deps struct {
	Dashboard DashboardQuery
	Refresher Refresher
	Runtime   ReloadableRuntime
	Config    *config.Manager
	Redactor  SecretRedactor
	Store     ports.Store
	Clock     ports.Clock
	Cfg       *config.Config
	Log       *slog.Logger
	Ready     func() bool
}

// Server is the HTTP delivery layer.
type Server struct {
	deps         Deps
	tmpl         *template.Template
	authFailures *authFailureLimiter
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
	return &Server{deps: d, tmpl: tmpl, authFailures: newAuthFailureLimiter()}, nil
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

	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/dashboard", s.handleAPIDashboard)
	api.HandleFunc("GET /api/v1/status", s.handleAPIStatus)
	api.HandleFunc("POST /api/v1/dismiss", s.handleAPIDismiss)
	api.HandleFunc("POST /api/v1/dismiss-all", s.handleAPIDismissAll)
	api.HandleFunc("POST /api/v1/refresh", s.handleAPIRefresh)
	api.HandleFunc("GET /api/v1/config", s.handleAPIConfig)
	api.HandleFunc("PUT /api/v1/config", s.handleAPIConfig)
	api.HandleFunc("PUT /api/v1/config/secrets/{name}", s.handleAPIConfigSecret)
	api.HandleFunc("DELETE /api/v1/config/secrets/{name}", s.handleAPIConfigSecret)

	root := http.NewServeMux()
	root.Handle("GET /healthz", HealthHandler())
	root.Handle("GET /readyz", ReadyHandler(s.deps.Ready))
	root.Handle("GET /static/", staticHandler)
	root.Handle("/api/", chain(api, apiAuth(s.deps.Cfg.AuthMode, s.deps.Cfg.Secrets.APIToken, s.deps.Log, s.authFailures)))
	root.Handle("/", chain(protected, devAuth(s.deps.Cfg.AuthMode, s), csrf(https)))

	return chain(root, recoverer(s.deps.Log), requestLog(s.deps.Log), securityHeaders(https))
}

func (s *Server) dashboard() DashboardQuery {
	if s.deps.Runtime != nil {
		return s.deps.Runtime
	}
	return s.deps.Dashboard
}

func (s *Server) refresher() Refresher {
	if s.deps.Runtime != nil {
		return s.deps.Runtime
	}
	return s.deps.Refresher
}

func (s *Server) currentConfig() *config.Config {
	if s.deps.Runtime != nil {
		if cfg, err := s.deps.Runtime.Config(); err == nil {
			return cfg
		}
	}
	return s.deps.Cfg
}

func cacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}
