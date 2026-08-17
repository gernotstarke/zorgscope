# zorgscope v1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the zorgscope dashboard: one Go binary on a scale-to-zero Fly Machine that stores GitHub issues/PRs/builds, Plausible statistics and Todoist tasks in Turso, marks what is new since the user's last visit, and renders it as a server-rendered tiled page.

**Architecture:** Modular monolith with a hexagonal core. `internal/domain` holds the rules and imports only the standard library; `internal/ports` declares `SourceFetcher`, `Store`, `Notifier` and `Clock`; adapters implement them. There is no background scheduler — an external cron service calls `POST /api/refresh`, which is the only writer of upstream data. The client is `html/template` plus vendored htmx, with no build step.

**Tech Stack:** Go 1.26, libSQL/Turso (`tursodatabase/libsql-client-go`), `shurcooL/githubv4` with `golang.org/x/oauth2`, `yuin/goldmark`, `gopkg.in/yaml.v3`, htmx (vendored), Docker Compose, GNU make, Fly.io, cron-job.org.

**Spec:** [`docs/superpowers/specs/2026-08-17-zorgscope-reset-design.md`](../specs/2026-08-17-zorgscope-reset-design.md)

## Global Constraints

- Module path is `github.com/gernotstarke/zorgscope`. Go directive `go 1.26`.
- `CGO_ENABLED=0` everywhere. No cgo, no Node, no local toolchain: **only `docker` and `make` may be assumed on the host** (C‑2).
- The external dependency set is exactly: `github.com/shurcooL/githubv4`, `golang.org/x/oauth2`, `github.com/tursodatabase/libsql-client-go`, `github.com/yuin/goldmark`, `gopkg.in/yaml.v3`. Adding a sixth needs a decision record.
- `internal/domain` imports **only** the standard library (QS‑5.1). `golangci-lint`'s `depguard` enforces this and confines `githubv4` to `internal/adapters/github` and the libsql driver to `internal/adapters/libsql`.
- Times are `time.Time` in Go and RFC 3339 UTC strings (`time.RFC3339`) in SQL. Never store local time.
- No secret value may be logged, rendered or written to the repository (QS‑4.3). Secrets come from the environment only.
- Every task ends with `make check` passing (`lint` + `test` + `docs-check`).
- Commit messages reference the requirement ids they satisfy, e.g. `feat(store): first-seen upsert (FR-5.3, QS-1.2)`.
- Requirement ids come from `docs/requirements/`; quality ids `QS‑x` name the test that proves them.

---

## File structure

| Path | Responsibility |
|------|----------------|
| `cmd/zorgscope/main.go` | Wiring, start-up, graceful shutdown |
| `cmd/fakesources/main.go` | Fixture-backed GitHub, Plausible and Todoist stand-ins |
| `internal/domain/item.go` | `Item`, `Kind`, the new rule, sorting, age buckets |
| `internal/domain/build.go` | `Build` |
| `internal/domain/metric.go` | `Metric` and change calculation |
| `internal/domain/source.go` | `SourceState`, `RefreshRun` |
| `internal/domain/dashboard.go` | `BuildDashboard` — assembles tiles from stored data |
| `internal/ports/ports.go` | `SourceFetcher`, `Store`, `Notifier`, `Clock`, `FetchResult` |
| `internal/config/config.go` | YAML plus environment, validation |
| `internal/adapters/libsql/store.go` | `Store` implementation |
| `internal/adapters/libsql/migrations/*.sql` | Embedded numbered migrations |
| `internal/adapters/github/issues.go` | Issues and PRs over GraphQL |
| `internal/adapters/github/builds.go` | Workflow runs over REST |
| `internal/adapters/plausible/plausible.go` | Aggregate statistics |
| `internal/adapters/todoist/todoist.go` | Overdue and due-today tasks |
| `internal/refresh/runner.go` | One refresh run over all fetchers |
| `internal/web/server.go` | Router and dependencies |
| `internal/web/auth.go` | Sign-in, session cookie, security headers |
| `internal/web/dashboard.go` | Dashboard and tile handlers |
| `internal/web/docs.go` | Markdown rendering at `/docs` |
| `internal/web/templates/*.html` | Layout, tiles, login, docs |
| `internal/web/static/` | CSS, vendored htmx, logo |
| `deploy/` | `Dockerfile`, `compose.yml`, `fly.toml` |
| `config/zorgscope.yaml` | Non-secret configuration |

---

## Task 1: Repository skeleton, Docker and make infrastructure

**Files:**

- Create: `go.mod`, `cmd/zorgscope/main.go`, `cmd/zorgscope/main_test.go`, `deploy/Dockerfile`, `deploy/compose.yml`, `deploy/fly.toml`, `deploy/env.example`, `.dockerignore`, `.golangci.yml`, `config/zorgscope.yaml`
- Modify: `Makefile`, `README.md`, `.gitignore`

**Interfaces:**

- Consumes: nothing
- Produces: a binary that serves `GET /healthz` returning `200` and the body `ok`; `make backend`, `make test`, `make lint`, `make check`, `make db-shell` targets

- [ ] **Step 1: Write the failing test**

`cmd/zorgscope/main_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	newMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Fatalf("body = %q, want %q", got, "ok")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `make test`
Expected: FAIL — `undefined: newMux` (and first `go.mod` errors until Step 3 creates it).

- [ ] **Step 3: Create the module and the minimal binary**

`go.mod`:

```text
module github.com/gernotstarke/zorgscope

go 1.26
```

`cmd/zorgscope/main.go`:

```go
// Command zorgscope serves the personal status dashboard.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	addr := ":" + envOr("PORT", "8080")
	srv := &http.Server{
		Addr:              addr,
		Handler:           newMux(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown failed", "err", err)
	}
}

// newMux is separate from main so that tests can exercise the routes.
func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

- [ ] **Step 4: Write the linter configuration**

`.golangci.yml` — the `depguard` block is the architectural rule of QS‑5.1 and QS‑5.2 in executable form:

```yaml
version: "2"
run:
  timeout: 3m
linters:
  enable:
    - depguard
    - errcheck
    - gosec
    - govet
    - ineffassign
    - misspell
    - revive
    - staticcheck
    - unused
  settings:
    depguard:
      rules:
        domain:
          files: ["**/internal/domain/**"]
          allow: ["$gostd", "github.com/gernotstarke/zorgscope/internal/domain"]
          deny:
            - pkg: "github.com/gernotstarke/zorgscope/internal/adapters"
              desc: "the domain must not know about adapters (QS-5.1)"
        ports:
          files: ["**/internal/ports/**"]
          allow: ["$gostd", "github.com/gernotstarke/zorgscope/internal/domain"]
        upstream-clients:
          files: ["!**/internal/adapters/github/**"]
          deny:
            - pkg: "github.com/shurcooL/githubv4"
              desc: "GitHub client belongs in internal/adapters/github (QS-5.2)"
        db-driver:
          files: ["!**/internal/adapters/libsql/**"]
          deny:
            - pkg: "github.com/tursodatabase/libsql-client-go"
              desc: "the libSQL driver belongs in internal/adapters/libsql (QS-5.2)"
```

- [ ] **Step 5: Write the container and Compose files**

`deploy/Dockerfile` — scratch base, non-root, ≤ 25 MB (QS‑3.4):

```dockerfile
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/zorgscope ./cmd/zorgscope

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/zorgscope /zorgscope
COPY --from=build /src/config/zorgscope.yaml /config/zorgscope.yaml
USER nonroot:nonroot
ENV PORT=8080 CONFIG_PATH=/config/zorgscope.yaml
EXPOSE 8080
ENTRYPOINT ["/zorgscope"]
```

`deploy/compose.yml` — the backend plus the local libSQL server (C‑4):

```yaml
services:
  db:
    image: ghcr.io/tursodatabase/libsql-server:latest
    environment:
      SQLD_NODE: primary
    ports: ["8081:8080"]
    volumes: ["zorgscope-db:/var/lib/sqld"]
    healthcheck:
      test: ["CMD", "/bin/sqld", "--version"]
      interval: 5s
      retries: 10

  app:
    build:
      context: ..
      dockerfile: deploy/Dockerfile
    env_file: ../.env
    environment:
      TURSO_URL: http://db:8080
      PORT: "8080"
    ports: ["8080:8080"]
    depends_on:
      db:
        condition: service_started
```

`deploy/env.example`:

```sh
# Local development. Copy to .env (git-ignored) and fill in.
# Sign-in token for the browser session; >= 32 characters.
ZORGSCOPE_TOKEN=
# Bearer secret cron-job.org sends to POST /api/refresh; >= 32 characters.
REFRESH_SECRET=
# Upstream credentials. A source without its secret stays disabled (FR-8.2 AC2).
GITHUB_TOKEN=
PLAUSIBLE_API_KEY=
TODOIST_TOKEN=
SLACK_WEBHOOK_URL=
# Turso. Locally these are set by deploy/compose.yml; leave blank here.
TURSO_URL=
TURSO_AUTH_TOKEN=
```

`deploy/fly.toml` — scale to zero (C‑3):

```toml
app = "zorgscope"
primary_region = "fra"

[build]
  dockerfile = "Dockerfile"

[env]
  PORT = "8080"
  CONFIG_PATH = "/config/zorgscope.yaml"

[http_service]
  internal_port = 8080
  force_https = true
  auto_stop_machines = "stop"
  auto_start_machines = true
  min_machines_running = 0

  [[http_service.checks]]
    grace_period = "5s"
    interval = "30s"
    method = "GET"
    path = "/healthz"
    timeout = "5s"

[[vm]]
  size = "shared-cpu-1x"
  memory = "256mb"
```

- [ ] **Step 6: Write the configuration file**

`config/zorgscope.yaml` — replace the repository list with the real one:

```yaml
timezone: Europe/Berlin

refresh:
  # What cron-job.org is configured to; used for the freshness display only.
  interval: 15m
  # How long a tile's data may be before it is shown as stale.
  stale_after: 45m

github:
  login: gernotstarke
  repos:
    - arc42/arc42.org-site
    - arc42/quality.arc42.org
    - gernotstarke/zorgscope

plausible:
  sites:
    - arc42.org
    - quality.arc42.org

todoist:
  filter: "overdue | today"

notifications:
  slack:
    enabled: false
```

- [ ] **Step 7: Rewrite the Makefile**

Keep the existing `GO_RUN`, `FLY_*` and `logo` machinery; remove `e2e`, `docs-html`, `docs-html-check`
and the `ZORGSCOPE_CONFIG_KEY` check. Add:

```makefile
COMPOSE := docker compose -f deploy/compose.yml

.PHONY: fakes db-shell db-reset
fakes: ## Run the fake upstream sources on http://localhost:9090
	$(GO_RUN) go run ./cmd/fakesources
db-shell: ## Open a SQL shell against the local libsql-server
	docker run --rm -it --network=container:$$($(COMPOSE) ps -q db) \
	  ghcr.io/tursodatabase/libsql-shell:latest http://localhost:8080
db-reset: ## Drop the local database volume
	-$(COMPOSE) down -v
```

`check-env` must now require `ZORGSCOPE_TOKEN` and `REFRESH_SECRET` and nothing else.

- [ ] **Step 8: Run the test and the lint**

Run: `make test && make lint`
Expected: PASS. `make test` reports `TestHealthz` passing; `golangci-lint` reports no issues.

- [ ] **Step 9: Verify the container really runs**

Run: `make backend`, then in another terminal `curl -fsS localhost:8080/healthz`
Expected: `ok`. Then `docker image inspect zorgscope-app --format '{{.Size}}'` is below 25 MB (QS‑3.4).

- [ ] **Step 10: Rewrite the README**

Describe what zorgscope is in the reset scope, the quick start (`make backend`, `make client`,
`make fakes`, `make test`, `make check`, `make fly-deploy`), the repository layout table matching the
File structure above, and links to `docs/requirements/`, `docs/decisions/`, `docs/concepts/`.
Delete `docs/guides/api.md` — the client-neutral API it documents is out of scope.

- [ ] **Step 11: Commit**

```bash
git add -A
git commit -m "feat(infra): repository skeleton, Docker, make and Fly configuration (FR-9.1, FR-9.3, QS-3.4)"
```

---

## Task 2: Configuration loading

**Files:**

- Create: `internal/config/config.go`, `internal/config/config_test.go`, `internal/config/testdata/valid.yaml`, `internal/config/testdata/bad-interval.yaml`
- Modify: `go.mod` (add `gopkg.in/yaml.v3`)

**Interfaces:**

- Consumes: `config/zorgscope.yaml` from Task 1
- Produces:

```go
package config

type Config struct {
	Timezone      string
	Refresh       Refresh
	GitHub        GitHub
	Plausible     Plausible
	Todoist       Todoist
	Notifications Notifications
	Secrets       Secrets
}

type Refresh struct {
	Interval   time.Duration
	StaleAfter time.Duration
}
type GitHub struct {
	Login   string
	Repos   []string
	BaseURL string // "" means api.github.com; the fakes set it
}
type Plausible struct {
	Sites   []string
	BaseURL string
}
type Todoist struct {
	Filter  string
	BaseURL string
}
type Notifications struct {
	Slack struct{ Enabled bool }
}
type Secrets struct {
	GitHubToken, PlausibleKey, TodoistToken, SlackWebhook string
	AppToken, RefreshSecret                               string
	TursoURL, TursoAuthToken                              string
}

// Load reads path, overlays secrets from env, and validates.
func Load(path string, env func(string) string) (Config, error)

// Enabled reports whether the named source has its credential (FR-8.2 AC2).
func (c Config) Enabled(source string) bool
```

- [ ] **Step 1: Write the failing tests**

`internal/config/config_test.go`:

```go
package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
)

func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

func TestLoadValid(t *testing.T) {
	cfg, err := config.Load("testdata/valid.yaml", env(map[string]string{
		"ZORGSCOPE_TOKEN": strings.Repeat("t", 32),
		"REFRESH_SECRET":  strings.Repeat("r", 32),
		"GITHUB_TOKEN":    "ghp_x",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Refresh.Interval != 15*time.Minute {
		t.Errorf("interval = %v, want 15m", cfg.Refresh.Interval)
	}
	if len(cfg.GitHub.Repos) != 2 {
		t.Errorf("repos = %d, want 2", len(cfg.GitHub.Repos))
	}
	if !cfg.Enabled("github") {
		t.Error("github should be enabled: its token is present")
	}
	if cfg.Enabled("todoist") {
		t.Error("todoist should be disabled: no token (FR-8.2 AC2)")
	}
}

func TestLoadRejectsBadDuration(t *testing.T) {
	_, err := config.Load("testdata/bad-interval.yaml", env(map[string]string{
		"ZORGSCOPE_TOKEN": strings.Repeat("t", 32),
		"REFRESH_SECRET":  strings.Repeat("r", 32),
	}))
	if err == nil {
		t.Fatal("want an error for an unparsable interval (FR-8.1 AC3)")
	}
	if !strings.Contains(err.Error(), "refresh.interval") {
		t.Errorf("error %q must name the offending field (FR-8.1 AC3)", err)
	}
}

func TestLoadRequiresAppToken(t *testing.T) {
	_, err := config.Load("testdata/valid.yaml", env(map[string]string{
		"ZORGSCOPE_TOKEN": "short",
		"REFRESH_SECRET":  strings.Repeat("r", 32),
	}))
	if err == nil {
		t.Fatal("want an error: ZORGSCOPE_TOKEN below 32 characters")
	}
}

func TestErrorNeverContainsSecretValues(t *testing.T) {
	const canary = "canary-secret-value-canary"
	_, err := config.Load("testdata/bad-interval.yaml", env(map[string]string{
		"ZORGSCOPE_TOKEN": canary + strings.Repeat("x", 32),
		"REFRESH_SECRET":  strings.Repeat("r", 32),
	}))
	if err != nil && strings.Contains(err.Error(), canary) {
		t.Fatal("a secret value leaked into an error message (QS-4.3)")
	}
}
```

`internal/config/testdata/valid.yaml`:

```yaml
timezone: Europe/Berlin
refresh:
  interval: 15m
  stale_after: 45m
github:
  login: someone
  repos: [org/one, org/two]
plausible:
  sites: [example.org]
todoist:
  filter: "overdue | today"
notifications:
  slack:
    enabled: false
```

`internal/config/testdata/bad-interval.yaml` is the same file with `interval: every-so-often`.

- [ ] **Step 2: Run and watch them fail**

Run: `make go ARGS="test ./internal/config/..."`
Expected: FAIL — package `config` does not exist.

- [ ] **Step 3: Implement `Load`**

Unmarshal into a private `fileConfig` whose duration fields are `string`, parse them with
`time.ParseDuration` and wrap failures as `fmt.Errorf("refresh.interval: %w", err)` so the field name
is in the message. Read the eight secrets through the injected `env` function — injected rather than
calling `os.Getenv` so the tests need no process environment. Validate: `timezone` must load with
`time.LoadLocation`; `github.repos` entries must match `owner/name`; `ZORGSCOPE_TOKEN` and
`REFRESH_SECRET` must be at least 32 characters. Never include a secret's value in an error.

`Enabled` maps `"github"→Secrets.GitHubToken`, `"plausible"→PlausibleKey`, `"todoist"→TodoistToken`
and reports whether the value is non-empty and the source has something configured (repositories,
sites, a filter).

- [ ] **Step 4: Run the tests**

Run: `make go ARGS="test ./internal/config/... -v"`
Expected: all four PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config go.mod go.sum
git commit -m "feat(config): YAML plus environment secrets with field-naming validation (FR-8.1, FR-8.2)"
```

---

## Task 3: Domain model and the new-detection rule

**Files:**

- Create: `internal/domain/item.go`, `internal/domain/item_test.go`, `internal/domain/build.go`, `internal/domain/metric.go`, `internal/domain/metric_test.go`, `internal/domain/source.go`

**Interfaces:**

- Consumes: nothing
- Produces:

```go
package domain

type Kind string

const (
	KindIssue Kind = "issue"
	KindPR    Kind = "pr"
	KindTask  Kind = "task"
)

type Item struct {
	Source      string
	ExternalID  string
	Kind        Kind
	Repo        string
	Number      int
	Title       string
	URL         string
	Author      string
	State       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DueAt       time.Time // zero when the item has no due date
	Priority    int
	FirstSeenAt time.Time
}

func (i Item) IsNew(lastVisit time.Time) bool
func SortItems(items []Item, lastVisit time.Time)
func CountNew(items []Item, lastVisit time.Time) int

type AgeBucket string

const (
	BucketDay   AgeBucket = "day"
	BucketWeek  AgeBucket = "week"
	BucketMonth AgeBucket = "month"
	BucketOlder AgeBucket = "older"
)

func Age(t, now time.Time) AgeBucket

type Build struct {
	Repo, Workflow, Conclusion, Status, RunURL string
	FinishedAt, FetchedAt                      time.Time
}

type Metric struct {
	Site                                      string
	WindowDays                                int
	Visitors, Pageviews                       int
	PrevVisitors, PrevPageviews               int
	FetchedAt                                 time.Time
}

func (m Metric) VisitorChange() (percent float64, known bool)
func (m Metric) PageviewChange() (percent float64, known bool)

type SourceState struct {
	Source        string
	LastSuccessAt time.Time
	LastError     string
	LastErrorAt   time.Time
	ItemCount     int
}

func (s SourceState) Stale(now time.Time, after time.Duration) bool
func (s SourceState) Failing() bool

type RefreshRun struct {
	ID                   int64
	StartedAt, FinishedAt time.Time
	Trigger              string
	OK                   bool
	Detail               string
}
```

- [ ] **Step 1: Write the failing tests for the new rule**

`internal/domain/item_test.go`:

```go
package domain_test

import (
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestIsNew(t *testing.T) {
	visit := at("2026-08-17T10:00:00Z")
	tests := []struct {
		name      string
		firstSeen time.Time
		want      bool
	}{
		{"seen after the visit is new", at("2026-08-17T10:00:01Z"), true},
		{"seen before the visit is not new", at("2026-08-17T09:59:59Z"), false},
		{"seen exactly at the visit is not new", visit, false},
		{"never seen is not new", time.Time{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			it := domain.Item{FirstSeenAt: tc.firstSeen}
			if got := it.IsNew(visit); got != tc.want {
				t.Errorf("IsNew() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSortItemsPutsNewFirstThenNewestUpdate(t *testing.T) {
	visit := at("2026-08-17T10:00:00Z")
	items := []domain.Item{
		{ExternalID: "old-recent", FirstSeenAt: at("2026-08-01T00:00:00Z"), UpdatedAt: at("2026-08-17T12:00:00Z")},
		{ExternalID: "new-stale", FirstSeenAt: at("2026-08-17T11:00:00Z"), UpdatedAt: at("2026-08-02T00:00:00Z")},
		{ExternalID: "new-recent", FirstSeenAt: at("2026-08-17T11:00:00Z"), UpdatedAt: at("2026-08-17T13:00:00Z")},
	}

	domain.SortItems(items, visit)

	want := []string{"new-recent", "new-stale", "old-recent"}
	for i, id := range want {
		if items[i].ExternalID != id {
			t.Fatalf("position %d = %q, want %q (order: %v)", i, items[i].ExternalID, id, ids(items))
		}
	}
}

func TestSortItemsIsStableForEqualKeys(t *testing.T) {
	visit := at("2026-08-17T10:00:00Z")
	same := at("2026-08-17T12:00:00Z")
	items := []domain.Item{
		{ExternalID: "a", UpdatedAt: same}, {ExternalID: "b", UpdatedAt: same}, {ExternalID: "c", UpdatedAt: same},
	}

	domain.SortItems(items, visit)

	for i, id := range []string{"a", "b", "c"} {
		if items[i].ExternalID != id {
			t.Fatalf("sort is not stable: got %v", ids(items))
		}
	}
}

func TestCountNew(t *testing.T) {
	visit := at("2026-08-17T10:00:00Z")
	items := []domain.Item{
		{FirstSeenAt: at("2026-08-17T11:00:00Z")},
		{FirstSeenAt: at("2026-08-17T09:00:00Z")},
		{FirstSeenAt: at("2026-08-17T12:00:00Z")},
	}
	if got := domain.CountNew(items, visit); got != 2 {
		t.Errorf("CountNew() = %d, want 2", got)
	}
}

func TestAge(t *testing.T) {
	now := at("2026-08-17T12:00:00Z")
	tests := []struct {
		in   string
		want domain.AgeBucket
	}{
		{"2026-08-17T11:00:00Z", domain.BucketDay},
		{"2026-08-16T11:00:00Z", domain.BucketWeek},
		{"2026-08-05T12:00:00Z", domain.BucketMonth},
		{"2026-06-01T12:00:00Z", domain.BucketOlder},
	}
	for _, tc := range tests {
		if got := domain.Age(at(tc.in), now); got != tc.want {
			t.Errorf("Age(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func ids(items []domain.Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ExternalID
	}
	return out
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `make go ARGS="test ./internal/domain/..."`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement the domain**

`IsNew` is `!i.FirstSeenAt.IsZero() && i.FirstSeenAt.After(lastVisit)`. `SortItems` uses
`sort.SliceStable` with the key `(!IsNew, -UpdatedAt)`. `Age` compares against `now` with the
boundaries 24 h, 7 d, 30 d.

`VisitorChange` returns `(0, false)` when the previous period is zero — a jump from nothing is not a
percentage — and otherwise `(float64(Visitors-PrevVisitors)/float64(PrevVisitors)*100, true)`.
Write a table test for it covering growth, decline, no change and a zero previous period.

`Stale` is `now.Sub(s.LastSuccessAt) > after`, true when `LastSuccessAt` is zero. `Failing` is
`s.LastError != "" && s.LastErrorAt.After(s.LastSuccessAt)`.

- [ ] **Step 4: Run the tests with coverage**

Run: `make test-domain`
Expected: PASS with coverage ≥ 90 % (QS‑5.1). Add cases until it is.

- [ ] **Step 5: Commit**

```bash
git add internal/domain
git commit -m "feat(domain): items, new-detection, sorting and metric change (FR-1.2, FR-5.3, QS-1.2)"
```

---

## Task 4: Ports

**Files:**

- Create: `internal/ports/ports.go`, `internal/ports/fake.go`

**Interfaces:**

- Consumes: `internal/domain`
- Produces:

```go
package ports

type FetchResult struct {
	Items   []domain.Item
	Builds  []domain.Build
	Metrics []domain.Metric
}

type SourceFetcher interface {
	Name() string
	Fetch(ctx context.Context) (FetchResult, error)
}

type Store interface {
	Migrate(ctx context.Context) error

	ReplaceItems(ctx context.Context, source string, items []domain.Item, now time.Time) (int, error)
	UpsertBuilds(ctx context.Context, builds []domain.Build, now time.Time) error
	UpsertMetrics(ctx context.Context, metrics []domain.Metric, now time.Time) error

	Items(ctx context.Context) ([]domain.Item, error)
	Builds(ctx context.Context) ([]domain.Build, error)
	Metrics(ctx context.Context) ([]domain.Metric, error)
	SourceStates(ctx context.Context) (map[string]domain.SourceState, error)

	RecordSourceOK(ctx context.Context, source string, at time.Time, count int) error
	RecordSourceError(ctx context.Context, source string, at time.Time, msg string) error

	LastVisit(ctx context.Context) (time.Time, error)
	SetLastVisit(ctx context.Context, t time.Time) error

	AcquireRefreshLease(ctx context.Context, holder string, now time.Time, ttl time.Duration) (bool, error)
	ReleaseRefreshLease(ctx context.Context, holder string) error

	StartRun(ctx context.Context, trigger string, at time.Time) (int64, error)
	FinishRun(ctx context.Context, id int64, at time.Time, ok bool, detail string) error
	LastRun(ctx context.Context) (domain.RefreshRun, error)

	MarkNotified(ctx context.Context, keys []string, at time.Time) error
	UnnotifiedKeys(ctx context.Context, keys []string) ([]string, error)

	Close() error
}

type Notifier interface {
	Notify(ctx context.Context, items []domain.Item) error
}

type Clock interface{ Now() time.Time }

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

// FixedClock is a test clock. Advance moves it forward.
type FixedClock struct{ T time.Time }

func (c *FixedClock) Now() time.Time          { return c.T }
func (c *FixedClock) Advance(d time.Duration) { c.T = c.T.Add(d) }

// FakeFetcher returns a fixed result, or an error, and counts its calls.
type FakeFetcher struct {
	SourceName string
	Result     FetchResult
	Err        error
	Calls      int
	Block      chan struct{} // when non-nil, Fetch waits on it before returning
}

func (f *FakeFetcher) Name() string
func (f *FakeFetcher) Fetch(ctx context.Context) (FetchResult, error)
```

- [ ] **Step 1: Write the interfaces and the fakes**

Ports have no behaviour of their own, so there is no test at this step; the fakes exist because
Tasks 11 and 12 need them. `FakeFetcher.Fetch` increments `Calls`, waits on `Block` when it is
non-nil (that is what QS‑1.7's concurrency test needs), respects `ctx.Done()`, and returns
`Result, Err`.

- [ ] **Step 2: Verify it compiles and lints**

Run: `make go ARGS="build ./..." && make lint`
Expected: no output, no findings. `depguard` must not complain: ports import only the domain.

- [ ] **Step 3: Commit**

```bash
git add internal/ports
git commit -m "feat(ports): SourceFetcher, Store, Notifier and Clock (QS-5.1)"
```

---

## Task 5: libSQL store, migrations and the first-seen invariant

This is the task that makes `NEW` correct, so it gets the most test weight.

**Files:**

- Create: `internal/adapters/libsql/store.go`, `internal/adapters/libsql/store_test.go`, `internal/adapters/libsql/migrate.go`, `internal/adapters/libsql/migrations/0001_initial.sql`
- Modify: `go.mod` (add `github.com/tursodatabase/libsql-client-go`)

**Interfaces:**

- Consumes: `ports.Store` from Task 4, `domain` from Task 3
- Produces: `func Open(url, authToken string) (*Store, error)`, `*Store` implementing `ports.Store`

- [ ] **Step 1: Write the migration**

`internal/adapters/libsql/migrations/0001_initial.sql` — the schema from spec §5, plus:

```sql
CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS items_source_idx ON items(source);
CREATE INDEX IF NOT EXISTS items_first_seen_idx ON items(first_seen_at);
```

- [ ] **Step 2: Write the failing store tests**

`internal/adapters/libsql/store_test.go`. The suite needs the libSQL container, so it skips when
`TEST_TURSO_URL` is unset; `make test` sets it.

```go
package libsql_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/libsql"
	"github.com/gernotstarke/zorgscope/internal/domain"
)

func newStore(t *testing.T) *libsql.Store {
	t.Helper()
	url := os.Getenv("TEST_TURSO_URL")
	if url == "" {
		t.Skip("TEST_TURSO_URL not set; run via `make test`")
	}
	s, err := libsql.Open(url, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := s.TruncateAll(context.Background()); err != nil {
		t.Fatalf("TruncateAll: %v", err)
	}
	return s
}

func at(s string) time.Time {
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return ts
}

func item(id, title string) domain.Item {
	return domain.Item{
		Source: "github", ExternalID: id, Kind: domain.KindIssue,
		Repo: "org/repo", Number: 1, Title: title, URL: "https://example/1",
		Author: "someone", State: "open",
		CreatedAt: at("2026-08-01T00:00:00Z"), UpdatedAt: at("2026-08-10T00:00:00Z"),
	}
}

// QS-1.2: the whole point of the schema. first_seen_at is written once and never again.
func TestReplaceItemsPreservesFirstSeen(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	first, second := at("2026-08-17T10:00:00Z"), at("2026-08-17T11:00:00Z")

	if _, err := s.ReplaceItems(ctx, "github", []domain.Item{item("1", "original")}, first); err != nil {
		t.Fatalf("first ReplaceItems: %v", err)
	}
	if _, err := s.ReplaceItems(ctx, "github", []domain.Item{item("1", "edited")}, second); err != nil {
		t.Fatalf("second ReplaceItems: %v", err)
	}

	got, err := s.Items(ctx)
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(got))
	}
	if !got[0].FirstSeenAt.Equal(first) {
		t.Errorf("FirstSeenAt = %v, want %v — a later refresh must not move it (FR-5.3 AC2)", got[0].FirstSeenAt, first)
	}
	if got[0].Title != "edited" {
		t.Errorf("Title = %q, want %q — content must be updated", got[0].Title, "edited")
	}
}

// FR-5.3 AC3: gone and back again counts as new again.
func TestReplaceItemsDeletesAbsentAndReSeesReturning(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	t1, t2, t3 := at("2026-08-17T10:00:00Z"), at("2026-08-17T11:00:00Z"), at("2026-08-17T12:00:00Z")

	mustReplace(t, s, ctx, []domain.Item{item("1", "a"), item("2", "b")}, t1)
	mustReplace(t, s, ctx, []domain.Item{item("1", "a")}, t2)

	if got := mustItems(t, s, ctx); len(got) != 1 {
		t.Fatalf("len(items) = %d, want 1: the absent item must be deleted", len(got))
	}

	mustReplace(t, s, ctx, []domain.Item{item("1", "a"), item("2", "b")}, t3)

	for _, it := range mustItems(t, s, ctx) {
		if it.ExternalID == "2" && !it.FirstSeenAt.Equal(t3) {
			t.Errorf("returning item FirstSeenAt = %v, want %v (FR-5.3 AC3)", it.FirstSeenAt, t3)
		}
		if it.ExternalID == "1" && !it.FirstSeenAt.Equal(t1) {
			t.Errorf("surviving item FirstSeenAt = %v, want %v", it.FirstSeenAt, t1)
		}
	}
}

// FR-5.5 AC1: one source's write must not touch another source's rows.
func TestReplaceItemsIsScopedToItsSource(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	now := at("2026-08-17T10:00:00Z")

	gh := item("1", "gh")
	td := item("t1", "task")
	td.Source, td.Kind = "todoist", domain.KindTask

	mustReplace(t, s, ctx, []domain.Item{gh}, now)
	if _, err := s.ReplaceItems(ctx, "todoist", []domain.Item{td}, now); err != nil {
		t.Fatalf("ReplaceItems(todoist): %v", err)
	}
	mustReplace(t, s, ctx, []domain.Item{gh}, now)

	if got := mustItems(t, s, ctx); len(got) != 2 {
		t.Fatalf("len(items) = %d, want 2: replacing github deleted todoist rows", len(got))
	}
}

// QS-1.3
func TestLastVisitRoundTrips(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	zero, err := s.LastVisit(ctx)
	if err != nil {
		t.Fatalf("LastVisit: %v", err)
	}
	if !zero.IsZero() {
		t.Errorf("LastVisit on an empty store = %v, want the zero time", zero)
	}
	want := at("2026-08-17T10:00:00Z")
	if err := s.SetLastVisit(ctx, want); err != nil {
		t.Fatalf("SetLastVisit: %v", err)
	}
	got, err := s.LastVisit(ctx)
	if err != nil {
		t.Fatalf("LastVisit: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("LastVisit = %v, want %v", got, want)
	}
}

// QS-1.7
func TestRefreshLeaseIsExclusiveAndExpires(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	now := at("2026-08-17T10:00:00Z")

	ok, err := s.AcquireRefreshLease(ctx, "a", now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("first acquire = %v, %v; want true, nil", ok, err)
	}
	ok, err = s.AcquireRefreshLease(ctx, "b", now.Add(time.Second), time.Minute)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if ok {
		t.Error("second holder acquired a live lease; a refresh must not run twice (QS-1.7)")
	}
	ok, err = s.AcquireRefreshLease(ctx, "b", now.Add(2*time.Minute), time.Minute)
	if err != nil || !ok {
		t.Fatalf("acquire after expiry = %v, %v; want true, nil — a crashed run must not lock forever", ok, err)
	}
	if err := s.ReleaseRefreshLease(ctx, "b"); err != nil {
		t.Fatalf("ReleaseRefreshLease: %v", err)
	}
}

func TestSourceStateRecordsSuccessAndError(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	ok, bad := at("2026-08-17T10:00:00Z"), at("2026-08-17T11:00:00Z")

	if err := s.RecordSourceOK(ctx, "github", ok, 7); err != nil {
		t.Fatalf("RecordSourceOK: %v", err)
	}
	if err := s.RecordSourceError(ctx, "github", bad, "boom"); err != nil {
		t.Fatalf("RecordSourceError: %v", err)
	}

	states, err := s.SourceStates(ctx)
	if err != nil {
		t.Fatalf("SourceStates: %v", err)
	}
	st := states["github"]
	if !st.LastSuccessAt.Equal(ok) {
		t.Errorf("LastSuccessAt = %v, want %v — an error must not erase the last success (FR-1.4 AC2)", st.LastSuccessAt, ok)
	}
	if st.LastError != "boom" || !st.LastErrorAt.Equal(bad) {
		t.Errorf("error = %q at %v, want %q at %v", st.LastError, st.LastErrorAt, "boom", bad)
	}
	if st.ItemCount != 7 {
		t.Errorf("ItemCount = %d, want 7", st.ItemCount)
	}
}

func TestUnnotifiedKeysFiltersWhatWasSent(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	if err := s.MarkNotified(ctx, []string{"github|1"}, at("2026-08-17T10:00:00Z")); err != nil {
		t.Fatalf("MarkNotified: %v", err)
	}
	got, err := s.UnnotifiedKeys(ctx, []string{"github|1", "github|2"})
	if err != nil {
		t.Fatalf("UnnotifiedKeys: %v", err)
	}
	if len(got) != 1 || got[0] != "github|2" {
		t.Errorf("UnnotifiedKeys = %v, want [github|2] (FR-6.1 AC2)", got)
	}
}

func mustReplace(t *testing.T, s *libsql.Store, ctx context.Context, items []domain.Item, now time.Time) {
	t.Helper()
	if _, err := s.ReplaceItems(ctx, "github", items, now); err != nil {
		t.Fatalf("ReplaceItems: %v", err)
	}
}

func mustItems(t *testing.T, s *libsql.Store, ctx context.Context) []domain.Item {
	t.Helper()
	got, err := s.Items(ctx)
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	return got
}
```

- [ ] **Step 3: Run and watch them fail**

Run: `make test`
Expected: FAIL — package `libsql` does not exist.

- [ ] **Step 4: Make `make test` provide the database**

Add a `db` service to the Go test run so `TEST_TURSO_URL` points at a live libSQL server:

```makefile
test: ## Unit + integration tests with race detector and coverage
	$(COMPOSE) up -d db
	$(GO_RUN_NET) sh -c 'TEST_TURSO_URL=http://db:8080 go test -race -coverprofile=coverage.out ./... \
	  && go tool cover -func=coverage.out | tail -1'
	$(COMPOSE) stop db
```

`GO_RUN_NET` is `GO_RUN` with `--network` set to the Compose network so `db` resolves. Skipping the
store tests must remain possible: `make go ARGS="test ./internal/domain/..."` needs no database.

- [ ] **Step 5: Implement the store**

`Open` builds the DSN — `url` alone for the local server, `url + "?authToken=" + token` for Turso —
and opens `sql.Open("libsql", dsn)`. `Migrate` reads the embedded `migrations/*.sql` in name order,
skips versions already in `schema_migrations`, and applies each inside one transaction.

`ReplaceItems` is the heart of the design:

```go
func (s *Store) ReplaceItems(ctx context.Context, source string, items []domain.Item, now time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	ids := make([]string, 0, len(items))
	for _, it := range items {
		// first_seen_at is in the INSERT column list but absent from the UPDATE SET clause.
		// That single asymmetry is what makes NEW correct (FR-5.3 AC2, QS-1.2).
		if _, err := tx.ExecContext(ctx, upsertItemSQL, args(it, now)...); err != nil {
			return 0, fmt.Errorf("upsert %s/%s: %w", source, it.ExternalID, err)
		}
		ids = append(ids, it.ExternalID)
	}
	if err := deleteAbsent(ctx, tx, source, ids); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return len(items), nil
}
```

`deleteAbsent` issues `DELETE FROM items WHERE source = ? AND external_id NOT IN (...)` with a
placeholder per id, and the plain `DELETE FROM items WHERE source = ?` when `ids` is empty.

The lease lives in `app_state` under the key `refresh_lease` with the value `holder|expiry`. Acquire
is one `INSERT … ON CONFLICT DO UPDATE … WHERE` whose `WHERE` clause admits the write only when the
stored expiry is in the past or the holder is the same; `RowsAffected() == 1` means acquired.

`TruncateAll` is a test helper on the concrete type — not on `ports.Store` — that deletes from every
table.

- [ ] **Step 6: Run the tests**

Run: `make test`
Expected: every store test PASSES, `-race` clean.

- [ ] **Step 7: Commit**

```bash
git add internal/adapters/libsql go.mod go.sum Makefile
git commit -m "feat(store): libSQL store with the first-seen invariant and refresh lease (FR-5.3, FR-5.5, QS-1.2, QS-1.7)"
```

---

## Task 6: Fake upstream sources

Written before the adapters so that each adapter can be developed against it (FR‑9.2).

**Files:**

- Create: `cmd/fakesources/main.go`, `cmd/fakesources/main_test.go`, `cmd/fakesources/testdata/github-issues.json`, `cmd/fakesources/testdata/github-runs.json`, `cmd/fakesources/testdata/plausible-aggregate.json`, `cmd/fakesources/testdata/todoist-tasks.json`

**Interfaces:**

- Consumes: nothing
- Produces: an HTTP server on `:9090` serving `POST /graphql`, `GET /repos/{owner}/{repo}/actions/runs`, `GET /api/v1/stats/aggregate`, `GET /rest/v2/tasks`, plus the control routes `POST /_control/add-issue` and `POST /_control/fail?source=github&status=500`

- [ ] **Step 1: Write the failing test**

```go
func TestGraphQLReturnsIssues(t *testing.T) {
	srv := httptest.NewServer(newServer())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/graphql", "application/json", strings.NewReader(`{"query":"{}"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Data struct {
			Repository struct {
				Issues struct{ Nodes []struct{ Title string } }
			}
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data.Repository.Issues.Nodes) == 0 {
		t.Fatal("want at least one issue in the fixture")
	}
}

func TestFailControlMakesTheSourceFail(t *testing.T) {
	srv := httptest.NewServer(newServer())
	defer srv.Close()

	post(t, srv.URL+"/_control/fail?source=github&status=500")

	resp, err := http.Post(srv.URL+"/graphql", "application/json", strings.NewReader(`{"query":"{}"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 — QS-1.4 needs an injectable failure", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `make go ARGS="test ./cmd/fakesources/..."`
Expected: FAIL — `undefined: newServer`.

- [ ] **Step 3: Implement the fake server**

`newServer()` returns an `*http.ServeMux` closing over a mutex-guarded state: the fixture documents,
an injected-failure status per source, and issues added through `/_control/add-issue`. The GraphQL
handler ignores the query and returns the fixture shaped exactly as `githubv4` will unmarshal it —
that shape is the contract, so the fixture must be written against the real schema field names
(`repository.issues.nodes[].number/title/url/author.login/createdAt/updatedAt`, `pageInfo.hasNextPage`,
`pageInfo.endCursor`). One fixture must have `hasNextPage: true` on the first call and `false` on the
second, so QS‑1.5's pagination test has something to page through.

`/_control/add-issue` appends an issue with `createdAt` set to now, which is what QS‑1.1 uses.

- [ ] **Step 4: Run the tests**

Run: `make go ARGS="test ./cmd/fakesources/... -v"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/fakesources
git commit -m "feat(fakes): fixture-backed GitHub, Plausible and Todoist stand-ins (FR-9.2)"
```

---

## Task 7: GitHub issues and pull requests adapter

**Files:**

- Create: `internal/adapters/github/issues.go`, `internal/adapters/github/issues_test.go`
- Modify: `go.mod` (add `github.com/shurcooL/githubv4`, `golang.org/x/oauth2`)

**Interfaces:**

- Consumes: `ports.SourceFetcher`, `ports.FetchResult`, `domain.Item`, `cmd/fakesources`
- Produces:

```go
package github

type Config struct {
	Token   string
	BaseURL string   // "" → https://api.github.com/graphql
	Repos   []string // "owner/name"
}

func NewIssueFetcher(cfg Config, hc *http.Client) *IssueFetcher
func (f *IssueFetcher) Name() string // "github"
func (f *IssueFetcher) Fetch(ctx context.Context) (ports.FetchResult, error)
```

- [ ] **Step 1: Write the failing tests**

```go
func TestFetchReturnsIssuesAndPRs(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	f := github.NewIssueFetcher(github.Config{
		Token: "x", BaseURL: srv.URL + "/graphql", Repos: []string{"org/repo"},
	}, srv.Client())

	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Items) == 0 {
		t.Fatal("want items")
	}
	for _, it := range res.Items {
		if it.Source != "github" {
			t.Errorf("Source = %q, want github", it.Source)
		}
		if it.Kind != domain.KindIssue && it.Kind != domain.KindPR {
			t.Errorf("Kind = %q, want issue or pr", it.Kind)
		}
		if it.ExternalID == "" || it.URL == "" || it.Title == "" {
			t.Errorf("incomplete item: %+v", it)
		}
		if it.CreatedAt.IsZero() || it.UpdatedAt.IsZero() {
			t.Errorf("item %s has no timestamps; FR-2.2 needs them", it.ExternalID)
		}
	}
}

// QS-1.5
func TestFetchFollowsPagination(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	f := github.NewIssueFetcher(github.Config{Token: "x", BaseURL: srv.URL + "/graphql", Repos: []string{"org/paged"}}, srv.Client())
	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Items) != 150 {
		t.Fatalf("len(items) = %d, want 150 — pagination was not followed (QS-1.5)", len(res.Items))
	}
}

// QS-1.4: one bad repository must not lose the others.
func TestFetchReportsFailureButKeepsGoodRepos(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()
	post(t, srv.URL+"/_control/fail?source=github&repo=org/bad&status=500")

	f := github.NewIssueFetcher(github.Config{Token: "x", BaseURL: srv.URL + "/graphql", Repos: []string{"org/repo", "org/bad"}}, srv.Client())
	res, err := f.Fetch(context.Background())

	if err == nil {
		t.Fatal("want an error naming the failing repository")
	}
	if !strings.Contains(err.Error(), "org/bad") {
		t.Errorf("error %q must name the failing repository", err)
	}
	if len(res.Items) == 0 {
		t.Error("items from the healthy repository must still be returned (QS-1.4)")
	}
}

func TestExternalIDIsStableAcrossFetches(t *testing.T) {
	// two Fetch calls must produce identical ExternalIDs, or every refresh would
	// re-mark everything as new (FR-5.3).
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `make go ARGS="test ./internal/adapters/github/..."`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement the fetcher**

Build the client with `oauth2.StaticTokenSource` and `githubv4.NewEnterpriseClient(baseURL, hc)`
when `BaseURL` is set, `githubv4.NewClient(hc)` otherwise. One query per repository requesting the
first 100 open issues and the first 100 open PRs with `pageInfo`, looping on `hasNextPage` with the
`after` cursor. `ExternalID` is `owner/name#number` prefixed by the kind — `issue:org/repo#12` —
because issue and PR numbers share a namespace on GitHub but the URL differs.

Collect per-repository errors into a `errors.Join` and return them alongside the items already
fetched, so the caller gets both. That is what makes QS‑1.4's assertion possible.

- [ ] **Step 4: Run the tests**

Run: `make go ARGS="test ./internal/adapters/github/... -v"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/adapters/github go.mod go.sum
git commit -m "feat(github): open issues and PRs over GraphQL with pagination (FR-2.1, FR-2.2, QS-1.4, QS-1.5)"
```

---

## Task 8: GitHub Actions build status

**Files:**

- Create: `internal/adapters/github/builds.go`, `internal/adapters/github/builds_test.go`

**Interfaces:**

- Consumes: Task 7's `Config`
- Produces: `func NewBuildFetcher(cfg Config, hc *http.Client) *BuildFetcher` with `Name() = "github-builds"` and a `Fetch` returning `FetchResult{Builds: …}`

- [ ] **Step 1: Write the failing tests**

```go
func TestFetchLatestCompletedRunPerRepo(t *testing.T) {
	// asserts one Build per configured repo, with Conclusion, Workflow, RunURL and FinishedAt set
}

func TestInProgressRunKeepsThePreviousConclusion(t *testing.T) {
	// FR-2.3 AC2: Status == "in_progress" and Conclusion == the last completed run's conclusion
}

func TestRepoWithoutWorkflowsYieldsNoBuildAndNoError(t *testing.T) {
	// FR-2.3 AC3
	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("a repo without workflows is not an error: %v", err)
	}
	if len(res.Builds) != 0 {
		t.Fatalf("len(builds) = %d, want 0", len(res.Builds))
	}
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `make go ARGS="test ./internal/adapters/github/... -run Build"`
Expected: FAIL — `undefined: NewBuildFetcher`.

- [ ] **Step 3: Implement**

`GET {base}/repos/{owner}/{repo}/actions/runs?branch={default}&per_page=10` with
`Authorization: Bearer`, `Accept: application/vnd.github+json` and `X-GitHub-Api-Version: 2022-11-28`.
Take the newest `completed` run for `Conclusion`, and if the newest run overall is `in_progress` or
`queued`, set `Status` to it. An empty `workflow_runs` array yields no build and no error.

- [ ] **Step 4: Run the tests**

Run: `make go ARGS="test ./internal/adapters/github/... -v"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/adapters/github
git commit -m "feat(github): Actions build status per repository (FR-2.3)"
```

---

## Task 9: Plausible adapter

**Files:**

- Create: `internal/adapters/plausible/plausible.go`, `internal/adapters/plausible/plausible_test.go`

**Interfaces:**

- Produces: `func New(cfg Config, hc *http.Client) *Fetcher` with `Config{APIKey, BaseURL string; Sites []string}`, `Name() = "plausible"`, `Fetch` returning `FetchResult{Metrics: …}`

- [ ] **Step 1: Write the failing tests**

```go
func TestFetchReturnsBothWindowsWithComparison(t *testing.T) {
	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Metrics) != 2 { // one site, 7 and 30 days
		t.Fatalf("len(metrics) = %d, want 2 (FR-3.1 AC1)", len(res.Metrics))
	}
	byWindow := map[int]domain.Metric{}
	for _, m := range res.Metrics {
		byWindow[m.WindowDays] = m
	}
	for _, w := range []int{7, 30} {
		m, ok := byWindow[w]
		if !ok {
			t.Fatalf("no metric for a %d-day window", w)
		}
		if m.Visitors == 0 || m.Pageviews == 0 {
			t.Errorf("%d-day window has no figures: %+v", w, m)
		}
		if m.PrevVisitors == 0 {
			t.Errorf("%d-day window has no comparison; FR-3.1 AC2 needs one", w)
		}
	}
}

func TestFetchErrorsNameTheSite(t *testing.T) {
	// with the fake failing, err must mention the site so FR-1.4 AC2 can show it
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `make go ARGS="test ./internal/adapters/plausible/..."`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

Two requests per site against
`{base}/api/v1/stats/aggregate?site_id={site}&period={7d|30d}&metrics=visitors,pageviews&compare=previous_period`
with `Authorization: Bearer {APIKey}`. Decode
`{"results":{"visitors":{"value":N,"comparison_value":M},"pageviews":{…}}}` into `domain.Metric`.
Wrap errors as `fmt.Errorf("plausible %s (%dd): %w", site, days, err)`.

- [ ] **Step 4: Run the tests**

Run: `make go ARGS="test ./internal/adapters/plausible/... -v"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/adapters/plausible
git commit -m "feat(plausible): 7- and 30-day visitors and pageviews with comparison (FR-3.1)"
```

---

## Task 10: Todoist adapter

**Files:**

- Create: `internal/adapters/todoist/todoist.go`, `internal/adapters/todoist/todoist_test.go`

**Interfaces:**

- Produces: `func New(cfg Config, hc *http.Client, clock ports.Clock) *Fetcher` with `Config{Token, BaseURL, Filter string}`, `Name() = "todoist"`

- [ ] **Step 1: Write the failing tests**

```go
func TestFetchReturnsOnlyOverdueAndDueToday(t *testing.T) {
	clock := &ports.FixedClock{T: at("2026-08-17T12:00:00Z")}
	// fixture holds one overdue, one due today, one due next week, one with no due date
	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("len(items) = %d, want 2 — later and undated tasks are out of scope (FR-4.1 AC3)", len(res.Items))
	}
	for _, it := range res.Items {
		if it.Kind != domain.KindTask {
			t.Errorf("Kind = %q, want task", it.Kind)
		}
		if it.DueAt.IsZero() {
			t.Errorf("task %s has no DueAt", it.ExternalID)
		}
	}
}

func TestOverdueSortsBeforeDueToday(t *testing.T) {
	// FR-4.1 AC2
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `make go ARGS="test ./internal/adapters/todoist/..."`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

`GET {base}/rest/v2/tasks?filter={url-encoded filter}` with `Authorization: Bearer {Token}`. Map
`id`, `content`, `project_id`, `priority`, `url` and `due.date`/`due.datetime` to `domain.Item` with
`Kind = KindTask`, `Source = "todoist"`, `ExternalID = "todoist:" + id`. Because Todoist's filter
strings are hard to trust, filter again in Go against `clock.Now()` in the configured timezone: keep
tasks whose due date is before the end of today.

- [ ] **Step 4: Run the tests**

Run: `make go ARGS="test ./internal/adapters/todoist/... -v"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/adapters/todoist
git commit -m "feat(todoist): overdue and due-today tasks (FR-4.1)"
```

---

## Task 11: The refresh runner

**Files:**

- Create: `internal/refresh/runner.go`, `internal/refresh/runner_test.go`

**Interfaces:**

- Consumes: `ports.Store`, `ports.SourceFetcher`, `ports.Clock`, `ports.FakeFetcher`
- Produces:

```go
package refresh

var ErrBusy = errors.New("a refresh is already running")

type Report struct {
	RunID               int64
	StartedAt, EndedAt  time.Time
	Trigger             string
	OK                  bool
	Sources             []SourceReport
}

type SourceReport struct {
	Source string
	Stored int
	Err    string
}

type Runner struct{ /* store, fetchers, clock, notifier, log */ }

func New(store ports.Store, fetchers []ports.SourceFetcher, clock ports.Clock, n ports.Notifier, log *slog.Logger) *Runner
func (r *Runner) Run(ctx context.Context, trigger string) (Report, error)
```

- [ ] **Step 1: Write the failing tests**

```go
func TestRunStoresEverySourceAndRecordsTheRun(t *testing.T) {
	store := newTestStore(t) // the real libsql store; the runner's contract is transactional
	fetchers := []ports.SourceFetcher{
		&ports.FakeFetcher{SourceName: "github", Result: ports.FetchResult{Items: []domain.Item{item("1")}}},
		&ports.FakeFetcher{SourceName: "todoist", Result: ports.FetchResult{Items: []domain.Item{task("t1")}}},
	}
	r := refresh.New(store, fetchers, &ports.FixedClock{T: now}, nil, discardLogger())

	rep, err := r.Run(context.Background(), "cron")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.OK || len(rep.Sources) != 2 {
		t.Fatalf("report = %+v, want OK with two sources", rep)
	}
	last, err := store.LastRun(context.Background())
	if err != nil {
		t.Fatalf("LastRun: %v", err)
	}
	if last.Trigger != "cron" || !last.OK {
		t.Errorf("LastRun = %+v, want a successful cron run (FR-5.4)", last)
	}
}

// FR-5.1 AC4 / QS-1.4
func TestOneFailingSourceDoesNotStopTheOthers(t *testing.T) {
	fetchers := []ports.SourceFetcher{
		&ports.FakeFetcher{SourceName: "github", Err: errors.New("boom")},
		&ports.FakeFetcher{SourceName: "todoist", Result: ports.FetchResult{Items: []domain.Item{task("t1")}}},
	}
	rep, err := r.Run(context.Background(), "cron")

	if err != nil {
		t.Fatalf("Run must not fail because one source did: %v", err)
	}
	if rep.OK {
		t.Error("report.OK must be false when a source failed")
	}
	items, _ := store.Items(context.Background())
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1 — the healthy source must be stored", len(items))
	}
	states, _ := store.SourceStates(context.Background())
	if states["github"].LastError == "" {
		t.Error("the failing source must record its error (FR-1.4 AC2)")
	}
}

// QS-1.7
func TestConcurrentRunsAreRejected(t *testing.T) {
	block := make(chan struct{})
	r := refresh.New(store, []ports.SourceFetcher{
		&ports.FakeFetcher{SourceName: "github", Block: block},
	}, clock, nil, discardLogger())

	done := make(chan error, 1)
	go func() { _, err := r.Run(context.Background(), "cron"); done <- err }()
	waitUntilLeaseTaken(t, store)

	if _, err := r.Run(context.Background(), "user"); !errors.Is(err, refresh.ErrBusy) {
		t.Fatalf("second run err = %v, want ErrBusy", err)
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatalf("first run: %v", err)
	}
}

// FR-5.5 AC2
func TestCancelledRunLeavesEarlierSourcesStored(t *testing.T) {
	// cancel the context between the two fetchers; assert the first source's items are
	// present and the second is recorded as failed
}

// QS-2.5
func TestReportCarriesDuration(t *testing.T) {
	// EndedAt.Sub(StartedAt) is recorded and finished runs have FinishedAt set
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `make go ARGS="test ./internal/refresh/..."`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement the runner**

```go
func (r *Runner) Run(ctx context.Context, trigger string) (Report, error) {
	now := r.clock.Now()
	holder := fmt.Sprintf("%s-%d", trigger, now.UnixNano())

	ok, err := r.store.AcquireRefreshLease(ctx, holder, now, leaseTTL)
	if err != nil {
		return Report{}, fmt.Errorf("acquire lease: %w", err)
	}
	if !ok {
		return Report{}, ErrBusy
	}
	defer func() { _ = r.store.ReleaseRefreshLease(context.WithoutCancel(ctx), holder) }()

	runID, err := r.store.StartRun(ctx, trigger, now)
	if err != nil {
		return Report{}, fmt.Errorf("start run: %w", err)
	}

	rep := Report{RunID: runID, StartedAt: now, Trigger: trigger, OK: true}
	var fresh []domain.Item

	for _, f := range r.fetchers {
		sr := r.runSource(ctx, f, now, &fresh)
		if sr.Err != "" {
			rep.OK = false
		}
		rep.Sources = append(rep.Sources, sr)
	}

	r.notify(ctx, fresh) // never fails the run (FR-6.1 AC3)

	rep.EndedAt = r.clock.Now()
	if err := r.store.FinishRun(ctx, runID, rep.EndedAt, rep.OK, detail(rep)); err != nil {
		r.log.Error("finish run", "err", err)
	}
	return rep, nil
}
```

`runSource` calls `Fetch`, and on error records `RecordSourceError` and returns without touching the
stored items — the previous data survives (FR‑1.4 AC3). On success it calls `ReplaceItems`,
`UpsertBuilds` and `UpsertMetrics`, then `RecordSourceOK`. Each of those store calls is its own
transaction, which is FR‑5.5 AC1. `leaseTTL` is 5 minutes: longer than QS‑2.5's 30-second budget, short
enough that a crashed machine unblocks the next cron trigger.

The `context.WithoutCancel` on release matters: a cancelled run must still free its lease.

`notify` is a no-op while `r.notifier` is nil; Task 17 fills it in.

- [ ] **Step 4: Run the tests**

Run: `make test`
Expected: PASS, `-race` clean.

- [ ] **Step 5: Commit**

```bash
git add internal/refresh
git commit -m "feat(refresh): single-flight refresh run with per-source transactions (FR-5.1, FR-5.4, FR-5.5, QS-1.7)"
```

---

## Task 12: Web server, authentication and security headers

**Files:**

- Create: `internal/web/server.go`, `internal/web/auth.go`, `internal/web/auth_test.go`, `internal/web/templates/layout.html`, `internal/web/templates/login.html`, `internal/web/static/app.css`
- Modify: `cmd/zorgscope/main.go`

**Interfaces:**

- Consumes: `ports.Store`, `refresh.Runner`, `config.Config`
- Produces:

```go
package web

type Options struct {
	Config  config.Config
	Store   ports.Store
	Runner  *refresh.Runner
	Clock   ports.Clock
	Log     *slog.Logger
}

func New(o Options) (*Server, error)
func (s *Server) Handler() http.Handler
```

- [ ] **Step 1: Write the failing tests**

```go
// QS-4.1 — the table is the point: a new route cannot be forgotten.
func TestEveryProtectedRouteRefusesAnonymousAccess(t *testing.T) {
	h := newTestServer(t).Handler()
	tests := []struct {
		method, path string
		wantStatus   int
	}{
		{http.MethodGet, "/", http.StatusSeeOther},          // FR-8.3 AC1: redirect, not 401
		{http.MethodGet, "/tile/github", http.StatusUnauthorized},
		{http.MethodPost, "/seen", http.StatusUnauthorized},
		{http.MethodPost, "/refresh", http.StatusUnauthorized},
		{http.MethodPost, "/api/refresh", http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if strings.Contains(rec.Body.String(), "arc42") {
				t.Error("an anonymous response leaked dashboard content")
			}
		})
	}
}

func TestPublicRoutesNeedNoSession(t *testing.T) {
	for _, path := range []string{"/healthz", "/login", "/docs", "/static/app.css"} {
		// want 200
	}
}

func TestSignInIssuesAHardenedCookie(t *testing.T) {
	rec := post(h, "/login", url.Values{"token": {testToken}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	c := cookieNamed(rec, "zorgscope_session")
	if c == nil {
		t.Fatal("no session cookie")
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || !c.Secure {
		t.Errorf("cookie = %+v, want HttpOnly, SameSite=Lax, Secure (FR-8.3 AC2)", c)
	}
	if strings.Contains(c.Value, testToken) {
		t.Error("the cookie contains the token itself (FR-8.3 AC2)")
	}
}

func TestChangingTheTokenInvalidatesExistingSessions(t *testing.T) {
	// FR-8.3 AC3: a cookie minted by a server with token A is rejected by a server with token B
}

func TestTamperedCookieIsRejected(t *testing.T) {
	// flip a byte of the signature; want the redirect to /login
}

func TestRefreshEndpointTakesTheBearerNotTheCookie(t *testing.T) {
	// with a valid session cookie but no bearer: 401. With the bearer: 200.
	// The two credentials are independent, so cron-job.org holds only refresh rights.
}

// QS-4.2
func TestFailedSignInsAreRateLimited(t *testing.T) {
	for i := 0; i < 10; i++ {
		post(h, "/login", url.Values{"token": {"wrong"}})
	}
	rec := post(h, "/login", url.Values{"token": {"wrong"}})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := post(h, "/login", url.Values{"token": {testToken}}); got.Code != http.StatusSeeOther {
		t.Error("a valid token must still be accepted after the limit (QS-4.2)")
	}
}

// QS-4.3
func TestNoResponseEverContainsASecret(t *testing.T) {
	const canary = "canary-token-value"
	// build a server whose every secret is canary+suffix, exercise every route
	// including error paths, and search each body and header for canary
}

// QS-4.4
func TestSecurityHeaders(t *testing.T) {
	rec := get(h, "/login")
	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" || strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP = %q, want a policy without unsafe-inline", csp)
	}
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `make go ARGS="test ./internal/web/..."`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

Routes with Go 1.22 method patterns. `requireSession` wraps page and fragment handlers: a `GET`
without a valid cookie redirects to `/login`, anything else returns 401 — the split FR‑8.3 AC1 asks
for. `requireBearer` wraps `POST /api/refresh` and compares with `subtle.ConstantTimeCompare`.

The cookie value is `base64(expiry) + "." + base64(HMAC-SHA256(key, expiry))` where
`key = SHA256("zorgscope-session-v1" + ZORGSCOPE_TOKEN)`. That derivation gives FR‑8.3 AC3 for free:
a new token means a new key means every old signature fails.

Rate limiting is a small in-memory token bucket keyed by client IP, 10 failures per 15 minutes. It
lives in memory deliberately: the machine stops when idle, so a persistent counter would add a
database write to every failed attempt for no security gain against an attacker who can simply wait.

Security headers come from one middleware wrapping everything. CSP is
`default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'`
— vendored htmx and a stylesheet file, so no `unsafe-inline` is needed.

Wire `main.go`: load config, open the store, migrate, build the enabled fetchers, build the runner,
build the server.

- [ ] **Step 4: Run the tests**

Run: `make test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/web cmd/zorgscope
git commit -m "feat(web): token sign-in, derived session cookie and security headers (FR-8.3, QS-4.1, QS-4.2, QS-4.4)"
```

---

## Task 13: Dashboard assembly in the domain

**Files:**

- Create: `internal/domain/dashboard.go`, `internal/domain/dashboard_test.go`

**Interfaces:**

- Produces:

```go
package domain

type DashboardInput struct {
	Now         time.Time
	LastVisitAt time.Time
	LastRun     RefreshRun
	StaleAfter  time.Duration
	Items       []Item
	Builds      []Build
	Metrics     []Metric
	States      map[string]SourceState
	Disabled    []string // sources without a credential (FR-8.2 AC2)
}

type Dashboard struct {
	GeneratedAt time.Time
	LastVisitAt time.Time
	LastRunAt   time.Time
	NewTotal    int
	Tiles       []Tile
}

type Tile struct {
	Name      string // "github", "builds", "sites", "tasks"
	Title     string
	NewCount  int
	Stale     bool
	Disabled  bool
	Error     string
	LastOKAt  time.Time
	Items     []Item
	Builds    []Build
	Sites     []SiteMetrics
}

type SiteMetrics struct {
	Site   string
	Week   Metric
	Month  Metric
}

func BuildDashboard(in DashboardInput) Dashboard
```

- [ ] **Step 1: Write the failing tests**

```go
func TestBuildDashboardCountsNewPerTileAndOverall(t *testing.T) {
	// FR-1.2 AC2/AC3
}

func TestFailingSourceKeepsItsItemsAndShowsTheError(t *testing.T) {
	// FR-1.4 AC3: Tile.Items is non-empty and Tile.Error is set
}

func TestDisabledSourceIsMarkedNotFailing(t *testing.T) {
	// FR-8.2 AC2: Disabled == true, Error == ""
}

func TestStaleIsDerivedFromLastSuccessAndStaleAfter(t *testing.T) {
	// FR-1.4 AC1
}

func TestSiteMetricsPairsTheTwoWindowsInConfigOrder(t *testing.T) {
	// FR-3.1 AC3
}

func TestTilesAppearEvenWhenEmpty(t *testing.T) {
	in := domain.DashboardInput{Now: now}
	d := domain.BuildDashboard(in)
	if len(d.Tiles) != 4 {
		t.Fatalf("len(tiles) = %d, want 4 — an empty tile still has an empty state (FR-1.4)", len(d.Tiles))
	}
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `make go ARGS="test ./internal/domain/... -run Dashboard"`
Expected: FAIL — `undefined: BuildDashboard`.

- [ ] **Step 3: Implement**

Partition items by source and kind, call `SortItems` and `CountNew` per tile, pair metrics into
`SiteMetrics` by site preserving input order, and derive `Stale`, `Disabled` and `Error` from
`States` and `Disabled`. Pure function, no I/O — which is why this task is in the domain and not in
`internal/web`.

- [ ] **Step 4: Run the tests with coverage**

Run: `make test-domain`
Expected: PASS, coverage still ≥ 90 %.

- [ ] **Step 5: Commit**

```bash
git add internal/domain
git commit -m "feat(domain): dashboard assembly with per-tile new counts and freshness (FR-1.2, FR-1.4)"
```

---

## Task 14: Dashboard rendering

**Files:**

- Create: `internal/web/dashboard.go`, `internal/web/dashboard_test.go`, `internal/web/templates/dashboard.html`, `internal/web/templates/tiles/{github,builds,sites,tasks}.html`, `internal/web/static/htmx.min.js`
- Modify: `internal/web/server.go`, `internal/web/static/app.css`

**Interfaces:**

- Consumes: `domain.BuildDashboard`, `ports.Store`
- Produces: `GET /`, `GET /tile/{name}`, `POST /seen`, `POST /refresh`

- [ ] **Step 1: Write the failing tests**

```go
func TestDashboardRendersTilesAndNewBadges(t *testing.T) {
	// seed the store with two items, one first-seen after the last visit
	body := getAuthed(t, h, "/").Body.String()
	if !strings.Contains(body, "NEW") {
		t.Error("the new item has no NEW badge (FR-1.2 AC1)")
	}
	if strings.Count(body, "NEW") != 1 {
		t.Errorf("NEW appears %d times, want 1", strings.Count(body, "NEW"))
	}
}

func TestTabTitleCarriesTheNewCount(t *testing.T) {
	// FR-1.2 AC3
	if !strings.Contains(body, "<title>(1) zorgscope") {
		t.Errorf("title does not carry the new count")
	}
}

func TestMarkAllSeenClearsTheBadges(t *testing.T) {
	// FR-1.3: POST /seen, then GET / has no NEW
	// and it must work without JavaScript: assert the response is a 303 redirect
}

func TestDashboardMakesNoUpstreamRequest(t *testing.T) {
	// FR-1.1 AC2: build the server with fetchers whose Fetch fails the test if called,
	// then GET / and assert Calls == 0
}

func TestTileFragmentRendersWithoutTheLayout(t *testing.T) {
	body := getAuthed(t, h, "/tile/github").Body.String()
	if strings.Contains(body, "<html") {
		t.Error("a tile fragment must be a fragment, not a page (FR-1.6 AC1)")
	}
}

func TestFailingSourceShowsItsErrorAndKeepsContent(t *testing.T) {
	// FR-1.4 AC2/AC3
}

// QS-2.3
func TestRenderedPageStaysInsideItsBudget(t *testing.T) {
	// seed 10 repos worth of items, 4 sites, 30 tasks
	if n := len(body); n > 150*1024 {
		t.Errorf("dashboard is %d bytes, budget is 150 kB (QS-2.3)", n)
	}
}

// QS-2.2
func BenchmarkDashboard(b *testing.B) {
	// asserts the p95 budget is plausible; report ns/op
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `make go ARGS="test ./internal/web/... -run Dashboard"`
Expected: FAIL — `undefined` handlers.

- [ ] **Step 3: Implement**

Templates are parsed once at start-up from an embedded FS. The handler reads items, builds, metrics,
states, last visit and last run from the store, calls `domain.BuildDashboard`, and executes
`dashboard.html`. `GET /tile/{name}` executes only that tile's template.

`POST /seen` calls `SetLastVisit(clock.Now())` and answers `303 See Other` to `/` — a plain form
post, so it works with JavaScript disabled (FR‑1.3 AC3). `POST /refresh` calls the runner and answers
`303`, or `409` on `refresh.ErrBusy`.

CSS uses custom properties on `:root` with a `@media (prefers-color-scheme: dark)` block redefining
them. No theme script, so no flash (FR‑1.5 AC1). Badges carry text as well as colour (FR‑1.5 AC2).
Download htmx once and commit it under `static/`; it is a dependency of the page, not of the module.

- [ ] **Step 4: Run the tests**

Run: `make test`
Expected: PASS.

- [ ] **Step 5: Look at it**

Run: `make fakes` in one terminal, `make backend` and `make client` in others.
Expected: a populated dashboard with tiles, badges and a working "mark all seen".

- [ ] **Step 6: Commit**

```bash
git add internal/web
git commit -m "feat(web): tiled dashboard with NEW badges and mark-all-seen (FR-1.1, FR-1.2, FR-1.3, FR-1.5, QS-2.3)"
```

---

## Task 15: The refresh endpoints

**Files:**

- Create: `internal/web/refresh.go`, `internal/web/refresh_test.go`
- Modify: `internal/web/server.go`

**Interfaces:**

- Produces: `POST /api/refresh` (bearer) and `POST /refresh` (session)

- [ ] **Step 1: Write the failing tests**

```go
func TestAPIRefreshRunsAndReportsPerSource(t *testing.T) {
	rec := postBearer(t, h, "/api/refresh", refreshSecret)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		OK      bool
		Sources []struct {
			Source string
			Stored int
			Err    string
		}
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Sources) == 0 {
		t.Error("the response must report per source (FR-5.1 AC3)")
	}
}

func TestAPIRefreshRejectsAWrongSecretAndFetchesNothing(t *testing.T) {
	f := &ports.FakeFetcher{SourceName: "github"}
	// ... server built with f
	rec := postBearer(t, h, "/api/refresh", "wrong")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if f.Calls != 0 {
		t.Error("an unauthenticated refresh fetched anyway (FR-5.1 AC2)")
	}
}

func TestSecondConcurrentRefreshGets409(t *testing.T) {
	// FR-5.2 AC2 / QS-1.7
}

// QS-2.5
func TestRefreshCompletesInsideTheBudget(t *testing.T) {
	start := time.Now()
	postBearer(t, h, "/api/refresh", refreshSecret)
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("refresh took %v, budget is 30s (QS-2.5)", d)
	}
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `make go ARGS="test ./internal/web/... -run Refresh"`
Expected: FAIL.

- [ ] **Step 3: Implement**

`POST /api/refresh` runs the runner with trigger `cron` and encodes the `Report` as JSON; `ErrBusy`
becomes 409. The handler sets its own timeout with `context.WithTimeout(r.Context(), 2*time.Minute)`
so a hung upstream cannot hold the machine awake indefinitely.

- [ ] **Step 4: Run the tests**

Run: `make test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/web
git commit -m "feat(web): cron and user refresh endpoints (FR-5.1, FR-5.2, QS-2.5)"
```

---

## Task 16: Documentation pages

**Files:**

- Create: `internal/web/docs.go`, `internal/web/docs_test.go`, `internal/web/templates/docs.html`, `internal/web/templates/docs_index.html`
- Modify: `go.mod` (add `github.com/yuin/goldmark`), `internal/web/server.go`

**Interfaces:**

- Produces: `GET /docs` and `GET /docs/{category}/{page}`, both unauthenticated

- [ ] **Step 1: Write the failing tests**

```go
func TestDocsIndexListsTheThreeCategories(t *testing.T) {
	body := get(h, "/docs").Body.String()
	for _, want := range []string{"Requirements", "Decisions", "Concepts"} {
		if !strings.Contains(body, want) {
			t.Errorf("index does not mention %q (FR-7.1 AC1)", want)
		}
	}
}

func TestDocPageRendersMarkdown(t *testing.T) {
	body := get(h, "/docs/requirements/01-goals").Body.String()
	if !strings.Contains(body, "<h1>") || !strings.Contains(body, "Goals") {
		t.Error("markdown was not rendered (FR-7.1 AC2)")
	}
}

func TestDocsNeedNoSession(t *testing.T) {
	// FR-7.1 AC3
}

func TestInternalLinksAreRewritten(t *testing.T) {
	// FR-7.2 AC2: "../requirements/01-goals.md" becomes "/docs/requirements/01-goals"
	if strings.Contains(body, `href="01-goals.md"`) || strings.Contains(body, "../requirements/") {
		t.Error("a raw .md link survived rewriting")
	}
}

func TestUnknownDocIs404NotAPathTraversal(t *testing.T) {
	for _, p := range []string{"/docs/requirements/nope", "/docs/../../etc/passwd", "/docs/requirements/../../../go.mod"} {
		if code := get(h, p).Code; code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", p, code)
		}
	}
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `make go ARGS="test ./internal/web/... -run Docs"`
Expected: FAIL.

- [ ] **Step 3: Implement**

`//go:embed all:docs` cannot reach outside the package, so add
`internal/web/docsfs/docs.go` holding `//go:embed requirements decisions concepts` over a directory
populated by a `make docs-sync` target — or, simpler and preferred: embed from the repository root by
placing `//go:embed docs` in a small `internal/docsfs` package at the module root level and importing
it. Choose the second; it keeps `docs/` as the single source.

Render with goldmark plus its GFM extension. Rewrite links in a goldmark AST walk: a destination
ending in `.md` becomes `/docs/<category>/<basename without extension>`, resolved against the current
page's directory. Look pages up by an exact match against the embedded file list, which makes traversal
impossible by construction rather than by sanitising strings.

- [ ] **Step 4: Run the tests**

Run: `make test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/web internal/docsfs go.mod go.sum
git commit -m "feat(web): render requirements, decisions and concepts at /docs (FR-7.1, FR-7.2)"
```

---

## Task 17: Slack notifications

**Files:**

- Create: `internal/adapters/slack/slack.go`, `internal/adapters/slack/slack_test.go`
- Modify: `internal/refresh/runner.go`, `cmd/zorgscope/main.go`

**Interfaces:**

- Produces: `func New(webhookURL string, hc *http.Client) *Notifier` implementing `ports.Notifier`

- [ ] **Step 1: Write the failing tests**

```go
func TestNotifyPostsOneMessagePerItem(t *testing.T) {
	// httptest server records the posted bodies; assert one per item with title and URL
}

func TestNotifyIsSkippedForAlreadyNotifiedItems(t *testing.T) {
	// FR-6.1 AC2: run the runner twice with the same items; the second run posts nothing
}

func TestSlackFailureDoesNotFailTheRun(t *testing.T) {
	// FR-6.1 AC3: webhook returns 500; Run returns nil and rep.OK stays true
}

func TestWebhookURLNeverAppearsInAnError(t *testing.T) {
	// QS-4.3
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `make go ARGS="test ./internal/adapters/slack/..."`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

`Notify` posts `{"text": "..."}` per item. In the runner, `notify` first calls
`store.UnnotifiedKeys` with the fresh items' keys (`source|external_id`), sends only those, then calls
`store.MarkNotified` for the ones that were sent — in that order, so a crash between send and mark
repeats a message rather than swallowing it. Errors are logged and dropped. Strip the webhook URL from
any wrapped error with a fixed replacement string.

- [ ] **Step 4: Run the tests**

Run: `make test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/adapters/slack internal/refresh cmd/zorgscope
git commit -m "feat(slack): notify once per newly seen issue or PR (FR-6.1)"
```

---

## Task 18: CI, deployment and the cron trigger

**Files:**

- Modify: `.github/workflows/ci.yml`, `.github/workflows/deploy.yml`
- Create: `docs/concepts/operations.md`

**Interfaces:**

- Consumes: everything
- Produces: green CI and a deployed app

- [ ] **Step 1: Rewrite the CI workflow**

One job: check out, set up Go 1.26, start the libSQL service container, run
`go vet ./...`, `golangci-lint run`, `go test -race ./...` with `TEST_TURSO_URL` set,
`govulncheck ./...` (QS‑4.5), and the markdown lint. The whole job must stay under 3 minutes
(QS‑5.3); if it does not, cache the module and build caches.

- [ ] **Step 2: Verify CI is green**

Run: push the branch and watch the run.
Expected: green, under 3 minutes.

- [ ] **Step 3: Create the Fly app and its secrets**

```bash
make fly ARGS="apps create zorgscope"
printf 'ZORGSCOPE_TOKEN=%s\nREFRESH_SECRET=%s\nGITHUB_TOKEN=%s\nPLAUSIBLE_API_KEY=%s\nTODOIST_TOKEN=%s\nTURSO_URL=%s\nTURSO_AUTH_TOKEN=%s\n' ... | make fly-secrets-import
make fly-deploy
```

Turso: create the database with the Turso CLI in a container, take its URL and a token.

- [ ] **Step 4: Verify the deployment**

Run: `make fly-status`, then `curl -fsS https://zorgscope.fly.dev/healthz`, then time 20 cold opens
of `/` with `curl -w '%{time_total}\n'` after `make fly ARGS="machine stop <id>"`.
Expected: `ok`; p95 under 2.5 s (QS‑2.1). Record the numbers in `docs/concepts/operations.md`.

- [ ] **Step 5: Configure cron-job.org**

Create a job calling `POST https://zorgscope.fly.dev/api/refresh` with header
`Authorization: Bearer <REFRESH_SECRET>` every 15 minutes, matching `refresh.interval` in the
configuration. Document it in `docs/concepts/operations.md`, including that the ping doubles as the
machine warmer (QS‑2.4).

- [ ] **Step 6: Verify a real refresh**

Run: `make fly-logs`, wait for the cron trigger, then open the dashboard.
Expected: a refresh run in the logs, real data on the page, the header showing the run time.

- [ ] **Step 7: Commit**

```bash
git add .github docs/concepts/operations.md
git commit -m "feat(ops): CI, Fly deployment and the cron-job.org trigger (FR-9.5, QS-2.1, QS-2.4, QS-4.5)"
```

---

## Task 19: Decisions and concepts

**Files:**

- Create: `docs/decisions/README.md`, `docs/decisions/0001…0008-*.md`, `docs/decisions/adr-template.md`, `docs/concepts/security-and-tokens.md`, `docs/concepts/data-storage.md`, `docs/concepts/configuration.md`

- [ ] **Step 1: Write the eight decision records**

MADR format — Context and Problem Statement, Considered Options, Decision Outcome, Consequences.
Subjects and their evidence in the code:

| ADR | Subject | Evidence |
|-----|---------|----------|
| 0001 | Go modular monolith with a hexagonal core | the `depguard` rules in `.golangci.yml` |
| 0002 | Server-rendered html/template plus htmx | `internal/web/templates`, no `package.json` |
| 0003 | Fly.io scaled to zero with an external cron trigger | `deploy/fly.toml`, `POST /api/refresh` |
| 0004 | Turso and libSQL, libsql-server locally | `internal/adapters/libsql`, `deploy/compose.yml` |
| 0005 | Embedded SQL migrations rather than a schema tool | `internal/adapters/libsql/migrations` |
| 0006 | First-seen versus last-visit as the definition of new | the upsert in `store.go`, `TestReplaceItemsPreservesFirstSeen` |
| 0007 | Token sign-in with a derived session cookie | `internal/web/auth.go` |
| 0008 | Docker and make as the only local toolchain | the `Makefile` |

Each must record what was rejected and why: 0003 against an always-on machine with an in-process
scheduler, 0005 against Atlas, 0006 against daily snapshots and per-item dismissals, 0007 against
passkeys. Those are the decisions the previous design took differently, and the reasons matter more
than the outcomes.

- [ ] **Step 2: Write the three concept pages**

*Security and token handling* — the two independent credentials, the cookie derivation, constant-time
comparison, rate limiting, what is never logged, and how to rotate each secret.
*Data storage* — the schema, the first-seen invariant with the upsert quoted, per-source transactions,
the lease, and how to inspect the database with `make db-shell`.
*Configuration* — what is YAML and what is environment, why the split, how a source becomes disabled,
and what changing the refresh interval requires (the YAML and cron-job.org, both).

- [ ] **Step 3: Check they render**

Run: `make backend`, open `/docs`.
Expected: every page listed and rendering, every internal link resolving (FR‑7.2 AC2).

- [ ] **Step 4: Lint the documentation**

Run: `make docs-check`
Expected: no markdownlint findings, no broken links.

- [ ] **Step 5: Commit**

```bash
git add docs/decisions docs/concepts
git commit -m "docs: decision records and concept pages (G-6, FR-7.1)"
```

---

## Task 20: Final verification against the requirements

- [ ] **Step 1: Walk the requirements**

Open `docs/requirements/04-functional-requirements.md` and confirm every **M** story has a passing
test naming its id. List any without one.

- [ ] **Step 2: Walk the quality scenarios**

Open `docs/requirements/05-quality-requirements.md` and confirm each scenario's measure has been
taken, not just intended. QS‑2.1, QS‑3.1, QS‑3.2 and QS‑3.3 are measured against the deployment;
record the actual numbers in `docs/concepts/operations.md`.

- [ ] **Step 3: Full check**

Run: `make check`
Expected: PASS.

- [ ] **Step 4: Commit and open the pull request**

```bash
git commit -am "docs(ops): measured quality scenario results"
gh pr create --fill
```

---

## Self-review notes

**Spec coverage.** Spec §2 scope → Tasks 7–10, 14, 16, 17. §3 runtime → Tasks 1, 11, 15, 18. §4
structure → the File structure table and Tasks 3–5. §5 data → Task 5. §6 client → Tasks 12, 14, 16.
§7 configuration → Task 2. §8 development and testing → Tasks 1, 6 and the test steps throughout. §9
documentation → Task 19. §10 risks → the measurement steps in Tasks 18 and 20.

**Deferred to v2, deliberately not in this plan:** FR‑2.4 (mentions and review requests), FR‑6.2
(email notifier), FR‑8.4 (configuration page in the browser). FR‑1.6 (htmx polling) is prepared by the
tile-fragment routes in Task 14 but its polling attribute is a one-line follow-up, not a task.

**Interface consistency.** `ports.Store` in Task 4 is the contract Tasks 5, 11, 12, 14, 15 and 17 use;
`ports.FetchResult` is what Tasks 7–10 return and Task 11 consumes; `domain.DashboardInput` in Task 13
is what Task 14 fills. `ReplaceItems` takes `now` because the store must not own a clock —
`FirstSeenAt` has to be the run's time, not the row's write time, or two sources in one run would
disagree about when "now" was.
