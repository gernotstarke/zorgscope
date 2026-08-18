// Package fakesources implements fixture-backed stand-ins for the three upstream services
// zorgscope reads from — GitHub (GraphQL and REST), Plausible and Todoist — plus a set of control
// routes an adapter's tests use to inject failures and added items. It exists so that Tasks 7–10
// can develop and test their adapters against a real HTTP server instead of a live network
// dependency and a real credential (FR-9.2).
//
// This package imitates upstream services; it deliberately does not know the shapes zorgscope
// maps them to. It imports only the standard library, and never internal/domain or
// internal/ports — a mapping bug in an adapter must not become invisible by the fake and the
// adapter happening to agree on a shared type.
package fakesources

import (
	"net/http"
	"sync"
)

// server holds every fixture and every piece of state the control routes mutate: injected
// repositories, injected run histories, injected tasks, and per-source (optionally
// per-repository) failure injection. Every field is guarded by mu — adapter tests run under
// -race, several fetch concurrently, and the control routes mutate state while requests read it.
type server struct {
	mu sync.Mutex

	githubRepos     map[string]*ghRepoFixture
	githubRuns      map[string]*runsFixture
	todoist         []todoistTask
	todoistProjects []todoistProject

	// fail maps source -> target -> HTTP status to return. target is a repository
	// ("owner/name") for github, and the empty string for a source-wide failure (also used by
	// plausible and todoist, which have no per-repository concept). A target-specific entry
	// takes precedence over a source-wide one.
	fail map[string]map[string]int
}

// NewServer returns an http.Handler serving fixture-backed stand-ins for GitHub, Plausible and
// Todoist, plus the /_control/* routes used to steer them from a test. Fixtures are embedded
// (fixtures.go), so the server behaves identically whether run under `go test` or
// `go run ./cmd/fakesources` — their working directories differ, but go:embed does not care.
func NewServer() http.Handler {
	s := &server{fail: map[string]map[string]int{}}
	if err := s.resetLocked(); err != nil {
		// The fixtures are embedded at build time, not read at runtime, so a failure here means
		// a broken fixture file shipped in the binary — a programming error, not a condition a
		// caller can recover from.
		panic(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /graphql", s.handleGraphQL)
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/runs", s.handleActionsRuns)
	mux.HandleFunc("GET /api/v1/stats/aggregate", s.handlePlausibleAggregate)
	mux.HandleFunc("GET /rest/v2/tasks", s.handleTodoistTasks)
	mux.HandleFunc("GET /rest/v2/projects", s.handleTodoistProjects)
	mux.HandleFunc("POST /_control/fail", s.handleControlFail)
	mux.HandleFunc("POST /_control/add-issue", s.handleControlAddIssue)
	mux.HandleFunc("POST /_control/reset", s.handleControlReset)
	return mux
}

// shouldFailLocked reports whether requests to source, for the given target, are currently
// failing, and with which status. target is a repository for github; callers pass "" for
// plausible and todoist. Caller must hold mu.
func (s *server) shouldFailLocked(source, target string) (status int, fail bool) {
	byTarget, ok := s.fail[source]
	if !ok {
		return 0, false
	}
	if target != "" {
		if status, ok := byTarget[target]; ok {
			return status, true
		}
	}
	if status, ok := byTarget[""]; ok {
		return status, true
	}
	return 0, false
}

// resetLocked reloads every fixture fresh and clears every injected failure. Caller must hold mu
// (or, at construction, be the only goroutine with access to s).
//
// Its two callers report the same error differently on purpose: NewServer (above) panics, because
// there it means a fixture embedded at build time is broken — a programming error the process
// cannot serve anything sensible around, so failing fast at startup is correct. handleControlReset
// (control.go) instead returns 500 to its caller: by then the server has been serving fine, a
// request is in flight, and crashing the whole process over one control call would take every
// other in-progress test down with it. The condition is identical; when in the request lifecycle
// it is discovered decides whether panicking is a favour or a liability.
func (s *server) resetLocked() error {
	fx, err := loadFixtures()
	if err != nil {
		return err
	}
	s.githubRepos = fx.repos
	s.githubRuns = fx.runs
	s.todoist = fx.tasks
	s.todoistProjects = fx.projects
	s.fail = map[string]map[string]int{}
	return nil
}
