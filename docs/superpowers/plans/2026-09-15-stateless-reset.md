# Stateless Reset Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serve the same issues-and-PRs page from a process that keeps no state between requests: no database, no refresh pipeline, no cron, three secrets, one deploy command.

**Architecture:** Keep the hexagonal skeleton and the proven parts (GitHub sign-in with push-access check, GitHub GraphQL fetcher, domain filter/grouping, templates). Add one small package, `internal/snapshot`, that caches the fetched list in memory with a TTL. Move the "last seen" mark into the signed session cookie. Delete everything that existed to serve the database: libsql, refresh, slack, migrate, the fakes' REST/control endpoints, and the web routes for runs, problems, builds, docs, stop and polling.

**Tech Stack:** Go 1.26, `html/template` + vendored htmx, `golang.org/x/oauth2`, `github.com/shurcooL/githubv4`, `gopkg.in/yaml.v3`. Docker-only local workflow, Fly.io.

**Spec:** `docs/superpowers/specs/2026-09-15-stateless-reset-design.md`

## Global Constraints

- `internal/domain` imports the standard library and itself only (depguard rule in `.golangci.yml` stays).
- No inline `<script>` or `<style>` in templates; CSP stays as it is in `internal/web/server.go`.
- No secret value is ever logged or rendered; the existing `web.Redact` and the libsql scrubber's spirit apply to every error shown on the page.
- The sign-in flow (`internal/web/signin.go`, `internal/web/auth.go` rate limiter and state cookie, `internal/adapters/github/access.go`) is not redesigned. Only the session payload gains a second integer.
- Session key derivation stays `SHA256("zorgscope-session-v2" + GITHUB_OAUTH_CLIENT_SECRET)`; existing OAuth Apps and Fly secrets are reused unchanged.
- Every task ends with `go build ./... && go vet ./... && go test -race ./...` green on the host (Go 1.27 is installed) and a commit. Task 5 ends with `make check` green (Docker).
- Commit messages name the requirement ids they touch, as the repository does today, and end with the attribution trailers the session provides.
- Do not add Make targets beyond the seven named in Task 5. Do not add a background ticker anywhere.

---

### Task 1: The snapshot cache

**Files:**

- Create: `internal/snapshot/snapshot.go`
- Create: `internal/snapshot/snapshot_test.go`
- Modify: `internal/ports/ports.go` (add `Source`; leave everything else for Task 2)

**Interfaces:**

- Consumes: `domain.Item`, `ports.Clock`.
- Produces: `ports.Source`, `snapshot.Cache`, `snapshot.Snapshot`, `snapshot.New(src ports.Source, ttl time.Duration, clock ports.Clock) *Cache`, `(*Cache).Get(ctx) Snapshot`, `(*Cache).Invalidate()`.

- [ ] **Step 1: Add the Source port**

Append to `internal/ports/ports.go`:

```go
// Source is where the item list comes from. One call returns every open issue and pull request
// of every configured repository; a partial failure returns the items that could be fetched
// together with a non-nil error, never one without the other.
type Source interface {
	Fetch(ctx context.Context) ([]domain.Item, error)
}

// SourceFunc adapts a function to Source.
type SourceFunc func(ctx context.Context) ([]domain.Item, error)

func (f SourceFunc) Fetch(ctx context.Context) ([]domain.Item, error) { return f(ctx) }
```

- [ ] **Step 2: Write the failing tests**

`internal/snapshot/snapshot_test.go`:

```go
package snapshot_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/snapshot"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

// countingSource counts calls and answers with whatever items/err are set at call time.
type countingSource struct {
	mu    sync.Mutex
	calls int
	items []domain.Item
	err   error
	block chan struct{} // when non-nil, Fetch waits on it before returning
}

func (s *countingSource) Fetch(ctx context.Context) ([]domain.Item, error) {
	s.mu.Lock()
	s.calls++
	items, err, block := s.items, s.err, s.block
	s.mu.Unlock()
	if block != nil {
		<-block
	}
	return items, err
}

func (s *countingSource) count() int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls }

func one(title string) []domain.Item { return []domain.Item{{Repo: "a/b", Number: 1, Title: title}} }

func TestFirstGetFetchesAndSecondWithinTTLDoesNot(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x")}
	c := snapshot.New(src, 5*time.Minute, clock)

	s1 := c.Get(context.Background())
	s2 := c.Get(context.Background())
	if src.count() != 1 {
		t.Fatalf("fetches = %d, want 1", src.count())
	}
	if len(s1.Items) != 1 || s1.FetchedAt != clock.t || s1.Err != nil {
		t.Fatalf("first snapshot = %+v", s1)
	}
	if s2.FetchedAt != s1.FetchedAt {
		t.Fatalf("second Get refetched: %v != %v", s2.FetchedAt, s1.FetchedAt)
	}
}

func TestGetRefetchesOnceTheSnapshotIsOlderThanTTL(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x")}
	c := snapshot.New(src, 5*time.Minute, clock)
	c.Get(context.Background())

	clock.t = clock.t.Add(5*time.Minute + time.Second)
	src.items = one("y")
	s := c.Get(context.Background())
	if src.count() != 2 || s.Items[0].Title != "y" || s.FetchedAt != clock.t {
		t.Fatalf("after ttl: fetches=%d snapshot=%+v", src.count(), s)
	}
}

func TestAFailedFetchKeepsTheOldItemsAndReportsTheError(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x")}
	c := snapshot.New(src, time.Minute, clock)
	good := c.Get(context.Background())

	clock.t = clock.t.Add(2 * time.Minute)
	src.err = errors.New("boom")
	src.items = nil
	s := c.Get(context.Background())
	if s.Err == nil || s.ErrAt != clock.t {
		t.Fatalf("error not reported: %+v", s)
	}
	if len(s.Items) != 1 || s.FetchedAt != good.FetchedAt {
		t.Fatalf("old items not kept: %+v", s)
	}
}

func TestAFailedFirstFetchYieldsAnEmptyListWithTheError(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{err: errors.New("boom")}
	c := snapshot.New(src, time.Minute, clock)
	s := c.Get(context.Background())
	if s.Err == nil || len(s.Items) != 0 || !s.FetchedAt.IsZero() {
		t.Fatalf("got %+v", s)
	}
}

func TestPartialResultIsKeptTogetherWithItsError(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("partial"), err: errors.New("one repo failed")}
	c := snapshot.New(src, time.Minute, clock)
	s := c.Get(context.Background())
	if s.Err == nil || len(s.Items) != 1 || s.FetchedAt != clock.t {
		t.Fatalf("partial result mishandled: %+v", s)
	}
}

func TestInvalidateForcesTheNextGetToFetch(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x")}
	c := snapshot.New(src, time.Hour, clock)
	c.Get(context.Background())
	c.Invalidate()
	c.Get(context.Background())
	if src.count() != 2 {
		t.Fatalf("fetches = %d, want 2", src.count())
	}
}

func TestConcurrentGetsShareOneFetch(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x"), block: make(chan struct{})}
	c := snapshot.New(src, time.Hour, clock)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.Get(context.Background()) }()
	}
	time.Sleep(50 * time.Millisecond) // let every goroutine reach the cache
	close(src.block)
	wg.Wait()
	if src.count() != 1 {
		t.Fatalf("fetches = %d, want 1", src.count())
	}
}

var _ ports.Source = (*countingSource)(nil)
```

- [ ] **Step 3: Run the tests to see them fail**

Run: `go test ./internal/snapshot/...`
Expected: FAIL, package `snapshot` does not exist.

- [ ] **Step 4: Implement the cache**

`internal/snapshot/snapshot.go`:

```go
// Package snapshot keeps the most recently fetched item list in memory and refetches it when it
// is older than a TTL. It is the whole of zorgscope's state: a process that restarts, or a Fly
// Machine that wakes from zero, starts empty and pays one fetch on its first page view.
package snapshot

import (
	"context"
	"sync"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// Snapshot is what a page renders from.
//
// Items and FetchedAt describe the last fetch that produced items — possibly with an error
// alongside, when some repositories failed and others did not. Err and ErrAt describe the last
// fetch that failed, and are cleared by the next fetch that does not. So a page can say both
// "this list is from 11:50" and "GitHub has been failing since 12:04" at once.
type Snapshot struct {
	Items     []domain.Item
	FetchedAt time.Time // zero until a fetch has returned items
	Err       error     // the most recent fetch error, nil once a fetch succeeds cleanly
	ErrAt     time.Time // when Err was recorded
}

// Cache is safe for concurrent use. A fetch runs under the mutex, so concurrent callers wait for
// the one fetch in flight rather than start their own: with one visitor and a fetch of a few
// seconds that is simpler than single-flight and equally correct.
type Cache struct {
	src   ports.Source
	ttl   time.Duration
	clock ports.Clock

	mu    sync.Mutex
	cur   Snapshot
	stale bool // set by Invalidate; cleared by the next fetch
}

// New returns an empty cache. ttl <= 0 means every Get fetches.
func New(src ports.Source, ttl time.Duration, clock ports.Clock) *Cache {
	return &Cache{src: src, ttl: ttl, clock: clock, stale: true}
}

// Get returns the current snapshot, fetching first if it is missing, invalidated, or older than
// the TTL. A failed fetch never discards the previous items.
func (c *Cache) Get(ctx context.Context) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.clock.Now()
	if !c.stale && !c.cur.FetchedAt.IsZero() && now.Sub(c.cur.FetchedAt) <= c.ttl {
		return c.cur
	}
	// A failing source is retried at most once per TTL as well, so a broken upstream does not
	// turn every page view into a fetch.
	if !c.stale && !c.cur.ErrAt.IsZero() && now.Sub(c.cur.ErrAt) <= c.ttl {
		return c.cur
	}

	items, err := c.src.Fetch(ctx)
	c.stale = false
	if items != nil || err == nil {
		c.cur.Items = items
		c.cur.FetchedAt = now
	}
	if err != nil {
		c.cur.Err = err
		c.cur.ErrAt = now
	} else {
		c.cur.Err = nil
		c.cur.ErrAt = time.Time{}
	}
	return c.cur
}

// Invalidate makes the next Get fetch regardless of age.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	c.stale = true
	c.mu.Unlock()
}
```

Note for the implementer: `TestAFailedFirstFetchYieldsAnEmptyListWithTheError` needs `items == nil` on the failed first fetch, so FetchedAt stays zero. `TestPartialResultIsKeptTogetherWithItsError` returns a non-nil slice with an error, so FetchedAt is set. Both are covered by the `items != nil || err == nil` condition.

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/snapshot/... ./internal/ports/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/snapshot internal/ports/ports.go
git commit -m "feat(snapshot): in-memory item cache with a TTL, the only state the process keeps (FR-1.1)"
```

---

### Task 2: Cut over the web layer and main; delete the storage, refresh and notification packages

This is the big one. The order inside the task matters so that the tree compiles at the end, not in between; commit once at the end.

**Files:**

- Modify: `internal/web/server.go` (Options, Server fields, route table, template set)
- Modify: `internal/web/auth.go` (session payload gains `seen`; `signedIn` → `session(r)`)
- Modify: `internal/web/signin.go` (mint with `seen = 0` on sign-in)
- Rewrite: `internal/web/dashboard.go`
- Delete: `internal/web/refresh.go`, `refresh_test.go`, `stop.go`, `stop_test.go`, `docs.go`, `docs_test.go`
- Delete templates: `builds.html`, `docs.html`, `docs_index.html`, `problems.html`, `stopping.html`, `fragments/alert.html`, `fragments/build-status.html`, `fragments/counts.html`
- Modify templates: `layout.html`, `dashboard.html`, `fragments/items.html`, `login.html` (only if it references deleted things)
- Modify: `internal/web/static/app.css` (remove rules for deleted views; keep the list, filters, NEW marker, theme)
- Delete: `internal/web/static/orbit.js` if it only served polling or the stop control; keep it otherwise
- Modify tests: `internal/web/dashboard_test.go`, `auth_test.go`, `signin_test.go`, `chrome_test.go`, `filter_test.go`, `static_cache_test.go` — delete tests of deleted features, port the rest to the fake Source
- Rewrite: `cmd/zorgscope/main.go`, `cmd/zorgscope/main_test.go`
- Delete: `internal/refresh/`, `internal/adapters/libsql/`, `internal/adapters/slack/`, `cmd/migrate/`
- Modify: `internal/ports/ports.go` (delete `Store`, `Notifier`, `SourceFetcher`; keep `FetchResult` until Task 3)
- Modify: `internal/config/config.go` only as far as compile needs it (the real config cut is Task 4)
- Modify: `go.mod`/`go.sum` via `go mod tidy` (drops libsql-client-go and goldmark)

**Interfaces:**

- Consumes: `snapshot.Cache`, `ports.Source`, `ports.AccessChecker`, `ports.Clock`, `domain.BuildDashboard` (still with today's `DashboardInput`; pass zero values for the run fields — Task 3 removes them).
- Produces: `web.Options{Config, Cache *snapshot.Cache, Clock, Log, Access, HTTPClient, BehindFlyProxy}`; routes as listed in the spec §6.

- [ ] **Step 1: Session payload with `seen`**

In `internal/web/auth.go`, replace `mint`/`valid` with a payload of two integers:

```go
// session is what the cookie proves: when the sign-in expires, and when the visitor last marked
// the list as seen (zero until they do). Both travel inside the signed payload, so neither can
// be forged, and neither needs a row anywhere.
type session struct {
	Expiry time.Time
	Seen   time.Time
}

func (c *sessionCodec) mint(s session) string {
	seen := int64(0)
	if !s.Seen.IsZero() {
		seen = s.Seen.Unix()
	}
	payload := strconv.FormatInt(s.Expiry.Unix(), 10) + ":" + strconv.FormatInt(seen, 10)
	return sessionEncoding.EncodeToString([]byte(payload)) + "." +
		sessionEncoding.EncodeToString(c.sign(payload))
}

// decode returns the session a cookie value proves, and false when the value is not a signature
// this codec produced over a session still valid at now.
func (c *sessionCodec) decode(value string, now time.Time) (session, bool) {
	encPayload, encSig, ok := strings.Cut(value, ".")
	if !ok {
		return session{}, false
	}
	payload, err := sessionEncoding.DecodeString(encPayload)
	if err != nil {
		return session{}, false
	}
	sig, err := sessionEncoding.DecodeString(encSig)
	if err != nil {
		return session{}, false
	}
	if subtle.ConstantTimeCompare(sig, c.sign(string(payload))) != 1 {
		return session{}, false
	}
	expStr, seenStr, ok := strings.Cut(string(payload), ":")
	if !ok {
		return session{}, false
	}
	exp, err1 := strconv.ParseInt(expStr, 10, 64)
	seen, err2 := strconv.ParseInt(seenStr, 10, 64)
	if err1 != nil || err2 != nil {
		return session{}, false
	}
	s := session{Expiry: time.Unix(exp, 0)}
	if seen > 0 {
		s.Seen = time.Unix(seen, 0)
	}
	return s, now.Before(s.Expiry)
}
```

`setSession(w, s session)` writes the cookie with `Expires: s.Expiry`. `signedIn(r)` becomes
`session(r *http.Request) (session, bool)`; `requireSession` calls it. `handleAuthCallback`
mints `session{Expiry: now.Add(sessionTTL)}`.

Tests to add to `auth_test.go`: a minted cookie decodes to the same expiry and seen; a cookie with
`seen` altered by one character fails; a cookie minted under the old single-integer format fails.

- [ ] **Step 2: Rewrite `dashboard.go`**

Handlers and their behaviour:

```go
// handleDashboard renders the whole page; handleItems renders only the #items fragment for the
// htmx filter form. Both read the same snapshot and the same session.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) { s.render(w, r, "dashboard.html") }
func (s *Server) handleItems(w http.ResponseWriter, r *http.Request)     { s.render(w, r, "fragments/items.html") }

func (s *Server) render(w http.ResponseWriter, r *http.Request, tmpl string) {
	sess, _ := s.session(r) // requireSession already admitted the request
	snap := s.cache.Get(r.Context())
	now := s.clock.Now()
	d := domain.BuildDashboard(domain.DashboardInput{
		Now: now, LastVisitAt: sess.Seen, Items: snap.Items,
		Repos: s.cfg.GitHub.Repos, Filter: parseFilter(r.URL.Query(), s.loc),
	})
	view := s.view(d, snap, now)
	s.execute(w, tmpl, view) // the existing template-execution helper, whatever it is called today
}

// handleSeen re-mints the cookie with seen = now and goes back to the page (FR-1.2).
func (s *Server) handleSeen(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.session(r)
	sess.Seen = s.clock.Now()
	s.setSession(w, sess)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleRefresh throws the snapshot away so the redirected GET fetches (FR-1.3).
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	s.cache.Invalidate()
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleLogout clears the session cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
```

The view model carries: `FetchedAt` formatted in the configured timezone (or "never"), an error
notice built from `snap.Err` passed through `web.Redact(s.cfg.Secrets, err.Error())` with
`snap.ErrAt`, `NewTotal`, `Total`, `Shown`, the filter echo, and the groups. Keep the existing
`itemsView` shape as far as templates use it; drop build, problem and run fields.

Route table (replace the existing slice):

```go
{"GET", "/healthz", s.handleHealthz, public},
{"GET", "/login", s.handleLoginForm, public},
{"GET", "/auth/github", s.handleAuthStart, public},
{"GET", "/auth/callback", s.handleAuthCallback, public},
{"GET", "/static/", s.static, public},
{"GET", "/{$}", s.handleDashboard, sessionRedirect},
{"GET", "/items", s.handleItems, session401},
{"POST", "/seen", s.handleSeen, session401},
{"POST", "/refresh", s.handleRefresh, session401},
{"POST", "/logout", s.handleLogout, session401},
```

Use whatever the current `route` struct calls its guard field; the three guard kinds already exist
(`requireSession(next, redirect bool)` and none).

- [ ] **Step 3: Templates**

`dashboard.html`: header with the logo, "fetched HH:MM" (or "never"), the error notice when set,
`NEW n` badge, a form `POST /seen` with a "Mark all seen" button, a form `POST /refresh` with a
"Refresh" button, a form `POST /logout`; then the filter form exactly as today; then
`{{template "fragments/items.html" .}}`. Remove the poll attribute (`hx-trigger="every …"`) and
every reference to builds, problems, docs, stop.

`fragments/items.html`: as today (groups, NEW marker per item, empty-state line), minus the
out-of-band counts/alert/build blocks.

`layout.html`: drop the nav links to `/docs`, `/problems`, `/builds`.

- [ ] **Step 4: Server options and main**

`web.Options` loses `Store`, `Runner`, `Stop`; gains `Cache *snapshot.Cache`. `NewServer` (or
whatever the constructor is called) panics/errors on a nil Cache the way it does on a nil Access.

`cmd/zorgscope/main.go` `run`:

```go
cfg, err := config.Load(...)            // as today
hc := &http.Client{Timeout: 20 * time.Second}
fetcher := github.NewIssueFetcher(githubConfig(cfg), hc)
src := ports.SourceFunc(func(ctx context.Context) ([]domain.Item, error) {
	r, err := fetcher.Fetch(ctx)
	return r.Items, err
})
cache := snapshot.New(src, cfg.GitHub.CacheTTL, ports.SystemClock{})
srv, err := web.New(web.Options{
	Config: cfg, Cache: cache, Clock: ports.SystemClock{}, Log: log,
	Access: github.NewAccessChecker(cfg.GitHub.BaseURL, cfg.GitHub.AuthRepo, hc),
	HTTPClient: hc,
})
return serve(ctx, log, srv)
```

Until Task 4 lands, read the TTL from the existing `cfg.Refresh.Interval` field and default it to
5 minutes when zero; Task 4 renames it. Delete `openStore`, `buildFetchers`, `buildNotifier`, the
`stop` plumbing and the migrate-on-start code. `main_test.go`: keep whatever still applies (config
failure exits non-zero, secret never printed), delete the rest.

- [ ] **Step 5: Delete the dead packages and tidy**

```bash
git rm -r internal/refresh internal/adapters/libsql internal/adapters/slack cmd/migrate
```

In `ports.go` delete `Store`, `Notifier`, `SourceFetcher`, the lease/run types they reference.
In `internal/config/config.go` delete only what no longer compiles (`Notifications`, the Turso
and Slack secret fields may stay until Task 4 if nothing breaks). Run `go mod tidy`.

- [ ] **Step 6: Tests**

`internal/web/dashboard_test.go`: build the server with a fake Source (a `ports.SourceFunc`
returning fixed items) and a fake clock. Cover: anonymous GET / redirects to /login; signed-in GET /
lists items grouped in configured order; NEW marker appears for an item created after `seen` and
not before; `POST /seen` re-mints the cookie and the next GET shows no NEW; `POST /refresh` makes
the source fetch again; a failing source shows the notice and keeps the previous list; `GET /items`
with `?repo=` narrows the list and returns only the fragment; the error notice never contains a
secret (reuse the canary pattern from the existing tests). Sign-in tests stay as they are.

- [ ] **Step 7: Build, vet, test, commit**

```bash
go build ./... && go vet ./... && go test -race ./...
git add -A
git commit -m "refactor(web,cmd): serve the page from an in-memory snapshot and a signed seen-mark; drop storage, refresh and notifications (FR-1.1, FR-1.2, FR-8.3)"
```

---

### Task 3: Slim the domain, the GitHub adapter and the fakes

**Files:**

- Modify: `internal/domain/item.go` (drop `Source`, `FirstSeenAt`; `IsNew` on `CreatedAt`)
- Modify: `internal/domain/dashboard.go` (`DashboardInput{Now, LastVisitAt, Items, Repos, Filter}`, `Dashboard{GeneratedAt, LastVisitAt, NewTotal, Total, Shown, Filter, Groups}`)
- Delete: `internal/domain/build.go`, `build_internal_test.go`, `problem.go`, `problem_test.go`, `source.go`, `source_test.go`
- Modify tests: `item_test.go`, `dashboard_test.go`, `filter_test.go`
- Modify: `internal/ports/ports.go` (delete `FetchResult`)
- Modify: `internal/adapters/github/issues.go` (`Fetch` returns `([]domain.Item, error)`; `Config` loses `RESTBaseURL`, `BadgeBaseURL`; delete `Name()`)
- Delete: `internal/adapters/github/builds.go`, `builds_test.go`
- Modify: `internal/adapters/github/issues_test.go`, `cost_test.go` (return type)
- Modify: `internal/fakesources/server.go` (routes: `GET /{$}`, `POST /graphql`, `GET /login/oauth/authorize`, `POST /login/oauth/access_token`, `GET /repos/{owner}/{repo}`, `POST /_control/oauth-user`)
- Delete: `internal/fakesources/github_rest.go`, `github_rest_test.go`, `control.go`, the `testdata/github/runs` directory, and the `fail`/`add-issue`/`reset`/`oauth-callback` control handlers
- Modify: `cmd/zorgscope/main.go` (drop the `SourceFunc` shim, pass the fetcher directly; drop REST/badge config)
- Modify: `internal/web` wherever `FirstSeenAt`, `Source` or `FetchResult` still appear

**Interfaces:**

- Produces: `domain.Item{Kind, Repo, Number, Title, Summary, URL, Author, State, CreatedAt, UpdatedAt}`, `(Item).IsNew(seen time.Time) bool`, `domain.DashboardInput`, `domain.Dashboard` as above; `github.Config{Token, BaseURL, Repos}`; `(*IssueFetcher).Fetch(ctx) ([]domain.Item, error)` so it satisfies `ports.Source`.

- [ ] **Step 1: Domain first, tests first**

`IsNew`:

```go
// IsNew reports whether the item was created after seen. The zero seen means the visitor has
// never marked the list, and then nothing is new: a first visit that shouts NEW at every item
// says nothing.
func (i Item) IsNew(seen time.Time) bool {
	return !seen.IsZero() && i.CreatedAt.After(seen)
}
```

Update `item_test.go`: created after seen → new; created at seen → not new; zero seen → not new.
`BuildDashboard` keeps `groupByRepo` and `CountNew` verbatim, drops `itemsBySource`,
`sourceHealth`, `buildStatus`, `buildProblems`, `lastRun`. Delete the build/problem/source
tests. Keep `SortItems` (new first, then newest created).

Run: `go test ./internal/domain/...` — expected PASS, and coverage still ≥ 90 %:
`go test -coverprofile=d.out ./internal/domain/... && go tool cover -func=d.out | tail -1`.

- [ ] **Step 2: Adapter and ports**

Change `Fetch` to return `([]domain.Item, error)` (body unchanged apart from the return line),
delete `builds.go` and its tests, drop the two REST/badge fields from `Config` and from
`githubConfig` in main. Delete `ports.FetchResult`. Fix `issues_test.go` and `cost_test.go`
assertions to the slice.

- [ ] **Step 3: Fakes**

Reduce `server.go` to the six routes above. Delete the REST handler, the control handlers that
mutate fixtures, and the runs fixtures. `cmd/fakesources/main.go` should need no change beyond
compile. Update `server_test.go` and `github_graphql_test.go` for the removed routes.

- [ ] **Step 4: Build, vet, test, commit**

```bash
go build ./... && go vet ./... && go test -race ./...
git add -A
git commit -m "refactor(domain,github,fakes): items are new when created after the seen mark; drop builds, problems, source health and the fakes' REST and control endpoints (FR-1.2)"
```

---

### Task 4: Configuration, environment, Compose, Fly

**Files:**

- Modify: `internal/config/config.go`, `config_test.go`, `testdata/*`
- Modify: `config/zorgscope.yaml`
- Modify: `deploy/env.example`, `deploy/compose.yml`, `deploy/fly.toml`
- Modify: `cmd/zorgscope/main.go` (read `cfg.GitHub.CacheTTL`)

**Interfaces:**

- Produces: `config.Config{Timezone, GitHub{AuthRepo, Repos, CacheTTL, BaseURL, OAuthBaseURL}, Secrets{GitHubToken, OAuthClientID, OAuthClientSecret}}`.

- [ ] **Step 1: Config**

YAML keys: `timezone`, `github.auth_repo`, `github.repos`, `github.cache_ttl` (duration, default
`5m`). Delete `refresh.*`, `github.login`, `notifications.*`, `GITHUB_BADGE_BASE_URL`,
`SLACK_WEBHOOK_URL`, `TURSO_URL`, `TURSO_AUTH_TOKEN`, `REFRESH_SECRET`, `minSecretLen`.

Validation: `auth_repo` in owner/name form; at least one repo, every one in owner/name form;
`GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET`, `GITHUB_TOKEN` non-empty (the third is new
— without it the page is empty, which looks like an outage; fail at start instead); the existing
`checkOAuthBaseURL`. Tests: each missing value names itself in the error; `cache_ttl` defaults
to 5m; the secret canary test still proves no value is ever in an error string.

Write `config/zorgscope.yaml` as in the spec §8 with the eight repositories.

- [ ] **Step 2: Environment example and Compose**

`deploy/env.example`: the three required lines with the comment about the local OAuth App's
callback URL, then the two optional `GITHUB_BASE_URL` / `GITHUB_OAUTH_BASE_URL` lines commented
out with `http://host.docker.internal:9090`, then `LOG_LEVEL=info`. Nothing else.

`deploy/compose.yml`: the `app` service only — build context, `env_file: ../.env`,
`PORT: "8080"`, port mapping. No `db`, no volume.

- [ ] **Step 3: Fly**

`deploy/fly.toml`: replace the header comment with two lines (scale to zero; a visitor starts the
Machine, the first view fetches). Everything else unchanged.

- [ ] **Step 4: Build, vet, test, commit**

```bash
go build ./... && go vet ./... && go test -race ./...
git add -A
git commit -m "config: three secrets, cache_ttl, no storage or notification settings; Compose without a database (FR-8.1)"
```

---

### Task 5: Makefile, CI, `make check`

**Files:**

- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`
- Delete: `.github/workflows/deploy.yml`
- Modify: `.golangci.yml` only if a depguard rule names a deleted package

- [ ] **Step 1: Makefile**

Seven targets: `help`, `backend`, `client`, `fakes`, `check`, `deploy`, `clean`.

`backend`: the `.env` guard needs `GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET`,
`GITHUB_TOKEN`; the hint text names the local OAuth App. Then `$(COMPOSE) up --build`.

`check`:

```make
check: ## Everything CI runs, plus fly.toml validation: vet, lint, race tests, domain coverage, markdownlint
	$(GO_RUN) go vet ./...
	docker run --rm -t -v "$(CURDIR)":/src -w /src -v $(GOMOD_VOL):/go/pkg/mod $(LINT_IMAGE) golangci-lint run ./...
	$(GO_RUN) sh -c 'go test -race -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1'
	$(GO_RUN) sh -c 'go test -coverprofile=domain.out ./internal/domain/... && \
	  go tool cover -func=domain.out | tail -1 | \
	  awk "{ if (\$$3+0 < 90) { print \"domain coverage below 90%\"; exit 1 } }"'
	docker run --rm -v "$(CURDIR)":/work -w /work davidanson/markdownlint-cli2:latest "docs/**/*.md" "README.md"
	$(FLY_RUN) config validate --strict --app "$(FLY_APP)" --config "$(FLY_CONFIG)"
```

No `db` service, no `-p 1`, no `GO_RUN_DB`, no lychee.

`deploy`:

```make
deploy: ## Build remotely on Fly and deploy; needs a fly login on this machine
	$(FLY_RUN) deploy --remote-only --config $(FLY_CONFIG)
```

Reuse the existing `FLY_RUN`/`FLY_CONFIG` variables (they exist for `check`). If `FLY_RUN` is a
Docker invocation, make sure it mounts the fly config directory the same way `check` does, and
that `fly auth` state is reachable; if it is not, run the host `fly` binary and say so in the
target's help text.

- [ ] **Step 2: CI**

`ci.yml`: one job `check` — checkout, setup-go 1.26 with cache, `go vet ./...`,
`golangci/golangci-lint-action@v9` with `version: v2.12.0`, `go test -race ./...`, the domain
coverage gate. No services, no docs job, no image job. `git rm .github/workflows/deploy.yml`.

- [ ] **Step 3: Run the gate**

Run: `make check`
Expected: green end to end. If markdownlint complains about docs, fix formatting only; content is
Task 6.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "build: make deploy, make check without a database, one CI job; the deploy workflow is gone (QS-5.3)"
```

---

### Task 6: Documentation

**Files:**

- Create: `docs/decisions/0010-stateless-no-database.md`
- Modify: `docs/decisions/0003-fly-scale-to-zero-external-cron.md`, `0004-turso-libsql.md`, `0005-embedded-sql-migrations.md`, `0006-first-seen-versus-last-visit.md` (status: superseded by 0010, one sentence each), `docs/decisions/README.md`
- Rewrite: `docs/requirements/01-goals.md`, `03-constraints.md`, `04-functional-requirements.md`, `05-quality-requirements.md`, `06-glossary.md`; touch `02-stakeholders.md` only if it names dropped features
- Delete: `docs/concepts/data-storage.md`
- Rewrite: `docs/concepts/configuration.md`, `docs/concepts/security-and-tokens.md`
- Delete: every file in `docs/superpowers/plans/` and `docs/superpowers/specs/` except `2026-09-15-stateless-reset-design.md` and `2026-09-15-stateless-reset.md`
- Rewrite: `README.md`

- [ ] **Step 1: ADR-0010**

Use `docs/decisions/adr-template.md`. Context: the first production deploy failed on every stateful
piece; the page needs none of them. Decision: no database; NEW is created-after-seen carried in the
signed session cookie; the list is cached in memory for `cache_ttl`; refresh is a button.
Consequences: no history, no notifications, no build status, first view after scale-to-zero pays
one fetch; the session cookie forgets the mark on sign-out; if state is ever wanted again, a Fly
volume with SQLite is the cheapest path. Supersedes 0004, 0005, 0006 and the cron half of 0003.

- [ ] **Step 2: Requirements**

Functional requirements, each with acceptance criteria that a test in the tree proves:

- FR‑1.1 The page lists open issues and PRs of the configured repositories, grouped in configuration order, from a list at most `cache_ttl` old.
- FR‑1.2 NEW marks items created after the visitor's last *mark seen*; "Mark all seen" moves the mark to now.
- FR‑1.3 "Refresh" fetches immediately.
- FR‑1.4 A failed fetch keeps the last list and says since when GitHub has been failing.
- FR‑2.1 Filters: repository, kind, created since, text; they never change the NEW count.
- FR‑8.1 Configuration: YAML for the list, environment for three secrets, refuse to start on a missing one.
- FR‑8.3 Sign-in with GitHub, push access to `auth_repo` admits, as ADR‑0009 (keep the five ACs).

Quality requirements: keep the ones with a test or lint behind them (domain on stdlib only, no
secret in any output, rate-limited sign-in, CI under three minutes, image under 25 MB if the check
remains — drop that line if the image job is gone). Delete the rest. Goals: one goal. Constraints:
Go, Docker-only, Fly scale-to-zero, GitHub OAuth App per environment.

- [ ] **Step 3: Concepts and README**

`configuration.md` = spec §8 in prose with the `.env` lines and the Fly secrets commands.
`security-and-tokens.md` = spec §4 plus what each of the three secrets can do if leaked and how to
rotate it (rotate the client secret → everyone signed out; rotate the PAT → nothing else changes).

README: what it is (two sentences), screenshot placeholder line, the three `.env` lines, the
seven targets in a table, "deploy = `make deploy`", pointers to the ADRs and requirements.

- [ ] **Step 4: Lint the docs and commit**

Run: `docker run --rm -v "$PWD":/work -w /work davidanson/markdownlint-cli2:latest "docs/**/*.md" "README.md"`
Expected: clean.

```bash
git add -A
git commit -m "docs: ADR-0010 stateless reset; requirements, concepts and README cut to what the app does"
```

---

### Task 7: Verify against real GitHub and deploy (needs Gernot's laptop)

Not a subagent task. Gernot, or a session with his shell:

- [ ] `.env` holds the three lines; `make backend`; open <http://localhost:8080>; sign in; the
  eight repositories' items appear; "Mark all seen" clears NEW; "Refresh" works; a filter narrows.
- [ ] `fly secrets set GITHUB_TOKEN=<pat> -a zorgscope` (the two OAuth secrets are already there).
- [ ] `make deploy`; open <https://zorgscope.fly.dev>; sign in; same checks.
- [ ] Merge the branch to main. Update the memory file `zorgscope-project.md` to the new state
  (seven targets including deploy, no database, cookie-borne seen mark).

## Self-review notes

- Spec §4 (cookie), §5 (cache, NEW), §6 (routes), §7 (fakes), §8 (config), §9 (make/CI), §10
  (docs) each map to Tasks 2, 1+3, 2, 3, 4, 5, 6. §11 testing is spread over Tasks 1–3.
- Names used across tasks: `ports.Source`/`SourceFunc` (T1, used T2, T3), `snapshot.Cache.Get/Invalidate` (T1, used T2), `session{Expiry, Seen}` (T2), `Item.IsNew(seen)` (T3, called by `groupByRepo` unchanged), `GitHub.CacheTTL` (T4, read by main from T2's fallback onward).
- Task 2 deliberately keeps `DashboardInput`'s dead fields for one task so that the domain cut in Task 3 is reviewable on its own.
