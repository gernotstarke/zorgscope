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
	"crypto/sha256"
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
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/refresh"
	"github.com/gernotstarke/zorgscope/internal/version"
)

//go:embed templates static
var embedded embed.FS

// pageFiles are the page templates, each of which supplies the "content" block that layout.html
// wraps. They are parsed one page at a time — layout plus that page — because every page defines a
// block of the same name, so a single template set would have them overwrite each other.
var pageFiles = []string{
	"login.html", "dashboard.html", "problems.html", "builds.html", "stopping.html",
	"docs.html", "docs_index.html",
}

// fragmentGlob matches the templates that are both part of a page and answerable on their own.
// They are parsed twice on purpose: into every page set, so that dashboard.html can compose the
// page out of them, and into a set of their own, so that GET /items can execute them without a
// layout (FR-1.6 AC1). One definition, two ways of reaching it — a poll therefore cannot return
// markup that differs from what the page drew.
const fragmentGlob = "templates/fragments/*.html"

// oauthTimeout bounds the code-for-token exchange when Options names no client of its own. A
// visitor is waiting on it: a GitHub that has not answered in fifteen seconds is better reported
// as a refusal they can retry than watched in silence.
const oauthTimeout = 15 * time.Second

// strictTransportSecurity is sent on every response (QS-4.4). Fly terminates TLS in front of this
// process and only ever serves it over HTTPS, so there is no plaintext deployment for the header
// to break.
const strictTransportSecurity = "max-age=31536000; includeSubDomains"

// contentSecurityPolicy carries no 'unsafe-inline' because it does not need to: htmx is vendored
// under /static and the stylesheet is a file, so there is no inline script or style to allow
// (QS-4.4).
// form-action and base-uri are named explicitly because default-src covers neither. Sign-in no
// longer submits anything — the button is a link to GET /auth/github — but the filter and the page
// controls are forms, and where a form may post to is not a detail: without form-action an
// injected <form action="https://elsewhere"> would be a working exfiltration route for whatever a
// page holds, and without base-uri an injected <base> would re-point every relative URL on the
// page, including that form's action and the sign-in link itself.
//
// form-action does not cover the sign-in redirect to github.com, and it must not have to: Chrome
// enforces form-action on the redirect that follows a *form submission*, which is exactly why
// starting the flow is a link navigation rather than a POST (design 2026-09-14 §2).
//
// img-src allows data: and nothing external. The build badges are someone else's artwork, and they
// used to be someone else's *request*: the policy named img.shields.io so the browser could fetch
// them while the page was being read. They are now fetched by the refresh run and carried in the
// page as data URIs (FR-2.3 AC5), so the host came back out — a page that waits on no third party
// should not be able to talk to one either.
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
	// Access decides who may sign in: it is asked, once per callback, whether the visitor whose
	// token it is given may push to the configured repository (FR-8.3). It is required — a server
	// that cannot answer that question would have to admit everybody or nobody.
	Access ports.AccessChecker
	// HTTPClient is what the OAuth code-for-token exchange uses. nil means a client bounded by
	// oauthTimeout; the tests point it at their in-process GitHub.
	HTTPClient *http.Client
	// BehindFlyProxy says whether Fly's proxy sits in front of this process, which decides
	// whether the Fly-Client-IP header may be believed (see clientIP). nil means "work it out
	// from the runtime environment"; the tests set it either way to exercise both paths.
	BehindFlyProxy *bool
	// Stop asks the process to shut down gracefully, and is what the header's stop control is
	// wired to (FR-1.7). It is optional: nil means this process cannot stop itself, and then the
	// control is not drawn and POST /stop says so rather than pretending. Nothing in the web
	// layer knows how a process ends — main owns that, and hands in the one function that does
	// it.
	Stop func()
}

// Server holds the HTTP layer's state: the parsed templates, the session codec, the sign-in and
// bearer rate limiters, and the built handler.
type Server struct {
	cfg    config.Config
	store  ports.Store
	runner *refresh.Runner
	clock  ports.Clock
	log    *slog.Logger
	pages  map[string]*template.Template
	// fragments is the set holding the templates GET /items executes without a layout. See
	// fragmentGlob for why the same files are also parsed into every page.
	fragments *template.Template
	assets    map[string]staticAsset
	// docs maps a documentation URL to its rendered page, and docIndex is the same pages grouped
	// for /docs. Both are built once, at start-up, by loadDocs.
	docs     map[string]*docPage
	docIndex []docCategory
	session  *sessionCodec
	signIn   *rateLimiter
	bearer   *rateLimiter
	access   ports.AccessChecker
	// httpClient is the client the code-for-token exchange runs on. It is an option rather than a
	// constant so that a test can point the exchange at an in-process GitHub; a deployment hands
	// in the same client its adapters use.
	httpClient *http.Client
	handler    http.Handler
	// ceiling is how long one refresh run may take; New sets it to refreshCeiling. It is a field
	// rather than a bare use of the constant so that a test can exercise the ceiling actually
	// firing without waiting the whole ceiling out — see refresh.go for what the value has to be.
	ceiling time.Duration
	// trustFlyClientIP is the decision behind clientIP: only a process actually running behind
	// Fly's proxy may believe the Fly-Client-IP header.
	trustFlyClientIP bool
	// stop shuts this process down; nil when the deployment cannot. See Options.Stop.
	stop func()
	// stopGrace is how long the process keeps serving after answering a stop request; New sets it
	// to defaultStopGrace. It is a field for the same reason ceiling is: a test has to be able to
	// watch the shutdown actually happen without waiting a second for every case.
	stopGrace time.Duration
	// loc is the configured timezone, and the only thing that reads it is the filter's date field:
	// a visitor who asks for items created since a date means their own day, not UTC's. It is
	// resolved once, in New, because time.LoadLocation reads the embedded database on every call.
	loc *time.Location
}

// New builds a Server. It fails when a required dependency or credential is missing: a process
// that cannot authenticate anyone must not start and serve an open dashboard instead. No error it
// returns names a credential's value.
func New(o Options) (*Server, error) {
	if o.Store == nil {
		return nil, errors.New("web: a store is required")
	}
	// The runner is as required as the store, and for the same kind of reason. On a Machine that
	// scales to zero the refresh endpoints are the only thing that ever writes upstream data:
	// without a runner this process would start, serve a dashboard that silently went stale, and
	// answer every cron trigger with 500 — a misconfiguration that looks healthy from the outside.
	// It fails here instead, where a deployment notices.
	if o.Runner == nil {
		return nil, errors.New("web: a refresh runner is required")
	}
	// The three things sign-in is made of. Without any of them this process would serve a sign-in
	// page that can only ever refuse, which is an outage wearing a working dashboard's clothes.
	if o.Config.Secrets.OAuthClientID == "" {
		return nil, errors.New("web: GITHUB_OAUTH_CLIENT_ID is not set")
	}
	if o.Config.Secrets.OAuthClientSecret == "" {
		return nil, errors.New("web: GITHUB_OAUTH_CLIENT_SECRET is not set")
	}
	if o.Access == nil {
		return nil, errors.New("web: an access checker is required")
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
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: oauthTimeout}
	}

	pages := make(map[string]*template.Template, len(pageFiles))
	for _, name := range pageFiles {
		t, err := template.New("layout").ParseFS(embedded,
			"templates/layout.html", "templates/"+name, fragmentGlob)
		if err != nil {
			return nil, fmt.Errorf("web: parsing %s: %w", name, err)
		}
		pages[name] = t
	}

	fragments, err := template.New("fragments").ParseFS(embedded, fragmentGlob)
	if err != nil {
		return nil, fmt.Errorf("web: parsing the fragments: %w", err)
	}

	// An empty timezone is UTC, which is what time.LoadLocation itself says; anything else that
	// will not load is a start-up failure rather than a silent fall back to UTC, because a
	// dashboard quietly filtering by the wrong day is worse than one that refuses to start.
	// config.Load validates the same string, so this only fires for a Config built in code.
	loc, err := time.LoadLocation(o.Config.Timezone)
	if err != nil {
		return nil, fmt.Errorf("web: timezone: %w", err)
	}

	assets, err := loadStatic()
	if err != nil {
		return nil, fmt.Errorf("web: static assets: %w", err)
	}

	docs, docIndex, err := loadDocs()
	if err != nil {
		return nil, fmt.Errorf("web: documentation: %w", err)
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
		fragments:        fragments,
		assets:           assets,
		docs:             docs,
		docIndex:         docIndex,
		session:          newSessionCodec(o.Config.Secrets.OAuthClientSecret),
		signIn:           newRateLimiter(signInAttempts, signInWindow),
		bearer:           newRateLimiter(signInAttempts, signInWindow),
		access:           o.Access,
		httpClient:       o.HTTPClient,
		trustFlyClientIP: trustFly,
		ceiling:          refreshCeiling,
		stop:             o.Stop,
		stopGrace:        defaultStopGrace,
		loc:              loc,
	}
	s.handler = securityHeaders(s.recoverPanics(canonicalPath(s.mux())))
	return s, nil
}

// assetURLs is every static asset's versioned URL, keyed by file name. It is built per render
// rather than once, because it is a handful of string concatenations over a map that never
// changes size, and a cached copy would be one more thing that can be stale.
func (s *Server) assetURLs() map[string]string {
	out := make(map[string]string, len(s.assets))
	for name := range s.assets {
		out[name] = s.assetURL(name)
	}
	return out
}

// Handler returns the server's HTTP handler: the route table behind the panic-recovery and
// security-header middleware.
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
		// The three public routes sign-in is made of (FR-8.3): the page, the redirect to GitHub,
		// and the callback that turns GitHub's answer into a session or into a refusal. They are
		// public because they are how a session is acquired; nothing behind them is.
		{http.MethodGet, "/login", authPublic, s.handleLoginForm, ""},
		{http.MethodGet, "/auth/github", authPublic, s.handleAuthStart, ""},
		{http.MethodGet, "/auth/callback", authPublic, s.handleAuthCallback, ""},
		{http.MethodGet, "/docs", authPublic, s.handleDocs, ""},
		{http.MethodGet, "/docs/", authPublic, s.handleDocs, "/docs/requirements/01-goals"},
		{http.MethodGet, "/static/", authPublic, s.handleStatic, "/static/app.css"},
		// Public because it decides a colour and nothing else: it reads no data, writes no data
		// and grants no access — it sets one display-preference cookie and redirects. It has to
		// be public because /login is, and the sign-in page is the one page an anonymous visitor
		// sees; a switch that only worked once you were inside would be missing from the only
		// page where the appearance is all there is.
		{http.MethodPost, "/theme", authPublic, s.handleTheme, ""},

		// "/{$}" and not "/": the bare pattern is the mux's catch-all and matches every path no
		// other route claims, so /admin, /no-such-page and /items/extra all answered 200
		// with the whole dashboard — a typo rendering the page it was not asking for, and a
		// fragment request answered with a document. "{$}" ends the pattern, so it matches the
		// root and nothing else and the mux answers everything else with its own 404, before any
		// handler and therefore before any credential is even considered. The probe is what the
		// tests that drive every route request, since the pattern is not itself a path.
		{http.MethodGet, "/{$}", authSessionPage, s.handleDashboard, "/"},
		{http.MethodGet, "/problems", authSessionPage, s.handleProblems, ""},
		{http.MethodGet, "/builds", authSessionPage, s.handleBuilds, ""},
		{http.MethodGet, "/items", authSessionFragment, s.handleItems, ""},
		{http.MethodPost, "/seen", authSessionFragment, s.handleSeen, ""},
		{http.MethodPost, "/refresh", authSessionFragment, s.handleRefresh, ""},
		// Stopping the process is session-authed and POST-only, like the two controls above it.
		// It must never be public and must never be reachable with REFRESH_SECRET: that
		// credential exists so an external scheduler can trigger a refresh, and a scheduler that
		// could also shut the Machine down would be one leaked cron URL away from being a
		// denial-of-service switch. The two credentials are independent and neither authorises
		// the other's action.
		{http.MethodPost, "/stop", authSessionFragment, s.handleStop, ""},

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

// canonicalPath answers any request whose path is not already in canonical form with 404, before
// the mux can see it.
//
// Left alone, http.ServeMux answers "/docs/../../etc/passwd" with a 307 to "/etc/passwd": it
// cleans the path and redirects to the result. That is not a traversal — nothing here ever touches
// a file system — but it is a worse answer than the truth. It echoes the request back in a
// Location header, it makes an attempt to escape /docs look like it went somewhere, and it means
// the paths a handler sees depend on a rewrite the handler cannot see. A request for a path no
// client would have sent is simply not a request for anything (QS-4.4).
//
// Real clients are unaffected: browsers resolve dot segments before sending, and every route in
// the table is already canonical.
func canonicalPath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != cleanPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// cleanPath is net/http's own notion of a canonical path: path.Clean of an absolute path, with a
// trailing slash preserved, because "/docs" and "/docs/" are two different routes.
func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	if p[0] != '/' {
		p = "/" + p
	}
	cleaned := path.Clean(p)
	if cleaned != "/" && p[len(p)-1] == '/' {
		cleaned += "/"
	}
	return cleaned
}

// recoverPanics turns a panicking handler into a 500 rather than a dropped connection.
//
// It wraps the whole mux rather than a single handler, because a panic is not a property of one
// route: it is the last thing standing between a bug anywhere in this process and a client that is
// given no status at all. That client is usually cron-job.org, which records what it was answered
// with — "500" is a far better entry in that history than a connection that simply closed. It sits
// inside securityHeaders so that this answer carries them too, and outside the mux so that no
// route can be registered past it (QS-4.1).
//
// The response says nothing beyond the fixed sentence. A panic value can quote whatever the
// panicking code was holding — a DSN, a token, a request body — and a stack trace names the
// filesystem this binary was built on; this is a public surface, so neither goes into it (QS-4.3).
// Both go to the log, scrubbed, which is where they can be read.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &recordingWriter{ResponseWriter: w}
		defer func() {
			p := recover()
			if p == nil {
				return
			}
			// http.ErrAbortHandler is net/http's own way of saying "abandon this response,
			// quietly". The server handles it itself; swallowing it here would turn a deliberate
			// abort into a 500 and log a bug that is not one.
			if err, ok := p.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(p)
			}
			s.log.Error("handler panicked",
				"method", r.Method,
				"path", r.URL.Path,
				"panic", Redact(s.cfg.Secrets, fmt.Sprint(p)),
				"stack", Redact(s.cfg.Secrets, string(debug.Stack())),
			)
			if rw.started {
				// The status line is already on the wire and cannot be taken back; writing a
				// second one would only add a "superfluous WriteHeader" to the log.
				return
			}
			http.Error(rw, internalErrorNotice, http.StatusInternalServerError)
		}()
		next.ServeHTTP(rw, r)
	})
}

// recordingWriter notes whether a response has been started, so that a panic recovered after the
// status line went out does not try to write a second one.
type recordingWriter struct {
	http.ResponseWriter
	started bool
}

func (w *recordingWriter) WriteHeader(status int) {
	w.started = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *recordingWriter) Write(b []byte) (int, error) {
	w.started = true
	return w.ResponseWriter.Write(b)
}

// Unwrap hands http.ResponseController the writer underneath, so that wrapping does not take away
// anything a handler could otherwise reach.
func (w *recordingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

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

// handleLoginForm, handleAuthStart and handleAuthCallback live in signin.go.

// handleDashboard, handleItems and handleSeen live in dashboard.go.

// handleRefresh and handleAPIRefresh live in refresh.go.

// handleDocs lives in docs.go.

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

	body, etag := a.stored, a.etag
	gzipped := a.gzipped != nil && acceptsGzip(r)
	if gzipped {
		body, etag = a.gzipped, strings.TrimSuffix(a.etag, `"`)+`-gzip"`
	}

	h := w.Header()
	h.Set("Content-Type", a.contentType)
	// The response varies by Accept-Encoding even when this particular request did not accept
	// gzip, so a shared cache must not hand the compressed body to a client that cannot read it.
	h.Set("Vary", "Accept-Encoding")
	h.Set("ETag", etag)
	h.Set("Cache-Control", cacheControlFor(r, a))

	if noneMatch(r, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	if gzipped {
		h.Set("Content-Encoding", "gzip")
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}

// cacheControlFor decides how long this response may be reused, and the answer turns entirely on
// whether the request named a version.
//
// A URL carrying the current content hash cannot go stale: a changed file is a different URL, so
// the copy a browser holds is correct forever. Everything the layout links is such a URL, so this
// is the ordinary case and it costs no request at all after the first.
//
// A URL without the hash — typed by hand, or held in a bookmark from before the versions existed
// — is the case that broke: it names a moving target, and any max-age at all is a window in which
// a browser renders new HTML against an old stylesheet. no-cache does not mean "do not store", it
// means "revalidate before reuse", so the browser still keeps the bytes and the check costs a 304
// with no body.
func cacheControlFor(r *http.Request, a staticAsset) string {
	if r.URL.Query().Get(assetVersionParam) == a.version {
		return "public, max-age=31536000, immutable"
	}
	return "public, no-cache"
}

// noneMatch reports whether the client already holds this exact representation. The header is a
// list, and a client that has both encodings sends both entity tags.
func noneMatch(r *http.Request, etag string) bool {
	for _, candidate := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		if strings.TrimSpace(candidate) == etag {
			return true
		}
	}
	return false
}

// assetVersionParam is the query parameter carrying an asset's content hash.
const assetVersionParam = "v"

// assetURL is the versioned URL of a static asset, which is what every template links. An unknown
// name returns the plain path rather than failing the render: a missing asset is a 404 on one
// request, and a page that refuses to render at all is a worse answer to the same mistake.
func (s *Server) assetURL(name string) string {
	a, ok := s.assets[name]
	if !ok {
		return "/static/" + name
	}
	return "/static/" + name + "?" + assetVersionParam + "=" + a.version
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
	// version is a short content hash, and it is what makes a stylesheet change actually reach a
	// browser. Without it the layout links a fixed URL, so a client that fetched /static/app.css
	// before a deploy keeps its copy until the max-age runs out — and a page whose HTML has
	// changed with a stylesheet that has not is not a stale cache, it is a broken page. It went
	// wrong exactly that way once: the document started carrying data-theme and the cached
	// stylesheet had never heard of it, so the appearance switch changed the icon and nothing
	// else.
	version string
	// etag identifies the *identity* representation. The gzip encoding gets its own, derived
	// from it, because a shared cache keyed on Vary: Accept-Encoding stores the two separately
	// and one ETag for both would let it answer a plain request with the compressed bytes.
	etag string
}

// assetVersionLen is how much of the content hash goes into the URL. Seven hex characters is 28
// bits: enough that two versions of one file never collide in practice, short enough that the
// links stay readable in the page source.
const assetVersionLen = 7

// compressibleStatic are the extensions worth gzipping. Everything else — the PNGs and the ICO of
// the logo — is already compressed, and gzipping it costs bytes rather than saving them.
// Deliberately absent: .png and .ico's larger cousins. A PNG is already deflate-compressed, so
// gzipping it again spends CPU on a cold start to add a few bytes. .ico is here because it is
// not: an ICO is uncompressed bitmap data and gzips to a fraction of its size.
var compressibleStatic = map[string]bool{
	".css": true, ".js": true, ".svg": true, ".json": true, ".html": true, ".txt": true,
	".xml": true, ".map": true, ".ico": true,
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

		sum := sha256.Sum256(body)
		hex := fmt.Sprintf("%x", sum)
		a := staticAsset{
			contentType: contentTypeFor(ext, body),
			stored:      body,
			version:     hex[:assetVersionLen],
			etag:        `"` + hex[:32] + `"`,
		}
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
	// Doc is the rendered documentation page docs.html shows; nil on every other page.
	Doc *docPage
	// Docs is the grouped list docs_index.html shows; nil on every other page.
	Docs []docCategory
	// Theme is the appearance the visitor asked for, and Path the page they are on so that the
	// theme control can send them back to it. Both are filled in by render, from the request,
	// for every page: an appearance that applied to some pages and not others would be worse
	// than none at all.
	Theme theme
	Path  string
	// Version is the build's semantic version, shown in the footer of every page. render fills
	// it in for every page rather than each handler doing so, because a footer that silently
	// lost its version on one page is exactly the kind of omission nobody notices until they
	// need to know which build they are looking at.
	Version string
	// assets maps a static file name to its versioned URL. It is unexported and reached through
	// the Asset method, so a template cannot accidentally link an unversioned path by indexing
	// the map with a name that is not in it.
	assets map[string]string
	// canStop is whether this process can shut itself down; render fills it in. Reached through
	// the CanStop method for the same reason assets is.
	canStop bool
}

// CanStop says whether this process can shut itself down, and therefore whether the header draws
// the stop control. render fills it in for every page from the server, so a deployment that cannot
// stop does not show a control that would answer 501.
func (d pageData) CanStop() bool { return d.canStop }

// Asset is the versioned URL of a static file, for the templates: {{.Asset "app.css"}}.
func (d pageData) Asset(name string) string { return d.assets[name] }

// ThemeAttr is the document's data-theme value, empty when the visitor follows the system. The
// attribute is then absent rather than present-and-saying-"system", so the stylesheet has one
// state to describe instead of two that mean the same thing.
func (d pageData) ThemeAttr() string {
	if d.Theme == themeSystem {
		return ""
	}
	return string(d.Theme)
}

// ThemeName, NextTheme and NextThemeLabel are what the control shows and submits. They are
// methods rather than fields because they are all derived from Theme, and a field that has to be
// kept in step with another field is a field that eventually is not.
// OrbitState is what the header's mark is doing: "refreshing" while a run is in flight, "stale"
// after one has failed, "idle" otherwise. See app.css and docs/logo/scanning-orbit-animation.md.
//
// It is derived from the dashboard rather than stored beside it, because a field that has to be
// kept in step with LastRunState is a field that eventually is not. Pages with no dashboard — the
// sign-in page, the documentation — are idle: they know nothing about refresh runs, and a mark
// that guessed would be saying something it had not been told.
func (d pageData) OrbitState() string {
	if d.Dashboard == nil {
		return "idle"
	}
	switch d.Dashboard.LastRunState {
	case "running":
		return "refreshing"
	case "failed":
		return "stale"
	default:
		return "idle"
	}
}

func (d pageData) ThemeName() string      { return string(d.Theme) }
func (d pageData) ThemeLabel() string     { return d.Theme.label() }
func (d pageData) NextTheme() string      { return string(d.Theme.next()) }
func (d pageData) NextThemeLabel() string { return d.Theme.next().label() }

// render executes a page template into a buffer before writing anything, so a template failing
// half way through cannot leave a truncated page behind a 200.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, data pageData) {
	t, ok := s.pages[page]
	if !ok {
		s.fail(w, r, "rendering", fmt.Errorf("no such page template: %s", page))
		return
	}
	data.Version = version.String()
	data.Theme = themeOf(r)
	data.Path = r.URL.Path
	data.assets = s.assetURLs()
	// The stop control is drawn only where it would work: this process must be able to stop, and
	// the visitor must be signed in. The appearance switch beside it is deliberately public — it
	// decides a colour — but stopping is not, and an anonymous visitor offered a control that
	// answers 401 has been told something untrue about the page.
	data.canStop = s.stop != nil && s.signedIn(r)
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		s.fail(w, r, "rendering", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// internalErrorNotice is what a visitor is told when something failed on this side. It names
// nothing: every server-side error text is logged, scrubbed, and never rendered (QS-4.3).
const internalErrorNotice = "Something went wrong. The details are in the log."

// fail is the single place a server-side error becomes a response. The visitor gets a fixed
// sentence; the error's own text is logged, scrubbed of every configured secret, because an error
// from the libSQL driver quotes the DSN and the DSN carries the Turso auth token (QS-4.3).
func (s *Server) fail(w http.ResponseWriter, r *http.Request, doing string, err error) {
	s.logFailure(r, doing, err)
	http.Error(w, internalErrorNotice, http.StatusInternalServerError)
}

// logFailure records a server-side failure. It is the half of fail that a handler answering in
// JSON shares: what reaches the log is the same either way, only the body differs.
func (s *Server) logFailure(r *http.Request, doing string, err error) {
	s.log.Error("request failed",
		"doing", doing,
		"method", r.Method,
		"path", r.URL.Path,
		"err", Redact(s.cfg.Secrets, err.Error()),
	)
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
