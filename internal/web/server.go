// Package web is zorgscope's HTTP layer: the route table, authentication, the security headers
// and the server-rendered pages.
//
// Two things in here are load-bearing beyond ordinary routing.
//
// The first is the route table itself (QS-4.1). Every route is declared once, in routes(), with
// the credential it needs; Handler wraps each one accordingly. Authentication is therefore not
// something a handler can forget to call — a route that names no credential is registered as
// public, and the tests fail unless that route is on their explicit list of public routes.
//
// The second is that no secret ever reaches a response or a log (QS-4.3). Handlers never render an
// error's text; they log it, scrubbed through Redact, and show the visitor a fixed message.
package web

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/refresh"
)

//go:embed templates static
var embedded embed.FS

// pageFiles are the page templates, each of which supplies the "content" block that layout.html
// wraps. They are parsed one page at a time — layout plus that page — because every page defines a
// block of the same name, so a single template set would have them overwrite each other.
var pageFiles = []string{"login.html", "dashboard.html"}

// tileGlob matches the per-tile fragment templates. They are parsed twice on purpose: into every
// page set, so that dashboard.html can compose the page out of them, and into a set of their own,
// so that GET /tile/{source} can execute one without a layout (FR-1.6 AC1). One definition, two
// ways of reaching it — a poll therefore cannot return markup that differs from what the page
// drew.
const tileGlob = "templates/tiles/*.html"

// strictTransportSecurity is sent on every response (QS-4.4). Fly terminates TLS in front of this
// process and only ever serves it over HTTPS, so there is no plaintext deployment for the header
// to break.
const strictTransportSecurity = "max-age=31536000; includeSubDomains"

// contentSecurityPolicy carries no 'unsafe-inline' because it does not need to: htmx is vendored
// under /static and the stylesheet is a file, so there is no inline script or style to allow
// (QS-4.4).
const contentSecurityPolicy = "default-src 'self'; img-src 'self' data:; style-src 'self'; " +
	"script-src 'self'; frame-ancestors 'none'"

// Options are the dependencies of a Server. Clock and Log are optional; the rest are required.
type Options struct {
	Config config.Config
	Store  ports.Store
	Runner *refresh.Runner
	Clock  ports.Clock
	Log    *slog.Logger
}

// Server holds the HTTP layer's state: the parsed templates, the session codec, the sign-in and
// bearer rate limiters, and the built handler.
type Server struct {
	cfg     config.Config
	store   ports.Store
	runner  *refresh.Runner
	clock   ports.Clock
	log     *slog.Logger
	pages   map[string]*template.Template
	tiles   *template.Template
	static  http.Handler
	session *sessionCodec
	signIn  *rateLimiter
	bearer  *rateLimiter
	handler http.Handler
}

// New builds a Server. It fails when a required dependency or credential is missing: a process
// that cannot authenticate anyone must not start and serve an open dashboard instead. No error it
// returns names a credential's value.
func New(o Options) (*Server, error) {
	if o.Store == nil {
		return nil, errors.New("web: a store is required")
	}
	if o.Config.Secrets.AppToken == "" {
		return nil, errors.New("web: ZORGSCOPE_TOKEN is not set")
	}
	if o.Config.Secrets.RefreshSecret == "" {
		return nil, errors.New("web: REFRESH_SECRET is not set")
	}
	if o.Clock == nil {
		o.Clock = ports.SystemClock{}
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}

	pages := make(map[string]*template.Template, len(pageFiles))
	for _, name := range pageFiles {
		t, err := template.New("layout").ParseFS(embedded,
			"templates/layout.html", "templates/"+name, tileGlob)
		if err != nil {
			return nil, fmt.Errorf("web: parsing %s: %w", name, err)
		}
		pages[name] = t
	}

	tiles, err := template.New("tiles").ParseFS(embedded, tileGlob)
	if err != nil {
		return nil, fmt.Errorf("web: parsing the tile fragments: %w", err)
	}

	staticDir, err := fs.Sub(embedded, "static")
	if err != nil {
		return nil, fmt.Errorf("web: static assets: %w", err)
	}

	s := &Server{
		cfg:     o.Config,
		store:   o.Store,
		runner:  o.Runner,
		clock:   o.Clock,
		log:     o.Log,
		pages:   pages,
		tiles:   tiles,
		static:  http.StripPrefix("/static/", http.FileServer(http.FS(staticDir))),
		session: newSessionCodec(o.Config.Secrets.AppToken),
		signIn:  newRateLimiter(signInAttempts, signInWindow),
		bearer:  newRateLimiter(signInAttempts, signInWindow),
	}
	s.handler = securityHeaders(s.mux())
	return s, nil
}

// Handler returns the server's HTTP handler: the route table behind the security-header
// middleware.
func (s *Server) Handler() http.Handler { return s.handler }

// authKind is the credential a route requires.
type authKind int

const (
	// authPublic needs no credential: liveness, sign-in, documentation and static assets.
	authPublic authKind = iota
	// authSessionPage needs the session cookie and is a browser navigation, so an anonymous GET
	// is redirected to the sign-in page rather than refused (FR-8.3 AC1).
	authSessionPage
	// authSessionFragment needs the session cookie but is not a navigation — an htmx fragment or
	// a form action — so an anonymous request is refused with 401. Redirecting an htmx swap would
	// paint the sign-in page into a tile.
	authSessionFragment
	// authBearer needs the REFRESH_SECRET bearer, and never accepts the session cookie: the cron
	// service holds the ability to trigger a refresh and nothing else.
	authBearer
)

// route is one entry of the route table.
type route struct {
	method  string
	pattern string
	auth    authKind
	handler http.HandlerFunc
	// probe is a concrete request path for a pattern containing a wildcard, used by the tests
	// that exercise every route in the table. It is empty when the pattern is already concrete.
	probe string
}

// probePath returns a concrete path that matches the route.
func (r route) probePath() string {
	if r.probe != "" {
		return r.probe
	}
	return r.pattern
}

// routes is the whole route table: every path this process answers, with the credential it
// requires. Adding a route means adding a line here, which is what makes "authentication cannot be
// forgotten" a property rather than a habit (QS-4.1).
//
// Several handlers are placeholders whose real bodies belong to later tasks; each says so. Their
// authentication is not a placeholder.
func (s *Server) routes() []route {
	return []route{
		{http.MethodGet, "/healthz", authPublic, s.handleHealthz, ""},
		{http.MethodGet, "/login", authPublic, s.handleLoginForm, ""},
		{http.MethodPost, "/login", authPublic, s.handleLoginSubmit, ""},
		{http.MethodGet, "/docs", authPublic, s.handleDocs, ""},
		{http.MethodGet, "/docs/", authPublic, s.handleDocs, "/docs/requirements/01-goals"},
		{http.MethodGet, "/static/", authPublic, s.handleStatic, "/static/app.css"},

		{http.MethodGet, "/", authSessionPage, s.handleDashboard, ""},
		{http.MethodGet, "/tile/{source}", authSessionFragment, s.handleTile, "/tile/github"},
		{http.MethodPost, "/seen", authSessionFragment, s.handleSeen, ""},
		{http.MethodPost, "/refresh", authSessionFragment, s.handleRefresh, ""},

		{http.MethodPost, "/api/refresh", authBearer, s.handleAPIRefresh, ""},
	}
}

// mux registers every route with the authentication its table entry declares.
func (s *Server) mux() *http.ServeMux {
	mux := http.NewServeMux()
	for _, rt := range s.routes() {
		var h http.Handler = rt.handler
		switch rt.auth {
		case authPublic:
		case authSessionPage:
			h = s.requireSession(h, true)
		case authSessionFragment:
			h = s.requireSession(h, false)
		case authBearer:
			h = s.requireBearer(h)
		}
		mux.Handle(rt.method+" "+rt.pattern, h)
	}
	return mux
}

// securityHeaders wraps the whole mux, so the headers are on every response — including the 404s
// and 405s the mux itself produces (QS-4.4).
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("Strict-Transport-Security", strictTransportSecurity)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------- handlers

// handleHealthz answers liveness without authentication and without touching an upstream or the
// database (FR-9.4 AC2): it must stay answerable while every source is down.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

// handleDashboard, handleTile and handleSeen live in dashboard.go.

// handleRefresh is the dashboard's own refresh button. Task 15 replaces this placeholder with the
// runner call, its 409 on refresh.ErrBusy and the re-rendered tiles.
func (s *Server) handleRefresh(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// handleAPIRefresh is the endpoint cron-job.org calls. Task 15 replaces this placeholder with the
// runner call and its JSON report; the bearer authentication in front of it is not a placeholder.
func (s *Server) handleAPIRefresh(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte("{\"ok\":true}\n"))
}

// handleDocs serves the embedded documentation. Task 16 replaces this placeholder with the
// goldmark rendering of docs/ and its link rewriting.
func (s *Server) handleDocs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte("<!doctype html><title>Documentation</title><p>Documentation.\n"))
}

// handleStatic serves the embedded assets: the stylesheet and vendored htmx.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	s.static.ServeHTTP(w, r)
}

// ---------------------------------------------------------------- rendering

// pageData is what every page template is executed with. Later tasks add their own fields.
type pageData struct {
	// Title is the page's own name, joined to the site name in the tab title. The dashboard
	// leaves it empty, because the dashboard is the site rather than a page within it.
	Title string
	// NewCount prefixes the tab title when it is greater than zero (FR-1.2 AC3).
	NewCount int
	// Dashboard is set only by handleDashboard. The layout reads it for the header's last-refresh
	// line, and dashboard.html for the tiles; every other page leaves it nil.
	Dashboard *dashboardView
	// Error is a message written for the visitor. It is never an error's own text: those can
	// carry a DSN or a token (QS-4.3).
	Error string
}

// render executes a page template into a buffer before writing anything, so a template failing
// half way through cannot leave a truncated page behind a 200.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, data pageData) {
	t, ok := s.pages[page]
	if !ok {
		s.fail(w, r, "rendering", fmt.Errorf("no such page template: %s", page))
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		s.fail(w, r, "rendering", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// fail is the single place a server-side error becomes a response. The visitor gets a fixed
// sentence; the error's own text is logged, scrubbed of every configured secret, because an error
// from the libSQL driver quotes the DSN and the DSN carries the Turso auth token (QS-4.3).
func (s *Server) fail(w http.ResponseWriter, r *http.Request, doing string, err error) {
	s.log.Error("request failed",
		"doing", doing,
		"method", r.Method,
		"path", r.URL.Path,
		"err", Redact(s.cfg.Secrets, err.Error()),
	)
	http.Error(w, "Something went wrong. The details are in the log.", http.StatusInternalServerError)
}

// Redact replaces every configured secret value found in text with a marker. It is the last line
// of defence for QS-4.3: an error from a dependency may quote a DSN, a URL or a header, and
// nothing that reaches a log or a response should be trusted to be free of one. Empty secrets are
// skipped, and the longest values are replaced first so that a secret containing another is not
// left partly visible.
func Redact(secrets config.Secrets, text string) string {
	values := []string{
		secrets.GitHubToken, secrets.PlausibleKey, secrets.TodoistToken, secrets.SlackWebhook,
		secrets.AppToken, secrets.RefreshSecret, secrets.TursoAuthToken, secrets.TursoURL,
	}
	nonEmpty := values[:0:0]
	for _, v := range values {
		if v != "" {
			nonEmpty = append(nonEmpty, v)
		}
	}
	sort.Slice(nonEmpty, func(i, j int) bool { return len(nonEmpty[i]) > len(nonEmpty[j]) })
	for _, v := range nonEmpty {
		text = strings.ReplaceAll(text, v, "[redacted]")
	}
	return text
}
