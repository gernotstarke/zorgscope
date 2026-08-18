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
	"compress/gzip"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path"
	"reflect"
	"sort"
	"strconv"
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
// form-action and base-uri are named explicitly because default-src covers neither. The one form
// on this site submits ZORGSCOPE_TOKEN, so where a form may post to is not a detail: without
// form-action an injected <form action="https://elsewhere"> would be a working exfiltration route
// for the credential, and without base-uri an injected <base> would re-point every relative URL on
// the page, including that form's action.
const contentSecurityPolicy = "default-src 'self'; img-src 'self' data:; style-src 'self'; " +
	"script-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'self'"

// Options are the dependencies of a Server. Clock, Log and BehindFlyProxy are optional; the rest
// are required.
type Options struct {
	Config config.Config
	Store  ports.Store
	Runner *refresh.Runner
	Clock  ports.Clock
	Log    *slog.Logger
	// BehindFlyProxy says whether Fly's proxy sits in front of this process, which decides
	// whether the Fly-Client-IP header may be believed (see clientIP). nil means "work it out
	// from the runtime environment"; the tests set it either way to exercise both paths.
	BehindFlyProxy *bool
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
	assets  map[string]staticAsset
	session *sessionCodec
	signIn  *rateLimiter
	bearer  *rateLimiter
	handler http.Handler
	// trustFlyClientIP is the decision behind clientIP: only a process actually running behind
	// Fly's proxy may believe the Fly-Client-IP header.
	trustFlyClientIP bool
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

	assets, err := loadStatic()
	if err != nil {
		return nil, fmt.Errorf("web: static assets: %w", err)
	}

	trustFly := behindFlyProxy()
	if o.BehindFlyProxy != nil {
		trustFly = *o.BehindFlyProxy
	}

	s := &Server{
		cfg:              o.Config,
		store:            o.Store,
		runner:           o.Runner,
		clock:            o.Clock,
		log:              o.Log,
		pages:            pages,
		tiles:            tiles,
		assets:           assets,
		session:          newSessionCodec(o.Config.Secrets.AppToken),
		signIn:           newRateLimiter(signInAttempts, signInWindow),
		bearer:           newRateLimiter(signInAttempts, signInWindow),
		trustFlyClientIP: trustFly,
	}
	s.handler = securityHeaders(s.mux())
	return s, nil
}

// Handler returns the server's HTTP handler: the route table behind the security-header
// middleware.
func (s *Server) Handler() http.Handler { return s.handler }

// authKind is the credential a route requires.
type authKind int

// The kinds are ordered most restrictive first, so that authKind's zero value is the most
// restrictive one and a table entry whose auth field is left out fails closed rather than open. A
// forgotten field used to mean authPublic, which is the one mistake QS-4.1's structure exists to
// make impossible.
const (
	// authBearer needs the REFRESH_SECRET bearer, and never accepts the session cookie: the cron
	// service holds the ability to trigger a refresh and nothing else. It is the zero value
	// because it is the credential no browser ever presents.
	authBearer authKind = iota
	// authSessionFragment needs the session cookie but is not a navigation — an htmx fragment or
	// a form action — so an anonymous request is refused with 401. Redirecting an htmx swap would
	// paint the sign-in page into a tile.
	authSessionFragment
	// authSessionPage needs the session cookie and is a browser navigation, so an anonymous GET
	// is redirected to the sign-in page rather than refused (FR-8.3 AC1).
	authSessionPage
	// authPublic needs no credential: liveness, sign-in, documentation and static assets. It is
	// deliberately last, so that it can only ever be chosen by naming it.
	authPublic
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
		mux.Handle(rt.method+" "+rt.pattern, s.wrap(rt))
	}
	return mux
}

// wrap puts the credential a route's table entry names in front of its handler. It is the only
// place a handler is paired with an authKind, which is what lets a test wrap a route of its own —
// including one whose auth field was left out — and see what the mux would have registered.
func (s *Server) wrap(rt route) http.Handler {
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
	return h
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
//
// It answers out of a map built at start-up rather than from an http.FileServer, for three
// reasons. A path that is not a known asset — including a directory — is a 404 rather than a
// listing of everything the binary embeds. A traversal cannot be expressed at all, because the
// path is a map key and never touches a file system. And each compressible asset is held gzipped
// as well as stored, so QS-2.3's budget is met on the wire (see loadStatic).
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	a, ok := s.assets[path.Clean(strings.TrimPrefix(r.URL.Path, "/static/"))]
	if !ok {
		http.NotFound(w, r)
		return
	}

	h := w.Header()
	h.Set("Content-Type", a.contentType)
	// The response varies by Accept-Encoding even when this particular request did not accept
	// gzip, so a shared cache must not hand the compressed body to a client that cannot read it.
	h.Set("Vary", "Accept-Encoding")
	h.Set("Cache-Control", "public, max-age=3600")

	body := a.stored
	if a.gzipped != nil && acceptsGzip(r) {
		h.Set("Content-Encoding", "gzip")
		body = a.gzipped
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}

// acceptsGzip reports whether the client said it can read a gzip-encoded body. A client that did
// not — a plain curl, a text browser, a proxy that strips the header — still gets the file, just
// uncompressed.
func acceptsGzip(r *http.Request) bool {
	for _, enc := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, _, _ := strings.Cut(enc, ";")
		if strings.EqualFold(strings.TrimSpace(name), "gzip") {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- static assets

// staticAsset is one embedded file, held in both the encodings this server can hand out.
type staticAsset struct {
	contentType string
	stored      []byte
	// gzipped is the gzip encoding of stored, or nil when the type does not compress or the
	// compression did not actually save anything.
	gzipped []byte
}

// compressibleStatic are the extensions worth gzipping. Everything else — the PNGs and the ICO of
// the logo — is already compressed, and gzipping it costs bytes rather than saving them.
var compressibleStatic = map[string]bool{
	".css": true, ".js": true, ".svg": true, ".json": true, ".html": true, ".txt": true,
	".xml": true, ".map": true,
}

// staticContentTypes pins the types this application actually serves, rather than depending on
// whatever the container's mime database happens to know. Anything else falls back to
// mime.TypeByExtension and then to a sniff.
var staticContentTypes = map[string]string{
	".css": "text/css; charset=utf-8",
	".js":  "text/javascript; charset=utf-8",
	".svg": "image/svg+xml",
	".png": "image/png",
	".ico": "image/x-icon",
}

// loadStatic reads every embedded asset once, at start-up, and pre-computes its gzip encoding.
//
// Compressing here rather than per request is what makes gzip free on a machine that scales to
// zero: the work happens once, on a body that never changes, at the highest compression level —
// and a cold start pays for it while it is parsing templates anyway. It is also what makes
// QS-2.3's budget reachable at all. The vendored htmx the requirement itself names is 50 kB
// stored, which is the whole budget on its own; gzipped it is about a third of that, and QS-2.3's
// goal — "small enough for a slow connection" — is about what crosses the wire, which is what the
// browser will ask for with Accept-Encoding: gzip.
func loadStatic() (map[string]staticAsset, error) {
	assets := make(map[string]staticAsset)
	err := fs.WalkDir(embedded, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := fs.ReadFile(embedded, p)
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(p, "static/")
		ext := strings.ToLower(path.Ext(name))

		a := staticAsset{contentType: contentTypeFor(ext, body), stored: body}
		if compressibleStatic[ext] {
			gz, err := gzipBytes(body)
			if err != nil {
				return err
			}
			// Only keep it when it is actually smaller; a tiny file can gzip larger.
			if len(gz) < len(body) {
				a.gzipped = gz
			}
		}
		assets[name] = a
		return nil
	})
	if err != nil {
		return nil, err
	}
	return assets, nil
}

func contentTypeFor(ext string, body []byte) string {
	if t, ok := staticContentTypes[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return http.DetectContentType(body)
}

func gzipBytes(body []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(body); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// flyAppName is the environment variable Fly sets in every Machine it runs.
const flyAppName = "FLY_APP_NAME"

// behindFlyProxy reports whether this process is running on a Fly Machine, and therefore whether
// Fly's proxy is in front of it. See clientIP for why that decides whether a header may be
// believed. FLY_APP_NAME is the signal because Fly injects it into the Machine's environment
// itself: nothing a request carries can set it, and no other way of running this binary — the
// Compose stack, `go run`, a test — has it.
func behindFlyProxy() bool { return os.Getenv(flyAppName) != "" }

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
	// The fields are read by reflection rather than listed here on purpose. A hand-written list
	// is a second place to remember: a ninth field on config.Secrets would be silently
	// unredacted, and nothing would fail. Reflection makes "every string field of Secrets" the
	// definition, so a new secret is covered the moment it is declared.
	v := reflect.ValueOf(secrets)
	values := make([]string, 0, v.NumField())
	for i := range v.NumField() {
		f := v.Field(i)
		if f.Kind() != reflect.String || f.String() == "" {
			continue
		}
		values = append(values, f.String())
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, v := range values {
		text = strings.ReplaceAll(text, v, "[redacted]")
	}
	return text
}
