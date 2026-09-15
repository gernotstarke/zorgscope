// Package fakesources implements a fixture-backed stand-in for the upstream service zorgscope
// reads from — GitHub's GraphQL API and its OAuth sign-in flow — plus one control route,
// POST /_control/oauth-user, that a sign-in test uses to steer which permissions the fixture
// answers with. It exists so that the adapters and the sign-in flow can be developed and tested
// against a real HTTP server instead of a live network dependency and a real credential.
//
// This package imitates upstream services; it deliberately does not know the shapes zorgscope
// maps them to. It imports only the standard library, and never internal/domain or
// internal/ports — a mapping bug in an adapter must not become invisible by the fake and the
// adapter happening to agree on a shared type.
package fakesources

import (
	"io"
	"net/http"
	"sync"
)

// server holds every fixture this fake serves: the injected repositories' issues and pull
// requests, plus the one piece of state POST /_control/oauth-user steers. Every field is guarded
// by mu — adapter tests run under -race and several fetch concurrently.
type server struct {
	mu sync.Mutex

	githubRepos map[string]*ghRepoFixture

	// oauthPermission selects, by key into permissionSets (oauth.go), the permissions block that
	// GET /repos/{owner}/{repo} answers with. POST /_control/oauth-user sets it.
	oauthPermission string
}

// routes is the whole table, so that GET / can list it: the first thing anyone does with a fake
// server is open it in a browser, and a 404 there says nothing.
var routes = []string{
	"POST /graphql",
	"GET /login/oauth/authorize",
	"POST /login/oauth/access_token",
	"GET /repos/{owner}/{repo}",
	"POST /_control/oauth-user",
}

// NewServer returns an http.Handler serving a fixture-backed stand-in for GitHub, plus the
// /_control/oauth-user route used to steer its sign-in permissions from a test. Fixtures are
// embedded (fixtures.go), so the server behaves identically whether run under `go test` or
// `go run ./cmd/fakesources` — their working directories differ, but go:embed does not care.
func NewServer() http.Handler {
	s := &server{}
	if err := s.resetLocked(); err != nil {
		// The fixtures are embedded at build time, not read at runtime, so a failure here means
		// a broken fixture file shipped in the binary — a programming error, not a condition a
		// caller can recover from.
		panic(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("POST /graphql", s.handleGraphQL)
	mux.HandleFunc("GET /login/oauth/authorize", s.handleAuthorize)
	mux.HandleFunc("POST /login/oauth/access_token", s.handleAccessToken)
	mux.HandleFunc("GET /repos/{owner}/{repo}", s.handleRepository)
	mux.HandleFunc("POST /_control/oauth-user", s.handleControlOAuthUser)
	return mux
}

// handleIndex serves GET / with a plain-text list of every route the fake serves, so that opening
// the fake in a browser says something useful instead of a bare 404.
func (s *server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "zorgscope fake sources — a fixture-backed GitHub.\n\nServes:\n")
	for _, r := range routes {
		_, _ = io.WriteString(w, "  "+r+"\n")
	}
	_, _ = io.WriteString(w, "\nPoint the backend here with GITHUB_BASE_URL and GITHUB_OAUTH_BASE_URL in .env.\n")
}

// resetLocked loads every fixture fresh and resets the sign-in permission to its default. Caller
// must hold mu (or, at construction, be the only goroutine with access to s). It panics on
// failure: the fixtures are embedded at build time, not read at runtime, so a failure here means
// a broken fixture file shipped in the binary — a programming error, not a condition a caller can
// recover from, and there is no request in flight yet for a graceful error to serve.
func (s *server) resetLocked() error {
	fx, err := loadFixtures()
	if err != nil {
		return err
	}
	s.githubRepos = fx.repos
	s.oauthPermission = "push"
	return nil
}
