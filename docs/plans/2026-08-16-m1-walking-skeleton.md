# zorgscope M1 – Walking Skeleton Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A running dashboard (`make app`) with header, Attention tile and Repositories tile fed by GitHub (real API or fake server), with daily snapshots, NEW / UNANSWERED / BUILD FAILED detection, dismissals, manual refresh, staleness display, dev‑mode auth, SQLite persistence, Docker image and CI — the vertical slice every later source plugs into.

**Architecture:** Hexagonal Go monolith: `internal/domain` (pure rules) ← `internal/ports` (interfaces + in‑memory store) ← `internal/app` (scheduler, snapshotter, dashboard query) ← `internal/adapters/{github,sqlite,clock}` and `internal/server` (net/http, html/template, htmx). `cmd/zorgscope` wires everything; `cmd/fakesources` serves a deterministic fake GitHub for e2e/demo. See `docs/architecture/` — especially ch. 5 (building blocks), 6 (runtime), 8 (concepts).

**Tech Stack:** Go 1.26 (std lib: `net/http` with 1.22+ method patterns, `html/template`, `log/slog`, `database/sql`, `embed`), `gopkg.in/yaml.v3`, `modernc.org/sqlite` (pure Go), `pgregory.net/rapid` (property tests), htmx 2.0.4 (vendored), Playwright 1.62 (e2e, in Docker), golangci‑lint v2.12, Docker Compose, GitHub Actions.

**Spec:** `docs/requirements/` (FR‑x, QS‑x, C‑x) and `docs/architecture/` (arc42) + `docs/architecture/decisions/` (ADR‑0001…0013). Read `docs/architecture/05-building-block-view.md` and `08-crosscutting-concepts.md` before starting any task.

## Global Constraints

* Module path `github.com/gernotstarke/zorgscope`; `go 1.26` in `go.mod` (C‑1).
* All commands through `make` → Docker; never assume a local Go/Node (C‑2, ADR‑0009). Handy: `make go ARGS="test ./internal/domain/ -run TestX -v"` runs any go command in the Go container (target added in Task 1).
* Import rules (ADR‑0002, enforced by depguard): `internal/domain` → std lib only; `internal/ports` → domain; `internal/app` → domain, ports, config; adapters → domain, ports (+ their own client libs); `internal/server` → app, domain, ports, config, web; `cmd/*` → anything. Test files may import test helpers (`ports/memstore`, `adapters/clock`, `test/fakes/...`, `rapid`).
* No `time.Now()` outside `internal/adapters/clock` and `cmd/`; everything takes `now` or a `ports.Clock` (§8.10).
* Times stored as Unix seconds UTC; dates as `YYYY-MM-DD` strings in the configured timezone (§8.4).
* Secrets only from env; never log them (FR‑8.2, QS‑3.3).
* Every HTTP response sets the security headers of §8.6; all assets self‑hosted (C‑7).
* Coverage gates: `internal/domain` ≥ 90 % (`make test-domain`), overall ≥ 70 %.
* Commit after every task (small commits, message convention in `docs/plans/README.md`).

## File structure (created by this plan)

```text
go.mod, go.sum
.golangci.yml
.github/workflows/ci.yml
cmd/zorgscope/main.go                     wiring, graceful shutdown
cmd/fakesources/main.go                   fake GitHub server for e2e/demo
internal/domain/{item,buckets,snapshot,dismissal,attention,sort,fetchstatus}.go (+_test)
internal/ports/{ports,errors}.go
internal/ports/memstore/memstore.go       in-memory ports.Store (tests + fakes)
internal/ports/storetest/storetest.go     contract test suite for any ports.Store
internal/adapters/clock/clock.go          Real and Fake clock
internal/config/{config,load,validate}.go (+_test, testdata/)
internal/adapters/sqlite/{store,items,snapshots,dismissals,status,migrate}.go, migrations/0001_init.sql (+_test)
internal/app/{scheduler,snapshotter,dashboard,view,actions,registry,credentials}.go (+_test)
test/fakes/github/{server,seed}.go        fake GitHub HTTP handler + seed data (+_test)
internal/adapters/github/{client,repo,mentions,query.go}.go (+_test)
web/embed.go, web/templates/*.html, web/static/{tokens.css,app.css,htmx.min.js}
internal/server/{server,middleware,auth,handlers,render}.go (+_test, testdata/*.golden.html)
deploy/{Dockerfile,Dockerfile.e2e,compose.yml,compose.e2e.yml}
test/e2e/{package.json,package-lock.json,playwright.config.ts,tests/smoke.spec.ts,config.e2e.yaml}
```

---

### Task 1: Go module, health endpoints, Docker image, CI skeleton

**Files:**
- Create: `go.mod`, `internal/server/health.go`, `internal/server/health_test.go`, `cmd/zorgscope/main.go`, `.golangci.yml`, `deploy/Dockerfile`, `deploy/compose.yml`, `.github/workflows/ci.yml`
- Modify: `Makefile` (add `go` target, bump images)

**Interfaces:**
- Produces: `server.HealthHandler() http.Handler` (200 "ok"), `server.ReadyHandler(ready func() bool) http.Handler` (200 "ready" / 503 "not ready").

- [ ] **Step 1: Create the module and Makefile helper**

```sh
cat > go.mod <<'X'
module github.com/gernotstarke/zorgscope

go 1.26
X
```

Edit `Makefile`: set `GO_IMAGE ?= golang:1.26` and `LINT_IMAGE ?= golangci/golangci-lint:v2.12.0`, and add after the `tidy` target:

```make
.PHONY: go
go: ## Run any go command in the Go container: make go ARGS="test ./... -run TestX -v"
	$(GO_RUN) go $(ARGS)
```

- [ ] **Step 2: Write the failing test**

`internal/server/health_test.go`:

```go
package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthHandler(t *testing.T) {
	rr := httptest.NewRecorder()
	HealthHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "ok" {
		t.Fatalf("got %d %q, want 200 ok", rr.Code, rr.Body.String())
	}
}

func TestReadyHandler(t *testing.T) {
	ready := false
	h := ReadyHandler(func() bool { return ready })
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("not ready: got %d, want 503", rr.Code)
	}
	ready = true
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("ready: got %d, want 200", rr.Code)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `make go ARGS="test ./internal/server/ -v"`
Expected: FAIL — `undefined: HealthHandler`.

- [ ] **Step 4: Implement**

`internal/server/health.go`:

```go
// Package server contains the HTTP delivery layer of zorgscope.
package server

import "net/http"

// HealthHandler answers liveness probes. It never touches data.
func HealthHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
}

// ReadyHandler answers readiness probes: 200 when ready() is true, else 503.
func ReadyHandler(ready func() bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if !ready() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready"))
			return
		}
		_, _ = w.Write([]byte("ready"))
	})
}
```

`cmd/zorgscope/main.go` (temporary; replaced in Task 15):

```go
// Command zorgscope runs the dashboard server.
package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/gernotstarke/zorgscope/internal/server"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", server.HealthHandler())
	mux.Handle("GET /readyz", server.ReadyHandler(func() bool { return true }))
	slog.Info("zorgscope listening", "addr", ":"+port)
	if err := http.ListenAndServe(":"+port, mux); err != nil { //nolint:gosec // timeouts configured in Task 15
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `make go ARGS="test ./internal/server/ -v"`
Expected: PASS (2 tests).

- [ ] **Step 6: Lint config**

`.golangci.yml`:

```yaml
version: "2"
run:
  timeout: 5m
linters:
  default: standard
  enable:
    - depguard
    - gosec
    - misspell
    - revive
    - gocritic
  settings:
    depguard:
      rules:
        domain:
          files: ["**/internal/domain/**", "!**/*_test.go"]
          allow: ["$gostd", "github.com/gernotstarke/zorgscope/internal/domain"]
        ports:
          files: ["**/internal/ports/**", "!**/*_test.go"]
          allow: ["$gostd", "github.com/gernotstarke/zorgscope/internal/domain", "github.com/gernotstarke/zorgscope/internal/ports"]
        app:
          files: ["**/internal/app/**", "!**/*_test.go"]
          allow: ["$gostd", "github.com/gernotstarke/zorgscope/internal/domain", "github.com/gernotstarke/zorgscope/internal/ports", "github.com/gernotstarke/zorgscope/internal/config"]
        adapters:
          files: ["**/internal/adapters/**", "!**/*_test.go"]
          allow: ["$gostd", "github.com/gernotstarke/zorgscope/internal/domain", "github.com/gernotstarke/zorgscope/internal/ports", "modernc.org/sqlite"]
    revive:
      rules:
        - name: exported
          disabled: false
    gosec:
      excludes: ["G101", "G404"] # G101 misfires on constant *names*; math/rand is fine for jitter
  exclusions:
    rules:
      - path: _test\.go
        linters: [gosec]
formatters:
  enable: [gofmt, goimports]
```

Run: `make lint` — Expected: no issues.

- [ ] **Step 7: Dockerfile and compose**

`deploy/Dockerfile`:

```dockerfile
# syntax=docker/dockerfile:1
ARG GO_IMAGE=golang:1.26
FROM ${GO_IMAGE} AS build
ARG CMD=zorgscope
WORKDIR /src
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${CMD}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app/bin
COPY config /app/config
ENV ZORGSCOPE_CONFIG=/app/config/zorgscope.yaml ZORGSCOPE_DATA=/data/zorgscope.db PORT=8080
EXPOSE 8080
VOLUME ["/data"]
USER nonroot
ENTRYPOINT ["/app/bin"]
```

`deploy/compose.yml`:

```yaml
services:
  zorgscope:
    build:
      context: ..
      dockerfile: deploy/Dockerfile
    ports: ["${PORT:-8080}:8080"]
    env_file: [../.env]
    environment:
      ZORGSCOPE_CONFIG: /app/config/zorgscope.yaml
      ZORGSCOPE_DATA: /data/zorgscope.db
    volumes:
      - zorgscope-data:/data
      - ../config:/app/config:ro
    restart: unless-stopped
volumes:
  zorgscope-data:
```

Run: `make image && docker run --rm -p 8081:8080 zorgscope:local & sleep 3; curl -s localhost:8081/healthz; docker stop $(docker ps -q --filter ancestor=zorgscope:local)`
Expected: prints `ok`.

- [ ] **Step 8: CI skeleton**

`.github/workflows/ci.yml`:

```yaml
name: ci
on:
  push:
    branches: [main]
  pull_request:
jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: make lint
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: make test
      - run: make test-domain
  image:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: make image
```

- [ ] **Step 9: Commit**

```bash
git add go.mod internal/server cmd/zorgscope .golangci.yml deploy/Dockerfile deploy/compose.yml .github/workflows/ci.yml Makefile
git commit -m "chore: go module, health endpoints, Dockerfile, compose, CI skeleton (FR-10.1, FR-10.3)"
```

---

### Task 2: Domain – Item, Kind, ItemID, payloads, age buckets

**Files:**
- Create: `internal/domain/item.go`, `internal/domain/item_test.go`, `internal/domain/buckets.go`, `internal/domain/buckets_test.go`

**Interfaces:**
- Produces: `domain.Kind` constants, `domain.ItemID{SourceID, ExternalID}` with `String()`/`ParseItemID`, `domain.Item`, payload structs, `DecodePayload[T]`, `EncodePayload`, `MustPayload`, `domain.Bucket` + `BucketOf(t, now)`.

- [ ] **Step 1: Write the failing tests**

`internal/domain/item_test.go`:

```go
package domain

import (
	"testing"
	"time"
)

func TestItemIDRoundTrip(t *testing.T) {
	id := ItemID{SourceID: "github:arc42/arc42-template", ExternalID: "issues/236"}
	s := id.String()
	if s != "github:arc42/arc42-template|issues/236" {
		t.Fatalf("String() = %q", s)
	}
	back, err := ParseItemID(s)
	if err != nil || back != id {
		t.Fatalf("ParseItemID(%q) = %v, %v", s, back, err)
	}
	if _, err := ParseItemID("no-separator"); err == nil {
		t.Fatal("expected error for missing separator")
	}
}

func TestPayloadRoundTrip(t *testing.T) {
	it := Item{Kind: KindPR, Payload: MustPayload(PRPayload{Draft: true, ReviewDecision: "REVIEW_REQUIRED", Comments: 3})}
	p, err := DecodePayload[PRPayload](it)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Draft || p.ReviewDecision != "REVIEW_REQUIRED" || p.Comments != 3 {
		t.Fatalf("payload = %+v", p)
	}
	empty, err := DecodePayload[PRPayload](Item{})
	if err != nil || empty != (PRPayload{}) {
		t.Fatalf("empty payload should decode to zero value, got %+v, %v", empty, err)
	}
	if _, err := DecodePayload[PRPayload](Item{Payload: []byte("{not json")}); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestEncodePayloadError(t *testing.T) {
	if _, err := EncodePayload(make(chan int)); err == nil {
		t.Fatal("expected error for unmarshalable value")
	}
	_ = time.Now // keep import used in later edits
}
```

`internal/domain/buckets_test.go`:

```go
package domain

import (
	"testing"
	"time"
)

func TestBucketOf(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		age  time.Duration
		want Bucket
	}{
		{0, BucketLT24h},
		{-time.Hour, BucketLT24h}, // clock skew: created "in the future"
		{23 * time.Hour, BucketLT24h},
		{24 * time.Hour, BucketLT7d},
		{6*24*time.Hour + 23*time.Hour, BucketLT7d},
		{7 * 24 * time.Hour, BucketLT30d},
		{29 * 24 * time.Hour, BucketLT30d},
		{30 * 24 * time.Hour, BucketGE30d},
		{400 * 24 * time.Hour, BucketGE30d},
	}
	for _, c := range cases {
		if got := BucketOf(now.Add(-c.age), now); got != c.want {
			t.Errorf("age %v: got %v want %v", c.age, got, c.want)
		}
	}
	if BucketLT24h.String() != "lt24h" || BucketGE30d.Label() != "≥ 30 d" {
		t.Fatalf("String/Label mismatch: %q %q", BucketLT24h.String(), BucketGE30d.Label())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make go ARGS="test ./internal/domain/ -v"`
Expected: FAIL — undefined `ItemID`, `BucketOf`, …

- [ ] **Step 3: Implement**

`internal/domain/item.go`:

```go
// Package domain holds zorgscope's data model and business rules. It has no I/O
// and no dependency outside the standard library (ADR-0002).
package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Kind classifies an Item.
type Kind string

// Item kinds.
const (
	KindIssue        Kind = "issue"
	KindPR           Kind = "pr"
	KindWorkflowRun  Kind = "workflow_run"
	KindMention      Kind = "mention"
	KindTask         Kind = "task"
	KindArticle      Kind = "article"
	KindMetricSeries Kind = "metric_series"
	KindCredential   Kind = "credential"
	KindHealthCheck  Kind = "health_check"
)

// ItemID identifies an item globally: the source it came from plus the source's own id.
type ItemID struct {
	SourceID   string
	ExternalID string
}

const idSeparator = "|"

// String renders "sourceID|externalID". Source ids never contain '|'.
func (id ItemID) String() string { return id.SourceID + idSeparator + id.ExternalID }

// ParseItemID is the inverse of String.
func ParseItemID(s string) (ItemID, error) {
	src, ext, ok := strings.Cut(s, idSeparator)
	if !ok || src == "" || ext == "" {
		return ItemID{}, errors.New("domain: invalid item id " + s)
	}
	return ItemID{SourceID: src, ExternalID: ext}, nil
}

// Item is the normalised unit of information shown on tiles (arc42 §8.1).
type Item struct {
	ID             ItemID
	Kind           Kind
	Title          string
	URL            string
	Author         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastActivityBy string
	LastActivityAt time.Time
	Labels         []string
	Payload        json.RawMessage // kind-specific, see *Payload types
	FirstSeen      time.Time       // set by the store on first insert
}

// Payload types — one per Kind that carries extra data.
type (
	// IssuePayload holds issue extras.
	IssuePayload struct {
		Comments int `json:"comments"`
	}
	// PRPayload holds pull request extras.
	PRPayload struct {
		Draft          bool   `json:"draft"`
		ReviewDecision string `json:"review_decision"`
		Comments       int    `json:"comments"`
	}
	// WorkflowRunPayload describes the latest workflow run of a repo.
	WorkflowRunPayload struct {
		RunID        int64  `json:"run_id"`
		WorkflowName string `json:"workflow_name"`
		Conclusion   string `json:"conclusion"` // success | failure | cancelled | ... | "" while running
		Status       string `json:"status"`     // completed | in_progress | queued
		Branch       string `json:"branch"`
	}
	// MentionPayload describes a notification thread.
	MentionPayload struct {
		Reason      string `json:"reason"`
		Repo        string `json:"repo"`
		SubjectType string `json:"subject_type"`
	}
	// TaskPayload holds Todoist task extras.
	TaskPayload struct {
		Project    string    `json:"project"`
		Priority   int       `json:"priority"` // 1 (highest) .. 4
		Due        time.Time `json:"due"`
		DueHasTime bool      `json:"due_has_time"`
		Recurring  bool      `json:"recurring"`
	}
	// ArticlePayload holds feed article extras.
	ArticlePayload struct {
		Summary  string `json:"summary"`
		Topic    string `json:"topic"`
		FeedName string `json:"feed_name"`
	}
	// MetricPage is one top page of a site.
	MetricPage struct {
		Path     string `json:"path"`
		Visitors int    `json:"visitors"`
	}
	// MetricSeriesPayload holds Plausible numbers for one site.
	MetricSeriesPayload struct {
		Visitors7d       int          `json:"visitors_7d"`
		Visitors30d      int          `json:"visitors_30d"`
		Pageviews30d     int          `json:"pageviews_30d"`
		DeltaVisitors7d  float64      `json:"delta_visitors_7d"` // fraction, e.g. 0.12 = +12 %
		DeltaVisitors30d float64      `json:"delta_visitors_30d"`
		Daily            []int        `json:"daily"`
		TopPages         []MetricPage `json:"top_pages"`
	}
	// CredentialPayload describes a watched credential (FR-11.x).
	CredentialPayload struct {
		Expires      *time.Time `json:"expires,omitempty"`
		WarnDays     int        `json:"warn_days"`
		UsedBy       string     `json:"used_by"`
		URL          string     `json:"url"`
		AutoDetected bool       `json:"auto_detected"`
		AuthFailed   bool       `json:"auth_failed"` // synthetic "AUTH FAILED" item for a source
	}
	// HealthCheckPayload describes a watched URL.
	HealthCheckPayload struct {
		StatusCode          int        `json:"status_code"`
		LatencyMs           int64      `json:"latency_ms"`
		OK                  bool       `json:"ok"`
		ConsecutiveFailures int        `json:"consecutive_failures"`
		CertExpires         *time.Time `json:"cert_expires,omitempty"`
		LastOK              time.Time  `json:"last_ok"`
	}
)

// DecodePayload decodes an item's payload into T. An empty payload yields the zero value.
func DecodePayload[T any](it Item) (T, error) {
	var v T
	if len(it.Payload) == 0 {
		return v, nil
	}
	err := json.Unmarshal(it.Payload, &v)
	return v, err
}

// EncodePayload marshals a payload struct.
func EncodePayload(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// MustPayload is EncodePayload for tests and adapters with static structs; panics on error.
func MustPayload(v any) json.RawMessage {
	b, err := EncodePayload(v)
	if err != nil {
		panic(err)
	}
	return b
}
```

`internal/domain/buckets.go`:

```go
package domain

import "time"

// Bucket is the coarse age class of an item (FR-2.4).
type Bucket int

// Age buckets, youngest first.
const (
	BucketLT24h Bucket = iota
	BucketLT7d
	BucketLT30d
	BucketGE30d
)

var bucketNames = [...]string{"lt24h", "lt7d", "lt30d", "ge30d"}
var bucketLabels = [...]string{"< 24 h", "< 7 d", "< 30 d", "≥ 30 d"}

// String is the CSS-friendly name.
func (b Bucket) String() string { return bucketNames[b] }

// Label is the human-readable name.
func (b Bucket) Label() string { return bucketLabels[b] }

// BucketOf classifies the age of t relative to now. Future timestamps count as youngest.
func BucketOf(t, now time.Time) Bucket {
	age := now.Sub(t)
	switch {
	case age < 24*time.Hour:
		return BucketLT24h
	case age < 7*24*time.Hour:
		return BucketLT7d
	case age < 30*24*time.Hour:
		return BucketLT30d
	default:
		return BucketGE30d
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make go ARGS="test ./internal/domain/ -v"` — Expected: PASS. Then `make lint` — Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/domain
git commit -m "feat(domain): Item, ItemID, payloads and age buckets (FR-2.1, FR-2.4)"
```

---

### Task 3: Domain – Snapshot, SnapshotDay, Dismissal

**Files:**
- Create: `internal/domain/snapshot.go`, `internal/domain/snapshot_test.go`, `internal/domain/dismissal.go`, `internal/domain/dismissal_test.go`

**Interfaces:**
- Produces: `domain.Snapshot{SourceID, Date, TakenAt, IDs}`, `NewSnapshot(sourceID, date, takenAt, ids)`, `(Snapshot).Contains`, `(Snapshot).IDList`, `Diff(prev, cur)`, `DateOf(t, loc)`, `SnapshotDay(now, hour, minute, loc) string`, `domain.Dismissal{ID, UpdatedAt, DismissedAt}`, `(Dismissal).Covers(item)`.

- [ ] **Step 1: Write the failing tests**

`internal/domain/snapshot_test.go`:

```go
package domain

import (
	"reflect"
	"testing"
	"time"
)

func TestSnapshotContainsAndDiff(t *testing.T) {
	prev := NewSnapshot("s", "2026-08-15", time.Time{}, []string{"a", "b", "c"})
	cur := NewSnapshot("s", "2026-08-16", time.Time{}, []string{"b", "c", "d", "e"})
	if !prev.Contains("a") || prev.Contains("d") {
		t.Fatal("Contains wrong")
	}
	added, removed := Diff(prev, cur)
	if !reflect.DeepEqual(added, []string{"d", "e"}) || !reflect.DeepEqual(removed, []string{"a"}) {
		t.Fatalf("Diff = %v %v", added, removed)
	}
	if got := cur.IDList(); !reflect.DeepEqual(got, []string{"b", "c", "d", "e"}) {
		t.Fatalf("IDList = %v", got)
	}
}

func TestSnapshotDay(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	cases := []struct {
		now  string
		want string
	}{
		{"2026-08-16T02:59:00+02:00", "2026-08-15"}, // before 03:00 → still yesterday's snapshot day
		{"2026-08-16T03:00:00+02:00", "2026-08-16"},
		{"2026-08-16T23:30:00+02:00", "2026-08-16"},
		{"2026-08-16T00:30:00Z", "2026-08-15"}, // 02:30 Berlin
	}
	for _, c := range cases {
		now, _ := time.Parse(time.RFC3339, c.now)
		if got := SnapshotDay(now, 3, 0, berlin); got != c.want {
			t.Errorf("SnapshotDay(%s) = %s want %s", c.now, got, c.want)
		}
	}
	if DateOf(time.Date(2026, 8, 16, 23, 30, 0, 0, time.UTC), berlin) != "2026-08-17" {
		t.Fatal("DateOf must use the location")
	}
}
```

`internal/domain/dismissal_test.go`:

```go
package domain

import (
	"testing"
	"time"
)

func TestDismissalCovers(t *testing.T) {
	id := ItemID{SourceID: "s", ExternalID: "issues/1"}
	upd := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	d := Dismissal{ID: id, UpdatedAt: upd, DismissedAt: upd.Add(time.Hour)}
	if !d.Covers(Item{ID: id, UpdatedAt: upd}) {
		t.Fatal("same updated_at must be covered")
	}
	if !d.Covers(Item{ID: id, UpdatedAt: upd.In(time.FixedZone("x", 3600))}) {
		t.Fatal("comparison must be by instant, not by location")
	}
	if d.Covers(Item{ID: id, UpdatedAt: upd.Add(time.Second)}) {
		t.Fatal("changed item must not be covered (FR-2.7 AC3)")
	}
	if d.Covers(Item{ID: ItemID{SourceID: "s", ExternalID: "issues/2"}, UpdatedAt: upd}) {
		t.Fatal("other item must not be covered")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make go ARGS="test ./internal/domain/ -run 'Snapshot|Dismissal' -v"` — Expected: FAIL (undefined).

- [ ] **Step 3: Implement**

`internal/domain/snapshot.go`:

```go
package domain

import (
	"sort"
	"time"
)

// Snapshot is the set of external ids a source had on a given snapshot day (FR-7.1).
type Snapshot struct {
	SourceID string
	Date     string // YYYY-MM-DD, the snapshot day (see SnapshotDay)
	TakenAt  time.Time
	IDs      map[string]struct{}
}

// NewSnapshot builds a snapshot from a list of external ids.
func NewSnapshot(sourceID, date string, takenAt time.Time, ids []string) Snapshot {
	s := Snapshot{SourceID: sourceID, Date: date, TakenAt: takenAt, IDs: make(map[string]struct{}, len(ids))}
	for _, id := range ids {
		s.IDs[id] = struct{}{}
	}
	return s
}

// Contains reports whether the external id was present.
func (s Snapshot) Contains(externalID string) bool {
	_, ok := s.IDs[externalID]
	return ok
}

// IDList returns the ids sorted, for persistence and tests.
func (s Snapshot) IDList() []string {
	out := make([]string, 0, len(s.IDs))
	for id := range s.IDs {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Diff returns ids present in cur but not prev (added) and vice versa (removed), sorted.
func Diff(prev, cur Snapshot) (added, removed []string) {
	for id := range cur.IDs {
		if !prev.Contains(id) {
			added = append(added, id)
		}
	}
	for id := range prev.IDs {
		if !cur.Contains(id) {
			removed = append(removed, id)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// DateOf formats t as YYYY-MM-DD in loc.
func DateOf(t time.Time, loc *time.Location) string { return t.In(loc).Format("2006-01-02") }

// SnapshotDay is the date of the most recent scheduled snapshot time (hour:minute in loc) at or before now.
// Before today's snapshot time it is yesterday's date. New-detection compares against the snapshot
// *before* this day, which guarantees every item is highlighted for at least 24 h (ADR-0008).
func SnapshotDay(now time.Time, hour, minute int, loc *time.Location) string {
	t := now.In(loc)
	boundary := time.Date(t.Year(), t.Month(), t.Day(), hour, minute, 0, 0, loc)
	if t.Before(boundary) {
		boundary = boundary.AddDate(0, 0, -1)
	}
	return boundary.Format("2006-01-02")
}
```

`internal/domain/dismissal.go`:

```go
package domain

import "time"

// Dismissal is the user's "seen" for one item at one state (FR-2.7). It stops covering the item as
// soon as the item's UpdatedAt changes.
type Dismissal struct {
	ID          ItemID
	UpdatedAt   time.Time // the item's UpdatedAt at dismissal time
	DismissedAt time.Time
}

// Covers reports whether d suppresses highlights for it.
func (d Dismissal) Covers(it Item) bool {
	return d.ID == it.ID && d.UpdatedAt.Unix() == it.UpdatedAt.Unix()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make go ARGS="test ./internal/domain/ -v"` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/domain
git commit -m "feat(domain): snapshots, snapshot day rule and dismissals (FR-7.1, FR-7.2, FR-2.7)"
```

---

### Task 4: Domain – Attention rules (the heart of QG‑1)

**Files:**
- Create: `internal/domain/attention.go`, `internal/domain/attention_test.go`, `internal/domain/attention_prop_test.go`
- Modify: `go.mod` (adds `pgregory.net/rapid`)

**Interfaces:**
- Produces: `domain.Level` (+ `String()`, `Badge()`, `NeedsAttention()`), `domain.Rules{Grace, StaleAfter, Me, Bots, WarnDays}`, `DefaultRules()`, `(Rules).IsBot/IsNew/IsUnanswered/IsStale`, `domain.Evaluation{Level, Bucket, New, Unanswered, Stale, Dismissed}`, `(Rules).Evaluate(item, prev *Snapshot, dis *Dismissal, now) Evaluation`.

- [ ] **Step 1: Write the failing tests**

`internal/domain/attention_test.go`:

```go
package domain

import (
	"testing"
	"time"
)

var (
	now0  = time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	rules = DefaultRules()
)

func issue(ext, author string, created time.Time, lastBy string, lastAt time.Time) Item {
	return Item{ID: ItemID{SourceID: "github:o/r", ExternalID: ext}, Kind: KindIssue, Author: author,
		CreatedAt: created, UpdatedAt: lastAt, LastActivityBy: lastBy, LastActivityAt: lastAt}
}

func TestIsNew(t *testing.T) {
	prev := NewSnapshot("github:o/r", "2026-08-15", now0, []string{"issues/1"})
	old := issue("issues/1", "alice", now0.Add(-48*time.Hour), "", time.Time{})
	fresh := issue("issues/2", "bob", now0.Add(-time.Hour), "", time.Time{})
	if rules.IsNew(old, &prev, now0) {
		t.Fatal("item in previous snapshot is not new")
	}
	if !rules.IsNew(fresh, &prev, now0) {
		t.Fatal("item absent from previous snapshot is new")
	}
	// first run: no snapshot → only items younger than 24 h are new (FR-2.2 AC3)
	if rules.IsNew(old, nil, now0) || !rules.IsNew(fresh, nil, now0) {
		t.Fatal("first-run rule violated")
	}
	// snapshot of another source is ignored (treated as nil)
	other := NewSnapshot("github:x/y", "2026-08-15", now0, []string{"issues/1"})
	if !rules.IsNew(fresh, &other, now0) || rules.IsNew(old, &other, now0) {
		t.Fatal("foreign snapshot must behave like nil")
	}
}

func TestIsUnanswered(t *testing.T) {
	created := now0.Add(-10 * time.Hour)
	cases := []struct {
		name string
		it   Item
		want bool
	}{
		{"no comments, past grace", issue("i", "alice", created, "", time.Time{}), true},
		{"no comments, within grace", issue("i", "alice", now0.Add(-time.Hour), "", time.Time{}), false},
		{"last comment by opener", issue("i", "alice", created, "alice", now0.Add(-time.Hour)), true},
		{"last comment by opener, different case", issue("i", "Alice", created, "alice", now0), true},
		{"last comment by bot", issue("i", "alice", created, "github-actions[bot]", now0), true},
		{"last comment by dependabot", issue("i", "alice", created, "dependabot", now0), true},
		{"answered by maintainer", issue("i", "alice", created, "gernotstarke", now0), false},
		{"answered by anyone else", issue("i", "alice", created, "carol", now0), false},
	}
	for _, c := range cases {
		if got := rules.IsUnanswered(c.it, now0); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	task := Item{Kind: KindTask, CreatedAt: created}
	if rules.IsUnanswered(task, now0) {
		t.Fatal("only issues and PRs can be unanswered")
	}
}

func TestEvaluateIssueLevels(t *testing.T) {
	prev := NewSnapshot("github:o/r", "2026-08-15", now0, []string{"issues/old", "issues/stale", "issues/answered"})
	newUnanswered := issue("issues/new", "alice", now0.Add(-6*time.Hour), "", time.Time{})
	oldUnanswered := issue("issues/old", "alice", now0.Add(-3*24*time.Hour), "", time.Time{})
	stale := issue("issues/stale", "alice", now0.Add(-60*24*time.Hour), "gernotstarke", now0.Add(-45*24*time.Hour))
	answered := issue("issues/answered", "alice", now0.Add(-3*24*time.Hour), "gernotstarke", now0.Add(-2*24*time.Hour))

	if ev := rules.Evaluate(newUnanswered, &prev, nil, now0); ev.Level != LevelNew || !ev.New || !ev.Unanswered || ev.Bucket != BucketLT24h {
		t.Fatalf("new+unanswered → New, got %+v", ev)
	}
	if ev := rules.Evaluate(oldUnanswered, &prev, nil, now0); ev.Level != LevelUnanswered || ev.Bucket != BucketLT7d {
		t.Fatalf("old unanswered → Unanswered, got %+v", ev)
	}
	if ev := rules.Evaluate(stale, &prev, nil, now0); ev.Level != LevelStale || !ev.Stale {
		t.Fatalf("stale → Stale, got %+v", ev)
	}
	if ev := rules.Evaluate(answered, &prev, nil, now0); ev.Level != LevelAged {
		t.Fatalf("answered → Aged, got %+v", ev)
	}
	// dismissal covers → None but flags still computed
	dis := &Dismissal{ID: oldUnanswered.ID, UpdatedAt: oldUnanswered.UpdatedAt}
	if ev := rules.Evaluate(oldUnanswered, &prev, dis, now0); ev.Level != LevelNone || !ev.Dismissed || !ev.Unanswered {
		t.Fatalf("dismissed → None, got %+v", ev)
	}
	// dismissal for another state does not cover
	stale2 := &Dismissal{ID: oldUnanswered.ID, UpdatedAt: oldUnanswered.UpdatedAt.Add(-time.Minute)}
	if ev := rules.Evaluate(oldUnanswered, &prev, stale2, now0); ev.Level != LevelUnanswered {
		t.Fatalf("outdated dismissal must not cover, got %+v", ev)
	}
}

func TestEvaluateOtherKinds(t *testing.T) {
	failed := Item{ID: ItemID{"github:o/r", "runs/1"}, Kind: KindWorkflowRun, CreatedAt: now0, UpdatedAt: now0,
		Payload: MustPayload(WorkflowRunPayload{Conclusion: "failure", Status: "completed"})}
	if ev := rules.Evaluate(failed, nil, nil, now0); ev.Level != LevelBuildFailed {
		t.Fatalf("failed run → BuildFailed, got %+v", ev)
	}
	ok := failed
	ok.Payload = MustPayload(WorkflowRunPayload{Conclusion: "success", Status: "completed"})
	if ev := rules.Evaluate(ok, nil, nil, now0); ev.Level != LevelNone {
		t.Fatalf("green run → None, got %+v", ev)
	}
	exp := now0.Add(10 * 24 * time.Hour)
	cred := Item{ID: ItemID{"watch:credentials", "c1"}, Kind: KindCredential, CreatedAt: now0, UpdatedAt: exp,
		Payload: MustPayload(CredentialPayload{Expires: &exp})}
	if ev := rules.Evaluate(cred, nil, nil, now0); ev.Level != LevelExpiring {
		t.Fatalf("expires in 10 d (warn 14) → Expiring, got %+v", ev)
	}
	past := now0.Add(-time.Hour)
	cred.Payload = MustPayload(CredentialPayload{Expires: &past})
	if ev := rules.Evaluate(cred, nil, nil, now0); ev.Level != LevelExpired {
		t.Fatalf("past → Expired, got %+v", ev)
	}
	cred.Payload = MustPayload(CredentialPayload{AuthFailed: true})
	if ev := rules.Evaluate(cred, nil, nil, now0); ev.Level != LevelAuthFailed {
		t.Fatalf("auth failed → AuthFailed, got %+v", ev)
	}
	down := Item{ID: ItemID{"watch:url:x", "x"}, Kind: KindHealthCheck, CreatedAt: now0,
		Payload: MustPayload(HealthCheckPayload{OK: false, ConsecutiveFailures: 2})}
	if ev := rules.Evaluate(down, nil, nil, now0); ev.Level != LevelDown {
		t.Fatalf("2 failures → Down, got %+v", ev)
	}
	down.Payload = MustPayload(HealthCheckPayload{OK: false, ConsecutiveFailures: 1})
	if ev := rules.Evaluate(down, nil, nil, now0); ev.Level != LevelNone {
		t.Fatalf("1 failure → None, got %+v", ev)
	}
	mention := Item{ID: ItemID{"github:mentions", "t1"}, Kind: KindMention, CreatedAt: now0.Add(-time.Hour)}
	if ev := rules.Evaluate(mention, nil, nil, now0); ev.Level != LevelNew {
		t.Fatalf("fresh mention → New, got %+v", ev)
	}
	task := Item{ID: ItemID{"todoist", "t"}, Kind: KindTask, CreatedAt: now0}
	if ev := rules.Evaluate(task, nil, nil, now0); ev.Level != LevelNone {
		t.Fatalf("task → None, got %+v", ev)
	}
}

func TestLevelMeta(t *testing.T) {
	if !LevelNew.NeedsAttention() || !LevelExpiring.NeedsAttention() || LevelStale.NeedsAttention() || LevelAged.NeedsAttention() {
		t.Fatal("NeedsAttention thresholds wrong")
	}
	if LevelBuildFailed.Badge() != "BUILD FAILED" || LevelNone.Badge() != "" || LevelNew.String() != "new" {
		t.Fatal("badge/string wrong")
	}
	if LevelAuthFailed <= LevelDown || LevelDown <= LevelExpired || LevelExpired <= LevelBuildFailed ||
		LevelBuildFailed <= LevelNew || LevelNew <= LevelUnanswered || LevelUnanswered <= LevelExpiring {
		t.Fatal("severity order wrong (used for sorting)")
	}
}
```

`internal/domain/attention_prop_test.go`:

```go
package domain

import (
	"testing"
	"time"

	"pgregory.net/rapid"
)

// QS-1.2: for any two snapshots, IsNew is true exactly for ids absent from the previous snapshot,
// and Diff agrees with IsNew.
func TestPropNewMatchesSnapshotDiff(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		idGen := rapid.SliceOfDistinct(rapid.StringMatching(`issues/[0-9]{1,3}`), func(s string) string { return s })
		prevIDs := idGen.Draw(t, "prev")
		curIDs := idGen.Draw(t, "cur")
		prev := NewSnapshot("s", "2026-08-15", now0, prevIDs)
		cur := NewSnapshot("s", "2026-08-16", now0, curIDs)
		added, _ := Diff(prev, cur)
		addedSet := map[string]bool{}
		for _, a := range added {
			addedSet[a] = true
		}
		r := DefaultRules()
		for _, id := range curIDs {
			it := Item{ID: ItemID{SourceID: "s", ExternalID: id}, Kind: KindIssue, CreatedAt: now0.Add(-100 * 24 * time.Hour)}
			if r.IsNew(it, &prev, now0) != addedSet[id] {
				t.Fatalf("IsNew(%s)=%v but Diff says %v", id, !addedSet[id], addedSet[id])
			}
		}
	})
}

// FR-2.7 AC3 / QS-1.6: a dismissal never covers an item whose UpdatedAt differs.
func TestPropDismissalNeverHidesChange(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		base := now0.Add(time.Duration(rapid.IntRange(-1_000_000, 1_000_000).Draw(t, "upd")) * time.Second)
		delta := time.Duration(rapid.IntRange(1, 1_000_000).Draw(t, "delta")) * time.Second
		id := ItemID{SourceID: "s", ExternalID: "x"}
		d := Dismissal{ID: id, UpdatedAt: base}
		if d.Covers(Item{ID: id, UpdatedAt: base.Add(delta)}) || d.Covers(Item{ID: id, UpdatedAt: base.Add(-delta)}) {
			t.Fatal("dismissal covered a changed item")
		}
		if !d.Covers(Item{ID: id, UpdatedAt: base}) {
			t.Fatal("dismissal must cover the unchanged item")
		}
	})
}
```

- [ ] **Step 2: Add the dependency and run tests to verify they fail**

Run: `make go ARGS="get pgregory.net/rapid@v1.3.0"` then `make go ARGS="test ./internal/domain/ -run 'Is|Evaluate|Level|Prop' -v"`
Expected: FAIL (undefined `DefaultRules`, …).

- [ ] **Step 3: Implement**

`internal/domain/attention.go`:

```go
package domain

import (
	"strings"
	"time"
)

// Level says how urgently an item needs the user (arc42 §8.2). Higher = more urgent; the order is
// used for sorting.
type Level int

// Attention levels.
const (
	LevelNone Level = iota
	LevelAged
	LevelStale
	LevelExpiring
	LevelUnanswered
	LevelNew
	LevelBuildFailed
	LevelExpired
	LevelDown
	LevelAuthFailed
)

var levelNames = [...]string{"none", "aged", "stale", "expiring", "unanswered", "new", "build_failed", "expired", "down", "auth_failed"}
var levelBadges = [...]string{"", "", "STALE", "EXPIRING", "UNANSWERED", "NEW", "BUILD FAILED", "EXPIRED", "DOWN", "AUTH FAILED"}

// String is the CSS-friendly name.
func (l Level) String() string { return levelNames[l] }

// Badge is the text shown on the tile ("" for none/aged).
func (l Level) Badge() string { return levelBadges[l] }

// NeedsAttention reports whether the level puts the item into the Attention tile.
func (l Level) NeedsAttention() bool { return l >= LevelExpiring }

// Rules are the configurable parameters of attention detection.
type Rules struct {
	Grace      time.Duration // an issue/PR younger than this is not yet "unanswered" (FR-2.3)
	StaleAfter time.Duration // no activity for this long → stale (FR-2.4)
	Me         string        // the owner's GitHub login
	Bots       []string      // substrings identifying bot accounts, e.g. "[bot]"
	WarnDays   int           // default warning horizon for expiries (FR-11.1)
}

// DefaultRules returns the documented defaults (config may override).
func DefaultRules() Rules {
	return Rules{Grace: 4 * time.Hour, StaleAfter: 30 * 24 * time.Hour, Bots: []string{"[bot]", "dependabot", "renovate"}, WarnDays: 14}
}

// Evaluation is the result of Evaluate.
type Evaluation struct {
	Level      Level
	Bucket     Bucket
	New        bool
	Unanswered bool
	Stale      bool
	Dismissed  bool
}

// IsBot reports whether login looks like a bot account.
func (r Rules) IsBot(login string) bool {
	l := strings.ToLower(login)
	for _, b := range r.Bots {
		if strings.Contains(l, strings.ToLower(b)) {
			return true
		}
	}
	return false
}

// IsNew implements FR-2.2/FR-7.2: absent from the previous snapshot of the same source; without a
// usable previous snapshot, created within the last 24 h.
func (r Rules) IsNew(it Item, prev *Snapshot, now time.Time) bool {
	if prev == nil || prev.SourceID != it.ID.SourceID {
		return now.Sub(it.CreatedAt) < 24*time.Hour
	}
	return !prev.Contains(it.ID.ExternalID)
}

// IsUnanswered implements FR-2.3: open issue/PR past the grace period whose last activity is by nobody,
// by the opener, or by a bot.
func (r Rules) IsUnanswered(it Item, now time.Time) bool {
	if it.Kind != KindIssue && it.Kind != KindPR {
		return false
	}
	if now.Sub(it.CreatedAt) < r.Grace {
		return false
	}
	by := it.LastActivityBy
	return by == "" || strings.EqualFold(by, it.Author) || r.IsBot(by)
}

// IsStale implements FR-2.4 AC2 for issues/PRs.
func (r Rules) IsStale(it Item, now time.Time) bool {
	if it.Kind != KindIssue && it.Kind != KindPR {
		return false
	}
	last := it.LastActivityAt
	if last.IsZero() {
		last = it.CreatedAt
	}
	return now.Sub(last) >= r.StaleAfter
}

func (r Rules) warnHorizon(days int) time.Duration {
	if days <= 0 {
		days = r.WarnDays
	}
	return time.Duration(days) * 24 * time.Hour
}

// Evaluate computes the attention level of an item (arc42 §8.2). prev may be nil; dis may be nil.
func (r Rules) Evaluate(it Item, prev *Snapshot, dis *Dismissal, now time.Time) Evaluation {
	ev := Evaluation{Bucket: BucketOf(it.CreatedAt, now)}
	ev.Dismissed = dis != nil && dis.Covers(it)
	raise := func(l Level) { // dismissal suppresses attention levels, never informational ones
		if !ev.Dismissed && l > ev.Level {
			ev.Level = l
		}
	}
	switch it.Kind {
	case KindWorkflowRun:
		p, _ := DecodePayload[WorkflowRunPayload](it)
		if p.Conclusion == "failure" {
			raise(LevelBuildFailed)
		}
	case KindCredential:
		p, _ := DecodePayload[CredentialPayload](it)
		switch {
		case p.AuthFailed:
			raise(LevelAuthFailed)
		case p.Expires != nil && p.Expires.Before(now):
			raise(LevelExpired)
		case p.Expires != nil && p.Expires.Sub(now) <= r.warnHorizon(p.WarnDays):
			raise(LevelExpiring)
		}
	case KindHealthCheck:
		p, _ := DecodePayload[HealthCheckPayload](it)
		if !p.OK && p.ConsecutiveFailures >= 2 {
			raise(LevelDown)
		} else if p.CertExpires != nil && p.CertExpires.Sub(now) <= r.warnHorizon(0) {
			raise(LevelExpiring)
		}
	case KindMention, KindArticle:
		ev.New = r.IsNew(it, prev, now)
		if ev.New {
			raise(LevelNew)
		}
	case KindIssue, KindPR:
		ev.New = r.IsNew(it, prev, now)
		ev.Unanswered = r.IsUnanswered(it, now)
		ev.Stale = r.IsStale(it, now)
		switch {
		case ev.New:
			raise(LevelNew)
		case ev.Unanswered:
			raise(LevelUnanswered)
		case ev.Stale:
			ev.Level = LevelStale
		default:
			ev.Level = LevelAged
		}
		if ev.Dismissed {
			ev.Level = LevelNone
		}
	case KindTask, KindMetricSeries:
		// informational only
	}
	return ev
}
```

- [ ] **Step 4: Run tests, coverage and lint**

Run: `make go ARGS="test ./internal/domain/ -v"` — Expected: PASS incl. both property tests.
Run: `make test-domain` — Expected: coverage ≥ 90 %.
Run: `make lint` — Expected: clean (rapid only imported from `_test.go`).

- [ ] **Step 5: Commit**

```bash
git add internal/domain go.mod go.sum
git commit -m "feat(domain): attention rules with property tests (FR-2.2, FR-2.3, FR-2.4, FR-11.x, QS-1.2)"
```

---

### Task 5: Domain – sorting/capping and FetchStatus

**Files:**
- Create: `internal/domain/sort.go`, `internal/domain/sort_test.go`, `internal/domain/fetchstatus.go`, `internal/domain/fetchstatus_test.go`

**Interfaces:**
- Produces: `domain.Evaluated{Item, Eval}`, `SortByUrgency([]Evaluated)`, `FilterAttention([]Evaluated) []Evaluated`, `Cap([]Evaluated, n) (shown []Evaluated, overflow int)`, `domain.FetchStatus{...}` with `Healthy()`, `DataAge(now)`.

- [ ] **Step 1: Write the failing tests**

`internal/domain/sort_test.go`:

```go
package domain

import (
	"testing"
	"time"
)

func ev(ext string, lvl Level, created time.Time) Evaluated {
	return Evaluated{Item: Item{ID: ItemID{"s", ext}, CreatedAt: created}, Eval: Evaluation{Level: lvl}}
}

func TestSortFilterCap(t *testing.T) {
	list := []Evaluated{
		ev("aged", LevelAged, now0),
		ev("unanswered-old", LevelUnanswered, now0.Add(-48*time.Hour)),
		ev("new-old", LevelNew, now0.Add(-2*time.Hour)),
		ev("new-fresh", LevelNew, now0.Add(-time.Hour)),
		ev("auth", LevelAuthFailed, now0.Add(-72*time.Hour)),
		ev("stale", LevelStale, now0.Add(-900*time.Hour)),
	}
	SortByUrgency(list)
	want := []string{"auth", "new-fresh", "new-old", "unanswered-old", "stale", "aged"}
	for i, w := range want {
		if list[i].Item.ID.ExternalID != w {
			t.Fatalf("pos %d: got %s want %s", i, list[i].Item.ID.ExternalID, w)
		}
	}
	att := FilterAttention(list)
	if len(att) != 4 {
		t.Fatalf("FilterAttention: got %d want 4 (auth, 2 new, unanswered)", len(att))
	}
	shown, overflow := Cap(att, 3)
	if len(shown) != 3 || overflow != 1 {
		t.Fatalf("Cap: %d shown, %d overflow", len(shown), overflow)
	}
	shown, overflow = Cap(att, 0)
	if len(shown) != 4 || overflow != 0 {
		t.Fatal("Cap(0) means unlimited")
	}
}
```

`internal/domain/fetchstatus_test.go`:

```go
package domain

import (
	"testing"
	"time"
)

func TestFetchStatus(t *testing.T) {
	s := FetchStatus{SourceID: "s"}
	if !s.Healthy() || s.DataAge(now0) != 0 {
		t.Fatal("never fetched: Healthy() (unknown counts as healthy) and zero DataAge")
	}
	s.LastSuccess = now0.Add(-10 * time.Minute)
	if !s.Healthy() || s.DataAge(now0) != 10*time.Minute {
		t.Fatalf("healthy after success: %+v", s)
	}
	s.LastError = now0.Add(-time.Minute)
	s.ErrorMsg = "boom"
	if s.Healthy() {
		t.Fatal("error after last success → unhealthy")
	}
	s.LastSuccess = now0
	if !s.Healthy() {
		t.Fatal("success after error → healthy again")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make go ARGS="test ./internal/domain/ -run 'Sort|FetchStatus' -v"` — Expected: FAIL.

- [ ] **Step 3: Implement**

`internal/domain/sort.go`:

```go
package domain

import "sort"

// Evaluated pairs an item with its evaluation.
type Evaluated struct {
	Item Item
	Eval Evaluation
}

// SortByUrgency orders by level (most urgent first), then newest first (FR-2.5).
func SortByUrgency(list []Evaluated) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Eval.Level != list[j].Eval.Level {
			return list[i].Eval.Level > list[j].Eval.Level
		}
		return list[i].Item.CreatedAt.After(list[j].Item.CreatedAt)
	})
}

// FilterAttention keeps items whose level needs attention.
func FilterAttention(list []Evaluated) []Evaluated {
	out := make([]Evaluated, 0, len(list))
	for _, e := range list {
		if e.Eval.Level.NeedsAttention() {
			out = append(out, e)
		}
	}
	return out
}

// Cap limits the list to n entries (n <= 0: unlimited) and reports how many were cut (FR-2.5 AC4).
func Cap(list []Evaluated, n int) (shown []Evaluated, overflow int) {
	if n <= 0 || len(list) <= n {
		return list, 0
	}
	return list[:n], len(list) - n
}
```

`internal/domain/fetchstatus.go`:

```go
package domain

import "time"

// FetchStatus is the health record of one source (arc42 §8.1, FR-10.1).
type FetchStatus struct {
	SourceID    string
	Kind        string // source kind, e.g. "github-repo"
	LastSuccess time.Time
	LastError   time.Time
	ErrorMsg    string
	NextRun     time.Time
	ItemCount   int
	Duration    time.Duration
	InFlight    bool
	AuthFailed  bool // last error was an authentication failure (FR-11.3)
}

// Healthy is true when the last fetch succeeded (or nothing failed yet). A success in the same second
// as an earlier error counts as healthy.
func (s FetchStatus) Healthy() bool {
	return s.LastError.IsZero() || !s.LastSuccess.Before(s.LastError)
}

// DataAge is how old the last good data is; zero when never fetched.
func (s FetchStatus) DataAge(now time.Time) time.Duration {
	if s.LastSuccess.IsZero() {
		return 0
	}
	return now.Sub(s.LastSuccess)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make go ARGS="test ./internal/domain/ -v"` and `make test-domain` — Expected: PASS, ≥ 90 %.

- [ ] **Step 5: Commit**

```bash
git add internal/domain
git commit -m "feat(domain): urgency sorting, capping and fetch status (FR-2.5, FR-10.1)"
```

---

### Task 6: Ports, typed errors, in‑memory store, store contract tests, clock

**Files:**
- Create: `internal/ports/ports.go`, `internal/ports/errors.go`, `internal/ports/errors_test.go`, `internal/ports/memstore/memstore.go`, `internal/ports/memstore/memstore_test.go`, `internal/ports/storetest/storetest.go`, `internal/adapters/clock/clock.go`, `internal/adapters/clock/clock_test.go`

**Interfaces:**
- Produces (used by every later task):

```go
// ports
type SourceFetcher interface { ID() string; Kind() string; Fetch(ctx context.Context) ([]domain.Item, error) }
const ( KindGitHubRepo = "github-repo"; KindGitHubMentions = "github-mentions"; KindPlausibleSite = "plausible-site"; KindTodoist = "todoist"; KindFeed = "feed"; KindWatchCredentials = "watch-credentials"; KindWatchURL = "watch-url" )
type ItemStore interface {
    ReplaceItems(ctx context.Context, sourceID string, items []domain.Item, now time.Time) error
    Items(ctx context.Context, sourceID string) ([]domain.Item, error)          // newest first
    AllItems(ctx context.Context) ([]domain.Item, error)
    ExternalIDs(ctx context.Context, sourceID string) ([]string, error)
}
type SnapshotStore interface {
    PutSnapshot(ctx context.Context, s domain.Snapshot) error                   // upsert by (source, date)
    LatestSnapshot(ctx context.Context, sourceID string) (*domain.Snapshot, error)      // nil, nil if none
    SnapshotBefore(ctx context.Context, sourceID, date string) (*domain.Snapshot, error) // latest with Date < date
    PruneSnapshots(ctx context.Context, beforeDate string) error
}
type DismissalStore interface {
    PutDismissal(ctx context.Context, d domain.Dismissal) error
    Dismissal(ctx context.Context, id domain.ItemID) (*domain.Dismissal, error)
    Dismissals(ctx context.Context) (map[domain.ItemID]domain.Dismissal, error)
}
type StatusStore interface {
    RecordStatus(ctx context.Context, s domain.FetchStatus) error
    Status(ctx context.Context, sourceID string) (*domain.FetchStatus, error)
    Statuses(ctx context.Context) ([]domain.FetchStatus, error)              // sorted by SourceID
}
type Store interface { ItemStore; SnapshotStore; DismissalStore; StatusStore }
type Clock interface { Now() time.Time }
type CredentialSink interface { ReportCredential(name string, expires *time.Time, usedBy string) }
type Notifier interface { Notify(ctx context.Context, title, body string) error }
// errors
var ErrAuth, ErrTransient, ErrPermanent error
type RateLimitedError struct{ ResetAt time.Time }   // implements error
func AsRateLimited(err error) (*RateLimitedError, bool)
// memstore.New() *memstore.Store implements ports.Store
// storetest.Run(t, func(t *testing.T) ports.Store)
// clock.Real{} ; clock.NewFake(t time.Time) *clock.Fake with Now(), Set(t), Advance(d)
```

- [ ] **Step 1: Write the failing tests**

`internal/ports/errors_test.go`:

```go
package ports

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestErrors(t *testing.T) {
	wrapped := fmt.Errorf("github: %w", ErrAuth)
	if !errors.Is(wrapped, ErrAuth) {
		t.Fatal("ErrAuth must survive wrapping")
	}
	rl := fmt.Errorf("github: %w", &RateLimitedError{ResetAt: time.Unix(100, 0)})
	got, ok := AsRateLimited(rl)
	if !ok || got.ResetAt.Unix() != 100 {
		t.Fatalf("AsRateLimited: %v %v", got, ok)
	}
	if _, ok := AsRateLimited(ErrTransient); ok {
		t.Fatal("transient is not rate limited")
	}
	if rl.Error() == "" {
		t.Fatal("message")
	}
}
```

`internal/ports/storetest/storetest.go` — the contract every store must fulfil (used by memstore now, sqlite in Task 8):

```go
// Package storetest holds the behavioural contract test for ports.Store implementations.
package storetest

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

var t0 = time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

func item(src, ext string, created time.Time) domain.Item {
	return domain.Item{ID: domain.ItemID{SourceID: src, ExternalID: ext}, Kind: domain.KindIssue, Title: "T " + ext,
		URL: "https://x/" + ext, Author: "alice", CreatedAt: created, UpdatedAt: created, Labels: []string{"bug"},
		Payload: domain.MustPayload(domain.IssuePayload{Comments: 1})}
}

// Run executes the contract against a fresh store per subtest.
func Run(t *testing.T, open func(t *testing.T) ports.Store) {
	t.Helper()
	ctx := context.Background()

	t.Run("items replace keeps first_seen and removes absent", func(t *testing.T) {
		s := open(t)
		a := item("s1", "a", t0.Add(-time.Hour))
		b := item("s1", "b", t0)
		if err := s.ReplaceItems(ctx, "s1", []domain.Item{a, b}, t0); err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceItems(ctx, "s2", []domain.Item{item("s2", "z", t0)}, t0); err != nil {
			t.Fatal(err)
		}
		got, err := s.Items(ctx, "s1")
		if err != nil || len(got) != 2 {
			t.Fatalf("Items: %v %d", err, len(got))
		}
		if got[0].ID.ExternalID != "b" {
			t.Fatalf("newest first, got %s", got[0].ID.ExternalID)
		}
		if !got[0].FirstSeen.Equal(t0) || got[1].Title != "T a" || got[1].Labels[0] != "bug" || len(got[1].Payload) == 0 {
			t.Fatalf("round trip lost data: %+v", got[1])
		}
		// second replace: b updated, a gone, c new
		b2 := b
		b2.UpdatedAt = t0.Add(time.Minute)
		if err := s.ReplaceItems(ctx, "s1", []domain.Item{b2, item("s1", "c", t0)}, t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		got, _ = s.Items(ctx, "s1")
		if len(got) != 2 {
			t.Fatalf("want 2 items, got %d", len(got))
		}
		for _, it := range got {
			switch it.ID.ExternalID {
			case "b":
				if !it.FirstSeen.Equal(t0) || !it.UpdatedAt.Equal(t0.Add(time.Minute)) {
					t.Fatalf("b must keep first_seen and take new updated_at: %+v", it)
				}
			case "c":
				if !it.FirstSeen.Equal(t0.Add(time.Hour)) {
					t.Fatalf("c first_seen = %v", it.FirstSeen)
				}
			default:
				t.Fatalf("unexpected %s", it.ID.ExternalID)
			}
		}
		ids, _ := s.ExternalIDs(ctx, "s1")
		if !reflect.DeepEqual(ids, []string{"b", "c"}) {
			t.Fatalf("ExternalIDs = %v", ids)
		}
		all, _ := s.AllItems(ctx)
		if len(all) != 3 {
			t.Fatalf("AllItems = %d", len(all))
		}
		if err := s.ReplaceItems(ctx, "s2", nil, t0); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Items(ctx, "s2"); len(got) != 0 {
			t.Fatal("empty replace clears source")
		}
	})

	t.Run("snapshots", func(t *testing.T) {
		s := open(t)
		if got, err := s.LatestSnapshot(ctx, "s1"); err != nil || got != nil {
			t.Fatalf("no snapshot yet: %v %v", got, err)
		}
		for _, d := range []string{"2026-08-14", "2026-08-16", "2026-08-15"} {
			if err := s.PutSnapshot(ctx, domain.NewSnapshot("s1", d, t0, []string{"x-" + d})); err != nil {
				t.Fatal(err)
			}
		}
		_ = s.PutSnapshot(ctx, domain.NewSnapshot("s2", "2026-08-16", t0, []string{"other"}))
		latest, _ := s.LatestSnapshot(ctx, "s1")
		if latest == nil || latest.Date != "2026-08-16" || !latest.Contains("x-2026-08-16") {
			t.Fatalf("latest = %+v", latest)
		}
		before, _ := s.SnapshotBefore(ctx, "s1", "2026-08-16")
		if before == nil || before.Date != "2026-08-15" {
			t.Fatalf("before = %+v", before)
		}
		if got, _ := s.SnapshotBefore(ctx, "s1", "2026-08-14"); got != nil {
			t.Fatal("nothing before the first")
		}
		// upsert same date replaces ids
		_ = s.PutSnapshot(ctx, domain.NewSnapshot("s1", "2026-08-16", t0.Add(time.Hour), []string{"y"}))
		latest, _ = s.LatestSnapshot(ctx, "s1")
		if !latest.Contains("y") || latest.Contains("x-2026-08-16") {
			t.Fatal("upsert must replace ids")
		}
		if err := s.PruneSnapshots(ctx, "2026-08-16"); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.SnapshotBefore(ctx, "s1", "2026-08-16"); got != nil {
			t.Fatal("pruned snapshots must be gone")
		}
		if got, _ := s.LatestSnapshot(ctx, "s2"); got == nil {
			t.Fatal("prune must not touch dates >= cutoff")
		}
	})

	t.Run("dismissals", func(t *testing.T) {
		s := open(t)
		id := domain.ItemID{SourceID: "s1", ExternalID: "issues/1"}
		if got, err := s.Dismissal(ctx, id); err != nil || got != nil {
			t.Fatalf("none yet: %v %v", got, err)
		}
		d := domain.Dismissal{ID: id, UpdatedAt: t0, DismissedAt: t0.Add(time.Minute)}
		if err := s.PutDismissal(ctx, d); err != nil {
			t.Fatal(err)
		}
		got, _ := s.Dismissal(ctx, id)
		if got == nil || !got.UpdatedAt.Equal(t0) {
			t.Fatalf("got %+v", got)
		}
		d.UpdatedAt = t0.Add(time.Hour)
		_ = s.PutDismissal(ctx, d) // upsert
		all, _ := s.Dismissals(ctx)
		if len(all) != 1 || !all[id].UpdatedAt.Equal(t0.Add(time.Hour)) {
			t.Fatalf("Dismissals = %+v", all)
		}
	})

	t.Run("statuses", func(t *testing.T) {
		s := open(t)
		if got, err := s.Status(ctx, "s1"); err != nil || got != nil {
			t.Fatalf("none yet: %v %v", got, err)
		}
		st := domain.FetchStatus{SourceID: "s1", Kind: "github-repo", LastSuccess: t0, ItemCount: 3, Duration: 2 * time.Second, NextRun: t0.Add(10 * time.Minute)}
		if err := s.RecordStatus(ctx, st); err != nil {
			t.Fatal(err)
		}
		st2 := st
		st2.SourceID = "s0"
		st2.LastError = t0
		st2.ErrorMsg = "boom"
		st2.AuthFailed = true
		_ = s.RecordStatus(ctx, st2)
		got, _ := s.Status(ctx, "s1")
		if got == nil || got.ItemCount != 3 || !got.LastSuccess.Equal(t0) || got.Duration != 2*time.Second {
			t.Fatalf("got %+v", got)
		}
		all, _ := s.Statuses(ctx)
		if len(all) != 2 || all[0].SourceID != "s0" || !all[0].AuthFailed || all[0].ErrorMsg != "boom" {
			t.Fatalf("Statuses = %+v", all)
		}
	})
}
```

`internal/ports/memstore/memstore_test.go`:

```go
package memstore

import (
	"testing"

	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/ports/storetest"
)

func TestContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) ports.Store { return New() })
}
```

`internal/adapters/clock/clock_test.go`:

```go
package clock

import (
	"testing"
	"time"
)

func TestFake(t *testing.T) {
	t0 := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	f := NewFake(t0)
	if !f.Now().Equal(t0) {
		t.Fatal("initial")
	}
	f.Advance(time.Hour)
	if !f.Now().Equal(t0.Add(time.Hour)) {
		t.Fatal("advance")
	}
	f.Set(t0)
	if !f.Now().Equal(t0) {
		t.Fatal("set")
	}
	if (Real{}).Now().IsZero() {
		t.Fatal("real clock")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make go ARGS="test ./internal/ports/... ./internal/adapters/clock/ -v"` — Expected: FAIL / build errors.

- [ ] **Step 3: Implement**

`internal/ports/ports.go`:

```go
// Package ports defines the interfaces between zorgscope's application core and the outside world
// (arc42 §5). Adapters implement them; the app consumes them.
package ports

import (
	"context"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// Source kinds (config section names / registry keys).
const (
	KindGitHubRepo       = "github-repo"
	KindGitHubMentions   = "github-mentions"
	KindPlausibleSite    = "plausible-site"
	KindTodoist          = "todoist"
	KindFeed             = "feed"
	KindWatchCredentials = "watch-credentials"
	KindWatchURL         = "watch-url"
)

// SourceFetcher retrieves the current items of one source.
type SourceFetcher interface {
	ID() string   // e.g. "github:arc42/arc42-template"
	Kind() string // one of the Kind* constants
	Fetch(ctx context.Context) ([]domain.Item, error)
}

// ItemStore caches the latest items per source.
type ItemStore interface {
	// ReplaceItems atomically replaces the items of a source, preserving FirstSeen of known ids
	// and setting FirstSeen=now for new ones.
	ReplaceItems(ctx context.Context, sourceID string, items []domain.Item, now time.Time) error
	// Items returns a source's items, newest (CreatedAt) first.
	Items(ctx context.Context, sourceID string) ([]domain.Item, error)
	AllItems(ctx context.Context) ([]domain.Item, error)
	ExternalIDs(ctx context.Context, sourceID string) ([]string, error)
}

// SnapshotStore persists daily id snapshots.
type SnapshotStore interface {
	PutSnapshot(ctx context.Context, s domain.Snapshot) error
	// LatestSnapshot returns nil, nil when the source has no snapshot.
	LatestSnapshot(ctx context.Context, sourceID string) (*domain.Snapshot, error)
	// SnapshotBefore returns the latest snapshot with Date < date, or nil, nil.
	SnapshotBefore(ctx context.Context, sourceID, date string) (*domain.Snapshot, error)
	// PruneSnapshots deletes snapshots with Date < beforeDate.
	PruneSnapshots(ctx context.Context, beforeDate string) error
}

// DismissalStore persists user dismissals.
type DismissalStore interface {
	PutDismissal(ctx context.Context, d domain.Dismissal) error
	Dismissal(ctx context.Context, id domain.ItemID) (*domain.Dismissal, error)
	Dismissals(ctx context.Context) (map[domain.ItemID]domain.Dismissal, error)
}

// StatusStore persists per-source fetch health.
type StatusStore interface {
	RecordStatus(ctx context.Context, s domain.FetchStatus) error
	Status(ctx context.Context, sourceID string) (*domain.FetchStatus, error)
	Statuses(ctx context.Context) ([]domain.FetchStatus, error) // sorted by SourceID
}

// Store is everything the app persists.
type Store interface {
	ItemStore
	SnapshotStore
	DismissalStore
	StatusStore
}

// Clock abstracts time.Now for testability.
type Clock interface{ Now() time.Time }

// CredentialSink receives credential expiries adapters detect (e.g. GitHub token header, FR-11.2).
type CredentialSink interface {
	ReportCredential(name string, expires *time.Time, usedBy string)
}

// Notifier is reserved for later push channels (Slack); unused in v1.
type Notifier interface {
	Notify(ctx context.Context, title, body string) error
}
```

`internal/ports/errors.go`:

```go
package ports

import (
	"errors"
	"fmt"
	"time"
)

// Sentinel errors adapters return so the scheduler can react (arc42 §8.7).
var (
	ErrAuth      = errors.New("authentication failed")
	ErrTransient = errors.New("transient upstream error")
	ErrPermanent = errors.New("permanent upstream error")
)

// RateLimitedError says when the upstream will accept requests again.
type RateLimitedError struct{ ResetAt time.Time }

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("rate limited until %s", e.ResetAt.UTC().Format(time.RFC3339))
}

// AsRateLimited unwraps a RateLimitedError.
func AsRateLimited(err error) (*RateLimitedError, bool) {
	var rl *RateLimitedError
	if errors.As(err, &rl) {
		return rl, true
	}
	return nil, false
}
```

`internal/ports/memstore/memstore.go`:

```go
// Package memstore is an in-memory ports.Store for tests and fakes.
package memstore

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// Store implements ports.Store in memory. Safe for concurrent use.
type Store struct {
	mu         sync.RWMutex
	items      map[string]map[string]domain.Item // sourceID → externalID → item
	snapshots  map[string][]domain.Snapshot      // sourceID → sorted by Date asc
	dismissals map[domain.ItemID]domain.Dismissal
	statuses   map[string]domain.FetchStatus
}

var _ ports.Store = (*Store)(nil)

// New returns an empty store.
func New() *Store {
	return &Store{items: map[string]map[string]domain.Item{}, snapshots: map[string][]domain.Snapshot{},
		dismissals: map[domain.ItemID]domain.Dismissal{}, statuses: map[string]domain.FetchStatus{}}
}

// ReplaceItems implements ports.Store.
func (s *Store) ReplaceItems(_ context.Context, sourceID string, items []domain.Item, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.items[sourceID]
	next := make(map[string]domain.Item, len(items))
	for _, it := range items {
		if prev, ok := old[it.ID.ExternalID]; ok {
			it.FirstSeen = prev.FirstSeen
		} else {
			it.FirstSeen = now
		}
		next[it.ID.ExternalID] = it
	}
	s.items[sourceID] = next
	return nil
}

func sortNewest(list []domain.Item) {
	sort.SliceStable(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
}

// Items implements ports.Store.
func (s *Store) Items(_ context.Context, sourceID string) ([]domain.Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Item, 0, len(s.items[sourceID]))
	for _, it := range s.items[sourceID] {
		out = append(out, it)
	}
	sortNewest(out)
	return out, nil
}

// AllItems implements ports.Store.
func (s *Store) AllItems(_ context.Context) ([]domain.Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Item
	for _, m := range s.items {
		for _, it := range m {
			out = append(out, it)
		}
	}
	sortNewest(out)
	return out, nil
}

// ExternalIDs implements ports.Store.
func (s *Store) ExternalIDs(_ context.Context, sourceID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.items[sourceID]))
	for id := range s.items[sourceID] {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// PutSnapshot implements ports.Store.
func (s *Store) PutSnapshot(_ context.Context, snap domain.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.snapshots[snap.SourceID]
	replaced := false
	for i := range list {
		if list[i].Date == snap.Date {
			list[i] = snap
			replaced = true
		}
	}
	if !replaced {
		list = append(list, snap)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Date < list[j].Date })
	s.snapshots[snap.SourceID] = list
	return nil
}

// LatestSnapshot implements ports.Store.
func (s *Store) LatestSnapshot(_ context.Context, sourceID string) (*domain.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.snapshots[sourceID]
	if len(list) == 0 {
		return nil, nil
	}
	snap := list[len(list)-1]
	return &snap, nil
}

// SnapshotBefore implements ports.Store.
func (s *Store) SnapshotBefore(_ context.Context, sourceID, date string) (*domain.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.snapshots[sourceID]
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].Date < date {
			snap := list[i]
			return &snap, nil
		}
	}
	return nil, nil
}

// PruneSnapshots implements ports.Store.
func (s *Store) PruneSnapshots(_ context.Context, beforeDate string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for src, list := range s.snapshots {
		kept := list[:0]
		for _, snap := range list {
			if snap.Date >= beforeDate {
				kept = append(kept, snap)
			}
		}
		s.snapshots[src] = kept
	}
	return nil
}

// PutDismissal implements ports.Store.
func (s *Store) PutDismissal(_ context.Context, d domain.Dismissal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dismissals[d.ID] = d
	return nil
}

// Dismissal implements ports.Store.
func (s *Store) Dismissal(_ context.Context, id domain.ItemID) (*domain.Dismissal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.dismissals[id]
	if !ok {
		return nil, nil
	}
	return &d, nil
}

// Dismissals implements ports.Store.
func (s *Store) Dismissals(_ context.Context) (map[domain.ItemID]domain.Dismissal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[domain.ItemID]domain.Dismissal, len(s.dismissals))
	for k, v := range s.dismissals {
		out[k] = v
	}
	return out, nil
}

// RecordStatus implements ports.Store.
func (s *Store) RecordStatus(_ context.Context, st domain.FetchStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses[st.SourceID] = st
	return nil
}

// Status implements ports.Store.
func (s *Store) Status(_ context.Context, sourceID string) (*domain.FetchStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.statuses[sourceID]
	if !ok {
		return nil, nil
	}
	return &st, nil
}

// Statuses implements ports.Store.
func (s *Store) Statuses(_ context.Context) ([]domain.FetchStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.FetchStatus, 0, len(s.statuses))
	for _, st := range s.statuses {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SourceID < out[j].SourceID })
	return out, nil
}
```

`internal/adapters/clock/clock.go`:

```go
// Package clock provides the real clock and a controllable fake (arc42 §8.10).
package clock

import (
	"sync"
	"time"
)

// Real uses time.Now — the only place in the codebase allowed to (besides cmd/).
type Real struct{}

// Now returns the wall clock time in UTC.
func (Real) Now() time.Time { return time.Now().UTC() }

// Fake is a settable clock for tests.
type Fake struct {
	mu sync.Mutex
	t  time.Time
}

// NewFake starts at t.
func NewFake(t time.Time) *Fake { return &Fake{t: t} }

// Now returns the fake time.
func (f *Fake) Now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.t }

// Set moves the clock to t.
func (f *Fake) Set(t time.Time) { f.mu.Lock(); f.t = t; f.mu.Unlock() }

// Advance moves the clock forward by d.
func (f *Fake) Advance(d time.Duration) { f.mu.Lock(); f.t = f.t.Add(d); f.mu.Unlock() }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make go ARGS="test ./internal/ports/... ./internal/adapters/clock/ -race -v"` — Expected: PASS. `make lint` — clean (revive may ask for comments on exported methods of memstore: add `// X implements ports.Store.` one-liners if it does).

- [ ] **Step 5: Commit**

```bash
git add internal/ports internal/adapters/clock
git commit -m "feat(ports): interfaces, typed errors, in-memory store with contract tests, clock"
```

---

### Task 7: Configuration – schema, loading, validation

**Files:**
- Create: `internal/config/config.go`, `internal/config/load.go`, `internal/config/validate.go`, `internal/config/config_test.go`, `internal/config/testdata/minimal.yaml`, `internal/config/testdata/unknown-key.yaml`
- Modify: `go.mod` (adds `gopkg.in/yaml.v3`)

**Interfaces:**
- Produces: `config.Config` (see code), `config.Load(path string, getenv func(string) string) (*Config, error)`, `config.Parse(r io.Reader, getenv func(string) string) (*Config, error)`, `(*Config).Validate() error`, `config.Default() Config`, `(*Config).Rules() domain.Rules`, `(GitHubConfig).RepoInterval(name string) time.Duration`, `config.ValidationError{Key, Msg}`.
- Env vars read: `GITHUB_TOKEN`, `PLAUSIBLE_API_KEY`, `TODOIST_TOKEN`, `SESSION_SECRET`, `ENROLL_TOKEN`, `AUTH_MODE` (`passkey`|`dev`, default `passkey`), `LOG_LEVEL`, `ZORGSCOPE_BASE_URL`, `ZORGSCOPE_DATA` (default `/data/zorgscope.db`), `GITHUB_BASE_URL`, `PLAUSIBLE_BASE_URL`, `TODOIST_BASE_URL`, `PORT`.

- [ ] **Step 1: Write the failing tests**

`internal/config/testdata/minimal.yaml`:

```yaml
server:
  base_url: http://localhost:8080
github:
  enabled: true
  me: gernotstarke
  repos:
    - arc42/arc42-template
    - name: gernotstarke/esabuch.de-site
      poll_interval: 5m
```

`internal/config/testdata/unknown-key.yaml`:

```yaml
server:
  base_url: http://localhost:8080
  prot: 8080
```

`internal/config/config_test.go`:

```go
package config

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadRepoConfigDefaultsAndOverrides(t *testing.T) {
	cfg, err := Load("testdata/minimal.yaml", env(map[string]string{"GITHUB_TOKEN": "t", "AUTH_MODE": "dev"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 8080 || cfg.Server.Timezone != "Europe/Berlin" || cfg.Server.Location == nil {
		t.Fatalf("server defaults: %+v", cfg.Server)
	}
	if cfg.GitHub.PollInterval != 10*time.Minute || cfg.GitHub.GracePeriod != 4*time.Hour || len(cfg.GitHub.Bots) != 3 {
		t.Fatalf("github defaults: %+v", cfg.GitHub)
	}
	if len(cfg.GitHub.Repos) != 2 || cfg.GitHub.Repos[0].Name != "arc42/arc42-template" || cfg.GitHub.Repos[1].PollInterval != 5*time.Minute {
		t.Fatalf("repos: %+v", cfg.GitHub.Repos)
	}
	if cfg.GitHub.RepoInterval("arc42/arc42-template") != 10*time.Minute || cfg.GitHub.RepoInterval("gernotstarke/esabuch.de-site") != 5*time.Minute {
		t.Fatal("RepoInterval")
	}
	if cfg.Secrets.GitHubToken != "t" || cfg.AuthMode != "dev" || cfg.DataPath != "/data/zorgscope.db" {
		t.Fatalf("env: %+v %s %s", cfg.Secrets, cfg.AuthMode, cfg.DataPath)
	}
	if cfg.Snapshot.Time != "03:00" || cfg.Snapshot.Hour != 3 || cfg.Snapshot.Minute != 0 || cfg.Snapshot.RetentionDays != 30 {
		t.Fatalf("snapshot defaults: %+v", cfg.Snapshot)
	}
	if cfg.UI.TilePollSeconds != 60 || cfg.UI.AttentionCap != 30 || cfg.UI.RefreshMinGapSeconds != 30 || len(cfg.UI.Tiles) != 6 {
		t.Fatalf("ui defaults: %+v", cfg.UI)
	}
	if !cfg.Watch.Enabled || cfg.Watch.WarnDays != 14 || cfg.Rules().WarnDays != 14 || cfg.Rules().Me != "gernotstarke" {
		t.Fatalf("watch/rules: %+v", cfg.Watch)
	}
	if cfg.GitHub.BaseURL != "https://api.github.com" {
		t.Fatalf("github base url default: %s", cfg.GitHub.BaseURL)
	}
}

func TestLoadRealConfig(t *testing.T) {
	cfg, err := Load("../../config/zorgscope.yaml", env(map[string]string{"GITHUB_TOKEN": "t", "PLAUSIBLE_API_KEY": "p", "TODOIST_TOKEN": "d", "SESSION_SECRET": strings.Repeat("x", 32), "ENROLL_TOKEN": "e"}))
	if err != nil {
		t.Fatalf("the shipped config must load: %v", err)
	}
	if len(cfg.GitHub.Repos) != 8 || len(cfg.Plausible.Sites) != 7 || len(cfg.Watch.URLs) != 1 {
		t.Fatalf("shipped config content changed unexpectedly: %d repos, %d sites, %d urls", len(cfg.GitHub.Repos), len(cfg.Plausible.Sites), len(cfg.Watch.URLs))
	}
}

func TestValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		env  map[string]string
		key  string
	}{
		{"unknown key", "server:\n  base_url: http://localhost:8080\n  prot: 1\n", nil, "prot"},
		{"bad base url", "server:\n  base_url: localhost\n", nil, "server.base_url"},
		{"dev mode on non-localhost", "server:\n  base_url: https://zorgscope.fly.dev\n", map[string]string{"AUTH_MODE": "dev"}, "AUTH_MODE"},
		{"bad auth mode", "server:\n  base_url: http://localhost:8080\n", map[string]string{"AUTH_MODE": "magic"}, "AUTH_MODE"},
		{"interval too small", "server:\n  base_url: http://localhost:8080\ngithub:\n  enabled: true\n  me: x\n  poll_interval: 5s\n  repos: [a/b]\n", map[string]string{"GITHUB_TOKEN": "t", "AUTH_MODE": "dev"}, "github.poll_interval"},
		{"bad repo name", "server:\n  base_url: http://localhost:8080\ngithub:\n  enabled: true\n  me: x\n  repos: [nope]\n", map[string]string{"GITHUB_TOKEN": "t", "AUTH_MODE": "dev"}, "github.repos[0]"},
		{"github enabled without token", "server:\n  base_url: http://localhost:8080\ngithub:\n  enabled: true\n  me: x\n  repos: [a/b]\n", map[string]string{"AUTH_MODE": "dev"}, "GITHUB_TOKEN"},
		{"github enabled without me", "server:\n  base_url: http://localhost:8080\ngithub:\n  enabled: true\n  repos: [a/b]\n", map[string]string{"GITHUB_TOKEN": "t", "AUTH_MODE": "dev"}, "github.me"},
		{"bad snapshot time", "server:\n  base_url: http://localhost:8080\nsnapshot:\n  time: 25:00\n", map[string]string{"AUTH_MODE": "dev"}, "snapshot.time"},
		{"bad timezone", "server:\n  base_url: http://localhost:8080\n  timezone: Mars/Olympus\n", nil, "server.timezone"},
		{"unknown tile", "server:\n  base_url: http://localhost:8080\nui:\n  tiles: [attention, weather]\n", map[string]string{"AUTH_MODE": "dev"}, "ui.tiles[1]"},
		{"bad credential date", "server:\n  base_url: http://localhost:8080\nwatch:\n  credentials:\n    - name: x\n      expires: 31.12.2026\n", map[string]string{"AUTH_MODE": "dev"}, "watch.credentials[0].expires"},
		{"passkey without session secret", "server:\n  base_url: https://zorgscope.fly.dev\n", map[string]string{"AUTH_MODE": "passkey"}, "SESSION_SECRET"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(c.yaml), env(c.env))
			if err == nil {
				t.Fatal("expected error")
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("expected ValidationError, got %T %v", err, err)
			}
			if !strings.Contains(ve.Key, c.key) {
				t.Fatalf("key = %q, want it to contain %q (msg %q)", ve.Key, c.key, ve.Msg)
			}
		})
	}
}

func TestDisabledSourcesNeedNoSecrets(t *testing.T) {
	y := "server:\n  base_url: http://localhost:8080\ngithub:\n  enabled: false\n"
	if _, err := Parse(strings.NewReader(y), env(map[string]string{"AUTH_MODE": "dev"})); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load("testdata/does-not-exist.yaml", env(nil)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
}
```

- [ ] **Step 2: Add yaml dependency; run tests to verify they fail**

Run: `make go ARGS="get gopkg.in/yaml.v3@v3.0.1"` then `make go ARGS="test ./internal/config/ -v"` — Expected: FAIL (undefined).

- [ ] **Step 3: Implement**

`internal/config/config.go`:

```go
// Package config loads and validates config/zorgscope.yaml plus secrets from the environment
// (arc42 §8.3, ADR-0007).
package config

import (
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	Server    ServerConfig    `yaml:"server"`
	UI        UIConfig        `yaml:"ui"`
	Snapshot  SnapshotConfig  `yaml:"snapshot"`
	GitHub    GitHubConfig    `yaml:"github"`
	Plausible PlausibleConfig `yaml:"plausible"`
	Todoist   TodoistConfig   `yaml:"todoist"`
	Feeds     FeedsConfig     `yaml:"feeds"`
	Watch     WatchConfig     `yaml:"watch"`

	// From environment (never from YAML):
	Secrets  Secrets `yaml:"-"`
	AuthMode string  `yaml:"-"` // "passkey" | "dev"
	LogLevel string  `yaml:"-"`
	DataPath string  `yaml:"-"`
}

// ServerConfig — section `server`.
type ServerConfig struct {
	BaseURL  string         `yaml:"base_url"`
	Port     int            `yaml:"port"`
	Timezone string         `yaml:"timezone"`
	Location *time.Location `yaml:"-"`
}

// UIConfig — section `ui`.
type UIConfig struct {
	TilePollSeconds      int      `yaml:"tile_poll_seconds"`
	AttentionCap         int      `yaml:"attention_cap"`
	RefreshMinGapSeconds int      `yaml:"refresh_min_gap_seconds"`
	Tiles                []string `yaml:"tiles"`
}

// KnownTiles are the tile names accepted in ui.tiles.
var KnownTiles = []string{"attention", "repos", "sites", "todoist", "news", "watch"}

// SnapshotConfig — section `snapshot`.
type SnapshotConfig struct {
	Time          string `yaml:"time"` // HH:MM
	RetentionDays int    `yaml:"retention_days"`
	Hour, Minute  int    `yaml:"-"`
}

// GitHubConfig — section `github`.
type GitHubConfig struct {
	Enabled      bool          `yaml:"enabled"`
	Me           string        `yaml:"me"`
	PollInterval time.Duration `yaml:"poll_interval"`
	GracePeriod  time.Duration `yaml:"grace_period"`
	StaleAfter   time.Duration `yaml:"stale_after"`
	Bots         []string      `yaml:"bots"`
	Mentions     bool          `yaml:"mentions"`
	Repos        []RepoConfig  `yaml:"repos"`
	BaseURL      string        `yaml:"base_url"` // overridable by GITHUB_BASE_URL (fakes)
}

// RepoConfig accepts either a plain "owner/name" string or a mapping with overrides.
type RepoConfig struct {
	Name         string        `yaml:"name"`
	PollInterval time.Duration `yaml:"poll_interval"`
}

// UnmarshalYAML implements the scalar-or-mapping form.
func (r *RepoConfig) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		r.Name = n.Value
		return nil
	}
	type plain RepoConfig
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*r = RepoConfig(p)
	return nil
}

// RepoInterval returns the effective poll interval for a repo.
func (g GitHubConfig) RepoInterval(name string) time.Duration {
	for _, r := range g.Repos {
		if r.Name == name && r.PollInterval > 0 {
			return r.PollInterval
		}
	}
	return g.PollInterval
}

// PlausibleConfig — section `plausible`.
type PlausibleConfig struct {
	Enabled      bool          `yaml:"enabled"`
	PollInterval time.Duration `yaml:"poll_interval"`
	Order        string        `yaml:"order"` // visitors | config
	Sites        []string      `yaml:"sites"`
	BaseURL      string        `yaml:"base_url"`
}

// TodoistConfig — section `todoist`.
type TodoistConfig struct {
	Enabled      bool          `yaml:"enabled"`
	PollInterval time.Duration `yaml:"poll_interval"`
	HorizonDays  int           `yaml:"horizon_days"`
	BaseURL      string        `yaml:"base_url"`
}

// FeedsConfig — section `feeds`.
type FeedsConfig struct {
	Enabled      bool          `yaml:"enabled"`
	PollInterval time.Duration `yaml:"poll_interval"`
	MaxItems     int           `yaml:"max_items"`
	GroupByTopic bool          `yaml:"group_by_topic"`
	Sources      []FeedSource  `yaml:"sources"`
}

// FeedSource is one feed.
type FeedSource struct {
	Name     string `yaml:"name"`
	URL      string `yaml:"url"`
	Topic    string `yaml:"topic"`
	MaxItems int    `yaml:"max_items"`
}

// WatchConfig — section `watch` (FR-11.x).
type WatchConfig struct {
	Enabled     bool               `yaml:"enabled"`
	WarnDays    int                `yaml:"warn_days"`
	Credentials []CredentialConfig `yaml:"credentials"`
	URLs        []URLCheckConfig   `yaml:"urls"`
}

// CredentialConfig is a manually registered expiring credential.
type CredentialConfig struct {
	Name      string    `yaml:"name"`
	Expires   string    `yaml:"expires"` // YYYY-MM-DD
	WarnDays  int       `yaml:"warn_days"`
	UsedBy    string    `yaml:"used_by"`
	URL       string    `yaml:"url"`
	ExpiresAt time.Time `yaml:"-"`
}

// URLCheckConfig is a health-checked URL.
type URLCheckConfig struct {
	Name               string        `yaml:"name"`
	URL                string        `yaml:"url"`
	ExpectStatus       int           `yaml:"expect_status"`
	ExpectBodyContains string        `yaml:"expect_body_contains"`
	PollInterval       time.Duration `yaml:"poll_interval"`
}

// Secrets come from the environment only.
type Secrets struct {
	GitHubToken     string
	PlausibleAPIKey string
	TodoistToken    string
	SessionSecret   string
	EnrollToken     string
}

// Default returns the documented defaults; YAML overrides them.
func Default() Config {
	return Config{
		Server:   ServerConfig{Port: 8080, Timezone: "Europe/Berlin"},
		UI:       UIConfig{TilePollSeconds: 60, AttentionCap: 30, RefreshMinGapSeconds: 30, Tiles: append([]string(nil), KnownTiles...)},
		Snapshot: SnapshotConfig{Time: "03:00", RetentionDays: 30},
		GitHub: GitHubConfig{PollInterval: 10 * time.Minute, GracePeriod: 4 * time.Hour, StaleAfter: 30 * 24 * time.Hour,
			Bots: []string{"[bot]", "dependabot", "renovate"}, Mentions: true, BaseURL: "https://api.github.com"},
		Plausible: PlausibleConfig{PollInterval: 30 * time.Minute, Order: "visitors", BaseURL: "https://plausible.io"},
		Todoist:   TodoistConfig{PollInterval: 5 * time.Minute, HorizonDays: 7, BaseURL: "https://api.todoist.com"},
		Feeds:     FeedsConfig{PollInterval: 30 * time.Minute, MaxItems: 20},
		Watch:     WatchConfig{Enabled: true, WarnDays: 14},
		AuthMode:  "passkey",
		LogLevel:  "info",
		DataPath:  "/data/zorgscope.db",
	}
}

// Rules derives the domain rules from the configuration.
func (c *Config) Rules() domain.Rules {
	return domain.Rules{Grace: c.GitHub.GracePeriod, StaleAfter: c.GitHub.StaleAfter, Me: c.GitHub.Me,
		Bots: c.GitHub.Bots, WarnDays: c.Watch.WarnDays}
}
```

`internal/config/load.go`:

```go
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Load reads the YAML file at path and applies environment overrides and validation.
func Load(path string, getenv func(string) string) (*Config, error) {
	f, err := os.Open(path) //nolint:gosec // path comes from the operator
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	cfg, err := Parse(f, getenv)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// Parse decodes YAML from r on top of Default(), applies env, validates.
func Parse(r io.Reader, getenv func(string) string) (*Config, error) {
	cfg := Default()
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, &ValidationError{Key: yamlErrorKey(err), Msg: err.Error()}
	}
	applyEnv(&cfg, getenv)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyEnv(cfg *Config, getenv func(string) string) {
	set := func(dst *string, key string) {
		if v := getenv(key); v != "" {
			*dst = v
		}
	}
	set(&cfg.Secrets.GitHubToken, "GITHUB_TOKEN")
	set(&cfg.Secrets.PlausibleAPIKey, "PLAUSIBLE_API_KEY")
	set(&cfg.Secrets.TodoistToken, "TODOIST_TOKEN")
	set(&cfg.Secrets.SessionSecret, "SESSION_SECRET")
	set(&cfg.Secrets.EnrollToken, "ENROLL_TOKEN")
	set(&cfg.AuthMode, "AUTH_MODE")
	set(&cfg.LogLevel, "LOG_LEVEL")
	set(&cfg.DataPath, "ZORGSCOPE_DATA")
	set(&cfg.Server.BaseURL, "ZORGSCOPE_BASE_URL")
	set(&cfg.GitHub.BaseURL, "GITHUB_BASE_URL")
	set(&cfg.Plausible.BaseURL, "PLAUSIBLE_BASE_URL")
	set(&cfg.Todoist.BaseURL, "TODOIST_BASE_URL")
	if p := getenv("PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			cfg.Server.Port = n
		}
	}
}

// yamlErrorKey extracts the offending field name from yaml.v3 messages like
// `yaml: unmarshal errors:\n  line 3: field prot not found in type config.ServerConfig`.
func yamlErrorKey(err error) string {
	msg := err.Error()
	const marker = "field "
	if i := indexOf(msg, marker); i >= 0 {
		rest := msg[i+len(marker):]
		if j := indexOf(rest, " "); j > 0 {
			return rest[:j]
		}
		return rest
	}
	return "yaml"
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

`internal/config/validate.go`:

```go
package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ValidationError names the offending key (dotted path or env var) and the problem (QS-4.3).
type ValidationError struct {
	Key string
	Msg string
}

func (e *ValidationError) Error() string { return fmt.Sprintf("invalid config: %s: %s", e.Key, e.Msg) }

func fail(key, format string, a ...any) error {
	return &ValidationError{Key: key, Msg: fmt.Sprintf(format, a...)}
}

var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

const minInterval = 10 * time.Second // small enough for e2e configs, large enough for rate limits

// Validate checks the configuration and resolves derived fields (Location, Hour/Minute, ExpiresAt).
func (c *Config) Validate() error {
	u, err := url.Parse(c.Server.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fail("server.base_url", "must be an absolute http(s) URL, got %q", c.Server.BaseURL)
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fail("server.port", "must be 1..65535")
	}
	loc, err := time.LoadLocation(c.Server.Timezone)
	if err != nil {
		return fail("server.timezone", "unknown timezone %q", c.Server.Timezone)
	}
	c.Server.Location = loc

	switch c.AuthMode {
	case "passkey":
		if len(c.Secrets.SessionSecret) < 32 {
			return fail("SESSION_SECRET", "must be at least 32 characters in passkey mode")
		}
	case "dev":
		host := u.Hostname()
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return fail("AUTH_MODE", "dev mode is only allowed when server.base_url points to localhost")
		}
	default:
		return fail("AUTH_MODE", "must be passkey or dev, got %q", c.AuthMode)
	}

	if c.UI.TilePollSeconds < 5 {
		return fail("ui.tile_poll_seconds", "must be >= 5")
	}
	if c.UI.RefreshMinGapSeconds < 1 {
		return fail("ui.refresh_min_gap_seconds", "must be >= 1")
	}
	for i, t := range c.UI.Tiles {
		if !contains(KnownTiles, t) {
			return fail(fmt.Sprintf("ui.tiles[%d]", i), "unknown tile %q (known: %s)", t, strings.Join(KnownTiles, ", "))
		}
	}

	if _, err := fmt.Sscanf(c.Snapshot.Time, "%d:%d", &c.Snapshot.Hour, &c.Snapshot.Minute); err != nil ||
		c.Snapshot.Hour < 0 || c.Snapshot.Hour > 23 || c.Snapshot.Minute < 0 || c.Snapshot.Minute > 59 {
		return fail("snapshot.time", "must be HH:MM, got %q", c.Snapshot.Time)
	}
	if c.Snapshot.RetentionDays < 1 {
		return fail("snapshot.retention_days", "must be >= 1")
	}

	if c.GitHub.Enabled {
		if c.GitHub.Me == "" {
			return fail("github.me", "required when github is enabled")
		}
		if c.Secrets.GitHubToken == "" {
			return fail("GITHUB_TOKEN", "required when github is enabled")
		}
		if err := checkInterval("github.poll_interval", c.GitHub.PollInterval); err != nil {
			return err
		}
		if c.GitHub.GracePeriod < 0 || c.GitHub.StaleAfter <= 0 {
			return fail("github.grace_period/stale_after", "must be positive")
		}
		for i, r := range c.GitHub.Repos {
			if !repoRe.MatchString(r.Name) {
				return fail(fmt.Sprintf("github.repos[%d]", i), "must be owner/name, got %q", r.Name)
			}
			if r.PollInterval != 0 {
				if err := checkInterval(fmt.Sprintf("github.repos[%d].poll_interval", i), r.PollInterval); err != nil {
					return err
				}
			}
		}
	}
	if c.Plausible.Enabled {
		if c.Secrets.PlausibleAPIKey == "" {
			return fail("PLAUSIBLE_API_KEY", "required when plausible is enabled")
		}
		if err := checkInterval("plausible.poll_interval", c.Plausible.PollInterval); err != nil {
			return err
		}
		if c.Plausible.Order != "visitors" && c.Plausible.Order != "config" {
			return fail("plausible.order", "must be visitors or config")
		}
		for i, s := range c.Plausible.Sites {
			if s == "" || strings.ContainsAny(s, "/ :") {
				return fail(fmt.Sprintf("plausible.sites[%d]", i), "must be a bare hostname, got %q", s)
			}
		}
	}
	if c.Todoist.Enabled {
		if c.Secrets.TodoistToken == "" {
			return fail("TODOIST_TOKEN", "required when todoist is enabled")
		}
		if err := checkInterval("todoist.poll_interval", c.Todoist.PollInterval); err != nil {
			return err
		}
		if c.Todoist.HorizonDays < 1 {
			return fail("todoist.horizon_days", "must be >= 1")
		}
	}
	if c.Feeds.Enabled {
		if err := checkInterval("feeds.poll_interval", c.Feeds.PollInterval); err != nil {
			return err
		}
		for i, f := range c.Feeds.Sources {
			if f.Name == "" {
				return fail(fmt.Sprintf("feeds.sources[%d].name", i), "required")
			}
			if fu, err := url.Parse(f.URL); err != nil || fu.Host == "" {
				return fail(fmt.Sprintf("feeds.sources[%d].url", i), "must be an absolute URL")
			}
		}
	}
	if c.Watch.WarnDays < 1 {
		return fail("watch.warn_days", "must be >= 1")
	}
	for i := range c.Watch.Credentials {
		cr := &c.Watch.Credentials[i]
		if cr.Name == "" {
			return fail(fmt.Sprintf("watch.credentials[%d].name", i), "required")
		}
		t, err := time.ParseInLocation("2006-01-02", cr.Expires, loc)
		if err != nil {
			return fail(fmt.Sprintf("watch.credentials[%d].expires", i), "must be YYYY-MM-DD, got %q", cr.Expires)
		}
		cr.ExpiresAt = t
	}
	for i := range c.Watch.URLs {
		w := &c.Watch.URLs[i]
		if w.Name == "" {
			return fail(fmt.Sprintf("watch.urls[%d].name", i), "required")
		}
		if wu, err := url.Parse(w.URL); err != nil || wu.Host == "" || (wu.Scheme != "http" && wu.Scheme != "https") {
			return fail(fmt.Sprintf("watch.urls[%d].url", i), "must be an absolute http(s) URL")
		}
		if w.ExpectStatus == 0 {
			w.ExpectStatus = 200
		}
		if w.PollInterval == 0 {
			w.PollInterval = 15 * time.Minute
		}
		if err := checkInterval(fmt.Sprintf("watch.urls[%d].poll_interval", i), w.PollInterval); err != nil {
			return err
		}
	}
	return nil
}

func checkInterval(key string, d time.Duration) error {
	if d < minInterval {
		return fail(key, "must be at least %s, got %s", minInterval, d)
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make go ARGS="test ./internal/config/ -v"` — Expected: PASS. If `TestValidationErrors/unknown_key` fails on the key, print `err.Error()` and adjust `yamlErrorKey` to the actual yaml.v3 message (it is `field prot not found in type config.ServerConfig`).
Run: `make lint` — clean.

- [ ] **Step 5: Commit**

```bash
git add internal/config go.mod go.sum
git commit -m "feat(config): YAML schema with defaults, env secrets and validation (FR-8.1, FR-8.2, FR-9.4, QS-4.3)"
```

---

### Task 8: SQLite store (pure Go) passing the store contract

**Files:**
- Create: `internal/adapters/sqlite/store.go`, `internal/adapters/sqlite/migrations/0001_init.sql`, `internal/adapters/sqlite/items.go`, `internal/adapters/sqlite/snapshots.go`, `internal/adapters/sqlite/dismissals.go`, `internal/adapters/sqlite/status.go`, `internal/adapters/sqlite/store_test.go`
- Modify: `go.mod` (adds `modernc.org/sqlite`)

**Interfaces:**
- Produces: `sqlite.Open(path string) (*sqlite.Store, error)` (creates file, applies migrations), `(*Store).Close() error`, `*sqlite.Store` implements `ports.Store`.

- [ ] **Step 1: Write the failing test**

`internal/adapters/sqlite/store_test.go`:

```go
package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/ports/storetest"
)

func TestContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) ports.Store {
		s, err := Open(filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(path) // second open must not fail or re-run migrations
	if err != nil {
		t.Fatal(err)
	}
	var v int
	if err := s.db.QueryRow("SELECT MAX(version) FROM schema_version").Scan(&v); err != nil || v != 1 {
		t.Fatalf("schema_version = %d, %v", v, err)
	}
	_ = s.Close()
}
```

- [ ] **Step 2: Add dependency; run test to verify it fails**

Run: `make go ARGS="get modernc.org/sqlite@v1.56.0"` then `make go ARGS="test ./internal/adapters/sqlite/ -v"` — Expected: FAIL (undefined Open).

- [ ] **Step 3: Implement**

`internal/adapters/sqlite/migrations/0001_init.sql`:

```sql
CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);

CREATE TABLE items (
  source_id        TEXT NOT NULL,
  external_id      TEXT NOT NULL,
  kind             TEXT NOT NULL,
  title            TEXT NOT NULL DEFAULT '',
  url              TEXT NOT NULL DEFAULT '',
  author           TEXT NOT NULL DEFAULT '',
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  last_activity_by TEXT NOT NULL DEFAULT '',
  last_activity_at INTEGER NOT NULL DEFAULT 0,
  labels           TEXT NOT NULL DEFAULT '[]',
  payload          TEXT NOT NULL DEFAULT '',
  first_seen       INTEGER NOT NULL,
  PRIMARY KEY (source_id, external_id)
);
CREATE INDEX items_source_created ON items (source_id, created_at DESC);

CREATE TABLE snapshots (
  source_id TEXT NOT NULL,
  date      TEXT NOT NULL,
  taken_at  INTEGER NOT NULL,
  ids       TEXT NOT NULL,
  PRIMARY KEY (source_id, date)
);

CREATE TABLE dismissals (
  source_id    TEXT NOT NULL,
  external_id  TEXT NOT NULL,
  updated_at   INTEGER NOT NULL,
  dismissed_at INTEGER NOT NULL,
  PRIMARY KEY (source_id, external_id)
);

CREATE TABLE fetch_status (
  source_id    TEXT PRIMARY KEY,
  kind         TEXT NOT NULL DEFAULT '',
  last_success INTEGER NOT NULL DEFAULT 0,
  last_error   INTEGER NOT NULL DEFAULT 0,
  error_msg    TEXT NOT NULL DEFAULT '',
  next_run     INTEGER NOT NULL DEFAULT 0,
  item_count   INTEGER NOT NULL DEFAULT 0,
  duration_ms  INTEGER NOT NULL DEFAULT 0,
  in_flight    INTEGER NOT NULL DEFAULT 0,
  auth_failed  INTEGER NOT NULL DEFAULT 0
);
```

`internal/adapters/sqlite/store.go`:

```go
// Package sqlite implements ports.Store on a single SQLite file using the pure-Go driver
// modernc.org/sqlite (ADR-0004).
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers driver "sqlite"

	"github.com/gernotstarke/zorgscope/internal/ports"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store implements ports.Store.
type Store struct{ db *sql.DB }

var _ ports.Store = (*Store)(nil)

// Open opens (or creates) the database at path, sets pragmas and applies pending migrations.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}
	db.SetMaxOpenConns(1) // single writer; WAL readers still see consistent state
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&current); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		v, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration name %q: %w", name, err)
		}
		if v <= current {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version, applied_at) VALUES (?, ?)`, v, time.Now().Unix()); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromUnix(u int64) time.Time {
	if u == 0 {
		return time.Time{}
	}
	return time.Unix(u, 0).UTC()
}
```

`internal/adapters/sqlite/items.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// ReplaceItems implements ports.ItemStore.
func (s *Store) ReplaceItems(ctx context.Context, sourceID string, items []domain.Item, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	firstSeen := map[string]int64{}
	rows, err := tx.QueryContext(ctx, `SELECT external_id, first_seen FROM items WHERE source_id = ?`, sourceID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var fs int64
		if err := rows.Scan(&id, &fs); err != nil {
			_ = rows.Close()
			return err
		}
		firstSeen[id] = fs
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_ = rows.Close()

	if _, err := tx.ExecContext(ctx, `DELETE FROM items WHERE source_id = ?`, sourceID); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO items (source_id, external_id, kind, title, url, author, created_at, updated_at,
		last_activity_by, last_activity_at, labels, payload, first_seen) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, it := range items {
		fs, ok := firstSeen[it.ID.ExternalID]
		if !ok {
			fs = now.Unix()
		}
		labels, err := json.Marshal(it.Labels)
		if err != nil {
			return err
		}
		if _, err := stmt.ExecContext(ctx, sourceID, it.ID.ExternalID, string(it.Kind), it.Title, it.URL, it.Author,
			unix(it.CreatedAt), unix(it.UpdatedAt), it.LastActivityBy, unix(it.LastActivityAt), string(labels), string(it.Payload), fs); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const itemColumns = `source_id, external_id, kind, title, url, author, created_at, updated_at, last_activity_by, last_activity_at, labels, payload, first_seen`

func scanItems(rows *sql.Rows) ([]domain.Item, error) {
	defer func() { _ = rows.Close() }()
	var out []domain.Item
	for rows.Next() {
		var it domain.Item
		var kind, labels, payload string
		var created, updated, lastAt, first int64
		if err := rows.Scan(&it.ID.SourceID, &it.ID.ExternalID, &kind, &it.Title, &it.URL, &it.Author, &created, &updated,
			&it.LastActivityBy, &lastAt, &labels, &payload, &first); err != nil {
			return nil, err
		}
		it.Kind = domain.Kind(kind)
		it.CreatedAt, it.UpdatedAt, it.LastActivityAt, it.FirstSeen = fromUnix(created), fromUnix(updated), fromUnix(lastAt), fromUnix(first)
		if err := json.Unmarshal([]byte(labels), &it.Labels); err != nil {
			return nil, err
		}
		if payload != "" {
			it.Payload = json.RawMessage(payload)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Items implements ports.ItemStore.
func (s *Store) Items(ctx context.Context, sourceID string) ([]domain.Item, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemColumns+` FROM items WHERE source_id = ? ORDER BY created_at DESC, external_id`, sourceID)
	if err != nil {
		return nil, err
	}
	return scanItems(rows)
}

// AllItems implements ports.ItemStore.
func (s *Store) AllItems(ctx context.Context) ([]domain.Item, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemColumns+` FROM items ORDER BY created_at DESC, source_id, external_id`)
	if err != nil {
		return nil, err
	}
	return scanItems(rows)
}

// ExternalIDs implements ports.ItemStore.
func (s *Store) ExternalIDs(ctx context.Context, sourceID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT external_id FROM items WHERE source_id = ? ORDER BY external_id`, sourceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
```

`internal/adapters/sqlite/snapshots.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// PutSnapshot implements ports.SnapshotStore (upsert by source+date).
func (s *Store) PutSnapshot(ctx context.Context, snap domain.Snapshot) error {
	ids, err := json.Marshal(snap.IDList())
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO snapshots (source_id, date, taken_at, ids) VALUES (?,?,?,?)
		ON CONFLICT(source_id, date) DO UPDATE SET taken_at = excluded.taken_at, ids = excluded.ids`,
		snap.SourceID, snap.Date, unix(snap.TakenAt), string(ids))
	return err
}

func (s *Store) querySnapshot(ctx context.Context, query string, args ...any) (*domain.Snapshot, error) {
	var src, date, ids string
	var taken int64
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&src, &date, &taken, &ids)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []string
	if err := json.Unmarshal([]byte(ids), &list); err != nil {
		return nil, err
	}
	snap := domain.NewSnapshot(src, date, fromUnix(taken), list)
	return &snap, nil
}

// LatestSnapshot implements ports.SnapshotStore.
func (s *Store) LatestSnapshot(ctx context.Context, sourceID string) (*domain.Snapshot, error) {
	return s.querySnapshot(ctx, `SELECT source_id, date, taken_at, ids FROM snapshots WHERE source_id = ? ORDER BY date DESC LIMIT 1`, sourceID)
}

// SnapshotBefore implements ports.SnapshotStore.
func (s *Store) SnapshotBefore(ctx context.Context, sourceID, date string) (*domain.Snapshot, error) {
	return s.querySnapshot(ctx, `SELECT source_id, date, taken_at, ids FROM snapshots WHERE source_id = ? AND date < ? ORDER BY date DESC LIMIT 1`, sourceID, date)
}

// PruneSnapshots implements ports.SnapshotStore.
func (s *Store) PruneSnapshots(ctx context.Context, beforeDate string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM snapshots WHERE date < ?`, beforeDate)
	return err
}
```

`internal/adapters/sqlite/dismissals.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// PutDismissal implements ports.DismissalStore.
func (s *Store) PutDismissal(ctx context.Context, d domain.Dismissal) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO dismissals (source_id, external_id, updated_at, dismissed_at) VALUES (?,?,?,?)
		ON CONFLICT(source_id, external_id) DO UPDATE SET updated_at = excluded.updated_at, dismissed_at = excluded.dismissed_at`,
		d.ID.SourceID, d.ID.ExternalID, unix(d.UpdatedAt), unix(d.DismissedAt))
	return err
}

// Dismissal implements ports.DismissalStore.
func (s *Store) Dismissal(ctx context.Context, id domain.ItemID) (*domain.Dismissal, error) {
	var upd, dis int64
	err := s.db.QueryRowContext(ctx, `SELECT updated_at, dismissed_at FROM dismissals WHERE source_id = ? AND external_id = ?`, id.SourceID, id.ExternalID).Scan(&upd, &dis)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &domain.Dismissal{ID: id, UpdatedAt: fromUnix(upd), DismissedAt: fromUnix(dis)}, nil
}

// Dismissals implements ports.DismissalStore.
func (s *Store) Dismissals(ctx context.Context) (map[domain.ItemID]domain.Dismissal, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source_id, external_id, updated_at, dismissed_at FROM dismissals`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[domain.ItemID]domain.Dismissal{}
	for rows.Next() {
		var d domain.Dismissal
		var upd, dis int64
		if err := rows.Scan(&d.ID.SourceID, &d.ID.ExternalID, &upd, &dis); err != nil {
			return nil, err
		}
		d.UpdatedAt, d.DismissedAt = fromUnix(upd), fromUnix(dis)
		out[d.ID] = d
	}
	return out, rows.Err()
}
```

`internal/adapters/sqlite/status.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// RecordStatus implements ports.StatusStore.
func (s *Store) RecordStatus(ctx context.Context, st domain.FetchStatus) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO fetch_status (source_id, kind, last_success, last_error, error_msg, next_run, item_count, duration_ms, in_flight, auth_failed)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(source_id) DO UPDATE SET kind=excluded.kind, last_success=excluded.last_success, last_error=excluded.last_error,
		error_msg=excluded.error_msg, next_run=excluded.next_run, item_count=excluded.item_count, duration_ms=excluded.duration_ms,
		in_flight=excluded.in_flight, auth_failed=excluded.auth_failed`,
		st.SourceID, st.Kind, unix(st.LastSuccess), unix(st.LastError), st.ErrorMsg, unix(st.NextRun), st.ItemCount,
		st.Duration.Milliseconds(), boolInt(st.InFlight), boolInt(st.AuthFailed))
	return err
}

const statusColumns = `source_id, kind, last_success, last_error, error_msg, next_run, item_count, duration_ms, in_flight, auth_failed`

func scanStatus(sc interface{ Scan(...any) error }) (domain.FetchStatus, error) {
	var st domain.FetchStatus
	var ls, le, nr, dur int64
	var inflight, auth int
	err := sc.Scan(&st.SourceID, &st.Kind, &ls, &le, &st.ErrorMsg, &nr, &st.ItemCount, &dur, &inflight, &auth)
	st.LastSuccess, st.LastError, st.NextRun = fromUnix(ls), fromUnix(le), fromUnix(nr)
	st.Duration = time.Duration(dur) * time.Millisecond
	st.InFlight, st.AuthFailed = inflight == 1, auth == 1
	return st, err
}

// Status implements ports.StatusStore.
func (s *Store) Status(ctx context.Context, sourceID string) (*domain.FetchStatus, error) {
	st, err := scanStatus(s.db.QueryRowContext(ctx, `SELECT `+statusColumns+` FROM fetch_status WHERE source_id = ?`, sourceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// Statuses implements ports.StatusStore.
func (s *Store) Statuses(ctx context.Context) ([]domain.FetchStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+statusColumns+` FROM fetch_status ORDER BY source_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.FetchStatus
	for rows.Next() {
		st, err := scanStatus(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make go ARGS="test ./internal/adapters/sqlite/ -race -v"` — Expected: PASS (all contract subtests + idempotent migration). `make lint` — clean (`time.Now()` in `migrate` is acceptable bookkeeping; if a lint rule complains, pass a clock — not required).

- [ ] **Step 5: Commit**

```bash
git add internal/adapters/sqlite go.mod go.sum
git commit -m "feat(sqlite): pure-Go SQLite store with migrations, passes store contract (ADR-0004, FR-7.3)"
```

---

### Task 9: App – Scheduler (per‑source polling, single‑flight, backoff, manual refresh)

**Files:**
- Create: `internal/app/scheduler.go`, `internal/app/scheduler_test.go`

**Interfaces:**
- Produces: `app.NewScheduler(items ports.ItemStore, status ports.StatusStore, clock ports.Clock, log *slog.Logger, minGap time.Duration) *Scheduler`, `(*Scheduler).Add(f ports.SourceFetcher, interval time.Duration)`, `(*Scheduler).SourceIDs() []string`, `(*Scheduler).FetchNow(ctx, sourceID) error`, `(*Scheduler).TriggerAll() int`, `(*Scheduler).InFlight() int`, `(*Scheduler).Run(ctx)`, `app.ErrInFlight`, `app.ErrUnknownSource`.
- Consumes: `ports.SourceFetcher`, `ports.ItemStore`, `ports.StatusStore`, `ports.Clock`, `ports.ErrAuth`, `ports.AsRateLimited`, `domain.FetchStatus`.

- [ ] **Step 1: Write the failing tests**

`internal/app/scheduler_test.go`:

```go
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/clock"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/ports/memstore"
)

type fakeFetcher struct {
	id    string
	items []domain.Item
	err   error
	calls atomic.Int32
	block chan struct{} // if non-nil, Fetch waits until closed
}

func (f *fakeFetcher) ID() string   { return f.id }
func (f *fakeFetcher) Kind() string { return ports.KindGitHubRepo }
func (f *fakeFetcher) Fetch(ctx context.Context) ([]domain.Item, error) {
	f.calls.Add(1)
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.items, f.err
}

var t0 = time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

func newSched(t *testing.T) (*Scheduler, *memstore.Store, *clock.Fake) {
	t.Helper()
	st := memstore.New()
	clk := clock.NewFake(t0)
	return NewScheduler(st, st, clk, slog.Default(), 30*time.Second), st, clk
}

func TestFetchNowSuccessStoresItemsAndStatus(t *testing.T) {
	s, st, clk := newSched(t)
	f := &fakeFetcher{id: "src", items: []domain.Item{{ID: domain.ItemID{SourceID: "src", ExternalID: "a"}, Kind: domain.KindIssue, CreatedAt: t0}}}
	s.Add(f, 10*time.Minute)
	if err := s.FetchNow(context.Background(), "src"); err != nil {
		t.Fatal(err)
	}
	items, _ := st.Items(context.Background(), "src")
	if len(items) != 1 || !items[0].FirstSeen.Equal(clk.Now()) {
		t.Fatalf("items = %+v", items)
	}
	status, _ := st.Status(context.Background(), "src")
	if status == nil || !status.LastSuccess.Equal(t0) || status.ItemCount != 1 || status.InFlight || !status.NextRun.Equal(t0.Add(10*time.Minute)) || status.Kind != ports.KindGitHubRepo {
		t.Fatalf("status = %+v", status)
	}
	if err := s.FetchNow(context.Background(), "nope"); !errors.Is(err, ErrUnknownSource) {
		t.Fatalf("unknown source: %v", err)
	}
}

func TestFetchErrorsBackOffAndRecover(t *testing.T) {
	s, st, clk := newSched(t)
	f := &fakeFetcher{id: "src", err: fmt.Errorf("boom: %w", ports.ErrTransient)}
	s.Add(f, 10*time.Minute)
	ctx := context.Background()
	for i, want := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute} {
		if err := s.FetchNow(ctx, "src"); err == nil {
			t.Fatal("expected error")
		}
		status, _ := st.Status(ctx, "src")
		if got := status.NextRun.Sub(clk.Now()); got != want {
			t.Fatalf("attempt %d: backoff %v want %v", i, got, want)
		}
		if status.Healthy() || status.ErrorMsg == "" || status.AuthFailed {
			t.Fatalf("status after error: %+v", status)
		}
	}
	f.err = nil
	if err := s.FetchNow(ctx, "src"); err != nil {
		t.Fatal(err)
	}
	status, _ := st.Status(ctx, "src")
	if !status.Healthy() || status.NextRun.Sub(clk.Now()) != 10*time.Minute {
		t.Fatalf("recovery must reset backoff: %+v", status)
	}
	f.err = ports.ErrTransient
	_ = s.FetchNow(ctx, "src")
	status, _ = st.Status(ctx, "src")
	if status.NextRun.Sub(clk.Now()) != time.Minute {
		t.Fatal("backoff must restart at 1m after a success")
	}
}

func TestBackoffIsCapped(t *testing.T) {
	s, st, clk := newSched(t)
	f := &fakeFetcher{id: "src", err: ports.ErrTransient}
	s.Add(f, time.Minute)
	for i := 0; i < 10; i++ {
		_ = s.FetchNow(context.Background(), "src")
	}
	status, _ := st.Status(context.Background(), "src")
	if status.NextRun.Sub(clk.Now()) != 30*time.Minute {
		t.Fatalf("cap = %v", status.NextRun.Sub(clk.Now()))
	}
}

func TestAuthAndRateLimitErrors(t *testing.T) {
	s, st, clk := newSched(t)
	auth := &fakeFetcher{id: "auth", err: fmt.Errorf("401: %w", ports.ErrAuth)}
	rl := &fakeFetcher{id: "rl", err: &ports.RateLimitedError{ResetAt: t0.Add(17 * time.Minute)}}
	s.Add(auth, time.Minute)
	s.Add(rl, time.Minute)
	_ = s.FetchNow(context.Background(), "auth")
	_ = s.FetchNow(context.Background(), "rl")
	a, _ := st.Status(context.Background(), "auth")
	if !a.AuthFailed {
		t.Fatalf("auth failed flag: %+v", a)
	}
	r, _ := st.Status(context.Background(), "rl")
	if !r.NextRun.Equal(clk.Now().Add(18 * time.Minute)) { // reset + 1 min safety
		t.Fatalf("rate limit next run = %v", r.NextRun)
	}
}

func TestSingleFlight(t *testing.T) {
	s, _, _ := newSched(t)
	f := &fakeFetcher{id: "src", block: make(chan struct{})}
	s.Add(f, time.Minute)
	done := make(chan error, 1)
	go func() { done <- s.FetchNow(context.Background(), "src") }()
	for s.InFlight() == 0 {
		time.Sleep(time.Millisecond)
	}
	if err := s.FetchNow(context.Background(), "src"); !errors.Is(err, ErrInFlight) {
		t.Fatalf("second concurrent fetch: %v", err)
	}
	if n := s.TriggerAll(); n != 0 {
		t.Fatalf("TriggerAll must skip in-flight sources, got %d", n)
	}
	close(f.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("calls = %d", f.calls.Load())
	}
}

func TestTriggerAllRespectsMinGap(t *testing.T) {
	s, _, clk := newSched(t)
	s.Add(&fakeFetcher{id: "a"}, time.Minute)
	s.Add(&fakeFetcher{id: "b"}, time.Minute)
	if n := s.TriggerAll(); n != 2 {
		t.Fatalf("fresh sources: %d", n)
	}
	// drain wake signals so the next TriggerAll can enqueue again
	for _, sc := range s.sources {
		<-sc.wake
	}
	_ = s.FetchNow(context.Background(), "a")
	if n := s.TriggerAll(); n != 1 {
		t.Fatalf("a fetched just now → only b: %d", n)
	}
	for _, sc := range s.sources {
		select {
		case <-sc.wake:
		default:
		}
	}
	clk.Advance(31 * time.Second)
	if n := s.TriggerAll(); n != 2 {
		t.Fatalf("after min gap: %d", n)
	}
}

func TestRunLoopPollsRepeatedly(t *testing.T) {
	s, _, _ := newSched(t)
	f := &fakeFetcher{id: "src"}
	s.Add(f, 20*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	s.Run(ctx)
	if f.calls.Load() < 3 {
		t.Fatalf("expected ≥3 polls, got %d", f.calls.Load())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make go ARGS="test ./internal/app/ -run 'Fetch|Backoff|Auth|Single|Trigger|RunLoop' -v"` — Expected: FAIL (undefined).

- [ ] **Step 3: Implement**

`internal/app/scheduler.go`:

```go
// Package app contains zorgscope's use cases: scheduling fetches, taking snapshots, building the
// dashboard view, dismissing items (arc42 §5).
package app

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// Errors returned by the scheduler.
var (
	ErrInFlight      = errors.New("fetch already in flight")
	ErrUnknownSource = errors.New("unknown source")
)

const (
	fetchTimeout   = 60 * time.Second
	initialBackoff = time.Minute
	maxBackoff     = 30 * time.Minute
	staggerStep    = 500 * time.Millisecond
)

type scheduled struct {
	fetcher  ports.SourceFetcher
	interval time.Duration
	running  bool
	backoff  time.Duration
	lastRun  time.Time
	nextRun  time.Time
	wake     chan struct{}
}

// Scheduler polls every registered source on its own interval with jitter, single-flight and
// exponential backoff (arc42 §6.1, ADR-0005).
type Scheduler struct {
	items  ports.ItemStore
	status ports.StatusStore
	clock  ports.Clock
	log    *slog.Logger
	minGap time.Duration

	mu      sync.Mutex
	sources []*scheduled
	byID    map[string]*scheduled
}

// NewScheduler creates a scheduler; minGap is the minimum time between two fetches of one source
// triggered by manual refresh (FR-1.4).
func NewScheduler(items ports.ItemStore, status ports.StatusStore, clock ports.Clock, log *slog.Logger, minGap time.Duration) *Scheduler {
	return &Scheduler{items: items, status: status, clock: clock, log: log, minGap: minGap, byID: map[string]*scheduled{}}
}

// Add registers a source with its poll interval.
func (s *Scheduler) Add(f ports.SourceFetcher, interval time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := &scheduled{fetcher: f, interval: interval, wake: make(chan struct{}, 1)}
	s.sources = append(s.sources, sc)
	s.byID[f.ID()] = sc
}

// SourceIDs lists registered sources in registration order.
func (s *Scheduler) SourceIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.sources))
	for _, sc := range s.sources {
		out = append(out, sc.fetcher.ID())
	}
	return out
}

// InFlight returns the number of sources currently fetching.
func (s *Scheduler) InFlight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, sc := range s.sources {
		if sc.running {
			n++
		}
	}
	return n
}

// FetchNow fetches one source synchronously (used at startup, by tests and by wake-ups).
func (s *Scheduler) FetchNow(ctx context.Context, sourceID string) error {
	s.mu.Lock()
	sc, ok := s.byID[sourceID]
	s.mu.Unlock()
	if !ok {
		return ErrUnknownSource
	}
	return s.fetch(ctx, sc)
}

// TriggerAll wakes every idle source whose last run is older than minGap; returns how many.
func (s *Scheduler) TriggerAll() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	n := 0
	for _, sc := range s.sources {
		if sc.running || (!sc.lastRun.IsZero() && now.Sub(sc.lastRun) < s.minGap) {
			continue
		}
		select {
		case sc.wake <- struct{}{}:
			n++
		default: // already queued
		}
	}
	return n
}

// Run polls all sources until ctx is cancelled. The first fetch of each source is staggered.
func (s *Scheduler) Run(ctx context.Context) {
	s.mu.Lock()
	sources := append([]*scheduled(nil), s.sources...)
	s.mu.Unlock()
	var wg sync.WaitGroup
	for i, sc := range sources {
		wg.Add(1)
		go func(i int, sc *scheduled) {
			defer wg.Done()
			s.loop(ctx, sc, time.Duration(i)*staggerStep)
		}(i, sc)
	}
	wg.Wait()
}

func (s *Scheduler) loop(ctx context.Context, sc *scheduled, delay time.Duration) {
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-sc.wake:
			timer.Stop()
		}
		_ = s.fetch(ctx, sc) // errors are recorded in status
		delay = s.delayUntilNext(sc)
	}
}

func (s *Scheduler) delayUntilNext(sc *scheduled) time.Duration {
	s.mu.Lock()
	next := sc.nextRun
	s.mu.Unlock()
	d := next.Sub(s.clock.Now())
	if d < 10*time.Millisecond { // never busy-loop; real intervals are minutes, tests use tens of ms
		d = 10 * time.Millisecond
	}
	// ±5 % jitter avoids synchronised bursts across sources
	jitter := time.Duration(rand.Int63n(int64(d)/10+1)) - d/20
	return d + jitter
}

func (s *Scheduler) fetch(ctx context.Context, sc *scheduled) error {
	s.mu.Lock()
	if sc.running {
		s.mu.Unlock()
		return ErrInFlight
	}
	sc.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		sc.running = false
		s.mu.Unlock()
	}()

	id, kind := sc.fetcher.ID(), sc.fetcher.Kind()
	start := s.clock.Now()
	st := domain.FetchStatus{SourceID: id, Kind: kind}
	if prev, err := s.status.Status(ctx, id); err == nil && prev != nil {
		st = *prev
	}
	st.InFlight = true
	_ = s.status.RecordStatus(ctx, st)

	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	items, err := sc.fetcher.Fetch(fctx)
	cancel()
	if err == nil {
		err = s.items.ReplaceItems(ctx, id, items, s.clock.Now())
	}
	done := s.clock.Now()
	st.InFlight = false
	st.Duration = done.Sub(start)

	s.mu.Lock()
	if err == nil {
		st.LastSuccess, st.ItemCount, st.ErrorMsg, st.AuthFailed = done, len(items), "", false
		sc.backoff = 0
		st.NextRun = done.Add(sc.interval)
		s.log.Info("fetched", "source", id, "items", len(items), "duration", st.Duration)
	} else {
		st.LastError, st.ErrorMsg = done, err.Error()
		st.AuthFailed = errors.Is(err, ports.ErrAuth)
		if rl, ok := ports.AsRateLimited(err); ok && rl.ResetAt.After(done) {
			st.NextRun = rl.ResetAt.Add(time.Minute)
		} else {
			if sc.backoff == 0 {
				sc.backoff = initialBackoff
			} else {
				sc.backoff *= 2
			}
			if sc.backoff > maxBackoff {
				sc.backoff = maxBackoff
			}
			st.NextRun = done.Add(sc.backoff)
		}
		s.log.Warn("fetch failed", "source", id, "err", err, "next_run", st.NextRun)
	}
	sc.lastRun, sc.nextRun = done, st.NextRun
	s.mu.Unlock()

	_ = s.status.RecordStatus(ctx, st)
	return err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make go ARGS="test ./internal/app/ -race -v"` — Expected: PASS. `make lint` — clean (`math/rand` allowed via gosec exclude G404).

- [ ] **Step 5: Commit**

```bash
git add internal/app
git commit -m "feat(app): scheduler with per-source intervals, single-flight, backoff, manual refresh (FR-1.3, FR-1.4, QS-1.4, QS-1.7)"
```

---

### Task 10: App – Snapshotter (daily snapshot, catch‑up, prune)

**Files:**
- Create: `internal/app/snapshotter.go`, `internal/app/snapshotter_test.go`

**Interfaces:**
- Produces: `app.NewSnapshotter(items ports.ItemStore, snaps ports.SnapshotStore, status ports.StatusStore, sources func() []string, hour, minute int, loc *time.Location, retentionDays int, clock ports.Clock, log *slog.Logger) *Snapshotter`, `(*Snapshotter).RunDue(ctx) (taken int, err error)`, `(*Snapshotter).Run(ctx, every time.Duration)`.
- Consumes: `domain.SnapshotDay`, `domain.NewSnapshot`, store ports.

- [ ] **Step 1: Write the failing tests**

`internal/app/snapshotter_test.go`:

```go
package app

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/clock"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports/memstore"
)

func TestSnapshotterTakesOnePerSnapshotDay(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	ctx := context.Background()
	st := memstore.New()
	clk := clock.NewFake(time.Date(2026, 8, 16, 8, 0, 0, 0, berlin)) // 08:00 Berlin, after the 03:00 snapshot time
	_ = st.ReplaceItems(ctx, "s1", []domain.Item{{ID: domain.ItemID{SourceID: "s1", ExternalID: "a"}}}, clk.Now())
	_ = st.RecordStatus(ctx, domain.FetchStatus{SourceID: "s1", LastSuccess: clk.Now()})
	_ = st.ReplaceItems(ctx, "never", nil, clk.Now()) // no successful fetch → skipped
	sn := NewSnapshotter(st, st, st, func() []string { return []string{"s1", "never"} }, 3, 0, berlin, 30, clk, slog.Default())

	n, err := sn.RunDue(ctx)
	if err != nil || n != 1 {
		t.Fatalf("first run: %d %v", n, err)
	}
	latest, _ := st.LatestSnapshot(ctx, "s1")
	if latest == nil || latest.Date != "2026-08-16" || !latest.Contains("a") {
		t.Fatalf("latest = %+v", latest)
	}
	if got, _ := st.LatestSnapshot(ctx, "never"); got != nil {
		t.Fatal("sources without a successful fetch must not be snapshotted")
	}
	if n, _ := sn.RunDue(ctx); n != 0 {
		t.Fatal("second run same day must be a no-op")
	}
	clk.Set(time.Date(2026, 8, 17, 2, 30, 0, 0, berlin)) // before 03:00 → still snapshot day 2026-08-16
	if n, _ := sn.RunDue(ctx); n != 0 {
		t.Fatal("before snapshot time nothing is due")
	}
	clk.Set(time.Date(2026, 8, 17, 3, 0, 0, 0, berlin))
	if n, _ := sn.RunDue(ctx); n != 1 {
		t.Fatal("at snapshot time a new one is due")
	}
	// catch-up: app was down for three days → exactly one snapshot for the current snapshot day
	clk.Set(time.Date(2026, 8, 20, 12, 0, 0, 0, berlin))
	if n, _ := sn.RunDue(ctx); n != 1 {
		t.Fatal("catch-up takes one snapshot")
	}
	latest, _ = st.LatestSnapshot(ctx, "s1")
	if latest.Date != "2026-08-20" {
		t.Fatalf("catch-up date = %s", latest.Date)
	}
	if before, _ := st.SnapshotBefore(ctx, "s1", "2026-08-20"); before == nil || before.Date != "2026-08-17" {
		t.Fatalf("previous snapshot must be the last real one: %+v", before)
	}
}

func TestSnapshotterPrunes(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	ctx := context.Background()
	st := memstore.New()
	clk := clock.NewFake(time.Date(2026, 8, 16, 12, 0, 0, 0, berlin))
	_ = st.RecordStatus(ctx, domain.FetchStatus{SourceID: "s1", LastSuccess: clk.Now()})
	_ = st.PutSnapshot(ctx, domain.NewSnapshot("s1", "2026-07-01", clk.Now(), nil)) // 46 days old
	_ = st.PutSnapshot(ctx, domain.NewSnapshot("s1", "2026-08-01", clk.Now(), nil)) // 15 days old
	sn := NewSnapshotter(st, st, st, func() []string { return []string{"s1"} }, 3, 0, berlin, 30, clk, slog.Default())
	if _, err := sn.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.SnapshotBefore(ctx, "s1", "2026-08-01"); got != nil {
		t.Fatal("snapshot older than retention must be pruned")
	}
	if got, _ := st.SnapshotBefore(ctx, "s1", "2026-08-16"); got == nil || got.Date != "2026-08-01" {
		t.Fatal("snapshot within retention must survive")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make go ARGS="test ./internal/app/ -run Snapshotter -v"` — Expected: FAIL.

- [ ] **Step 3: Implement**

`internal/app/snapshotter.go`:

```go
package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// Snapshotter records one id snapshot per source and snapshot day (FR-7.1, ADR-0008).
type Snapshotter struct {
	items     ports.ItemStore
	snaps     ports.SnapshotStore
	status    ports.StatusStore
	sources   func() []string
	hour, min int
	loc       *time.Location
	retention int
	clock     ports.Clock
	log       *slog.Logger
}

// NewSnapshotter wires a snapshotter. sources returns the ids to snapshot (usually Scheduler.SourceIDs).
func NewSnapshotter(items ports.ItemStore, snaps ports.SnapshotStore, status ports.StatusStore, sources func() []string,
	hour, minute int, loc *time.Location, retentionDays int, clock ports.Clock, log *slog.Logger) *Snapshotter {
	return &Snapshotter{items: items, snaps: snaps, status: status, sources: sources, hour: hour, min: minute,
		loc: loc, retention: retentionDays, clock: clock, log: log}
}

// RunDue takes snapshots for every source that has none for the current snapshot day and prunes old
// ones. Sources without a successful fetch are skipped (an empty snapshot would be noise, not truth).
func (s *Snapshotter) RunDue(ctx context.Context) (int, error) {
	now := s.clock.Now()
	day := domain.SnapshotDay(now, s.hour, s.min, s.loc)
	taken := 0
	for _, src := range s.sources() {
		latest, err := s.snaps.LatestSnapshot(ctx, src)
		if err != nil {
			return taken, err
		}
		if latest != nil && latest.Date >= day {
			continue
		}
		st, err := s.status.Status(ctx, src)
		if err != nil {
			return taken, err
		}
		if st == nil || st.LastSuccess.IsZero() {
			continue
		}
		ids, err := s.items.ExternalIDs(ctx, src)
		if err != nil {
			return taken, err
		}
		if err := s.snaps.PutSnapshot(ctx, domain.NewSnapshot(src, day, now, ids)); err != nil {
			return taken, err
		}
		s.log.Info("snapshot taken", "source", src, "date", day, "ids", len(ids))
		taken++
	}
	dayT, err := time.ParseInLocation("2006-01-02", day, s.loc)
	if err != nil {
		return taken, err
	}
	cutoff := dayT.AddDate(0, 0, -s.retention).Format("2006-01-02")
	return taken, s.snaps.PruneSnapshots(ctx, cutoff)
}

// Run calls RunDue immediately and then every `every` until ctx is cancelled.
func (s *Snapshotter) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		if _, err := s.RunDue(ctx); err != nil {
			s.log.Error("snapshot run failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make go ARGS="test ./internal/app/ -race -v"` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app
git commit -m "feat(app): daily snapshotter with catch-up and pruning (FR-7.1, QS-1.3)"
```

---

### Task 11: App – Dashboard query, view models, dismiss actions, registry, credential book

**Files:**
- Create: `internal/app/view.go`, `internal/app/dashboard.go`, `internal/app/dashboard_test.go`, `internal/app/actions.go`, `internal/app/actions_test.go`, `internal/app/registry.go`, `internal/app/registry_test.go`, `internal/app/credentials.go`, `internal/app/credentials_test.go`

**Interfaces:**
- Produces:

```go
// view.go
type View struct { Header HeaderView; Attention AttentionView; Repos []RepoCard; Tiles []string; GeneratedAt time.Time }
type HeaderView struct { Date, Time, DataAsOf string; Sources []SourceStatusView; Refreshing int; AttentionCount int }
type SourceStatusView struct { ID, Kind, Age, Error string; Healthy, AuthFailed, InFlight bool }
type AttentionView struct { Rows []AttentionRow; Overflow, Total int }
type AttentionRow struct { ID, Source, SourceShort, Number, Title, URL, Author, Age, Bucket, Badge, Level, Kind string; UpdatedAt int64 }
type RepoCard struct { Name, ShortName, URL string; OpenIssues, OpenPRs, New, Unanswered int; Build BuildView }
type BuildView struct { State, Workflow, URL, Age string } // State: ok | failed | running | unknown
func HumanAge(d time.Duration) string
func SourceLabel(sourceID string) (label, short string)
// dashboard.go
func NewDashboard(store ports.Store, clock ports.Clock, cfg *config.Config) *Dashboard
func (d *Dashboard) Build(ctx context.Context) (View, error)
func (d *Dashboard) EvaluateAll(ctx context.Context) ([]domain.Evaluated, error)
// actions.go
func Dismiss(ctx context.Context, store ports.DismissalStore, clock ports.Clock, id domain.ItemID, updatedAt time.Time) error
func DismissAll(ctx context.Context, store ports.DismissalStore, clock ports.Clock, evs []domain.Evaluated) error
// registry.go
type Source struct { Fetcher ports.SourceFetcher; Interval time.Duration }
type Deps struct { Cfg *config.Config; HTTP *http.Client; Sink ports.CredentialSink; Log *slog.Logger }
type Builder func(d Deps) ([]Source, error)
func NewRegistry() *Registry ; (*Registry).Register(kind string, b Builder) ; (*Registry).Build(d Deps) ([]Source, error)
// credentials.go
type Credential struct { Name string; Expires *time.Time; UsedBy string; ReportedAt time.Time }
func NewCredentialBook(clock ports.Clock) *CredentialBook ; (*CredentialBook).ReportCredential(name string, expires *time.Time, usedBy string) ; (*CredentialBook).List() []Credential
```

- [ ] **Step 1: Write the failing tests**

`internal/app/dashboard_test.go`:

```go
package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/clock"
	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports/memstore"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	y := "server:\n  base_url: http://localhost:8080\ngithub:\n  enabled: true\n  me: gernotstarke\n  repos: [arc42/arc42-template, arc42/arc42.org-site]\n"
	cfg, err := config.Parse(strings.NewReader(y), func(k string) string {
		return map[string]string{"GITHUB_TOKEN": "t", "AUTH_MODE": "dev"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func gh(repo, ext string, kind domain.Kind, author string, created time.Time, lastBy string, lastAt time.Time) domain.Item {
	return domain.Item{ID: domain.ItemID{SourceID: "github:" + repo, ExternalID: ext}, Kind: kind, Title: "Title " + ext,
		URL: "https://github.com/" + repo + "/" + ext, Author: author, CreatedAt: created, UpdatedAt: created,
		LastActivityBy: lastBy, LastActivityAt: lastAt}
}

func TestDashboardBuild(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	clk := clock.NewFake(t0) // 12:00 UTC = 14:00 Berlin, snapshot day 2026-08-16
	cfg := testConfig(t)
	tpl := "github:arc42/arc42-template"
	site := "github:arc42/arc42.org-site"
	// snapshot for the previous day contains issues/1 and pulls/3 (issues/2 is new)
	_ = st.PutSnapshot(ctx, domain.NewSnapshot(tpl, "2026-08-15", t0, []string{"issues/1", "pulls/3"}))
	items := []domain.Item{
		gh("arc42/arc42-template", "issues/1", domain.KindIssue, "alice", t0.Add(-3*24*time.Hour), "", time.Time{}),                // unanswered
		gh("arc42/arc42-template", "issues/2", domain.KindIssue, "bob", t0.Add(-2*time.Hour), "", time.Time{}),                     // new (within grace → not unanswered)
		gh("arc42/arc42-template", "pulls/3", domain.KindPR, "carol", t0.Add(-5*24*time.Hour), "gernotstarke", t0.Add(-time.Hour)), // answered → aged
	}
	failed := gh("arc42/arc42-template", "runs/99", domain.KindWorkflowRun, "", t0.Add(-30*time.Minute), "", time.Time{})
	failed.Title = "build"
	failed.Payload = domain.MustPayload(domain.WorkflowRunPayload{RunID: 99, WorkflowName: "build", Conclusion: "failure", Status: "completed", Branch: "main"})
	_ = st.ReplaceItems(ctx, tpl, append(items, failed), t0)
	_ = st.RecordStatus(ctx, domain.FetchStatus{SourceID: tpl, Kind: "github-repo", LastSuccess: t0.Add(-5 * time.Minute), ItemCount: 4})
	_ = st.RecordStatus(ctx, domain.FetchStatus{SourceID: site, Kind: "github-repo", LastError: t0.Add(-time.Minute), ErrorMsg: "401", AuthFailed: true})

	d := NewDashboard(st, clk, cfg)
	v, err := d.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v.Header.Date != "Sun 16 Aug 2026" || v.Header.Time != "14:00" || v.Header.DataAsOf != "13:55" {
		t.Fatalf("header = %+v", v.Header)
	}
	if len(v.Header.Sources) != 2 || v.Header.Sources[1].ID != site || !v.Header.Sources[1].AuthFailed || v.Header.Sources[0].Age != "5m" {
		t.Fatalf("sources = %+v", v.Header.Sources)
	}
	// attention: auth failed (site), build failed, new issue 2, unanswered issue 1 — in that order
	got := make([]string, 0, len(v.Attention.Rows))
	for _, r := range v.Attention.Rows {
		got = append(got, r.Badge)
	}
	if strings.Join(got, ",") != "AUTH FAILED,BUILD FAILED,NEW,UNANSWERED" {
		t.Fatalf("attention badges = %v", got)
	}
	if v.Attention.Total != 4 || v.Header.AttentionCount != 4 || v.Attention.Overflow != 0 {
		t.Fatalf("attention counts = %+v", v.Attention)
	}
	row := v.Attention.Rows[2]
	if row.Number != "#2" || row.SourceShort != "arc42-template" || row.Age != "2h" || row.Bucket != "lt24h" || row.URL == "" || row.UpdatedAt != t0.Add(-2*time.Hour).Unix() {
		t.Fatalf("row = %+v", row)
	}
	// repo cards: template first (attention 2), then site
	if len(v.Repos) != 2 || v.Repos[0].ShortName != "arc42-template" || v.Repos[0].OpenIssues != 2 || v.Repos[0].OpenPRs != 1 ||
		v.Repos[0].New != 1 || v.Repos[0].Unanswered != 1 || v.Repos[0].Build.State != "failed" || v.Repos[0].Build.Workflow != "build" {
		t.Fatalf("repos = %+v", v.Repos)
	}
	if v.Repos[1].Build.State != "unknown" {
		t.Fatalf("no run → unknown: %+v", v.Repos[1].Build)
	}
	// dismiss the unanswered one → disappears
	if err := Dismiss(ctx, st, clk, items[0].ID, items[0].UpdatedAt); err != nil {
		t.Fatal(err)
	}
	v, _ = d.Build(ctx)
	if v.Attention.Total != 3 || v.Repos[0].Unanswered != 0 {
		t.Fatalf("after dismiss: %+v", v.Attention)
	}
	// cap
	cfg.UI.AttentionCap = 2
	v, _ = d.Build(ctx)
	if len(v.Attention.Rows) != 2 || v.Attention.Overflow != 1 || v.Attention.Total != 3 {
		t.Fatalf("cap: %+v", v.Attention)
	}
}

func TestHumanAgeAndSourceLabel(t *testing.T) {
	cases := map[time.Duration]string{30 * time.Second: "now", 5 * time.Minute: "5m", 3 * time.Hour: "3h", 47 * time.Hour: "47h", 49 * time.Hour: "2d", 40 * 24 * time.Hour: "40d"}
	for d, want := range cases {
		if got := HumanAge(d); got != want {
			t.Errorf("HumanAge(%v) = %q want %q", d, got, want)
		}
	}
	if l, s := SourceLabel("github:arc42/arc42-template"); l != "arc42/arc42-template" || s != "arc42-template" {
		t.Fatalf("label %q %q", l, s)
	}
	if l, s := SourceLabel("github:mentions"); l != "GitHub mentions" || s != "mentions" {
		t.Fatalf("label %q %q", l, s)
	}
	if l, _ := SourceLabel("watch:credentials"); l != "watch:credentials" {
		t.Fatalf("fallback label %q", l)
	}
}
```

`internal/app/actions_test.go`:

```go
package app

import (
	"context"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/clock"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports/memstore"
)

func TestDismissAll(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	clk := clock.NewFake(t0)
	evs := []domain.Evaluated{
		{Item: domain.Item{ID: domain.ItemID{SourceID: "s", ExternalID: "a"}, UpdatedAt: t0.Add(-time.Hour)}},
		{Item: domain.Item{ID: domain.ItemID{SourceID: "s", ExternalID: "b"}, UpdatedAt: t0.Add(-2 * time.Hour)}},
	}
	if err := DismissAll(ctx, st, clk, evs); err != nil {
		t.Fatal(err)
	}
	all, _ := st.Dismissals(ctx)
	if len(all) != 2 || !all[evs[1].Item.ID].DismissedAt.Equal(t0) || !all[evs[1].Item.ID].UpdatedAt.Equal(evs[1].Item.UpdatedAt) {
		t.Fatalf("dismissals = %+v", all)
	}
}
```

`internal/app/registry_test.go`:

```go
package app

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

type nopFetcher struct{ id string }

func (n nopFetcher) ID() string                                   { return n.id }
func (n nopFetcher) Kind() string                                 { return "nop" }
func (n nopFetcher) Fetch(context.Context) ([]domain.Item, error) { return nil, nil }

func TestRegistryBuildsAllKinds(t *testing.T) {
	r := NewRegistry()
	r.Register("a", func(Deps) ([]Source, error) { return []Source{{Fetcher: nopFetcher{"a1"}, Interval: time.Minute}}, nil })
	r.Register("b", func(Deps) ([]Source, error) {
		return []Source{{Fetcher: nopFetcher{"b1"}, Interval: time.Minute}, {Fetcher: nopFetcher{"b2"}, Interval: 2 * time.Minute}}, nil
	})
	srcs, err := r.Build(Deps{Log: slog.Default()})
	if err != nil || len(srcs) != 3 {
		t.Fatalf("Build: %d %v", len(srcs), err)
	}
	if srcs[0].Fetcher.ID() != "a1" || srcs[2].Interval != 2*time.Minute {
		t.Fatalf("order/intervals wrong: %+v", srcs)
	}
}
```

`internal/app/credentials_test.go`:

```go
package app

import (
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/clock"
)

func TestCredentialBook(t *testing.T) {
	clk := clock.NewFake(t0)
	b := NewCredentialBook(clk)
	exp := t0.Add(24 * time.Hour)
	b.ReportCredential("zorgscope GitHub token", &exp, "zorgscope")
	b.ReportCredential("zorgscope GitHub token", &exp, "zorgscope") // idempotent
	b.ReportCredential("other", nil, "x")
	list := b.List()
	if len(list) != 2 || list[0].Name != "other" || list[1].Expires == nil || !list[1].Expires.Equal(exp) || !list[1].ReportedAt.Equal(t0) {
		t.Fatalf("list = %+v", list)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make go ARGS="test ./internal/app/ -run 'Dashboard|HumanAge|DismissAll|Registry|CredentialBook' -v"` — Expected: FAIL.

- [ ] **Step 3: Implement**

`internal/app/view.go`:

```go
package app

import (
	"fmt"
	"strings"
	"time"
)

// View is everything the page template needs (arc42 §12 "view model": no domain logic inside).
type View struct {
	Header      HeaderView
	Attention   AttentionView
	Repos       []RepoCard
	Tiles       []string
	GeneratedAt time.Time
}

// HeaderView feeds tile_header.html.
type HeaderView struct {
	Date           string // "Sun 16 Aug 2026"
	Time           string // "14:00"
	DataAsOf       string // "13:55" or "never"
	Sources        []SourceStatusView
	Refreshing     int
	AttentionCount int
}

// SourceStatusView is one staleness dot in the header.
type SourceStatusView struct {
	ID         string
	Kind       string
	Age        string
	Error      string
	Healthy    bool
	AuthFailed bool
	InFlight   bool
}

// AttentionView feeds tile_attention.html.
type AttentionView struct {
	Rows     []AttentionRow
	Overflow int
	Total    int
}

// AttentionRow is one line in the Attention tile.
type AttentionRow struct {
	ID          string // domain.ItemID.String()
	Source      string // "arc42/arc42-template", "GitHub mentions", ...
	SourceShort string // "arc42-template"
	Number      string // "#236" or ""
	Title       string
	URL         string
	Author      string
	Age         string
	Bucket      string // lt24h | lt7d | lt30d | ge30d
	Badge       string // NEW | UNANSWERED | ...
	Level       string // css class
	Kind        string
	UpdatedAt   int64 // unix seconds, sent back with dismiss
}

// RepoCard feeds tile_repos.html.
type RepoCard struct {
	Name       string
	ShortName  string
	URL        string
	OpenIssues int
	OpenPRs    int
	New        int
	Unanswered int
	Build      BuildView
}

// BuildView is the CI status dot of a repo (FR-3.2).
type BuildView struct {
	State    string // ok | failed | running | unknown
	Workflow string
	URL      string
	Age      string
}

// HumanAge renders a duration compactly: now, 5m, 3h, 47h, 2d.
func HumanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// SourceLabel derives display names from a source id.
func SourceLabel(sourceID string) (label, short string) {
	switch {
	case sourceID == "github:mentions":
		return "GitHub mentions", "mentions"
	case strings.HasPrefix(sourceID, "github:"):
		full := strings.TrimPrefix(sourceID, "github:")
		_, name, _ := strings.Cut(full, "/")
		return full, name
	default:
		return sourceID, sourceID
	}
}
```

`internal/app/dashboard.go`:

```go
package app

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// Dashboard assembles the View from stores — never from upstream (arc42 §6.2, QG-2).
type Dashboard struct {
	store ports.Store
	clock ports.Clock
	cfg   *config.Config
}

// NewDashboard creates a dashboard query service.
func NewDashboard(store ports.Store, clock ports.Clock, cfg *config.Config) *Dashboard {
	return &Dashboard{store: store, clock: clock, cfg: cfg}
}

// EvaluateAll loads all items and evaluates each against its previous snapshot and dismissal.
// Sources in auth-failed state contribute a synthetic AUTH FAILED credential item (FR-11.3).
func (d *Dashboard) EvaluateAll(ctx context.Context) ([]domain.Evaluated, error) {
	now := d.clock.Now()
	rules := d.cfg.Rules()
	items, err := d.store.AllItems(ctx)
	if err != nil {
		return nil, err
	}
	dismissals, err := d.store.Dismissals(ctx)
	if err != nil {
		return nil, err
	}
	statuses, err := d.store.Statuses(ctx)
	if err != nil {
		return nil, err
	}
	for _, st := range statuses {
		if st.AuthFailed {
			label, _ := SourceLabel(st.SourceID)
			items = append(items, domain.Item{
				ID: domain.ItemID{SourceID: "watch:auth", ExternalID: st.SourceID}, Kind: domain.KindCredential,
				Title: "AUTH FAILED: " + label + " — " + st.ErrorMsg, CreatedAt: st.LastError,
				UpdatedAt: st.LastError.Truncate(24 * time.Hour), // dismissal holds for the day, re-appears next day if still failing
				Payload:   domain.MustPayload(domain.CredentialPayload{AuthFailed: true, UsedBy: st.SourceID}),
			})
		}
	}
	day := domain.SnapshotDay(now, d.cfg.Snapshot.Hour, d.cfg.Snapshot.Minute, d.cfg.Server.Location)
	prevBySource := map[string]*domain.Snapshot{}
	out := make([]domain.Evaluated, 0, len(items))
	for _, it := range items {
		prev, ok := prevBySource[it.ID.SourceID]
		if !ok {
			prev, err = d.store.SnapshotBefore(ctx, it.ID.SourceID, day)
			if err != nil {
				return nil, err
			}
			prevBySource[it.ID.SourceID] = prev
		}
		var dis *domain.Dismissal
		if dd, ok := dismissals[it.ID]; ok {
			dis = &dd
		}
		out = append(out, domain.Evaluated{Item: it, Eval: rules.Evaluate(it, prev, dis, now)})
	}
	return out, nil
}

// Build produces the complete view.
func (d *Dashboard) Build(ctx context.Context) (View, error) {
	now := d.clock.Now()
	loc := d.cfg.Server.Location
	evs, err := d.EvaluateAll(ctx)
	if err != nil {
		return View{}, err
	}
	statuses, err := d.store.Statuses(ctx)
	if err != nil {
		return View{}, err
	}
	v := View{GeneratedAt: now, Tiles: d.cfg.UI.Tiles}
	v.Header = d.header(now, loc, statuses)
	v.Attention = d.attention(evs, now)
	v.Header.AttentionCount = v.Attention.Total
	v.Repos = d.repos(evs, now)
	return v, nil
}

func (d *Dashboard) header(now time.Time, loc *time.Location, statuses []domain.FetchStatus) HeaderView {
	h := HeaderView{Date: now.In(loc).Format("Mon 2 Jan 2006"), Time: now.In(loc).Format("15:04"), DataAsOf: "never"}
	var latest time.Time
	for _, st := range statuses {
		sv := SourceStatusView{ID: st.SourceID, Kind: st.Kind, Healthy: st.Healthy(), AuthFailed: st.AuthFailed, InFlight: st.InFlight, Error: st.ErrorMsg}
		if !st.LastSuccess.IsZero() {
			sv.Age = HumanAge(now.Sub(st.LastSuccess))
			if st.LastSuccess.After(latest) {
				latest = st.LastSuccess
			}
		} else {
			sv.Age = "never"
		}
		if st.InFlight {
			h.Refreshing++
		}
		h.Sources = append(h.Sources, sv)
	}
	if !latest.IsZero() {
		h.DataAsOf = latest.In(loc).Format("15:04")
	}
	return h
}

func (d *Dashboard) attention(evs []domain.Evaluated, now time.Time) AttentionView {
	list := domain.FilterAttention(evs)
	domain.SortByUrgency(list)
	shown, overflow := domain.Cap(list, d.cfg.UI.AttentionCap)
	av := AttentionView{Total: len(list), Overflow: overflow}
	for _, e := range shown {
		label, short := SourceLabel(e.Item.ID.SourceID)
		row := AttentionRow{ID: e.Item.ID.String(), Source: label, SourceShort: short, Title: e.Item.Title, URL: e.Item.URL,
			Author: e.Item.Author, Age: HumanAge(now.Sub(e.Item.CreatedAt)), Bucket: e.Eval.Bucket.String(),
			Badge: e.Eval.Level.Badge(), Level: e.Eval.Level.String(), Kind: string(e.Item.Kind), UpdatedAt: e.Item.UpdatedAt.Unix()}
		if e.Item.Kind == domain.KindIssue || e.Item.Kind == domain.KindPR {
			if _, num, ok := strings.Cut(e.Item.ID.ExternalID, "/"); ok {
				row.Number = "#" + num
			}
		}
		av.Rows = append(av.Rows, row)
	}
	return av
}

func (d *Dashboard) repos(evs []domain.Evaluated, now time.Time) []RepoCard {
	bySource := map[string][]domain.Evaluated{}
	for _, e := range evs {
		bySource[e.Item.ID.SourceID] = append(bySource[e.Item.ID.SourceID], e)
	}
	cards := make([]RepoCard, 0, len(d.cfg.GitHub.Repos))
	for _, r := range d.cfg.GitHub.Repos {
		_, short := SourceLabel("github:" + r.Name)
		card := RepoCard{Name: r.Name, ShortName: short, URL: "https://github.com/" + r.Name, Build: BuildView{State: "unknown"}}
		var run *domain.Evaluated
		for i, e := range bySource["github:"+r.Name] {
			switch e.Item.Kind {
			case domain.KindIssue:
				card.OpenIssues++
			case domain.KindPR:
				card.OpenPRs++
			case domain.KindWorkflowRun:
				if run == nil || e.Item.CreatedAt.After(run.Item.CreatedAt) {
					run = &bySource["github:"+r.Name][i]
				}
				continue
			default:
				continue
			}
			if e.Eval.New && !e.Eval.Dismissed {
				card.New++
			}
			if e.Eval.Unanswered && !e.Eval.Dismissed {
				card.Unanswered++
			}
		}
		if run != nil {
			p, _ := domain.DecodePayload[domain.WorkflowRunPayload](run.Item)
			card.Build = BuildView{Workflow: p.WorkflowName, URL: run.Item.URL, Age: HumanAge(now.Sub(run.Item.CreatedAt))}
			switch {
			case p.Status != "completed":
				card.Build.State = "running"
			case p.Conclusion == "success":
				card.Build.State = "ok"
			case p.Conclusion == "failure":
				card.Build.State = "failed"
			default:
				card.Build.State = "unknown"
			}
		}
		cards = append(cards, card)
	}
	sort.SliceStable(cards, func(i, j int) bool {
		ai, aj := cards[i].New+cards[i].Unanswered, cards[j].New+cards[j].Unanswered
		if ai != aj {
			return ai > aj
		}
		return cards[i].Name < cards[j].Name
	})
	return cards
}
```

`internal/app/actions.go`:

```go
package app

import (
	"context"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// Dismiss records that the user has seen an item in its current state (FR-2.7).
func Dismiss(ctx context.Context, store ports.DismissalStore, clock ports.Clock, id domain.ItemID, updatedAt time.Time) error {
	return store.PutDismissal(ctx, domain.Dismissal{ID: id, UpdatedAt: updatedAt, DismissedAt: clock.Now()})
}

// DismissAll dismisses every given item (FR-2.7 AC4).
func DismissAll(ctx context.Context, store ports.DismissalStore, clock ports.Clock, evs []domain.Evaluated) error {
	for _, e := range evs {
		if err := Dismiss(ctx, store, clock, e.Item.ID, e.Item.UpdatedAt); err != nil {
			return err
		}
	}
	return nil
}
```

`internal/app/registry.go`:

```go
package app

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// Source is a fetcher with its poll interval.
type Source struct {
	Fetcher  ports.SourceFetcher
	Interval time.Duration
}

// Deps is what builders get to construct adapters.
type Deps struct {
	Cfg  *config.Config
	HTTP *http.Client
	Sink ports.CredentialSink
	Log  *slog.Logger
}

// Builder constructs the sources of one kind from config; it returns nothing when the kind is disabled.
type Builder func(d Deps) ([]Source, error)

// Registry maps source kinds to builders — the single place to register a new kind (QS-4.2).
type Registry struct {
	kinds    []string
	builders map[string]Builder
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry { return &Registry{builders: map[string]Builder{}} }

// Register adds a builder for a kind (registration order = build order).
func (r *Registry) Register(kind string, b Builder) {
	if _, dup := r.builders[kind]; !dup {
		r.kinds = append(r.kinds, kind)
	}
	r.builders[kind] = b
}

// Build runs every builder.
func (r *Registry) Build(d Deps) ([]Source, error) {
	var out []Source
	for _, kind := range r.kinds {
		srcs, err := r.builders[kind](d)
		if err != nil {
			return nil, fmt.Errorf("build sources for %s: %w", kind, err)
		}
		out = append(out, srcs...)
	}
	return out, nil
}
```

`internal/app/credentials.go`:

```go
package app

import (
	"sort"
	"sync"
	"time"

	"github.com/gernotstarke/zorgscope/internal/ports"
)

// Credential is an expiry reported by an adapter (FR-11.2).
type Credential struct {
	Name       string
	Expires    *time.Time
	UsedBy     string
	ReportedAt time.Time
}

// CredentialBook collects auto-detected credential expiries; the watch source (M2) reads them.
type CredentialBook struct {
	mu      sync.Mutex
	clock   ports.Clock
	entries map[string]Credential
}

var _ ports.CredentialSink = (*CredentialBook)(nil)

// NewCredentialBook creates an empty book.
func NewCredentialBook(clock ports.Clock) *CredentialBook {
	return &CredentialBook{clock: clock, entries: map[string]Credential{}}
}

// ReportCredential implements ports.CredentialSink (idempotent by name).
func (b *CredentialBook) ReportCredential(name string, expires *time.Time, usedBy string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries[name] = Credential{Name: name, Expires: expires, UsedBy: usedBy, ReportedAt: b.clock.Now()}
}

// List returns all credentials sorted by name.
func (b *CredentialBook) List() []Credential {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Credential, 0, len(b.entries))
	for _, c := range b.entries {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make go ARGS="test ./internal/app/ -race -v"` — Expected: PASS. `make lint` — clean (app imports only domain/ports/config + std lib).

- [ ] **Step 5: Commit**

```bash
git add internal/app
git commit -m "feat(app): dashboard query and view models, dismiss actions, source registry, credential book (FR-2.5, FR-3.1, FR-3.2, FR-11.3)"
```

---

### Task 12: Fake GitHub server (test double for adapter tests, e2e and demo)

**Files:**
- Create: `test/fakes/github/server.go`, `test/fakes/github/seed.go`, `test/fakes/github/server_test.go`

**Interfaces:**
- Produces (package `githubfake`, import path `github.com/gernotstarke/zorgscope/test/fakes/github`):

```go
type Comment struct { Author string; At time.Time }
type Issue struct { Number int; Title, URL, Author string; CreatedAt, UpdatedAt time.Time; Labels []string; Comments []Comment; IsPR, Draft bool; ReviewDecision string; Reviews []Comment }
type Run struct { ID int64; Name, Status, Conclusion, Branch, URL string; CreatedAt, UpdatedAt time.Time }
type Notification struct { ID, Reason, SubjectTitle, SubjectURL, SubjectType, Repo string; UpdatedAt time.Time; Unread bool }
type Repo struct { Owner, Name, DefaultBranch string; Issues []Issue; Runs []Run }
func New() *Server
func (s *Server) Handler() http.Handler
func (s *Server) AddRepo(r Repo)                       // replaces existing owner/name
func (s *Server) AddIssue(repo string, i Issue)        // upsert by number
func (s *Server) SetRuns(repo string, runs []Run)
func (s *Server) SetNotifications(n []Notification)
func (s *Server) SetTokenExpiry(t time.Time)           // adds GitHub-Authentication-Token-Expiration header
func (s *Server) FailNext(status int, rateLimited bool) // next request fails once
func (s *Server) Requests() int                        // number of API requests served
func (s *Server) Reset()                               // back to empty
func Seed(s *Server, now time.Time)                    // deterministic demo data (see seed.go)
```

- Routes: `POST /graphql` (issues/PRs, requires `Authorization: Bearer …`), `GET /repos/{owner}/{repo}/actions/runs`, `GET /notifications`, control API `POST /__control/issues` (`{"repo":"o/n","issue":{Issue JSON}}`), `POST /__control/runs` (`{"repo":"o/n","runs":[Run JSON]}`), `POST /__control/fail` (`{"status":403,"rate_limited":true}`), `POST /__control/reset` (reseed with `Seed`).

- [ ] **Step 1: Write the failing test**

`test/fakes/github/server_test.go`:

```go
package githubfake

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func gql(t *testing.T, srv *httptest.Server, token string, vars map[string]any) (*http.Response, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"query": "query { repository(owner:$owner,name:$name) {...} }", "variables": vars})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/graphql", bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	_ = resp.Body.Close()
	return resp, out
}

func TestGraphQLIssuesAndPagination(t *testing.T) {
	s := New()
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	Seed(s, now)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, _ := gql(t, srv, "", map[string]any{"owner": "arc42", "name": "arc42-template", "n": 100, "withIssues": true, "withPRs": true})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token → 401, got %d", resp.StatusCode)
	}
	resp, out := gql(t, srv, "tok", map[string]any{"owner": "arc42", "name": "arc42-template", "n": 2, "withIssues": true, "withPRs": true})
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	repo := out["data"].(map[string]any)["repository"].(map[string]any)
	issues := repo["issues"].(map[string]any)
	if len(issues["nodes"].([]any)) != 2 || issues["pageInfo"].(map[string]any)["hasNextPage"] != true {
		t.Fatalf("page 1 = %v", issues)
	}
	cursor := issues["pageInfo"].(map[string]any)["endCursor"].(string)
	_, out = gql(t, srv, "tok", map[string]any{"owner": "arc42", "name": "arc42-template", "n": 2, "withIssues": true, "withPRs": false, "issuesAfter": cursor})
	issues = out["data"].(map[string]any)["repository"].(map[string]any)["issues"].(map[string]any)
	if len(issues["nodes"].([]any)) != 1 || issues["pageInfo"].(map[string]any)["hasNextPage"] != false {
		t.Fatalf("page 2 = %v", issues)
	}
	if _, ok := out["data"].(map[string]any)["repository"].(map[string]any)["pullRequests"]; ok {
		t.Fatal("withPRs=false must omit pullRequests")
	}
	_, out = gql(t, srv, "tok", map[string]any{"owner": "nobody", "name": "nothing", "n": 2, "withIssues": true, "withPRs": true})
	if out["data"].(map[string]any)["repository"] != nil {
		t.Fatal("unknown repo → repository null")
	}
	if s.Requests() < 4 {
		t.Fatalf("requests = %d", s.Requests())
	}
}

func TestRunsNotificationsAndControl(t *testing.T) {
	s := New()
	Seed(s, time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC))
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	get := func(path string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer tok")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		_ = resp.Body.Close()
		return resp, b.String()
	}
	resp, body := get("/repos/arc42/arc42.org-site/actions/runs?branch=main&per_page=1")
	if resp.StatusCode != 200 || !strings.Contains(body, `"conclusion":"failure"`) {
		t.Fatalf("runs: %d %s", resp.StatusCode, body)
	}
	resp, body = get("/notifications?participating=true")
	if resp.StatusCode != 200 || !strings.Contains(body, `"reason":"mention"`) {
		t.Fatalf("notifications: %d %s", resp.StatusCode, body)
	}
	// control: inject an issue, then it shows up
	payload := `{"repo":"arc42/arc42-template","issue":{"Number":999,"Title":"Injected","Author":"tester","CreatedAt":"2026-08-16T11:00:00Z","UpdatedAt":"2026-08-16T11:00:00Z"}}`
	resp, err := http.Post(srv.URL+"/__control/issues", "application/json", strings.NewReader(payload))
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("control issues: %v %d", err, resp.StatusCode)
	}
	_, out := gql(t, srv, "tok", map[string]any{"owner": "arc42", "name": "arc42-template", "n": 100, "withIssues": true, "withPRs": false})
	if !strings.Contains(mustJSON(out), "Injected") {
		t.Fatal("injected issue missing")
	}
	// fail next
	resp, _ = http.Post(srv.URL+"/__control/fail", "application/json", strings.NewReader(`{"status":403,"rate_limited":true}`))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatal("control fail")
	}
	resp, _ = get("/notifications")
	if resp.StatusCode != 403 || resp.Header.Get("X-RateLimit-Remaining") != "0" || resp.Header.Get("X-RateLimit-Reset") == "" {
		t.Fatalf("forced failure: %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = get("/notifications")
	if resp.StatusCode != 200 {
		t.Fatal("failure is one-shot")
	}
	s.SetTokenExpiry(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	resp, _ = get("/notifications")
	if resp.Header.Get("GitHub-Authentication-Token-Expiration") != "2026-09-01 00:00:00 UTC" {
		t.Fatalf("expiry header = %q", resp.Header.Get("GitHub-Authentication-Token-Expiration"))
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make go ARGS="test ./test/fakes/github/ -v"` — Expected: FAIL (undefined).

- [ ] **Step 3: Implement**

`test/fakes/github/server.go`:

```go
// Package githubfake is a deterministic in-memory imitation of the parts of the GitHub API zorgscope
// uses (GraphQL issues/PRs, REST workflow runs and notifications). It serves adapter tests, e2e tests
// and the local demo (ADR-0010).
package githubfake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Comment is a comment or review by Author at At.
type Comment struct {
	Author string
	At     time.Time
}

// Issue is an issue or (IsPR) pull request.
type Issue struct {
	Number         int
	Title          string
	URL            string
	Author         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Labels         []string
	Comments       []Comment
	IsPR           bool
	Draft          bool
	ReviewDecision string
	Reviews        []Comment
}

// Run is a workflow run.
type Run struct {
	ID         int64
	Name       string
	Status     string
	Conclusion string
	Branch     string
	URL        string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Notification is a notification thread.
type Notification struct {
	ID           string
	Reason       string
	SubjectTitle string
	SubjectURL   string
	SubjectType  string
	Repo         string
	UpdatedAt    time.Time
	Unread       bool
}

// Repo is a repository with its open issues/PRs and runs.
type Repo struct {
	Owner, Name   string
	DefaultBranch string
	Issues        []Issue
	Runs          []Run
}

// Server holds the fake state. Safe for concurrent use.
type Server struct {
	mu            sync.Mutex
	repos         map[string]*Repo
	notifications []Notification
	tokenExpiry   *time.Time
	failStatus    int
	failRateLimit bool
	requests      int
	seedNow       time.Time
}

// New returns an empty server.
func New() *Server { return &Server{repos: map[string]*Repo{}} }

// Reset clears all state.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repos = map[string]*Repo{}
	s.notifications = nil
	s.tokenExpiry = nil
	s.failStatus = 0
}

// AddRepo adds or replaces a repo.
func (s *Server) AddRepo(r Repo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.DefaultBranch == "" {
		r.DefaultBranch = "main"
	}
	for k := range r.Issues {
		if r.Issues[k].URL == "" {
			r.Issues[k].URL = issueURL(r.Owner+"/"+r.Name, r.Issues[k])
		}
	}
	rr := r
	s.repos[r.Owner+"/"+r.Name] = &rr
}

func issueURL(repo string, i Issue) string {
	kind := "issues"
	if i.IsPR {
		kind = "pull"
	}
	return fmt.Sprintf("https://github.com/%s/%s/%d", repo, kind, i.Number)
}

// AddIssue upserts an issue by number; the repo is created if missing.
func (s *Server) AddIssue(repo string, i Issue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.repos[repo]
	if r == nil {
		owner, name, _ := strings.Cut(repo, "/")
		r = &Repo{Owner: owner, Name: name, DefaultBranch: "main"}
		s.repos[repo] = r
	}
	if i.URL == "" {
		i.URL = issueURL(repo, i)
	}
	for k := range r.Issues {
		if r.Issues[k].Number == i.Number {
			r.Issues[k] = i
			return
		}
	}
	r.Issues = append(r.Issues, i)
}

// SetRuns replaces the runs of a repo.
func (s *Server) SetRuns(repo string, runs []Run) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.repos[repo]; r != nil {
		r.Runs = runs
	}
}

// SetNotifications replaces notifications.
func (s *Server) SetNotifications(n []Notification) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifications = n
}

// SetTokenExpiry makes every response carry the token expiration header.
func (s *Server) SetTokenExpiry(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokenExpiry = &t
}

// FailNext makes the next API request fail with status (rate-limit headers when rateLimited).
func (s *Server) FailNext(status int, rateLimited bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failStatus, s.failRateLimit = status, rateLimited
}

// Requests counts API requests served (control endpoints excluded).
func (s *Server) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /graphql", s.api(s.graphql))
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/runs", s.api(s.runs))
	mux.HandleFunc("GET /notifications", s.api(s.notificationsHandler))
	mux.HandleFunc("POST /__control/issues", s.controlIssues)
	mux.HandleFunc("POST /__control/runs", s.controlRuns)
	mux.HandleFunc("POST /__control/fail", s.controlFail)
	mux.HandleFunc("POST /__control/reset", s.controlReset)
	return mux
}

// api wraps API handlers with auth, forced failures, counting and common headers.
func (s *Server) api(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests++
		fail, rl := s.failStatus, s.failRateLimit
		s.failStatus = 0
		exp := s.tokenExpiry
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-RateLimit-Limit", "5000")
		if exp != nil {
			w.Header().Set("GitHub-Authentication-Token-Expiration", exp.UTC().Format("2006-01-02 15:04:05 MST"))
		}
		if fail != 0 {
			if rl {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(15*time.Minute).Unix(), 10))
			}
			w.WriteHeader(fail)
			_, _ = fmt.Fprintf(w, `{"message":"forced failure %d"}`, fail)
			return
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(strings.ToLower(auth), "bearer ") || len(auth) <= 7 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		w.Header().Set("X-RateLimit-Remaining", "4999")
		h(w, r)
	}
}

func actor(login string) any {
	if login == "" {
		return nil
	}
	return map[string]any{"login": login}
}

func (s *Server) issueNode(i Issue) map[string]any {
	labels := make([]any, 0, len(i.Labels))
	for _, l := range i.Labels {
		labels = append(labels, map[string]any{"name": l})
	}
	node := map[string]any{
		"number": i.Number, "title": i.Title, "url": i.URL, "createdAt": i.CreatedAt.UTC().Format(time.RFC3339),
		"updatedAt": i.UpdatedAt.UTC().Format(time.RFC3339), "author": actor(i.Author),
		"labels":   map[string]any{"nodes": labels},
		"comments": map[string]any{"totalCount": len(i.Comments), "nodes": lastComment(i.Comments, "createdAt")},
	}
	if i.IsPR {
		node["isDraft"] = i.Draft
		node["reviewDecision"] = i.ReviewDecision
		node["reviews"] = map[string]any{"nodes": lastComment(i.Reviews, "submittedAt")}
	}
	return node
}

func lastComment(cs []Comment, timeKey string) []any {
	if len(cs) == 0 {
		return []any{}
	}
	c := cs[len(cs)-1]
	return []any{map[string]any{"author": actor(c.Author), timeKey: c.At.UTC().Format(time.RFC3339)}}
}

func (s *Server) page(list []Issue, after any, n int) map[string]any {
	sort.SliceStable(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
	start := 0
	if c, ok := after.(string); ok && c != "" {
		start, _ = strconv.Atoi(c)
	}
	if n <= 0 {
		n = 100
	}
	end := start + n
	if end > len(list) {
		end = len(list)
	}
	nodes := make([]any, 0)
	for _, i := range list[start:end] {
		nodes = append(nodes, s.issueNode(i))
	}
	return map[string]any{
		"pageInfo": map[string]any{"hasNextPage": end < len(list), "endCursor": strconv.Itoa(end)},
		"nodes":    nodes,
	}
}

func (s *Server) graphql(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"message":"bad json"}`, http.StatusBadRequest)
		return
	}
	v := req.Variables
	owner, _ := v["owner"].(string)
	name, _ := v["name"].(string)
	n := 100
	if f, ok := v["n"].(float64); ok {
		n = int(f)
	}
	withIssues, _ := v["withIssues"].(bool)
	withPRs, _ := v["withPRs"].(bool)

	s.mu.Lock()
	repo := s.repos[owner+"/"+name]
	var issues, prs []Issue
	var defaultBranch string
	if repo != nil {
		defaultBranch = repo.DefaultBranch
		for _, i := range repo.Issues {
			if i.IsPR {
				prs = append(prs, i)
			} else {
				issues = append(issues, i)
			}
		}
	}
	s.mu.Unlock()

	data := map[string]any{"rateLimit": map[string]any{"cost": 1, "remaining": 4999, "resetAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}}
	if repo == nil {
		data["repository"] = nil
	} else {
		rm := map[string]any{"nameWithOwner": owner + "/" + name, "defaultBranchRef": map[string]any{"name": defaultBranch}}
		if withIssues {
			rm["issues"] = s.page(issues, v["issuesAfter"], n)
		}
		if withPRs {
			rm["pullRequests"] = s.page(prs, v["prsAfter"], n)
		}
		data["repository"] = rm
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func (s *Server) runs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	repo := s.repos[r.PathValue("owner")+"/"+r.PathValue("repo")]
	var runs []Run
	if repo != nil {
		runs = append(runs, repo.Runs...)
	}
	s.mu.Unlock()
	if repo == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		return
	}
	branch := r.URL.Query().Get("branch")
	list := make([]any, 0)
	for _, run := range runs {
		if branch != "" && run.Branch != branch {
			continue
		}
		list = append(list, map[string]any{"id": run.ID, "name": run.Name, "html_url": run.URL, "status": run.Status,
			"conclusion": run.Conclusion, "head_branch": run.Branch, "created_at": run.CreatedAt.UTC().Format(time.RFC3339),
			"updated_at": run.UpdatedAt.UTC().Format(time.RFC3339), "run_started_at": run.CreatedAt.UTC().Format(time.RFC3339)})
	}
	if per := r.URL.Query().Get("per_page"); per != "" {
		if p, err := strconv.Atoi(per); err == nil && p < len(list) {
			list = list[:p]
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(list), "workflow_runs": list})
}

func (s *Server) notificationsHandler(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	ns := append([]Notification(nil), s.notifications...)
	s.mu.Unlock()
	list := make([]any, 0, len(ns))
	for _, n := range ns {
		list = append(list, map[string]any{"id": n.ID, "reason": n.Reason, "unread": n.Unread,
			"updated_at": n.UpdatedAt.UTC().Format(time.RFC3339),
			"subject":    map[string]any{"title": n.SubjectTitle, "url": n.SubjectURL, "type": n.SubjectType},
			"repository": map[string]any{"full_name": n.Repo}})
	}
	_ = json.NewEncoder(w).Encode(list)
}

func (s *Server) controlIssues(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Repo  string `json:"repo"`
		Issue Issue  `json:"issue"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Repo == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.AddIssue(req.Repo, req.Issue)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) controlRuns(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Repo string `json:"repo"`
		Runs []Run  `json:"runs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Repo == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.SetRuns(req.Repo, req.Runs)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) controlFail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Status      int  `json:"status"`
		RateLimited bool `json:"rate_limited"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Status == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.FailNext(req.Status, req.RateLimited)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) controlReset(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	now := s.seedNow
	s.mu.Unlock()
	s.Reset()
	if now.IsZero() {
		now = time.Now()
	}
	Seed(s, now)
	w.WriteHeader(http.StatusNoContent)
}
```

`test/fakes/github/seed.go`:

```go
package githubfake

import "time"

// Seed fills the server with a small, deterministic data set relative to now:
//   - arc42/arc42-template: #236 answered by gernotstarke (aged), #240 opened 2 h ago without
//     comments (NEW, inside grace), #233 30 d old with last comment by the opener (UNANSWERED),
//     PR #237 20 d old, no comments (UNANSWERED); latest run on master succeeded.
//   - arc42/arc42.org-site: #12 answered; latest run on main FAILED.
//   - one mention notification in an unmonitored repo.
func Seed(s *Server, now time.Time) {
	s.mu.Lock()
	s.seedNow = now
	s.mu.Unlock()
	d := func(h float64) time.Time { return now.Add(-time.Duration(h * float64(time.Hour))) }
	s.AddRepo(Repo{Owner: "arc42", Name: "arc42-template", DefaultBranch: "master",
		Issues: []Issue{
			{Number: 236, Title: "Add/Replace images/arc42-logo.png with high quality image (vector?)", Author: "lwbt", CreatedAt: d(24 * 65), UpdatedAt: d(24 * 51),
				Comments: []Comment{{Author: "gernotstarke", At: d(24 * 51)}}},
			{Number: 240, Title: "Typo in section 8 of the EN template", Author: "newcomer", CreatedAt: d(2), UpdatedAt: d(2)},
			{Number: 233, Title: "Consider using GitHub Releases instead of committing build artifacts", Author: "lwbt", CreatedAt: d(24 * 30), UpdatedAt: d(24 * 29),
				Labels: []string{"enhancement"}, Comments: []Comment{{Author: "gernotstarke", At: d(24 * 29.5)}, {Author: "lwbt", At: d(24 * 29)}}},
			{Number: 237, Title: "Add example stakeholder table to EN Section 1", Author: "Sofeso", CreatedAt: d(24 * 20), UpdatedAt: d(24 * 19),
				IsPR: true, ReviewDecision: "REVIEW_REQUIRED", Labels: []string{"documentation"}},
		},
		Runs: []Run{{ID: 1001, Name: "build", Status: "completed", Conclusion: "success", Branch: "master",
			URL: "https://github.com/arc42/arc42-template/actions/runs/1001", CreatedAt: d(6), UpdatedAt: d(5.9)}},
	})
	s.AddRepo(Repo{Owner: "arc42", Name: "arc42.org-site", DefaultBranch: "main",
		Issues: []Issue{{Number: 12, Title: "Broken link on downloads page", Author: "visitor", CreatedAt: d(24 * 3), UpdatedAt: d(24 * 2),
			Comments: []Comment{{Author: "gernotstarke", At: d(24 * 2)}}}},
		Runs: []Run{{ID: 2002, Name: "deploy", Status: "completed", Conclusion: "failure", Branch: "main",
			URL: "https://github.com/arc42/arc42.org-site/actions/runs/2002", CreatedAt: d(1), UpdatedAt: d(0.9)}},
	})
	s.SetNotifications([]Notification{{ID: "n-1", Reason: "mention", SubjectTitle: "Would love your view on quality scenarios", SubjectType: "Issue",
		SubjectURL: "https://api.github.com/repos/someone/architecture-notes/issues/7", Repo: "someone/architecture-notes", UpdatedAt: d(3), Unread: true}})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make go ARGS="test ./test/fakes/github/ -race -v"` — Expected: PASS. `make lint` — clean (this package is test infrastructure; `time.Now()` is allowed here).

- [ ] **Step 5: Commit**

```bash
git add test/fakes/github
git commit -m "test(fakes): deterministic fake GitHub server with control API (ADR-0010)"
```

---

### Task 13: GitHub adapter – client, repo fetcher (GraphQL + runs), mentions fetcher

**Files:**
- Create: `internal/adapters/github/client.go`, `internal/adapters/github/repo.go`, `internal/adapters/github/mentions.go`, `internal/adapters/github/repo_test.go`, `internal/adapters/github/mentions_test.go`

**Interfaces:**
- Produces: `github.NewClient(hc *http.Client, baseURL, token string, sink ports.CredentialSink) *Client`, `github.NewRepoFetcher(c *Client, fullName string) (*RepoFetcher, error)` (`ID()` = `"github:" + fullName`, `Kind()` = `ports.KindGitHubRepo`, exported field `PageSize int` default 100), `github.NewMentionsFetcher(c *Client, excludeRepos []string) *MentionsFetcher` (`ID()` = `"github:mentions"`, `Kind()` = `ports.KindGitHubMentions`), `github.TokenCredentialName = "zorgscope GitHub token"`.
- External ids: `issues/<n>`, `pulls/<n>`, `runs/<id>`, mentions: notification thread id.
- Consumes: `ports.*` errors, `domain.Item` + payloads, `githubfake` (tests only).

- [ ] **Step 1: Write the failing tests**

`internal/adapters/github/repo_test.go`:

```go
package github

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	githubfake "github.com/gernotstarke/zorgscope/test/fakes/github"
)

type sinkRec struct {
	name   string
	exp    *time.Time
	usedBy string
	calls  int
}

func (s *sinkRec) ReportCredential(name string, exp *time.Time, usedBy string) {
	s.name, s.exp, s.usedBy = name, exp, usedBy
	s.calls++
}

var now0 = time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

func newFake(t *testing.T) (*githubfake.Server, *httptest.Server) {
	t.Helper()
	fs := githubfake.New()
	githubfake.Seed(fs, now0)
	srv := httptest.NewServer(fs.Handler())
	t.Cleanup(srv.Close)
	return fs, srv
}

func byExt(items []domain.Item) map[string]domain.Item {
	m := map[string]domain.Item{}
	for _, it := range items {
		m[it.ID.ExternalID] = it
	}
	return m
}

func TestRepoFetcherMapsIssuesPRsAndRun(t *testing.T) {
	_, srv := newFake(t)
	c := NewClient(srv.Client(), srv.URL, "tok", nil)
	f, err := NewRepoFetcher(c, "arc42/arc42-template")
	if err != nil {
		t.Fatal(err)
	}
	if f.ID() != "github:arc42/arc42-template" || f.Kind() != ports.KindGitHubRepo {
		t.Fatalf("id/kind: %s %s", f.ID(), f.Kind())
	}
	items, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := byExt(items)
	if len(m) != 5 { // 3 issues + 1 PR + 1 run
		t.Fatalf("items = %d: %v", len(m), m)
	}
	i236 := m["issues/236"]
	if i236.Kind != domain.KindIssue || i236.Author != "lwbt" || i236.LastActivityBy != "gernotstarke" || i236.URL != "https://github.com/arc42/arc42-template/issues/236" ||
		i236.ID.SourceID != f.ID() || i236.LastActivityAt.IsZero() {
		t.Fatalf("issue 236 = %+v", i236)
	}
	p, _ := domain.DecodePayload[domain.IssuePayload](i236)
	if p.Comments != 1 {
		t.Fatalf("issue payload = %+v", p)
	}
	i233 := m["issues/233"]
	if i233.LastActivityBy != "lwbt" || len(i233.Labels) != 1 || i233.Labels[0] != "enhancement" {
		t.Fatalf("issue 233 = %+v", i233)
	}
	pr := m["pulls/237"]
	if pr.Kind != domain.KindPR || pr.Author != "Sofeso" || pr.LastActivityBy != "" {
		t.Fatalf("pr = %+v", pr)
	}
	pp, _ := domain.DecodePayload[domain.PRPayload](pr)
	if pp.ReviewDecision != "REVIEW_REQUIRED" || pp.Draft {
		t.Fatalf("pr payload = %+v", pp)
	}
	run := m["runs/1001"]
	if run.Kind != domain.KindWorkflowRun || run.Title != "build" || run.URL == "" {
		t.Fatalf("run = %+v", run)
	}
	rp, _ := domain.DecodePayload[domain.WorkflowRunPayload](run)
	if rp.Conclusion != "success" || rp.Branch != "master" || rp.RunID != 1001 || rp.WorkflowName != "build" {
		t.Fatalf("run payload = %+v", rp)
	}
}

func TestRepoFetcherPaginates(t *testing.T) {
	fs, srv := newFake(t)
	c := NewClient(srv.Client(), srv.URL, "tok", nil)
	f, _ := NewRepoFetcher(c, "arc42/arc42-template")
	f.PageSize = 2
	items, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 5 {
		t.Fatalf("items = %d", len(items))
	}
	if fs.Requests() < 3 { // ≥2 GraphQL pages + 1 runs request
		t.Fatalf("expected paginated requests, got %d", fs.Requests())
	}
}

func TestRepoFetcherErrors(t *testing.T) {
	fs, srv := newFake(t)
	ctx := context.Background()
	noTok := NewClient(srv.Client(), srv.URL, "", nil)
	f, _ := NewRepoFetcher(noTok, "arc42/arc42-template")
	if _, err := f.Fetch(ctx); !errors.Is(err, ports.ErrAuth) {
		t.Fatalf("missing token → ErrAuth, got %v", err)
	}
	c := NewClient(srv.Client(), srv.URL, "tok", nil)
	f, _ = NewRepoFetcher(c, "arc42/arc42-template")
	fs.FailNext(403, true)
	_, err := f.Fetch(ctx)
	if rl, ok := ports.AsRateLimited(err); !ok || rl.ResetAt.IsZero() {
		t.Fatalf("rate limit → RateLimitedError, got %v", err)
	}
	fs.FailNext(502, false)
	if _, err := f.Fetch(ctx); !errors.Is(err, ports.ErrTransient) {
		t.Fatalf("502 → ErrTransient, got %v", err)
	}
	unknown, _ := NewRepoFetcher(c, "nobody/nothing")
	if _, err := unknown.Fetch(ctx); !errors.Is(err, ports.ErrPermanent) {
		t.Fatalf("unknown repo → ErrPermanent, got %v", err)
	}
	if _, err := NewRepoFetcher(c, "not-a-repo"); err == nil {
		t.Fatal("bad name must fail")
	}
}

func TestTokenExpiryReported(t *testing.T) {
	fs, srv := newFake(t)
	exp := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	fs.SetTokenExpiry(exp)
	sink := &sinkRec{}
	c := NewClient(srv.Client(), srv.URL, "tok", sink)
	f, _ := NewRepoFetcher(c, "arc42/arc42-template")
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sink.name != TokenCredentialName || sink.exp == nil || !sink.exp.Equal(exp) || sink.usedBy != "zorgscope" {
		t.Fatalf("sink = %+v", sink)
	}
	calls := sink.calls
	_, _ = f.Fetch(context.Background())
	if sink.calls != calls {
		t.Fatal("unchanged expiry must not be re-reported on every request")
	}
}
```

`internal/adapters/github/mentions_test.go`:

```go
package github

import (
	"context"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	githubfake "github.com/gernotstarke/zorgscope/test/fakes/github"
)

func TestMentionsFetcher(t *testing.T) {
	fs, srv := newFake(t)
	fs.SetNotifications([]githubfake.Notification{
		{ID: "1", Reason: "mention", SubjectTitle: "Please look", SubjectType: "Issue", SubjectURL: "https://api.github.com/repos/x/y/issues/7", Repo: "x/y", UpdatedAt: now0, Unread: true},
		{ID: "2", Reason: "review_requested", SubjectTitle: "PR", SubjectType: "PullRequest", SubjectURL: "https://api.github.com/repos/x/y/pulls/8", Repo: "x/y", UpdatedAt: now0.Add(-time.Hour)},
		{ID: "3", Reason: "subscribed", SubjectTitle: "noise", SubjectType: "Issue", SubjectURL: "https://api.github.com/repos/x/y/issues/9", Repo: "x/y", UpdatedAt: now0},
		{ID: "4", Reason: "mention", SubjectTitle: "monitored", SubjectType: "Issue", SubjectURL: "https://api.github.com/repos/arc42/arc42-template/issues/240", Repo: "arc42/arc42-template", UpdatedAt: now0},
	})
	c := NewClient(srv.Client(), srv.URL, "tok", nil)
	f := NewMentionsFetcher(c, []string{"arc42/arc42-template"})
	if f.ID() != "github:mentions" || f.Kind() != ports.KindGitHubMentions {
		t.Fatalf("id/kind %s %s", f.ID(), f.Kind())
	}
	items, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	m := byExt(items)
	one := m["1"]
	if one.Kind != domain.KindMention || one.URL != "https://github.com/x/y/issues/7" || one.Title != "Please look" || !one.CreatedAt.Equal(now0) {
		t.Fatalf("mention 1 = %+v", one)
	}
	two := m["2"]
	if two.URL != "https://github.com/x/y/pull/8" {
		t.Fatalf("pull url conversion: %s", two.URL)
	}
	p, _ := domain.DecodePayload[domain.MentionPayload](two)
	if p.Reason != "review_requested" || p.Repo != "x/y" || p.SubjectType != "PullRequest" {
		t.Fatalf("payload = %+v", p)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make go ARGS="test ./internal/adapters/github/ -v"` — Expected: FAIL (undefined).

- [ ] **Step 3: Implement**

`internal/adapters/github/client.go`:

```go
// Package github talks to the GitHub GraphQL and REST APIs (ADR-0011).
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gernotstarke/zorgscope/internal/ports"
)

// TokenCredentialName is the name under which the token expiry is reported (FR-11.2).
const TokenCredentialName = "zorgscope GitHub token"

const expiryHeader = "GitHub-Authentication-Token-Expiration"

// Client is a minimal authenticated GitHub HTTP client shared by all fetchers.
type Client struct {
	http    *http.Client
	baseURL string
	token   string
	sink    ports.CredentialSink

	mu           sync.Mutex
	lastReported string
}

// NewClient creates a client. baseURL is "https://api.github.com" in production, the fake in tests.
// sink may be nil.
func NewClient(hc *http.Client, baseURL, token string, sink ports.CredentialSink) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{http: hc, baseURL: strings.TrimRight(baseURL, "/"), token: token, sink: sink}
}

// do performs a request, maps HTTP errors to port errors and decodes JSON into out (if non-nil).
func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) (http.Header, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "zorgscope (+https://github.com/gernotstarke/zorgscope)")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ports.ErrTransient, err)
	}
	defer func() { _ = resp.Body.Close() }()
	c.noteExpiry(resp.Header)
	if err := mapStatus(resp); err != nil {
		return resp.Header, err
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.Header, fmt.Errorf("%w: decode: %v", ports.ErrPermanent, err)
		}
	}
	return resp.Header, nil
}

func mapStatus(resp *http.Response) error {
	code := resp.StatusCode
	switch {
	case code >= 200 && code < 300:
		return nil
	case code == http.StatusUnauthorized:
		return fmt.Errorf("%w: github 401", ports.ErrAuth)
	case code == http.StatusForbidden || code == http.StatusTooManyRequests:
		if resp.Header.Get("X-RateLimit-Remaining") == "0" || code == http.StatusTooManyRequests {
			reset := time.Now().Add(15 * time.Minute)
			if v, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil && v > 0 {
				reset = time.Unix(v, 0)
			}
			return &ports.RateLimitedError{ResetAt: reset}
		}
		return fmt.Errorf("%w: github 403", ports.ErrAuth)
	case code == http.StatusNotFound:
		return fmt.Errorf("%w: github 404", ports.ErrPermanent)
	case code >= 500:
		return fmt.Errorf("%w: github %d", ports.ErrTransient, code)
	default:
		return fmt.Errorf("%w: github %d", ports.ErrPermanent, code)
	}
}

// noteExpiry reports the token expiration header to the sink once per distinct value.
func (c *Client) noteExpiry(h http.Header) {
	if c.sink == nil {
		return
	}
	v := h.Get(expiryHeader)
	if v == "" {
		return
	}
	c.mu.Lock()
	changed := v != c.lastReported
	c.lastReported = v
	c.mu.Unlock()
	if !changed {
		return
	}
	for _, layout := range []string{"2006-01-02 15:04:05 MST", "2006-01-02 15:04:05 -0700", time.RFC3339} {
		if t, err := time.Parse(layout, v); err == nil {
			t = t.UTC()
			c.sink.ReportCredential(TokenCredentialName, &t, "zorgscope")
			return
		}
	}
}

// graphql posts a query and decodes response.data into out.
func (c *Client) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if _, err := c.do(ctx, http.MethodPost, "/graphql", body, &resp); err != nil {
		return err
	}
	if len(resp.Errors) > 0 && (len(resp.Data) == 0 || string(resp.Data) == "null") {
		msgs := make([]string, 0, len(resp.Errors))
		for _, e := range resp.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("%w: graphql: %s", ports.ErrPermanent, strings.Join(msgs, "; "))
	}
	if err := json.Unmarshal(resp.Data, out); err != nil {
		return fmt.Errorf("%w: graphql decode: %v", ports.ErrPermanent, err)
	}
	return nil
}
```

`internal/adapters/github/repo.go`:

```go
package github

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// repoQuery fetches open issues and PRs with the last comment/review author (verified against the
// live API on 2026-08-16). @include lets us continue paginating one list after the other is done.
const repoQuery = `query($owner:String!,$name:String!,$n:Int!,$issuesAfter:String,$prsAfter:String,$withIssues:Boolean!,$withPRs:Boolean!){
  rateLimit{cost remaining resetAt}
  repository(owner:$owner,name:$name){
    nameWithOwner defaultBranchRef{name}
    issues(states:OPEN,first:$n,after:$issuesAfter,orderBy:{field:CREATED_AT,direction:DESC}) @include(if:$withIssues){
      pageInfo{hasNextPage endCursor}
      nodes{ number title url createdAt updatedAt author{login} labels(first:10){nodes{name}}
             comments(last:1){totalCount nodes{author{login} createdAt}} }
    }
    pullRequests(states:OPEN,first:$n,after:$prsAfter,orderBy:{field:CREATED_AT,direction:DESC}) @include(if:$withPRs){
      pageInfo{hasNextPage endCursor}
      nodes{ number title url createdAt updatedAt isDraft reviewDecision author{login} labels(first:10){nodes{name}}
             comments(last:1){totalCount nodes{author{login} createdAt}}
             reviews(last:1){nodes{author{login} submittedAt}} }
    }
  }
}`

type gqlActor struct {
	Login string `json:"login"`
}

type gqlNode struct {
	Number         int       `json:"number"`
	Title          string    `json:"title"`
	URL            string    `json:"url"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	IsDraft        bool      `json:"isDraft"`
	ReviewDecision string    `json:"reviewDecision"`
	Author         *gqlActor `json:"author"`
	Labels         struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Comments struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Author    *gqlActor `json:"author"`
			CreatedAt time.Time `json:"createdAt"`
		} `json:"nodes"`
	} `json:"comments"`
	Reviews struct {
		Nodes []struct {
			Author      *gqlActor `json:"author"`
			SubmittedAt time.Time `json:"submittedAt"`
		} `json:"nodes"`
	} `json:"reviews"`
}

type gqlPage struct {
	PageInfo struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
	Nodes []gqlNode `json:"nodes"`
}

type repoData struct {
	Repository *struct {
		NameWithOwner    string `json:"nameWithOwner"`
		DefaultBranchRef *struct {
			Name string `json:"name"`
		} `json:"defaultBranchRef"`
		Issues       *gqlPage `json:"issues"`
		PullRequests *gqlPage `json:"pullRequests"`
	} `json:"repository"`
}

type runsResponse struct {
	WorkflowRuns []struct {
		ID           int64     `json:"id"`
		Name         string    `json:"name"`
		HTMLURL      string    `json:"html_url"`
		Status       string    `json:"status"`
		Conclusion   string    `json:"conclusion"`
		HeadBranch   string    `json:"head_branch"`
		CreatedAt    time.Time `json:"created_at"`
		UpdatedAt    time.Time `json:"updated_at"`
		RunStartedAt time.Time `json:"run_started_at"`
	} `json:"workflow_runs"`
}

var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// RepoFetcher fetches open issues, PRs and the latest default-branch workflow run of one repository.
type RepoFetcher struct {
	c        *Client
	owner    string
	name     string
	PageSize int
}

// NewRepoFetcher validates "owner/name" and creates a fetcher.
func NewRepoFetcher(c *Client, fullName string) (*RepoFetcher, error) {
	if !repoNameRe.MatchString(fullName) {
		return nil, fmt.Errorf("github: repository name %q must be owner/name", fullName)
	}
	owner, name := splitRepo(fullName)
	return &RepoFetcher{c: c, owner: owner, name: name, PageSize: 100}, nil
}

func splitRepo(full string) (owner, name string) {
	for i := 0; i < len(full); i++ {
		if full[i] == '/' {
			return full[:i], full[i+1:]
		}
	}
	return full, ""
}

// ID implements ports.SourceFetcher.
func (f *RepoFetcher) ID() string { return "github:" + f.owner + "/" + f.name }

// Kind implements ports.SourceFetcher.
func (f *RepoFetcher) Kind() string { return ports.KindGitHubRepo }

// Fetch implements ports.SourceFetcher.
func (f *RepoFetcher) Fetch(ctx context.Context) ([]domain.Item, error) {
	var items []domain.Item
	var issuesAfter, prsAfter any
	withIssues, withPRs := true, true
	defaultBranch := ""
	for withIssues || withPRs {
		var data repoData
		vars := map[string]any{"owner": f.owner, "name": f.name, "n": f.PageSize, "issuesAfter": issuesAfter, "prsAfter": prsAfter,
			"withIssues": withIssues, "withPRs": withPRs}
		if err := f.c.graphql(ctx, repoQuery, vars, &data); err != nil {
			return nil, err
		}
		if data.Repository == nil {
			return nil, fmt.Errorf("%w: repository %s/%s not found or not accessible", ports.ErrPermanent, f.owner, f.name)
		}
		if data.Repository.DefaultBranchRef != nil {
			defaultBranch = data.Repository.DefaultBranchRef.Name
		}
		if withIssues {
			if data.Repository.Issues == nil {
				withIssues = false
			} else {
				for _, n := range data.Repository.Issues.Nodes {
					items = append(items, f.mapNode(n, false))
				}
				withIssues = data.Repository.Issues.PageInfo.HasNextPage
				issuesAfter = data.Repository.Issues.PageInfo.EndCursor
			}
		}
		if withPRs {
			if data.Repository.PullRequests == nil {
				withPRs = false
			} else {
				for _, n := range data.Repository.PullRequests.Nodes {
					items = append(items, f.mapNode(n, true))
				}
				withPRs = data.Repository.PullRequests.PageInfo.HasNextPage
				prsAfter = data.Repository.PullRequests.PageInfo.EndCursor
			}
		}
	}
	if defaultBranch != "" {
		run, err := f.latestRun(ctx, defaultBranch)
		if err != nil {
			return nil, err
		}
		if run != nil {
			items = append(items, *run)
		}
	}
	return items, nil
}

func login(a *gqlActor) string {
	if a == nil {
		return "ghost"
	}
	return a.Login
}

func (f *RepoFetcher) mapNode(n gqlNode, isPR bool) domain.Item {
	kind, prefix := domain.KindIssue, "issues/"
	if isPR {
		kind, prefix = domain.KindPR, "pulls/"
	}
	it := domain.Item{
		ID:        domain.ItemID{SourceID: f.ID(), ExternalID: fmt.Sprintf("%s%d", prefix, n.Number)},
		Kind:      kind,
		Title:     n.Title,
		URL:       n.URL,
		Author:    login(n.Author),
		CreatedAt: n.CreatedAt,
		UpdatedAt: n.UpdatedAt,
	}
	for _, l := range n.Labels.Nodes {
		it.Labels = append(it.Labels, l.Name)
	}
	if len(n.Comments.Nodes) > 0 {
		c := n.Comments.Nodes[len(n.Comments.Nodes)-1]
		it.LastActivityBy, it.LastActivityAt = login(c.Author), c.CreatedAt
	}
	if len(n.Reviews.Nodes) > 0 {
		r := n.Reviews.Nodes[len(n.Reviews.Nodes)-1]
		if r.SubmittedAt.After(it.LastActivityAt) {
			it.LastActivityBy, it.LastActivityAt = login(r.Author), r.SubmittedAt
		}
	}
	if isPR {
		it.Payload = domain.MustPayload(domain.PRPayload{Draft: n.IsDraft, ReviewDecision: n.ReviewDecision, Comments: n.Comments.TotalCount})
	} else {
		it.Payload = domain.MustPayload(domain.IssuePayload{Comments: n.Comments.TotalCount})
	}
	return it
}

func (f *RepoFetcher) latestRun(ctx context.Context, branch string) (*domain.Item, error) {
	q := url.Values{"branch": {branch}, "per_page": {"1"}, "exclude_pull_requests": {"true"}}
	var resp runsResponse
	path := fmt.Sprintf("/repos/%s/%s/actions/runs?%s", url.PathEscape(f.owner), url.PathEscape(f.name), q.Encode())
	if _, err := f.c.do(ctx, "GET", path, nil, &resp); err != nil {
		return nil, err
	}
	if len(resp.WorkflowRuns) == 0 {
		return nil, nil
	}
	r := resp.WorkflowRuns[0]
	created := r.RunStartedAt
	if created.IsZero() {
		created = r.CreatedAt
	}
	return &domain.Item{
		ID: domain.ItemID{SourceID: f.ID(), ExternalID: fmt.Sprintf("runs/%d", r.ID)}, Kind: domain.KindWorkflowRun,
		Title: r.Name, URL: r.HTMLURL, CreatedAt: created, UpdatedAt: r.UpdatedAt,
		Payload: domain.MustPayload(domain.WorkflowRunPayload{RunID: r.ID, WorkflowName: r.Name, Conclusion: r.Conclusion, Status: r.Status, Branch: r.HeadBranch}),
	}, nil
}
```

`internal/adapters/github/mentions.go`:

```go
package github

import (
	"context"
	"strings"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// MentionsFetcher turns GitHub notifications addressed to the user into mention items (FR-2.6).
type MentionsFetcher struct {
	c       *Client
	exclude map[string]bool
}

// NewMentionsFetcher creates the fetcher; excludeRepos are monitored repos already covered by RepoFetchers.
func NewMentionsFetcher(c *Client, excludeRepos []string) *MentionsFetcher {
	ex := make(map[string]bool, len(excludeRepos))
	for _, r := range excludeRepos {
		ex[strings.ToLower(r)] = true
	}
	return &MentionsFetcher{c: c, exclude: ex}
}

// ID implements ports.SourceFetcher.
func (f *MentionsFetcher) ID() string { return "github:mentions" }

// Kind implements ports.SourceFetcher.
func (f *MentionsFetcher) Kind() string { return ports.KindGitHubMentions }

var mentionReasons = map[string]bool{"mention": true, "team_mention": true, "review_requested": true, "assign": true, "author": true}

type notification struct {
	ID        string    `json:"id"`
	Reason    string    `json:"reason"`
	Unread    bool      `json:"unread"`
	UpdatedAt time.Time `json:"updated_at"`
	Subject   struct {
		Title string `json:"title"`
		URL   string `json:"url"`
		Type  string `json:"type"`
	} `json:"subject"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// Fetch implements ports.SourceFetcher.
func (f *MentionsFetcher) Fetch(ctx context.Context) ([]domain.Item, error) {
	var list []notification
	if _, err := f.c.do(ctx, "GET", "/notifications?all=false&participating=true&per_page=50", nil, &list); err != nil {
		return nil, err
	}
	var items []domain.Item
	for _, n := range list {
		if !mentionReasons[n.Reason] || f.exclude[strings.ToLower(n.Repository.FullName)] {
			continue
		}
		items = append(items, domain.Item{
			ID: domain.ItemID{SourceID: f.ID(), ExternalID: n.ID}, Kind: domain.KindMention,
			Title: n.Subject.Title, URL: htmlURL(n.Subject.URL), CreatedAt: n.UpdatedAt, UpdatedAt: n.UpdatedAt,
			Payload: domain.MustPayload(domain.MentionPayload{Reason: n.Reason, Repo: n.Repository.FullName, SubjectType: n.Subject.Type}),
		})
	}
	return items, nil
}

// htmlURL converts an API subject URL into the web URL.
func htmlURL(api string) string {
	u := strings.Replace(api, "https://api.github.com/repos/", "https://github.com/", 1)
	return strings.Replace(u, "/pulls/", "/pull/", 1)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make go ARGS="test ./internal/adapters/github/ -race -v"` — Expected: PASS. `make lint` — clean (`time.Now()` in `mapStatus` fallback is acceptable; if you prefer, thread a clock — not required).

- [ ] **Step 5: Commit**

```bash
git add internal/adapters/github
git commit -m "feat(github): GraphQL repo fetcher with runs, mentions fetcher, token expiry detection (FR-2.1, FR-2.6, FR-3.2, FR-11.2, ADR-0011)"
```

---

### Task 14: HTTP server – templates, CSS, htmx, handlers, dev auth, CSRF, security headers, golden tests

**Files:**
- Create: `web/embed.go`, `web/templates/layout.html`, `web/templates/page.html`, `web/templates/tile_header.html`, `web/templates/tile_attention.html`, `web/templates/tile_repos.html`, `web/templates/unauthorized.html`, `web/static/tokens.css`, `web/static/app.css`, `web/static/htmx.min.js` (vendored), `internal/server/server.go`, `internal/server/middleware.go`, `internal/server/handlers.go`, `internal/server/render.go`, `internal/server/server_test.go`, `internal/server/render_test.go`, `internal/server/testdata/attention_rows.golden.html`, `internal/server/testdata/attention_empty.golden.html`, `internal/server/testdata/repos.golden.html`
- Modify: none (health.go stays)

**Interfaces:**
- Produces: `server.Deps{Dashboard *app.Dashboard; Refresher server.Refresher; Store ports.Store; Clock ports.Clock; Cfg *config.Config; Log *slog.Logger; Ready func() bool}`, `server.Refresher interface{ TriggerAll() int; InFlight() int }`, `server.New(d Deps) (*Server, error)`, `(*Server).Handler() http.Handler`.
- Routes: `GET /` page · `GET /tiles/{name}` (`header|attention|repos`) · `POST /dismiss` (form: `id`, `updated_at`) · `POST /dismiss-all` · `POST /refresh` (202) · `GET /status` (JSON) · `GET /healthz` · `GET /readyz` · `GET /static/*`.
- CSRF: cookie `zs_csrf` (HttpOnly) + header `X-CSRF-Token` (htmx `hx-headers` on `<body>`); POST without match → 403.

- [ ] **Step 1: Vendor htmx and create the embed package**

```sh
mkdir -p web/static web/templates
docker run --rm -v "$PWD/web/static":/out curlimages/curl:latest -fsSL https://unpkg.com/htmx.org@2.0.4/dist/htmx.min.js -o /out/htmx.min.js
head -c 60 web/static/htmx.min.js   # must start with a JS comment/banner, not HTML
```

`web/embed.go`:

```go
// Package web embeds templates and static assets (ADR-0003, C-7).
package web

import "embed"

// FS contains templates/*.html and static/*.
//
//go:embed templates/*.html static/*
var FS embed.FS
```

- [ ] **Step 2: Write the failing tests**

`internal/server/server_test.go`:

```go
package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/clock"
	"github.com/gernotstarke/zorgscope/internal/app"
	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports/memstore"
)

var t0 = time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

type fakeRefresher struct{ calls int }

func (f *fakeRefresher) TriggerAll() int { f.calls++; return 2 }
func (f *fakeRefresher) InFlight() int   { return 0 }

func newTestServer(t *testing.T, authMode string) (*httptest.Server, *memstore.Store, *fakeRefresher) {
	t.Helper()
	y := "server:\n  base_url: http://localhost:8080\ngithub:\n  enabled: true\n  me: gernotstarke\n  repos: [arc42/arc42-template]\n"
	env := map[string]string{"GITHUB_TOKEN": "t", "AUTH_MODE": authMode, "SESSION_SECRET": strings.Repeat("s", 32)}
	cfg, err := config.Parse(strings.NewReader(y), func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	st := memstore.New()
	clk := clock.NewFake(t0)
	ctx := context.Background()
	_ = st.ReplaceItems(ctx, "github:arc42/arc42-template", []domain.Item{{
		ID: domain.ItemID{SourceID: "github:arc42/arc42-template", ExternalID: "issues/240"}, Kind: domain.KindIssue,
		Title: "Typo in section 8", URL: "https://github.com/arc42/arc42-template/issues/240", Author: "newcomer",
		CreatedAt: t0.Add(-2 * time.Hour), UpdatedAt: t0.Add(-2 * time.Hour)}}, t0)
	_ = st.RecordStatus(ctx, domain.FetchStatus{SourceID: "github:arc42/arc42-template", Kind: "github-repo", LastSuccess: t0.Add(-time.Minute), ItemCount: 1})
	ref := &fakeRefresher{}
	srv, err := New(Deps{Dashboard: app.NewDashboard(st, clk, cfg), Refresher: ref, Store: st, Clock: clk, Cfg: cfg, Log: slog.Default(), Ready: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st, ref
}

func get(t *testing.T, ts *httptest.Server, path string) (*http.Response, string) {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	buf := make([]byte, 64*1024)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	_ = resp.Body.Close()
	return resp, sb.String()
}

func TestPageRendersDashboardWithSecurityHeaders(t *testing.T) {
	ts, _, _ := newTestServer(t, "dev")
	resp, body := get(t, ts, "/")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	for _, want := range []string{"<title>(1) zorgscope</title>", `id="tile-attention"`, "Typo in section 8", "NEW", `id="tile-repos"`, "arc42-template", "/static/htmx.min.js", `hx-headers=`} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
	h := resp.Header
	if !strings.HasPrefix(h.Get("Content-Security-Policy"), "default-src 'self'") || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Referrer-Policy") != "no-referrer" || h.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("security headers: %v", h)
	}
	if h.Get("Strict-Transport-Security") != "" {
		t.Fatal("no HSTS on http base_url")
	}
	var csrf *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "zs_csrf" {
			csrf = c
		}
	}
	if csrf == nil || !csrf.HttpOnly || csrf.SameSite != http.SameSiteLaxMode {
		t.Fatalf("csrf cookie = %+v", csrf)
	}
}

func TestTileFragments(t *testing.T) {
	ts, _, _ := newTestServer(t, "dev")
	resp, body := get(t, ts, "/tiles/attention")
	if resp.StatusCode != 200 || !strings.HasPrefix(strings.TrimSpace(body), `<section id="tile-attention"`) || strings.Contains(body, "<html") {
		t.Fatalf("attention fragment: %d %s", resp.StatusCode, body[:min(len(body), 200)])
	}
	resp, _ = get(t, ts, "/tiles/nope")
	if resp.StatusCode != 404 {
		t.Fatalf("unknown tile → 404, got %d", resp.StatusCode)
	}
	resp, body = get(t, ts, "/tiles/header")
	if resp.StatusCode != 200 || !strings.Contains(body, "13:59") { // data as of, Berlin time
		t.Fatalf("header fragment: %d %s", resp.StatusCode, body)
	}
}

func TestDismissRequiresCSRFAndStores(t *testing.T) {
	ts, st, _ := newTestServer(t, "dev")
	// obtain csrf cookie
	resp, _ := get(t, ts, "/")
	var csrf string
	for _, c := range resp.Cookies() {
		if c.Name == "zs_csrf" {
			csrf = c.Value
		}
	}
	form := url.Values{"id": {"github:arc42/arc42-template|issues/240"}, "updated_at": {"1786874400"}} // t0-2h = 2026-08-16T10:00:00Z
	post := func(withCookie, withHeader bool) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/dismiss", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if withCookie {
			req.AddCookie(&http.Cookie{Name: "zs_csrf", Value: csrf})
		}
		if withHeader {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		r, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
		return r
	}
	if r := post(true, false); r.StatusCode != http.StatusForbidden {
		t.Fatalf("missing header → 403, got %d", r.StatusCode)
	}
	if r := post(false, true); r.StatusCode != http.StatusForbidden {
		t.Fatalf("missing cookie → 403, got %d", r.StatusCode)
	}
	if r := post(true, true); r.StatusCode != http.StatusOK {
		t.Fatalf("valid → 200 fragment, got %d", r.StatusCode)
	}
	d, _ := st.Dismissal(context.Background(), domain.ItemID{SourceID: "github:arc42/arc42-template", ExternalID: "issues/240"})
	if d == nil || d.UpdatedAt.Unix() != 1786874400 || !d.DismissedAt.Equal(t0) {
		t.Fatalf("dismissal = %+v", d)
	}
	_, body := get(t, ts, "/tiles/attention")
	if !strings.Contains(body, "Nothing needs your attention") {
		t.Fatalf("after dismiss the tile must be empty: %s", body)
	}
}

func TestRefreshAndStatus(t *testing.T) {
	ts, _, ref := newTestServer(t, "dev")
	resp, _ := get(t, ts, "/")
	var csrf string
	for _, c := range resp.Cookies() {
		if c.Name == "zs_csrf" {
			csrf = c.Value
		}
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "zs_csrf", Value: csrf})
	req.Header.Set("X-CSRF-Token", csrf)
	r, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Body.Close()
	if r.StatusCode != http.StatusAccepted || ref.calls != 1 {
		t.Fatalf("refresh: %d calls=%d", r.StatusCode, ref.calls)
	}
	resp, body := get(t, ts, "/status")
	if resp.StatusCode != 200 || !strings.Contains(body, `"github:arc42/arc42-template"`) || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("status: %d %s", resp.StatusCode, body)
	}
}

func TestPasskeyModeBlocksEverythingButHealth(t *testing.T) {
	ts, _, _ := newTestServer(t, "passkey")
	for _, p := range []string{"/", "/tiles/attention", "/status"} {
		resp, _ := get(t, ts, p)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: got %d want 401", p, resp.StatusCode)
		}
	}
	if resp, _ := get(t, ts, "/healthz"); resp.StatusCode != 200 {
		t.Fatal("healthz must be public")
	}
	if resp, _ := get(t, ts, "/static/app.css"); resp.StatusCode != 200 {
		t.Fatal("static must be public")
	}
}

func TestStaticAssets(t *testing.T) {
	ts, _, _ := newTestServer(t, "dev")
	resp, body := get(t, ts, "/static/app.css")
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "text/css") || resp.Header.Get("Cache-Control") == "" || len(body) == 0 {
		t.Fatalf("app.css: %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = get(t, ts, "/static/htmx.min.js")
	if resp.StatusCode != 200 {
		t.Fatal("htmx must be served")
	}
}
```

`internal/server/render_test.go` (golden files; regenerate with `make go ARGS="test ./internal/server/ -run Golden -args -update"`):

```go
package server

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/gernotstarke/zorgscope/internal/app"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden.html")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (run with -update): %v", path, err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(got)) {
		t.Fatalf("golden %s differs:\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

func TestGoldenTiles(t *testing.T) {
	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	rows := app.View{Tiles: []string{"attention", "repos"}}
	rows.Attention = app.AttentionView{Total: 3, Overflow: 1, Rows: []app.AttentionRow{
		{ID: "github:arc42/arc42-template|issues/240", Source: "arc42/arc42-template", SourceShort: "arc42-template", Number: "#240", Title: "Typo in section 8", URL: "https://github.com/arc42/arc42-template/issues/240", Author: "newcomer", Age: "2h", Bucket: "lt24h", Badge: "NEW", Level: "new", Kind: "issue", UpdatedAt: 1786968000},
		{ID: "github:arc42/arc42-template|pulls/237", Source: "arc42/arc42-template", SourceShort: "arc42-template", Number: "#237", Title: "Add example stakeholder table", URL: "https://github.com/arc42/arc42-template/pull/237", Author: "Sofeso", Age: "20d", Bucket: "lt30d", Badge: "UNANSWERED", Level: "unanswered", Kind: "pr", UpdatedAt: 1785400000},
	}}
	rows.Repos = []app.RepoCard{
		{Name: "arc42/arc42-template", ShortName: "arc42-template", URL: "https://github.com/arc42/arc42-template", OpenIssues: 3, OpenPRs: 1, New: 1, Unanswered: 2, Build: app.BuildView{State: "ok", Workflow: "build", URL: "https://github.com/arc42/arc42-template/actions/runs/1001", Age: "6h"}},
		{Name: "arc42/arc42.org-site", ShortName: "arc42.org-site", URL: "https://github.com/arc42/arc42.org-site", OpenIssues: 1, Build: app.BuildView{State: "failed", Workflow: "deploy", URL: "https://github.com/arc42/arc42.org-site/actions/runs/2002", Age: "1h"}},
	}
	data := pageData{View: rows, CSRF: "csrf-token", PollSeconds: 60}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "tile_attention", data); err != nil {
		t.Fatal(err)
	}
	golden(t, "attention_rows", buf.Bytes())
	buf.Reset()
	if err := tmpl.ExecuteTemplate(&buf, "tile_repos", data); err != nil {
		t.Fatal(err)
	}
	golden(t, "repos", buf.Bytes())
	buf.Reset()
	empty := pageData{View: app.View{}, CSRF: "csrf-token", PollSeconds: 60}
	if err := tmpl.ExecuteTemplate(&buf, "tile_attention", empty); err != nil {
		t.Fatal(err)
	}
	golden(t, "attention_empty", buf.Bytes())
	// every template must execute with an empty view (no nil-pointer traps)
	for _, name := range []string{"layout", "tile_header", "tile_attention", "tile_repos", "unauthorized"} {
		if err := tmpl.ExecuteTemplate(&bytes.Buffer{}, name, empty); err != nil {
			t.Fatalf("%s with empty view: %v", name, err)
		}
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `make go ARGS="test ./internal/server/ -v"` — Expected: FAIL (undefined New, parseTemplates, …).

- [ ] **Step 4: Templates and CSS**

`web/templates/layout.html`:

```html
{{define "layout"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>{{if .View.Header.AttentionCount}}({{.View.Header.AttentionCount}}) {{end}}zorgscope</title>
<link rel="stylesheet" href="/static/tokens.css">
<link rel="stylesheet" href="/static/app.css">
<meta name="htmx-config" content='{"includeIndicatorStyles": false}'>
<script src="/static/htmx.min.js" defer></script>
</head>
<body hx-headers='{"X-CSRF-Token": "{{.CSRF}}"}'>
<main class="dashboard">
{{template "tile_header" .}}
<section class="grid">
{{range .View.Tiles}}{{if eq . "attention"}}{{template "tile_attention" $}}{{else if eq . "repos"}}{{template "tile_repos" $}}{{end}}{{end}}
</section>
</main>
</body>
</html>{{end}}
```

`web/templates/tile_header.html`:

```html
{{define "tile_header"}}<header id="tile-header" class="header" hx-get="/tiles/header" hx-trigger="every {{.PollSeconds}}s" hx-swap="outerHTML">
  <div class="header-main">
    <h1 class="wordmark">zorg<span>scope</span></h1>
    <p class="today"><time>{{.View.Header.Date}}</time> · <span class="clock">{{.View.Header.Time}}</span></p>
  </div>
  <div class="header-status">
    <span class="attention-count {{if .View.Header.AttentionCount}}has-items{{end}}" title="items needing attention">{{.View.Header.AttentionCount}}</span>
    <ul class="sources" aria-label="data sources">
      {{range .View.Header.Sources}}<li class="source {{if .AuthFailed}}source-auth{{else if not .Healthy}}source-error{{else}}source-ok{{end}}{{if .InFlight}} source-busy{{end}}" title="{{.ID}} · {{if .Healthy}}data {{.Age}} old{{else}}error: {{.Error}}{{end}}"></li>{{end}}
    </ul>
    <span class="asof">data as of {{.View.Header.DataAsOf}}{{if .View.Header.Refreshing}} · refreshing ({{.View.Header.Refreshing}})…{{end}}</span>
    <button class="btn" hx-post="/refresh" hx-swap="none" title="fetch all sources now">↻ refresh</button>
  </div>
</header>{{end}}
```

`web/templates/tile_attention.html`:

```html
{{define "tile_attention"}}<section id="tile-attention" class="tile tile-attention" hx-get="/tiles/attention" hx-trigger="every {{.PollSeconds}}s" hx-swap="outerHTML" aria-live="polite">
  <header class="tile-head">
    <h2>Attention <span class="count">{{.View.Attention.Total}}</span></h2>
    {{if .View.Attention.Rows}}<button class="btn btn-quiet" hx-post="/dismiss-all" hx-target="#tile-attention" hx-swap="outerHTML">dismiss all</button>{{end}}
  </header>
  {{if .View.Attention.Rows}}<ul class="rows">
    {{range .View.Attention.Rows}}<li class="row level-{{.Level}} bucket-{{.Bucket}}">
      <span class="badge badge-{{.Level}}">{{.Badge}}</span>
      <a class="row-title" href="{{.URL}}" target="_blank" rel="noopener"><span class="row-source">{{.SourceShort}}{{if .Number}} {{.Number}}{{end}}</span> {{.Title}}</a>
      <span class="row-meta">{{if .Author}}{{.Author}} · {{end}}{{.Age}}</span>
      <button class="dismiss" hx-post="/dismiss" hx-vals='{"id": "{{.ID}}", "updated_at": {{.UpdatedAt}}}' hx-target="#tile-attention" hx-swap="outerHTML" aria-label="dismiss {{.Title}}" title="seen">✕</button>
    </li>{{end}}
  </ul>
  {{if .View.Attention.Overflow}}<p class="more">+ {{.View.Attention.Overflow}} more</p>{{end}}
  {{else}}<p class="empty">Nothing needs your attention 🎉</p>{{end}}
</section>{{end}}
```

`web/templates/tile_repos.html`:

```html
{{define "tile_repos"}}<section id="tile-repos" class="tile tile-repos" hx-get="/tiles/repos" hx-trigger="every {{.PollSeconds}}s" hx-swap="outerHTML">
  <header class="tile-head"><h2>Repositories</h2></header>
  {{if .View.Repos}}<ul class="cards">
    {{range .View.Repos}}<li class="card">
      <a class="card-title" href="{{.URL}}" target="_blank" rel="noopener">{{.ShortName}}</a>
      <a class="build build-{{.Build.State}}" {{if .Build.URL}}href="{{.Build.URL}}" target="_blank" rel="noopener"{{end}} title="{{if .Build.Workflow}}{{.Build.Workflow}}: {{.Build.State}} · {{.Build.Age}}{{else}}no workflow run{{end}}"><span class="dot"></span></a>
      <dl class="counts">
        <div><dt>issues</dt><dd>{{.OpenIssues}}</dd></div>
        <div><dt>PRs</dt><dd>{{.OpenPRs}}</dd></div>
        <div class="{{if .New}}hot{{end}}"><dt>new</dt><dd>{{.New}}</dd></div>
        <div class="{{if .Unanswered}}hot{{end}}"><dt>unanswered</dt><dd>{{.Unanswered}}</dd></div>
      </dl>
    </li>{{end}}
  </ul>{{else}}<p class="empty">No repositories configured.</p>{{end}}
</section>{{end}}
```

`web/templates/unauthorized.html`:

```html
{{define "unauthorized"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>zorgscope – sign in</title>
<link rel="stylesheet" href="/static/tokens.css"><link rel="stylesheet" href="/static/app.css"></head>
<body><main class="dashboard narrow"><h1 class="wordmark">zorg<span>scope</span></h1>
<p class="empty">Sign-in with a passkey is not available in this build yet (planned in milestone M3). Run locally with <code>AUTH_MODE=dev</code>.</p>
</main></body></html>{{end}}
```

`web/static/tokens.css` — the design tokens (arc42 §8.5): one calm neutral palette, one accent, semantic colours, a four‑step age scale, spacing/type/radius scales, light and dark:

```css
:root {
  color-scheme: light dark;
  --font-sans: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
  --font-mono: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  --fs-xs: 0.75rem; --fs-sm: 0.875rem; --fs-md: 1rem; --fs-lg: 1.25rem; --fs-xl: 1.75rem;
  --sp-1: 4px; --sp-2: 8px; --sp-3: 12px; --sp-4: 16px; --sp-5: 24px; --sp-6: 32px;
  --radius: 10px; --radius-sm: 6px;
  --shadow: 0 1px 2px rgba(0,0,0,.06), 0 4px 16px rgba(0,0,0,.05);

  --bg: #f4f5f7; --surface: #ffffff; --surface-2: #eef0f3; --border: #e1e4e8;
  --text: #1b1f24; --text-muted: #5b6470; --accent: #2f6fed; --accent-ink: #ffffff;
  --ok: #1f9d55; --warn: #d97706; --danger: #dc2626; --info: #6b7280;

  --age-lt24h: #2f6fed; --age-lt7d: #7c3aed; --age-lt30d: #a16207; --age-ge30d: #9ca3af;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #0f1216; --surface: #171b21; --surface-2: #1f242c; --border: #2a313b;
    --text: #e6e8eb; --text-muted: #9aa4b2; --accent: #6b9cff; --accent-ink: #0b1020;
    --ok: #34c37a; --warn: #f59e0b; --danger: #f87171; --info: #9ca3af;
    --age-lt24h: #6b9cff; --age-lt7d: #a78bfa; --age-lt30d: #d6a11c; --age-ge30d: #6b7280;
    --shadow: 0 1px 2px rgba(0,0,0,.4), 0 6px 20px rgba(0,0,0,.35);
  }
}
@media (prefers-reduced-motion: reduce) { *, *::before, *::after { animation: none !important; transition: none !important; } }
```

`web/static/app.css`:

```css
* { box-sizing: border-box; }
html, body { margin: 0; padding: 0; }
body { font-family: var(--font-sans); font-size: var(--fs-md); line-height: 1.45; color: var(--text); background: var(--bg); -webkit-font-smoothing: antialiased; }
a { color: inherit; text-decoration: none; }
a:hover .row-title, .row-title:hover, .card-title:hover { text-decoration: underline; }
button.btn { font: inherit; font-size: var(--fs-sm); padding: var(--sp-1) var(--sp-3); border-radius: 999px; border: 1px solid var(--border); background: var(--surface); color: var(--text); cursor: pointer; }
button.btn:hover { border-color: var(--accent); color: var(--accent); }
button.btn-quiet { border-color: transparent; color: var(--text-muted); }
:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }

.dashboard { max-width: 1800px; margin: 0 auto; padding: var(--sp-4); }
.dashboard.narrow { max-width: 640px; padding-top: 10vh; }

/* header */
.header { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: var(--sp-3); padding: var(--sp-2) var(--sp-3) var(--sp-4); }
.wordmark { font-size: var(--fs-xl); font-weight: 700; letter-spacing: -0.02em; margin: 0; }
.wordmark span { color: var(--accent); }
.today { margin: 0; color: var(--text-muted); font-size: var(--fs-sm); }
.header-status { display: flex; align-items: center; gap: var(--sp-3); font-size: var(--fs-sm); color: var(--text-muted); }
.attention-count { min-width: 2.2em; text-align: center; padding: 2px 8px; border-radius: 999px; background: var(--surface-2); font-weight: 600; font-variant-numeric: tabular-nums; }
.attention-count.has-items { background: var(--accent); color: var(--accent-ink); }
.sources { list-style: none; display: flex; gap: 5px; margin: 0; padding: 0; }
.source { width: 9px; height: 9px; border-radius: 50%; background: var(--info); }
.source-ok { background: var(--ok); } .source-error { background: var(--warn); } .source-auth { background: var(--danger); }
.source-busy { animation: pulse 1.2s ease-in-out infinite; }
@keyframes pulse { 50% { opacity: .35; } }

/* grid & tiles */
.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(340px, 1fr)); gap: var(--sp-4); align-items: start; }
.tile { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); box-shadow: var(--shadow); padding: var(--sp-4); }
.tile-attention { grid-column: span 2; }
@media (max-width: 760px) { .tile-attention { grid-column: span 1; } }
.tile-head { display: flex; align-items: baseline; justify-content: space-between; margin-bottom: var(--sp-3); }
.tile-head h2 { margin: 0; font-size: var(--fs-lg); font-weight: 650; }
.count { margin-left: var(--sp-2); font-size: var(--fs-sm); color: var(--text-muted); font-weight: 500; }
.empty, .more { color: var(--text-muted); font-size: var(--fs-sm); margin: var(--sp-2) 0 0; }

/* attention rows */
.rows { list-style: none; margin: 0; padding: 0; }
.row { display: grid; grid-template-columns: auto 1fr auto auto; align-items: center; gap: var(--sp-3); padding: var(--sp-2) var(--sp-3); border-left: 3px solid var(--age-ge30d); border-radius: var(--radius-sm); }
.row + .row { margin-top: var(--sp-1); }
.row:hover { background: var(--surface-2); }
.bucket-lt24h { border-left-color: var(--age-lt24h); } .bucket-lt7d { border-left-color: var(--age-lt7d); } .bucket-lt30d { border-left-color: var(--age-lt30d); }
.row-title { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.row-source { color: var(--text-muted); font-size: var(--fs-sm); margin-right: var(--sp-1); }
.row-meta { color: var(--text-muted); font-size: var(--fs-xs); white-space: nowrap; }
.badge { font-size: 0.68rem; font-weight: 700; letter-spacing: .04em; padding: 2px 7px; border-radius: 4px; background: var(--surface-2); color: var(--text-muted); white-space: nowrap; }
.badge-new { background: var(--accent); color: var(--accent-ink); }
.badge-unanswered { background: var(--warn); color: #fff; }
.badge-build_failed, .badge-expired, .badge-down, .badge-auth_failed { background: var(--danger); color: #fff; }
.badge-expiring { background: var(--warn); color: #fff; }
.dismiss { border: 0; background: transparent; color: var(--text-muted); cursor: pointer; font-size: var(--fs-sm); padding: var(--sp-1) var(--sp-2); border-radius: var(--radius-sm); min-width: 44px; min-height: 32px; }
.dismiss:hover { color: var(--danger); background: var(--surface-2); }

/* repo cards */
.cards { list-style: none; margin: 0; padding: 0; display: grid; grid-template-columns: repeat(auto-fill, minmax(190px, 1fr)); gap: var(--sp-2); }
.card { min-width: 0; border: 1px solid var(--border); border-radius: var(--radius-sm); padding: var(--sp-2) var(--sp-3); display: grid; grid-template-columns: 1fr auto; align-items: center; row-gap: var(--sp-1); }
.card-title { font-weight: 600; font-size: var(--fs-sm); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.build .dot { display: inline-block; width: 10px; height: 10px; border-radius: 50%; background: var(--info); }
.build-ok .dot { background: var(--ok); } .build-failed .dot { background: var(--danger); } .build-running .dot { background: var(--warn); animation: pulse 1.2s ease-in-out infinite; }
.counts { grid-column: 1 / -1; display: flex; flex-wrap: wrap; gap: var(--sp-1) var(--sp-3); margin: 0; font-size: var(--fs-xs); color: var(--text-muted); }
.counts div { display: flex; flex-direction: column; }
.counts dt { order: 2; } .counts dd { order: 1; margin: 0; font-size: var(--fs-md); font-weight: 600; color: var(--text); font-variant-numeric: tabular-nums; }
.counts .hot dd { color: var(--warn); }
```

- [ ] **Step 5: Implement the server**

`internal/server/render.go`:

```go
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
		s.deps.Log.Error("render failed", "template", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}
```

`internal/server/middleware.go`:

```go
package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const csrfCookie = "zs_csrf"

func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic", "err", rec, "path", r.URL.Path)
					http.Error(w, "internal error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }

func requestLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			if strings.HasPrefix(r.URL.Path, "/static/") || r.URL.Path == "/healthz" {
				return
			}
			log.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.status, "duration_ms", time.Since(start).Milliseconds())
		})
	}
}

// securityHeaders implements arc42 §8.6 (QS-3.4).
func securityHeaders(https bool) func(http.Handler) http.Handler {
	csp := "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", csp)
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("X-Frame-Options", "DENY")
			if https {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// csrf uses the double-submit pattern: an HttpOnly cookie whose value the server also renders into
// htmx's hx-headers; POSTs must echo it in X-CSRF-Token (QS-3.5). The token is stored on the request
// context by csrfToken().
func csrf(secure bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := ""
			if c, err := r.Cookie(csrfCookie); err == nil && len(c.Value) == 64 {
				token = c.Value
			}
			if token == "" {
				b := make([]byte, 32)
				if _, err := rand.Read(b); err != nil {
					http.Error(w, "internal error", http.StatusInternalServerError)
					return
				}
				token = hex.EncodeToString(b)
				http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 86400 * 30}) //nolint:gosec // Secure follows the scheme of base_url
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
				c, err := r.Cookie(csrfCookie)
				hdr := r.Header.Get("X-CSRF-Token")
				if err != nil || hdr == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(hdr)) != 1 {
					http.Error(w, "csrf token mismatch", http.StatusForbidden)
					return
				}
				if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
					http.Error(w, "cross-site request rejected", http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, withCSRF(r, token))
		})
	}
}

// devAuth lets everything through in dev mode and rejects otherwise (replaced by passkeys in M3, FR-9.x).
func devAuth(mode string, s *Server) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if mode == "dev" {
				next.ServeHTTP(w, r)
				return
			}
			if strings.Contains(r.Header.Get("Accept"), "text/html") && r.Header.Get("HX-Request") == "" {
				s.render(w, http.StatusUnauthorized, "unauthorized", pageData{})
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
}
```

`internal/server/server.go`:

```go
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
```

`internal/server/handlers.go`:

```go
package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gernotstarke/zorgscope/internal/app"
	"github.com/gernotstarke/zorgscope/internal/domain"
)

var tileTemplates = map[string]string{"header": "tile_header", "attention": "tile_attention", "repos": "tile_repos"}

func (s *Server) data(r *http.Request, v app.View) pageData {
	return pageData{View: v, CSRF: csrfToken(r), PollSeconds: s.deps.Cfg.UI.TilePollSeconds}
}

func (s *Server) buildView(w http.ResponseWriter, r *http.Request) (app.View, bool) {
	v, err := s.deps.Dashboard.Build(r.Context())
	if err != nil {
		s.deps.Log.Error("dashboard build failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return app.View{}, false
	}
	return v, true
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	v, ok := s.buildView(w, r)
	if !ok {
		return
	}
	s.render(w, http.StatusOK, "layout", s.data(r, v))
}

func (s *Server) handleTile(w http.ResponseWriter, r *http.Request) {
	name, ok := tileTemplates[r.PathValue("name")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	v, ok := s.buildView(w, r)
	if !ok {
		return
	}
	s.render(w, http.StatusOK, name, s.data(r, v))
}

func (s *Server) handleDismiss(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	id, err := domain.ParseItemID(r.PostForm.Get("id"))
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	upd, err := strconv.ParseInt(r.PostForm.Get("updated_at"), 10, 64)
	if err != nil {
		http.Error(w, "bad updated_at", http.StatusBadRequest)
		return
	}
	if err := app.Dismiss(r.Context(), s.deps.Store, s.deps.Clock, id, time.Unix(upd, 0).UTC()); err != nil {
		s.deps.Log.Error("dismiss failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.handleTileNamed(w, r, "tile_attention")
}

func (s *Server) handleDismissAll(w http.ResponseWriter, r *http.Request) {
	evs, err := s.deps.Dashboard.EvaluateAll(r.Context())
	if err == nil {
		err = app.DismissAll(r.Context(), s.deps.Store, s.deps.Clock, domain.FilterAttention(evs))
	}
	if err != nil {
		s.deps.Log.Error("dismiss all failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.handleTileNamed(w, r, "tile_attention")
}

func (s *Server) handleTileNamed(w http.ResponseWriter, r *http.Request, tmpl string) {
	v, ok := s.buildView(w, r)
	if !ok {
		return
	}
	s.render(w, http.StatusOK, tmpl, s.data(r, v))
}

func (s *Server) handleRefresh(w http.ResponseWriter, _ *http.Request) {
	n := s.deps.Refresher.TriggerAll()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("refreshing " + strconv.Itoa(n) + " sources"))
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	statuses, err := s.deps.Store.Statuses(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	type entry struct {
		SourceID    string `json:"source_id"`
		Kind        string `json:"kind"`
		LastSuccess string `json:"last_success,omitempty"`
		LastError   string `json:"last_error,omitempty"`
		Error       string `json:"error,omitempty"`
		NextRun     string `json:"next_run,omitempty"`
		Items       int    `json:"items"`
		InFlight    bool   `json:"in_flight"`
		AuthFailed  bool   `json:"auth_failed"`
	}
	fmtT := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	}
	out := make([]entry, 0, len(statuses))
	for _, st := range statuses {
		out = append(out, entry{SourceID: st.SourceID, Kind: st.Kind, LastSuccess: fmtT(st.LastSuccess), LastError: fmtT(st.LastError),
			Error: st.ErrorMsg, NextRun: fmtT(st.NextRun), Items: st.ItemCount, InFlight: st.InFlight, AuthFailed: st.AuthFailed})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"sources": out, "in_flight": s.deps.Refresher.InFlight()})
}
```

- [ ] **Step 6: Generate goldens, run tests, lint**

Run: `make go ARGS="test ./internal/server/ -run Golden -args -update"` (creates the three golden files — open them and check they look like sensible HTML), then `make go ARGS="test ./internal/server/ -race -v"` — Expected: PASS. Then `make lint` — clean.
If `TestTileFragments` fails on `13:59`: the header shows `DataAsOf` = last success (t0−1 min = 11:59 UTC = 13:59 Berlin) — check the config timezone default.

- [ ] **Step 7: Commit**

```bash
git add web internal/server
git commit -m "feat(server): dashboard page, tile fragments, dismiss/refresh, CSRF, security headers, dev auth, golden tests (FR-1.x, FR-2.5, FR-2.7, FR-9.4, QS-3.4, QS-3.5)"
```

---

### Task 15: Wiring – `cmd/zorgscope`, redacting logger, `cmd/fakesources`, demo & e2e compose, Playwright smoke, CI e2e

**Files:**
- Create: `internal/logging/logging.go`, `internal/logging/logging_test.go`, `cmd/zorgscope/main.go` (replace), `cmd/zorgscope/sources.go`, `cmd/fakesources/main.go`, `deploy/compose.e2e.yml`, `deploy/Dockerfile.e2e`, `test/e2e/package.json`, `test/e2e/package-lock.json` (generated), `test/e2e/playwright.config.ts`, `test/e2e/tests/smoke.spec.ts`, `test/e2e/config.e2e.yaml`
- Modify: `.github/workflows/ci.yml` (e2e job), `Makefile` (`demo` target), `README.md` (status)

**Interfaces:**
- Produces: `logging.New(w io.Writer, level string, secrets []string) *slog.Logger` (JSON, redacts any attribute value equal to a secret), running binaries.
- Consumes: everything from Tasks 1–14.

- [ ] **Step 1: Write the failing logger test**

`internal/logging/logging_test.go`:

```go
package logging

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedactsSecretsAndHonoursLevel(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "info", []string{"ghp_supersecret", ""})
	log.Debug("hidden")
	log.Info("token seen", "token", "ghp_supersecret", "other", "fine", "n", 3)
	log.Warn("nested", "err", "auth failed for ghp_supersecret")
	out := buf.String()
	if strings.Contains(out, "hidden") {
		t.Fatal("debug must be suppressed at info level")
	}
	if strings.Contains(out, "ghp_supersecret") {
		t.Fatalf("secret leaked: %s", out)
	}
	if !strings.Contains(out, `"token":"[REDACTED]"`) || !strings.Contains(out, `"other":"fine"`) || !strings.Contains(out, "auth failed for [REDACTED]") {
		t.Fatalf("unexpected output: %s", out)
	}
	if !strings.Contains(out, `"level":"INFO"`) || !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("expected JSON lines: %s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make go ARGS="test ./internal/logging/ -v"` — Expected: FAIL.

- [ ] **Step 3: Implement the logger**

`internal/logging/logging.go`:

```go
// Package logging builds the JSON slog logger with secret redaction (FR-10.2, QS-3.3).
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// New returns a JSON logger at the given level ("debug", "info", "warn", "error"); any string
// attribute containing one of secrets is redacted.
func New(w io.Writer, level string, secrets []string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	clean := make([]string, 0, len(secrets))
	for _, s := range secrets {
		if s != "" {
			clean = append(clean, s)
		}
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Value.Kind() != slog.KindString {
			return a
		}
		v := a.Value.String()
		for _, s := range clean {
			if strings.Contains(v, s) {
				v = strings.ReplaceAll(v, s, "[REDACTED]")
			}
		}
		return slog.String(a.Key, v)
	}})
	return slog.New(h)
}
```

Run: `make go ARGS="test ./internal/logging/ -v"` — Expected: PASS.

- [ ] **Step 4: Wire the application**

`cmd/zorgscope/sources.go`:

```go
package main

import (
	"github.com/gernotstarke/zorgscope/internal/adapters/github"
	"github.com/gernotstarke/zorgscope/internal/app"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// registerSources is the single place where source kinds are wired (QS-4.2). M2 adds
// plausible, todoist, feed and watch here.
func registerSources(reg *app.Registry) {
	reg.Register(ports.KindGitHubRepo, githubSources)
}

func githubSources(d app.Deps) ([]app.Source, error) {
	cfg := d.Cfg
	if !cfg.GitHub.Enabled {
		return nil, nil
	}
	client := github.NewClient(d.HTTP, cfg.GitHub.BaseURL, cfg.Secrets.GitHubToken, d.Sink)
	var out []app.Source
	names := make([]string, 0, len(cfg.GitHub.Repos))
	for _, r := range cfg.GitHub.Repos {
		f, err := github.NewRepoFetcher(client, r.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, app.Source{Fetcher: f, Interval: cfg.GitHub.RepoInterval(r.Name)})
		names = append(names, r.Name)
	}
	if cfg.GitHub.Mentions {
		out = append(out, app.Source{Fetcher: github.NewMentionsFetcher(client, names), Interval: cfg.GitHub.PollInterval})
	}
	return out, nil
}
```

`cmd/zorgscope/main.go` (replaces the Task 1 stub):

```go
// Command zorgscope runs the dashboard: config → store → sources → scheduler → HTTP (arc42 §6.7).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/clock"
	"github.com/gernotstarke/zorgscope/internal/adapters/sqlite"
	"github.com/gernotstarke/zorgscope/internal/app"
	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/logging"
	"github.com/gernotstarke/zorgscope/internal/server"
)

func main() {
	if err := run(); err != nil {
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			fmt.Fprintln(os.Stderr, "zorgscope:", err)
			os.Exit(2)
		}
		slog.Error("zorgscope failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := os.Getenv("ZORGSCOPE_CONFIG")
	if cfgPath == "" {
		cfgPath = "config/zorgscope.yaml"
	}
	cfg, err := config.Load(cfgPath, os.Getenv)
	if err != nil {
		return err
	}
	log := logging.New(os.Stdout, cfg.LogLevel, []string{cfg.Secrets.GitHubToken, cfg.Secrets.PlausibleAPIKey,
		cfg.Secrets.TodoistToken, cfg.Secrets.SessionSecret, cfg.Secrets.EnrollToken})
	slog.SetDefault(log)

	store, err := sqlite.Open(cfg.DataPath)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	clk := clock.Real{}
	book := app.NewCredentialBook(clk)
	reg := app.NewRegistry()
	registerSources(reg)
	sources, err := reg.Build(app.Deps{Cfg: cfg, HTTP: &http.Client{Timeout: 30 * time.Second}, Sink: book, Log: log})
	if err != nil {
		return err
	}
	sched := app.NewScheduler(store, store, clk, log, time.Duration(cfg.UI.RefreshMinGapSeconds)*time.Second)
	for _, s := range sources {
		sched.Add(s.Fetcher, s.Interval)
	}
	snap := app.NewSnapshotter(store, store, store, sched.SourceIDs, cfg.Snapshot.Hour, cfg.Snapshot.Minute,
		cfg.Server.Location, cfg.Snapshot.RetentionDays, clk, log)
	dash := app.NewDashboard(store, clk, cfg)
	srv, err := server.New(server.Deps{Dashboard: dash, Refresher: sched, Store: store, Clock: clk, Cfg: cfg, Log: log,
		Ready: func() bool { return true }})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go sched.Run(ctx)
	go snap.Run(ctx, time.Minute)

	httpSrv := &http.Server{
		Addr: fmt.Sprintf(":%d", cfg.Server.Port), Handler: srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	log.Info("zorgscope listening", "addr", httpSrv.Addr, "base_url", cfg.Server.BaseURL, "sources", len(sources), "auth_mode", cfg.AuthMode)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	log.Info("shutting down")
	return httpSrv.Shutdown(shutdownCtx)
}
```

`cmd/fakesources/main.go`:

```go
// Command fakesources serves fake upstream APIs (GitHub in M1) for e2e tests and the local demo.
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	githubfake "github.com/gernotstarke/zorgscope/test/fakes/github"
)

func main() {
	addr := flag.String("addr", ":9090", "listen address")
	flag.Parse()
	gh := githubfake.New()
	githubfake.Seed(gh, time.Now())
	slog.Info("fakesources listening", "addr", *addr, "github", "/graphql, /repos/…/actions/runs, /notifications, /__control/*")
	srv := &http.Server{Addr: *addr, Handler: gh.Handler(), ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("fakesources stopped", "err", err)
		os.Exit(1)
	}
}
```

Run: `make build` — Expected: `bin/zorgscope` and `bin/fakesources` built. `make lint` — clean. `make test` — all green.

- [ ] **Step 5: e2e configuration, compose and Playwright**

`test/e2e/config.e2e.yaml`:

```yaml
server:
  base_url: http://localhost:8080
ui:
  tile_poll_seconds: 5
  refresh_min_gap_seconds: 1
github:
  enabled: true
  me: gernotstarke
  poll_interval: 10s
  grace_period: 4h
  repos: [arc42/arc42-template, arc42/arc42.org-site]
plausible: { enabled: false }
todoist: { enabled: false }
feeds: { enabled: false }
watch: { enabled: false }
```

`deploy/compose.e2e.yml`:

```yaml
services:
  fakesources:
    build:
      context: ..
      dockerfile: deploy/Dockerfile
      args: { CMD: fakesources }
    command: ["-addr", ":9090"]
  zorgscope:
    build:
      context: ..
      dockerfile: deploy/Dockerfile
    depends_on: [fakesources]
    ports: ["${PORT:-8080}:8080"]
    environment:
      ZORGSCOPE_CONFIG: /e2e/config.e2e.yaml
      ZORGSCOPE_DATA: /data/zorgscope.db
      ZORGSCOPE_BASE_URL: http://localhost:8080
      GITHUB_BASE_URL: http://fakesources:9090
      GITHUB_TOKEN: fake-token
      AUTH_MODE: dev
      LOG_LEVEL: info
    volumes:
      - ../test/e2e/config.e2e.yaml:/e2e/config.e2e.yaml:ro
    tmpfs: ["/data"]
  playwright:
    build:
      context: ..
      dockerfile: deploy/Dockerfile.e2e
    depends_on: [zorgscope]
    environment:
      BASE_URL: http://zorgscope:8080
      FAKES_URL: http://fakesources:9090
      CI: "1"
    volumes:
      - ../test/e2e/playwright-report:/e2e/playwright-report
      - ../test/e2e/test-results:/e2e/test-results
```

`deploy/Dockerfile.e2e`:

```dockerfile
FROM mcr.microsoft.com/playwright:v1.62.1-noble
WORKDIR /e2e
COPY test/e2e/package.json test/e2e/package-lock.json ./
RUN npm ci
COPY test/e2e/ ./
CMD ["npx", "playwright", "test"]
```

`test/e2e/package.json`:

```json
{
  "name": "zorgscope-e2e",
  "private": true,
  "devDependencies": {
    "@playwright/test": "1.62.1"
  }
}
```

Generate the lock file (once, commit it): `docker run --rm -v "$PWD/test/e2e":/e2e -w /e2e node:22 npm install --package-lock-only`

`test/e2e/playwright.config.ts`:

```ts
import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './tests',
  timeout: 60_000,
  workers: 1,
  fullyParallel: false,
  retries: 1,
  reporter: [['list'], ['html', { open: 'never', outputFolder: 'playwright-report' }]],
  use: {
    baseURL: process.env.BASE_URL ?? 'http://localhost:8080',
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { browserName: 'chromium' } }],
});
```

`test/e2e/tests/smoke.spec.ts`:

```ts
import { test, expect } from '@playwright/test';

const FAKES = process.env.FAKES_URL ?? 'http://localhost:9090';

test('dashboard renders seeded attention items and repo cards', async ({ page }) => {
  await page.goto('/');
  await expect(page).toHaveTitle(/zorgscope/);
  const attention = page.locator('#tile-attention');
  await expect(attention).toBeVisible();
  // first fetch happens right after start; the tile polls every 5 s
  await expect(attention.getByText('Add example stakeholder table')).toBeVisible({ timeout: 30_000 });
  await expect(attention.locator('.badge-unanswered').first()).toBeVisible();
  await expect(attention.locator('.badge-new').first()).toBeVisible();      // #240 opened 2 h ago
  await expect(attention.locator('.badge-build_failed')).toHaveCount(1);    // arc42.org-site deploy failed
  const repos = page.locator('#tile-repos');
  await expect(repos.getByText('arc42-template')).toBeVisible();
  await expect(repos.locator('.build-failed')).toHaveCount(1);
  await expect(page.locator('#tile-header .source-ok').first()).toBeVisible();
});

test('a newly opened issue appears with NEW after refresh (QS-1.1)', async ({ page, request }) => {
  await page.goto('/');
  await expect(page.locator('#tile-attention .row').first()).toBeVisible({ timeout: 30_000 });
  const now = new Date().toISOString();
  const res = await request.post(`${FAKES}/__control/issues`, {
    data: { repo: 'arc42/arc42-template', issue: { Number: 4711, Title: 'Freshly opened by contributor', Author: 'contributor', CreatedAt: now, UpdatedAt: now } },
  });
  expect(res.status()).toBe(204);
  await page.getByRole('button', { name: /refresh/ }).click();
  const row = page.locator('#tile-attention .row', { hasText: 'Freshly opened by contributor' });
  await expect(row).toBeVisible({ timeout: 30_000 });
  await expect(row.locator('.badge')).toHaveText('NEW');
});

test('dismiss removes an item and survives reload (FR-2.7)', async ({ page }) => {
  await page.goto('/');
  const row = page.locator('#tile-attention .row', { hasText: 'Add example stakeholder table' });
  await expect(row).toBeVisible({ timeout: 30_000 });
  await row.getByRole('button', { name: /dismiss/ }).click();
  await expect(page.locator('#tile-attention .row', { hasText: 'Add example stakeholder table' })).toHaveCount(0);
  await page.reload();
  await expect(page.locator('#tile-attention .row', { hasText: 'Add example stakeholder table' })).toHaveCount(0);
});

test('fits a phone viewport without horizontal scroll (QS-5.2)', async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 800 });
  await page.goto('/');
  await expect(page.locator('#tile-attention')).toBeVisible();
  const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
  expect(scrollWidth).toBeLessThanOrEqual(375);
});
```

Add to `Makefile` (after `e2e`):

```make
.PHONY: demo
demo: ## Run the dashboard against fake sources on http://localhost:$(PORT) (no tokens needed)
	$(COMPOSE_E2E) up --build -d fakesources zorgscope
	@echo ">> demo running at http://localhost:$(PORT) (stop with: make demo-stop)"
.PHONY: demo-stop
demo-stop: ## Stop the demo
	$(COMPOSE_E2E) down -v
```

Add the e2e job to `.github/workflows/ci.yml`:

```yaml
  e2e:
    runs-on: ubuntu-latest
    needs: [test]
    steps:
      - uses: actions/checkout@v4
      - run: make e2e
      - uses: actions/upload-artifact@v4
        if: failure()
        with:
          name: playwright-report
          path: test/e2e/playwright-report
```

- [ ] **Step 6: Run everything**

Run: `make e2e` — Expected: 4 passed, compose exits 0.
Run: `make demo` then open http://localhost:8080 in a browser: header, Attention tile with NEW/UNANSWERED/BUILD FAILED rows, Repositories tile with a red dot for arc42.org-site; click ✕ on a row → it disappears; click refresh → "refreshing (…)" flashes in the header. `make demo-stop`.
Run: `make app` with a real `GITHUB_TOKEN` in `.env` (and `GITHUB_BASE_URL` unset): the real repos appear within ~10 s. `make stop`.
Run: `make check` — lint, tests, docs all green.

- [ ] **Step 7: Update README status and commit**

In `README.md` replace the *Status* paragraph with: "M1 (walking skeleton) implemented: GitHub attention + repositories, snapshots, dismiss, refresh, dev auth, SQLite, Docker, CI. Next: M2 (all sources) — see `docs/plans/README.md`."

```bash
git add -A
git commit -m "feat: wire zorgscope binary, fake sources, demo/e2e compose, Playwright smoke, CI e2e (M1 complete)"
git push
```

---

## Self‑review notes (done while writing)

* **Spec coverage (M1 scope):** FR‑1.1–1.5 (T14), FR‑2.1–2.7 (T4, T11, T13, T14), FR‑3.1/3.2 (T11, T13, T14), FR‑7.1–7.3 (T3, T8, T10), FR‑8.1–8.3 (T7), FR‑9.4 (T7, T14), FR‑10.1–10.4 (T1, T14, T15), FR‑11.2 partially (token expiry is detected and stored in the CredentialBook; the Watch tile that displays it is M2), FR‑11.3 (T11). QS‑1.2/1.3/1.4/1.6/1.7 have direct tests (T4, T9, T10); QS‑1.1 and QS‑5.2 are covered by the e2e smoke; QS‑3.3/3.4/3.5 by T14/T15 tests.
* **Deferred to M2/M3 on purpose:** Plausible/Todoist/Feeds/Watch tiles, passkeys and sessions, fly.io deploy, browser matrix, axe, `/status` HTML page (M1 ships JSON), hot reload, `adding-a-source.md` (written in M2 while adding the second kind).
* **Type consistency checked:** `ports.SourceFetcher.Kind() string` (not `domain.Kind`), `domain.FetchStatus.Kind string`, `app.Deps`/`app.Source`/`app.Builder`, `server.Deps`, `pageData`, `githubfake` exported API, `config.Config` field names used by app/server/main.
* **Known plan risks for the executor:** yaml.v3 error text for unknown fields (Task 7 step 4 tells you what to adjust), html/template attribute contexts in `tile_repos.html` (if the escaper rejects the conditional `href`, render two `<a>` variants with `{{if}}…{{else}}…{{end}}` around whole elements), Playwright locator strictness (`.first()` is used where several matches are expected).
