# Fast First View Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cut the first page view after a cold start from about eleven seconds to about two and a half, and show the animated zorgscope mark instead of a blank tab while GitHub is being asked.

**Architecture:** The GitHub adapter fetches every repository and connection side by side instead of one after the other. The snapshot cache never blocks: `Get` returns at once, starting one detached, budget-bounded goroutine when a fetch is due and reporting `Fetching` while it runs. The two page handlers answer a request during a fetch with a wait page — the mark, animated by CSS alone, with a status line — that polls its own path with htmx until the page is ready, and reloads by `<meta http-equiv="refresh">` when JavaScript is off.

**Tech Stack:** Go 1.26 module (host Go 1.27), `html/template`, htmx 2.0.4 (vendored), hand-written CSS with `light-dark()`, `sync.WaitGroup`, `net/http/httptest`. Docker-only gate (`make check`).

**Spec:** `docs/superpowers/specs/2026-09-16-fast-first-view-design.md`

## Global Constraints

- No inline `<script>`, `<style>` or `style=` attribute anywhere; `contentSecurityPolicy` in `internal/web/server.go` does not change (QS‑4.4). No new JavaScript file.
- No background ticker, no loop, no timer that runs without a request: the only goroutine is the one a request starts, bounded by `fetchBudget = 60 * time.Second` (C‑3, ADR‑0011).
- `Fetch` keeps its contract: every per-repository error collected with `errors.Join`, every fetched item returned, items in configuration order with issues before pull requests (FR‑1.4); `TestGraphQLRequestBudget` keeps counting exactly 20 requests for 10 repositories (QS‑3.5).
- The wait page appears on every fetch — cold start, expired TTL, Refresh — never a stale list while a fetch runs.
- Poll interval `every 500ms`; poll element id `waiting`; a poll during a fetch is answered `204 No Content`; the no-JavaScript fallback is `<noscript><meta http-equiv="refresh" content="2"></noscript>` in `<head>`.
- Status line text, exactly: `Asking GitHub about N repositories…` (N = `len(cfg.GitHub.Repos)`, the ellipsis is one character U+2026).
- Large mark: `internal/web/static/logo-large.jpg`, 512 px, JPEG quality 80, served as `image/jpeg`, never gzipped, at most 45 kB.
- Budgets: wait page HTML ≤ 20 kB; its static assets ≤ 100 kB on the wire; dashboard budgets of QS‑2.3 unchanged.
- No new configuration key, no new Make target, no new route, no database.
- Every task ends with `go build ./... && go vet ./... && go test -race ./...` green on the host and exactly one commit; Task 5 also ends with `make check` green.
- Stage files explicitly. Never `git add -A`. Never commit `.agent/`, `.agents/`, `.claude/`, `_bmad/`, `.env`, `coverage.out` or `domain.out`.
- Commit messages name the requirement ids they touch and end with a blank line and `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`.
- Comments use British spelling (golangci `misspell` locale UK): colour, behaviour, normalise. `revive` runs: every exported identifier has a doc comment starting with its name.
- Work on branch `feat/fast-first-view` (already exists, spec committed at `10f40e8`).

---

### Task 1: Fan out the GitHub fetch

Model tier: cheap (the code is complete below).

**Files:**

- Modify: `internal/adapters/github/issues.go` (the `Fetch` method, lines 68–96, and its doc comment)
- Modify: `internal/adapters/github/issues_test.go` (append tests)

**Interfaces:**

- Consumes: `f.fetchIssues(ctx, owner, name)`, `f.fetchPullRequests(ctx, owner, name)`, `splitRepo(repo)` — all unchanged.
- Produces: `(*IssueFetcher).Fetch(ctx) ([]domain.Item, error)` — same signature, same result, faster.

- [ ] **Step 1: Write the failing tests**

Append to `internal/adapters/github/issues_test.go`:

```go
// QS-2.7: the requests of one fetch run side by side. Twenty requests that each take 200 ms would
// take four seconds one after the other; side by side they take about one round trip.
func TestFetchRunsRepositoriesSideBySide(t *testing.T) {
	echo := connectionEchoHandler(t, func(string) (nodes []any, hasNextPage bool, endCursor string) {
		return []any{}, false, ""
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		echo(w, r)
	}))
	defer srv.Close()

	f := github.NewIssueFetcher(github.Config{
		Token: "x", BaseURL: srv.URL, Repos: representativeRepos(),
	}, srv.Client())

	start := time.Now()
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("a fetch of %d repositories took %v; side by side it should take about one "+
			"200 ms round trip (QS-2.7)", len(representativeRepos()), elapsed)
	}
}

// FR-1.4: running side by side changes when items arrive, never where they land. The result is
// the sequential one — repositories in configuration order, a repository's issues before its pull
// requests — however the upstream happens to answer. The handler answers after a random pause so
// that a fetch which merely appended results as they came in would fail here.
func TestFetchKeepsConfigurationOrder(t *testing.T) {
	echo := connectionEchoHandler(t, func(string) (nodes []any, hasNextPage bool, endCursor string) {
		return []any{stuckNode(1)}, false, ""
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Duration(rand.IntN(30)) * time.Millisecond)
		echo(w, r)
	}))
	defer srv.Close()

	repos := []string{"org/one", "org/two", "org/three", "org/four"}
	f := github.NewIssueFetcher(github.Config{Token: "x", BaseURL: srv.URL, Repos: repos}, srv.Client())

	items, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	var got []string
	for _, it := range items {
		got = append(got, string(it.Kind)+":"+it.Repo)
	}
	var want []string
	for _, repo := range repos {
		want = append(want, string(domain.KindIssue)+":"+repo, string(domain.KindPR)+":"+repo)
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("items in order %v, want %v", got, want)
	}
}

// FR-1.4 AC4 across the fan-out: one repository failing, one that is not owner/name, and the
// rest fine. Every error is collected, and the good repositories' items are all returned.
func TestFetchCollectsEveryErrorAndKeepsTheGoodItems(t *testing.T) {
	echo := connectionEchoHandler(t, func(string) (nodes []any, hasNextPage bool, endCursor string) {
		return []any{stuckNode(1)}, false, ""
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"name":"broken"`) {
			http.Error(w, "upstream on fire", http.StatusBadGateway)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		echo(w, r)
	}))
	defer srv.Close()

	repos := []string{"org/good", "org/broken", "not-a-repo", "org/fine"}
	f := github.NewIssueFetcher(github.Config{Token: "x", BaseURL: srv.URL, Repos: repos}, srv.Client())

	items, err := f.Fetch(context.Background())
	if err == nil {
		t.Fatal("want an error naming the broken repository and the malformed name")
	}
	for _, needle := range []string{"org/broken", `"not-a-repo" is not owner/name`} {
		if !strings.Contains(err.Error(), needle) {
			t.Errorf("error %q does not mention %s", err, needle)
		}
	}
	got := map[string]int{}
	for _, it := range items {
		got[it.Repo]++
	}
	if got["org/good"] != 2 || got["org/fine"] != 2 || got["org/broken"] != 0 {
		t.Errorf("items per repository = %v, want org/good:2 org/fine:2 org/broken:0", got)
	}
}

// A cancelled context ends the fetch promptly, and Fetch does not return before every one of its
// goroutines has: nothing of a fetch outlives the call.
func TestFetchStopsWhenCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // hang until the client gives up
	}))
	defer srv.Close()

	f := github.NewIssueFetcher(github.Config{
		Token: "x", BaseURL: srv.URL, Repos: representativeRepos(),
	}, srv.Client())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := f.Fetch(ctx)
	if err == nil {
		t.Fatal("want the cancellation reported as an error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Fetch took %v to notice the cancellation", elapsed)
	}
}
```

Add `"math/rand/v2"` to the imports of `issues_test.go` (`io`, `bytes`, `strings`, `time`, `http`, `httptest`, `domain` are already imported there). `stuckNode` and `connectionEchoHandler` already exist in that file. Note `connectionEchoHandler` ignores the request path, so `BaseURL: srv.URL` is fine for these servers; the fixture-backed tests keep `srv.URL + "/graphql"`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/adapters/github/ -run 'TestFetchRunsRepositoriesSideBySide|TestFetchKeepsConfigurationOrder|TestFetchCollectsEveryErrorAndKeepsTheGoodItems|TestFetchStopsWhenCancelled' -v`

Expected: `TestFetchRunsRepositoriesSideBySide` FAILS ("took 4.0…s"); the other three PASS already (they pin behaviour the fan-out must keep). `go vet` must be clean — if `rand` is reported unused, the import is misplaced.

- [ ] **Step 3: Replace `Fetch`**

In `internal/adapters/github/issues.go`, add `"sync"` to the imports and replace the `Fetch` method and its doc comment (lines 68–96) with:

```go
// Fetch retrieves open issues and open pull requests for every repository in f.repos, so that
// *IssueFetcher satisfies ports.Source. A failure fetching one repository does not lose items
// already fetched from the others (QS-1.4): every per-repository error is collected, joined with
// errors.Join, and returned alongside every item successfully fetched — never returned early on
// the first failure.
//
// The repositories are fetched side by side (QS-2.7): one goroutine per repository and
// connection, all started at once. There is no separate concurrency limit, because QS-3.5 caps the
// configuration at ten repositories and therefore at twenty requests in flight. Each goroutine
// writes into its own slot, and the slots are read out in order once every goroutine has finished,
// so the result is the one the sequential loop produced — repositories in configuration order, a
// repository's issues before its pull requests — whatever order the upstream answered in.
func (f *IssueFetcher) Fetch(ctx context.Context) ([]domain.Item, error) {
	type slot struct {
		items []domain.Item
		err   error
	}
	// Two slots per repository: issues at 2i, pull requests at 2i+1.
	slots := make([]slot, 2*len(f.repos))

	var wg sync.WaitGroup
	for i, repo := range f.repos {
		owner, name, ok := splitRepo(repo)
		if !ok {
			slots[2*i].err = fmt.Errorf("github: %q is not owner/name", repo)
			continue
		}
		wg.Add(2)
		go func(s *slot) {
			defer wg.Done()
			s.items, s.err = f.fetchIssues(ctx, owner, name)
			if s.err != nil {
				s.err = fmt.Errorf("github: fetching issues for %s: %w", repo, s.err)
			}
		}(&slots[2*i])
		go func(s *slot) {
			defer wg.Done()
			s.items, s.err = f.fetchPullRequests(ctx, owner, name)
			if s.err != nil {
				s.err = fmt.Errorf("github: fetching pull requests for %s: %w", repo, s.err)
			}
		}(&slots[2*i+1])
	}
	wg.Wait()

	var items []domain.Item
	var errs []error
	for _, s := range slots {
		items = append(items, s.items...)
		if s.err != nil {
			errs = append(errs, s.err)
		}
	}
	return items, errors.Join(errs...)
}
```

- [ ] **Step 4: Run the package's tests under the race detector**

Run: `go test -race ./internal/adapters/github/ -v 2>&1 | grep -E '^(=== RUN|--- (PASS|FAIL)|ok|FAIL|PASS)' | grep -v '=== RUN'`

Expected: every test PASS, including `TestGraphQLRequestBudget` (still 20), `TestFetchFollowsPagination`, `TestFetchStopsWhenCursorNeverAdvances`, `TestFetchStopsAtPageCap`, and the four new ones.

- [ ] **Step 5: Whole-module check and commit**

Run: `go build ./... && go vet ./... && go test -race ./...`

Expected: all `ok`.

```bash
git add internal/adapters/github/issues.go internal/adapters/github/issues_test.go
git commit -m "perf(github): fetch every repository and connection side by side, results kept in configuration order (QS-2.7, FR-1.4, QS-3.5)

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 2: A cache that never blocks

Model tier: standard (the cache code is complete below, but the web test helpers must be adapted with judgement).

**Files:**

- Modify: `internal/snapshot/snapshot.go` (whole file)
- Modify: `internal/snapshot/snapshot_test.go` (whole file)
- Modify: `internal/web/auth_test.go` (`newTestServerWith`, lines 875–884; add helpers)
- Modify: whichever `internal/web/*_test.go` tests fail after the change (see Step 6)

**Interfaces:**

- Consumes: `ports.Source`, `ports.Clock` — unchanged.
- Produces: `snapshot.Snapshot.Fetching bool`; `(*snapshot.Cache).Get(ctx) Snapshot` returning at once; `(*snapshot.Cache).Invalidate()` unchanged. Web test helpers `newColdServerWith(t, mutate) *Server`, `warmCache(t, c *snapshot.Cache)`, `getSettled(t, h, path, cookie) *httptest.ResponseRecorder`, the marker constant `waitingMarker = "id=\"waiting\""`.

- [ ] **Step 1: Rewrite the snapshot tests**

Replace `internal/snapshot/snapshot_test.go` with:

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

func (s *countingSource) Fetch(_ context.Context) ([]domain.Item, error) {
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

func (s *countingSource) set(items []domain.Item, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items, s.err = items, err
}

func one(title string) []domain.Item { return []domain.Item{{Repo: "a/b", Number: 1, Title: title}} }

// settled calls Get until no fetch is in flight and returns what landed. Get returns at once, so a
// test that wants the result of the fetch it just triggered waits here; the fakes answer in
// microseconds, so the loop is a formality with a deadline for when something is actually wrong.
func settled(t *testing.T, c *snapshot.Cache) snapshot.Snapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s := c.Get(context.Background())
		if !s.Fetching {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatal("the fetch never landed")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestFirstGetFetchesAndSecondWithinTTLDoesNot(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x")}
	c := snapshot.New(src, 5*time.Minute, clock)

	s1 := settled(t, c)
	s2 := c.Get(context.Background())
	if src.count() != 1 {
		t.Fatalf("fetches = %d, want 1", src.count())
	}
	if len(s1.Items) != 1 || s1.FetchedAt != clock.t || s1.Err != nil {
		t.Fatalf("first snapshot = %+v", s1)
	}
	if s2.FetchedAt != s1.FetchedAt || s2.Fetching {
		t.Fatalf("second Get refetched: %+v", s2)
	}
}

// QS-2.6: Get never waits for the source. The first Get of an empty cache returns before the
// source has answered, says a fetch is in flight, and carries nothing yet.
func TestFirstGetReturnsAtOnceWithNothingWhileFetching(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x"), block: make(chan struct{})}
	c := snapshot.New(src, time.Hour, clock)

	s := c.Get(context.Background()) // would hang here if Get waited for the source
	if !s.Fetching || len(s.Items) != 0 || !s.FetchedAt.IsZero() {
		t.Fatalf("got %+v, want an empty snapshot with Fetching set", s)
	}
	close(src.block)
	if got := settled(t, c); len(got.Items) != 1 || got.FetchedAt != clock.t {
		t.Fatalf("after the fetch landed: %+v", got)
	}
}

// While a refetch is in flight the previous list is what Get returns — marked Fetching, so the page
// can decide to wait rather than show it (design §5.1).
func TestGetDuringARefetchReturnsThePreviousListMarkedFetching(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x")}
	c := snapshot.New(src, time.Minute, clock)
	first := settled(t, c)

	clock.t = clock.t.Add(2 * time.Minute)
	block := make(chan struct{})
	src.mu.Lock()
	src.items, src.block = one("y"), block
	src.mu.Unlock()

	s := c.Get(context.Background())
	if !s.Fetching || len(s.Items) != 1 || s.Items[0].Title != "x" || s.FetchedAt != first.FetchedAt {
		t.Fatalf("during the refetch: %+v, want the previous list marked Fetching", s)
	}
	close(block)
	if got := settled(t, c); got.Items[0].Title != "y" || got.FetchedAt != clock.t {
		t.Fatalf("after the refetch: %+v", got)
	}
}

func TestGetRefetchesOnceTheSnapshotIsOlderThanTTL(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x")}
	c := snapshot.New(src, 5*time.Minute, clock)
	settled(t, c)

	clock.t = clock.t.Add(5*time.Minute + time.Second)
	src.set(one("y"), nil)
	s := settled(t, c)
	if src.count() != 2 || s.Items[0].Title != "y" || s.FetchedAt != clock.t {
		t.Fatalf("after ttl: fetches=%d snapshot=%+v", src.count(), s)
	}
}

func TestAFailedFetchKeepsTheOldItemsAndReportsTheError(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x")}
	c := snapshot.New(src, time.Minute, clock)
	good := settled(t, c)

	clock.t = clock.t.Add(2 * time.Minute)
	src.set(nil, errors.New("boom"))
	s := settled(t, c)
	if s.Err == nil || s.ErrAt != clock.t {
		t.Fatalf("error not reported: %+v", s)
	}
	if len(s.Items) != 1 || s.FetchedAt != good.FetchedAt {
		t.Fatalf("old items not kept: %+v", s)
	}
	// A failure is not retried within the TTL: the next Get serves the failure, not a fetch.
	if again := c.Get(context.Background()); again.Fetching || src.count() != 2 {
		t.Fatalf("a failure was retried at once: fetches=%d %+v", src.count(), again)
	}
}

func TestAFailedFirstFetchYieldsAnEmptyListWithTheError(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{err: errors.New("boom")}
	c := snapshot.New(src, time.Minute, clock)
	s := settled(t, c)
	if s.Err == nil || len(s.Items) != 0 || !s.FetchedAt.IsZero() {
		t.Fatalf("got %+v", s)
	}
}

func TestPartialResultIsKeptTogetherWithItsError(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("partial"), err: errors.New("one repo failed")}
	c := snapshot.New(src, time.Minute, clock)
	s := settled(t, c)
	if s.Err == nil || len(s.Items) != 1 || s.FetchedAt != clock.t {
		t.Fatalf("partial result mishandled: %+v", s)
	}
}

func TestInvalidateForcesTheNextGetToFetch(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x")}
	c := snapshot.New(src, time.Hour, clock)
	settled(t, c)
	c.Invalidate()
	settled(t, c)
	if src.count() != 2 {
		t.Fatalf("fetches = %d, want 2", src.count())
	}
}

// Refresh pressed while a fetch is already running changes nothing: that fetch is the freshest
// list there can be, and its landing clears the mark Invalidate set (design §4).
func TestInvalidateDuringAFetchDoesNotStartAnother(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x"), block: make(chan struct{})}
	c := snapshot.New(src, time.Hour, clock)

	c.Get(context.Background()) // starts the fetch
	c.Invalidate()
	if s := c.Get(context.Background()); !s.Fetching {
		t.Fatalf("got %+v, want the running fetch reported", s)
	}
	close(src.block)
	settled(t, c)
	c.Get(context.Background())
	if src.count() != 1 {
		t.Fatalf("fetches = %d, want 1: Invalidate during a fetch must not queue a second one", src.count())
	}
}

func TestManyCallersShareOneFetch(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x"), block: make(chan struct{})}
	c := snapshot.New(src, time.Hour, clock)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s := c.Get(context.Background()); !s.Fetching {
				t.Errorf("a caller during the fetch got %+v, want Fetching", s)
			}
		}()
	}
	wg.Wait() // every caller returned while the source is still blocked
	close(src.block)
	settled(t, c)
	if src.count() != 1 {
		t.Fatalf("fetches = %d, want 1", src.count())
	}
}

// ctxSource starts a fetch, then waits for either its release channel or its context. It checks
// its context once more after being released, so a fetch whose context was cancelled while it
// waited fails deterministically rather than on whichever select case the runtime happens to pick.
type ctxSource struct {
	started, release chan struct{}
	items            []domain.Item
}

func (s *ctxSource) Fetch(ctx context.Context) ([]domain.Item, error) {
	close(s.started)
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.items, nil
}

// The fetch is detached from the request that started it: the visitor who happened to trigger it
// must not be able to cancel it by going away, or the error would be served to everyone for a TTL.
func TestACallerThatGoesAwayDoesNotCancelTheFetch(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &ctxSource{started: make(chan struct{}), release: make(chan struct{}), items: one("x")}
	c := snapshot.New(src, time.Minute, clock)

	ctx, cancel := context.WithCancel(context.Background())
	c.Get(ctx)
	<-src.started
	cancel() // the visitor closes the tab mid-fetch
	close(src.release)

	s := settled(t, c)
	if s.Err != nil || len(s.Items) != 1 || s.FetchedAt != clock.t {
		t.Fatalf("a cancelled caller poisoned the fetch: %+v", s)
	}
}

// FR-1.4 AC1 and AC4: when one repository fails and another does not, the page keeps the failing
// repository's previous items, and FetchedAt stays at the last fetch whose items are all current —
// it is what "Mark all seen" acknowledges, and the failing repository's list is not current.
func TestAPartialFetchKeepsTheFailingRepositorysItemsAndTheOldFetchedAt(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: []domain.Item{
		{Repo: "a/b", Number: 1, Title: "a/b before"},
		{Repo: "c/d", Number: 2, Title: "c/d two"},
		{Repo: "c/d", Number: 3, Title: "c/d three"},
	}}
	c := snapshot.New(src, time.Minute, clock)
	good := settled(t, c)

	clock.t = clock.t.Add(2 * time.Minute)
	src.set([]domain.Item{{Repo: "a/b", Number: 1, Title: "a/b after"}}, errors.New("c/d: unexpected status 502"))
	s := settled(t, c)

	if s.Err == nil || s.ErrAt != clock.t {
		t.Errorf("error not reported: %+v", s)
	}
	if s.FetchedAt != good.FetchedAt {
		t.Errorf("FetchedAt = %v, want the good fetch's %v: the c/d list is not current", s.FetchedAt, good.FetchedAt)
	}
	got := map[string]bool{}
	for _, it := range s.Items {
		got[it.Title] = true
	}
	want := map[string]bool{"a/b after": true, "c/d two": true, "c/d three": true}
	if len(s.Items) != len(want) {
		t.Errorf("items = %+v, want exactly %v", s.Items, want)
	}
	for title := range want {
		if !got[title] {
			t.Errorf("items lack %q: %+v", title, s.Items)
		}
	}
	// The previous slice is shared with renders that may still be running; it must be untouched.
	if good.Items[0].Title != "a/b before" || len(good.Items) != 3 {
		t.Errorf("the previous snapshot's slice was mutated: %+v", good.Items)
	}
}

var _ ports.Source = (*countingSource)(nil)
```

- [ ] **Step 2: Run the snapshot tests to verify they fail**

Run: `go test ./internal/snapshot/ 2>&1 | head -20`

Expected: compile error, `s.Fetching undefined`.

- [ ] **Step 3: Rewrite the cache**

Replace `internal/snapshot/snapshot.go` with:

```go
// Package snapshot keeps the most recently fetched item list in memory and refetches it when it
// is older than a TTL. It is the whole of zorgscope's state: a process that restarts, or a Fly
// Machine that wakes from zero, starts empty and fetches on its first page view.
//
// Get never waits for that fetch (QS-2.6). When one is due it starts it in a goroutine of its own
// and returns what is known so far, marked Fetching; the page shows a wait page and asks again.
// The goroutine is the one piece of work this process ever does outside a request, it exists only
// because a request asked, and it lives at most fetchBudget (ADR-0011). There is no ticker.
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
// Items is the newest list of every repository: the items of the last fetch that produced any,
// plus — when that fetch failed for some repositories — the previous items of each repository it
// returned nothing for. FetchedAt is the last fetch whose items are all current, and does not move
// on a partial fetch: it is what the page states as "fetched at" and what "Mark all seen"
// acknowledges, and neither may claim a repository that did not fetch (FR-1.2, FR-1.4). The one
// exception is a partial first fetch, which has nothing older to fall back on and stamps now.
//
// Err and ErrAt describe the last fetch that failed, and are cleared by the next fetch that does
// not. So a page can say both "this list is from 11:50" and "GitHub has been failing since 12:04"
// at once.
//
// Fetching reports that a fetch is in flight: everything else in the snapshot is what was known
// before it started. A page that would rather wait than show that (FR-1.9) reads this field.
type Snapshot struct {
	Items     []domain.Item
	FetchedAt time.Time // zero until a fetch has returned items
	Err       error     // the most recent fetch error, nil once a fetch succeeds cleanly
	ErrAt     time.Time // when Err was recorded
	Fetching  bool
}

// fetchBudget bounds one fetch. A fetch is shared by every caller that finds it in flight, so it
// runs detached from the context of the caller that happened to trigger it — a visitor closing the
// tab must not cancel it and leave everyone else an error for a whole TTL. Detached, nothing would
// stop a hung upstream from holding the in-flight mark, and every page view with it, indefinitely;
// the budget is that stop, and is generous for a fetch that runs its repositories side by side.
const fetchBudget = 60 * time.Second

// Cache is safe for concurrent use. At most one fetch runs at a time: a Get that finds one in
// flight reports it rather than starting another.
type Cache struct {
	src   ports.Source
	ttl   time.Duration
	clock ports.Clock

	mu       sync.Mutex
	cur      Snapshot // cur.Fetching is always false; Get sets it on the copy it returns
	stale    bool     // set by Invalidate; cleared when the next fetch lands
	inflight bool     // a fetch goroutine is running
}

// New returns an empty cache. ttl is how long both a fetched list and a failure are reused before
// the next Get fetches again, measured against clock; config.Load only ever supplies a positive
// one.
func New(src ports.Source, ttl time.Duration, clock ports.Clock) *Cache {
	return &Cache{src: src, ttl: ttl, clock: clock, stale: true}
}

// Get returns the current snapshot at once. When a fetch is due — nothing fetched yet,
// invalidated, the list older than the TTL, or the last failure older than the TTL — and none is
// in flight, it starts one and returns with Fetching set; while one is in flight, it returns the
// previous snapshot with Fetching set. ctx's cancellation does not reach the fetch — see
// fetchBudget.
func (c *Cache) Get(ctx context.Context) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.inflight {
		return c.fetching()
	}
	now := c.clock.Now()
	if !c.due(now) {
		return c.cur
	}

	c.inflight = true
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchBudget)
	go func() {
		defer cancel()
		items, err := c.src.Fetch(fetchCtx)
		c.land(now, items, err)
	}()
	return c.fetching()
}

// fetching is the snapshot Get returns while a fetch runs: what is known, marked. Called with
// the mutex held.
func (c *Cache) fetching() Snapshot {
	s := c.cur
	s.Fetching = true
	return s
}

// due reports whether a fetch should start now. A failing source is retried at most once per TTL
// as well, so a broken upstream does not turn every page view into a fetch. Called with the mutex
// held.
func (c *Cache) due(now time.Time) bool {
	if c.stale {
		return true
	}
	if !c.cur.FetchedAt.IsZero() && now.Sub(c.cur.FetchedAt) <= c.ttl {
		return false
	}
	if !c.cur.ErrAt.IsZero() && now.Sub(c.cur.ErrAt) <= c.ttl {
		return false
	}
	return true
}

// land records the result of a fetch that began at began. A failed fetch never discards the
// previous items.
func (c *Cache) land(began time.Time, items []domain.Item, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.inflight = false
	c.stale = false
	switch {
	case err == nil:
		c.cur.Items, c.cur.FetchedAt = items, began
	case items == nil:
		// Nothing fetched at all: the previous snapshot stands as it was.
	case c.cur.FetchedAt.IsZero():
		// A partial first fetch has no older list to fill in from.
		c.cur.Items, c.cur.FetchedAt = items, began
	default:
		// A partial fetch: fill in the repositories that failed, and leave FetchedAt alone.
		c.cur.Items = mergePartial(c.cur.Items, items)
	}
	if err != nil {
		c.cur.Err = err
		c.cur.ErrAt = began
	} else {
		c.cur.Err = nil
		c.cur.ErrAt = time.Time{}
	}
}

// mergePartial is the list after a fetch that failed for some repositories: every fresh item, plus
// the previous items of each repository the fresh result holds nothing for.
//
// Repositories are told apart by Item.Repo alone, because the source reports a partial failure as
// one joined error rather than per repository. A repository that fetched cleanly but now has no
// open items is therefore indistinguishable from one that failed, and keeps its old items until the
// next clean fetch — the cheaper mistake, since the alternative blanks a repository on a failure.
//
// It builds a new slice and writes into neither argument: prev is the slice earlier snapshots
// handed to renders that may still be reading it.
func mergePartial(prev, fresh []domain.Item) []domain.Item {
	fetched := make(map[string]bool, len(fresh))
	for _, it := range fresh {
		fetched[it.Repo] = true
	}
	out := make([]domain.Item, 0, len(fresh)+len(prev))
	out = append(out, fresh...)
	for _, it := range prev {
		if !fetched[it.Repo] {
			out = append(out, it)
		}
	}
	return out
}

// Invalidate makes the next Get fetch regardless of age. Called while a fetch is in flight it
// changes nothing: that fetch is the freshest list there can be, and its landing clears the mark.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	c.stale = true
	c.mu.Unlock()
}
```

- [ ] **Step 4: Run the snapshot tests under the race detector**

Run: `go test -race ./internal/snapshot/ -v 2>&1 | grep -E '^(--- |ok|FAIL|PASS)'`

Expected: every test PASS. `go vet ./internal/snapshot/` clean (the `cancel` is called inside the goroutine; vet's lostcancel accepts that).

- [ ] **Step 5: Add the web test helpers**

In `internal/web/auth_test.go`, replace `newTestServerWith` (lines 875–884) with:

```go
// newTestServerWith builds a server whose cache is already warm: the first fetch has landed, so
// GET / shows the list at once rather than the wait page (FR-1.9). Tests about the wait page
// itself, or about what a cold cache does, use newColdServerWith. The warm-up counts as one call
// on the source.
func newTestServerWith(t *testing.T, mutate func(*Options)) *Server {
	t.Helper()
	o := testOptions()
	mutate(&o)
	s, err := New(o)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	warmCache(t, o.Cache)
	return s
}

// newColdServerWith is newTestServerWith without the warm-up: the cache is empty and the first
// page view starts the first fetch.
func newColdServerWith(t *testing.T, mutate func(*Options)) *Server {
	t.Helper()
	o := testOptions()
	mutate(&o)
	s, err := New(o)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return s
}

// warmCache triggers a fetch and waits until it has landed. The fakes answer at once, so the loop
// is a formality with a deadline for when something is actually wrong.
func warmCache(t *testing.T, c *snapshot.Cache) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for c.Get(context.Background()).Fetching {
		if time.Now().After(deadline) {
			t.Fatal("the warm-up fetch never landed")
		}
		time.Sleep(time.Millisecond)
	}
}

// waitingMarker is what only the wait page carries: the id of its polling element.
const waitingMarker = `id="waiting"`

// getSettled is getAs for a request that may find a fetch in flight — after Refresh, or after the
// clock moved past the TTL. It asks again until the answer is not the wait page.
func getSettled(t *testing.T, h http.Handler, path string, c *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		rec := getAs(h, path, c)
		if !strings.Contains(rec.Body.String(), waitingMarker) {
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s kept answering the wait page", path)
		}
		time.Sleep(time.Millisecond)
	}
}
```

`snapshot`, `context`, `strings`, `time`, `http`, `httptest` are already imported in `auth_test.go`; check with `go vet ./internal/web/`.

- [ ] **Step 6: Run the web tests and settle the ones that now see an empty or in-flight cache**

Run: `go test ./internal/web/ 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'`

Before Task 4 the handlers do not yet know about `Fetching`, so a page view during a fetch renders the current snapshot (empty on a cold cache). Expected failures are tests that (a) build a server without `newTestServerWith` and read `/` at once, or (b) press Refresh or move the clock past the TTL and then read `/` at once, or (c) assert an absolute call count. Fix each according to its kind — do not change what a test asserts about behaviour:

- (a) `BenchmarkDashboard` and any test that calls `New(o)` directly: add `warmCache(t, o.Cache)` (or `warmCache(b, …)` needs a `testing.TB`; if so, change `warmCache`'s first parameter to `testing.TB`).
- (b) `TestRefreshInvalidatesTheCacheSoTheNextGetFetches`, `TestAFailingSourceShowsTheNoticeAndKeepsThePreviousList`, `TestMarkSeenUsesTheFetchedAtOfTheSnapshotShownNotTheClick`, `TestAPartialFetchKeepsTheFailingRepositoryAndTheSeenAtOfTheGoodFetch`, `TestTheHeaderStatesWhenTheListWasFetched` and any other that reads `/` or `/sites` after a Refresh or a clock move: replace that `getAs`/`getAuthed` with `getSettled(t, h, path, cookie)`. A test that must observe the *result* of a refetch through `CallCount()` must call `getSettled` first, since the goroutine is what makes the call.
- (c) A test asserting `CallCount() == n` now sees the warm-up as call 1: adjust the expected number by exactly one and say so in a comment (`// one warm-up fetch, then …`). Do not adjust anything else.
- `TestTwoPageViewsWithinTheTTLShareOneFetch` should pass unchanged (warm-up 1, two views still 1). If it does not, the cache is wrong, not the test.

Run again until: `ok  github.com/gernotstarke/zorgscope/internal/web`.

- [ ] **Step 7: Whole-module check and commit**

Run: `go build ./... && go vet ./... && go test -race ./...`

Expected: all `ok`.

```bash
git add internal/snapshot/snapshot.go internal/snapshot/snapshot_test.go internal/web/auth_test.go
git add $(git diff --name-only internal/web/ | grep _test.go)
git status --short   # only files under internal/snapshot and internal/web/*_test.go are staged
git commit -m "feat(snapshot): Get returns at once — a due fetch runs in a request-started, budget-bounded goroutine and the snapshot reports Fetching (QS-2.6, FR-1.4)

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 3: The large mark and the orbit stylesheet

Model tier: cheap (the code is complete below).

**Files:**

- Create: `internal/web/static/logo-large.jpg` (generated, ~37 kB)
- Modify: `internal/web/server.go` (`staticContentTypes`, lines 605–611)
- Modify: `internal/web/static/app.css` (append a section)
- Modify: `internal/web/templates/layout.html` (two comments)
- Modify: `internal/web/static_cache_test.go` (append a test)
- Modify: `internal/web/contrast_test.go` (append a test)

**Interfaces:**

- Consumes: `loadStatic`, `assetURL` — unchanged.
- Produces: the asset `logo-large.jpg` reachable as `{{.Asset "logo-large.jpg"}}`; CSS classes `.waiting`, `.orbit-stage`, `.orbit-mark`, `.orbit-ring`, `.orbit-track`, `.orbit-beam`, `.polling-pulse`, `.waiting-status`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/web/static_cache_test.go`:

```go
// FR-1.9: the wait page's mark is a 512 px JPEG, served as such and never gzipped — a JPEG is
// already compressed, and a gzip wrapper would only add bytes. Its size is pinned so a regenerated
// file cannot quietly blow the wait page's budget (QS-2.3).
func TestLargeMarkIsServedAsAJPEGWithoutGzip(t *testing.T) {
	h := dashHandler(t, &fakeSource{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/static/logo-large.jpg", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /static/logo-large.jpg = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", ct)
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want none: a JPEG is not gzipped", enc)
	}
	if n := rec.Body.Len(); n > 45*1024 {
		t.Errorf("logo-large.jpg is %d bytes, at most 45 kB is allowed", n)
	}
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte{0xFF, 0xD8, 0xFF}) {
		t.Error("the body does not start with the JPEG magic bytes")
	}
}
```

Add `"bytes"` to that file's imports if it is not there.

Append to `internal/web/contrast_test.go`:

```go
// FR-1.9 AC4: the wait page's status line is text on the page background, and it must be readable
// in both appearances — it is the one signal that is not colour or motion.
func TestWaitingStatusKeepsTextReadable(t *testing.T) {
	raw, err := fs.ReadFile(embedded, "static/app.css")
	if err != nil {
		t.Fatalf("reading the embedded app.css: %v", err)
	}
	css := string(raw)
	if !strings.Contains(css, ".waiting-status {") {
		t.Fatal("app.css defines no .waiting-status rule")
	}
	bg, ok := lightDarkToken(t, css, "bg")
	if !ok {
		t.Fatal("no --bg token")
	}
	text, ok := lightDarkToken(t, css, "text")
	if !ok {
		t.Fatal("no --text token")
	}
	for i, name := range []string{"light", "dark"} {
		if r := contrastRatio(text[i], bg[i]); r < 4.5 {
			t.Errorf("--text on --bg (%s) = %.2f:1, want at least 4.5:1", name, r)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/web/ -run 'TestLargeMarkIsServedAsAJPEGWithoutGzip|TestWaitingStatusKeepsTextReadable' -v 2>&1 | grep -E '^(---|FAIL|ok)'`

Expected: both FAIL (404 for the mark; no `.waiting-status` rule).

- [ ] **Step 3: Generate the mark**

Run on the macOS host (a one-time asset step, like `logo.png` before it; nothing is installed):

```bash
sips -Z 512 -s format jpeg -s formatOptions 80 docs/logo/zorgscope-logo.jpeg --out internal/web/static/logo-large.jpg
ls -l internal/web/static/logo-large.jpg   # expect about 37000 bytes
```

If `sips` is unavailable, `docker run --rm -v "$PWD:/w" -w /w dpokidov/imagemagick docs/logo/zorgscope-logo.jpeg -resize 512x512 -quality 80 internal/web/static/logo-large.jpg` produces the same.

- [ ] **Step 4: Pin the content type and write the stylesheet**

In `internal/web/server.go`, add one line to `staticContentTypes`:

```go
	".jpg": "image/jpeg",
```

Append to `internal/web/static/app.css`:

```css
/* ---------------------------------------------------------------- the wait page (FR-1.9)

   Shown while a fetch runs: the mark, large, with the scanning orbit from
   docs/logo/scanning-orbit-animation.md — a lime beam circling once per 1.05 s and a reddish
   heartbeat from the centre. CSS alone: keyframes on transform and opacity, nothing scripted
   (QS-4.4). The status line beside it is the signal that is neither colour nor motion. */
:root {
  --orbit-beam: #8ee038;                           /* the mark's own focal lime */
  --orbit-track: light-dark(#bfe3cf, #24503a);
  --orbit-pulse: light-dark(#c9433c, #ef655b);
}

.waiting {
  display: grid;
  justify-items: center;
  gap: 1.5rem;
  padding: clamp(1.5rem, 6vh, 4rem) 1rem;
}

.orbit-stage {
  position: relative;
  width: min(70vmin, 420px);
  aspect-ratio: 1;
}

/* The artwork is a square with a dark ground; the clip keeps the ring and a hair of that ground,
   so the mark reads as a disc on either appearance. */
.orbit-mark {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  clip-path: circle(42.5%);
  object-fit: cover;
}

.orbit-ring {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  overflow: visible;
}

.orbit-track,
.orbit-beam {
  fill: none;
  vector-effect: non-scaling-stroke;
}

.orbit-track {
  stroke: var(--orbit-track);
  stroke-width: 1.6;
}

/* pathLength="100" on the circle makes the dash array a percentage: a 17 % arc, 83 % gap. */
.orbit-beam {
  stroke: var(--orbit-beam);
  stroke-width: 3.8;
  stroke-linecap: round;
  stroke-dasharray: 17 83;
  transform-box: view-box;
  transform-origin: 50px 50px;
  transform: rotate(-90deg);
  animation: orbit-scan 1050ms linear infinite;
}

.polling-pulse {
  position: absolute;
  top: 50%;
  left: 50%;
  width: 8.4%;
  aspect-ratio: 1;
  border: 2px solid var(--orbit-pulse);
  border-radius: 50%;
  background: color-mix(in srgb, var(--orbit-pulse) 23%, transparent);
  opacity: 0;
  transform: translate(-50%, -50%) scale(0.86);
  pointer-events: none;
  animation: polling-heartbeat 1050ms cubic-bezier(0.65, 0, 0.35, 1) infinite;
}

@keyframes orbit-scan {
  from { transform: rotate(-90deg); }
  to { transform: rotate(270deg); }
}

@keyframes polling-heartbeat {
  0% { opacity: 0.18; transform: translate(-50%, -50%) scale(0.86); }
  36% { opacity: 0.88; transform: translate(-50%, -50%) scale(1.16); }
  100% { opacity: 0; transform: translate(-50%, -50%) scale(1.86); }
}

.waiting-status {
  margin: 0;
  color: var(--text);
  font-size: 1.05rem;
  text-align: center;
}

/* No rotation and no scaling: a thicker, stationary arc and a steady centre ring say "working"
   without motion; the status line says it in words either way (FR-1.9 AC4). */
@media (prefers-reduced-motion: reduce) {
  .orbit-beam {
    animation: none;
    stroke-dasharray: 34 66;
  }
  .polling-pulse {
    animation: none;
    opacity: 0.82;
    transform: translate(-50%, -50%) scale(1.08);
  }
}
```

In `internal/web/templates/layout.html`, correct the two stale comments that mention a `make logo` target, which no longer exists. In the comment above the icon links, replace the sentence "Both files come from make logo." with "Both files were cut once from the artwork in docs/logo and committed." In the comment above the mark, replace "generated by make logo from the artwork in docs/logo" with "cut once from the artwork in docs/logo and committed".

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -race ./internal/web/ 2>&1 | tail -3`

Expected: `ok`. `TestStaticAssetsFitTheirBudgetOnTheWire` still passes: the dashboard links no new asset.

- [ ] **Step 6: Whole-module check and commit**

Run: `go build ./... && go vet ./... && go test -race ./...`

```bash
git add internal/web/static/logo-large.jpg internal/web/server.go internal/web/static/app.css internal/web/templates/layout.html internal/web/static_cache_test.go internal/web/contrast_test.go
git commit -m "style(web): the large mark and the scanning-orbit stylesheet for the wait page, CSS only (FR-1.9, QS-4.4)

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 4: The wait page, the poll and the handlers

Model tier: standard.

**Files:**

- Create: `internal/web/templates/waiting.html`
- Modify: `internal/web/templates/layout.html` (add the `head` block)
- Modify: `internal/web/server.go` (`pageFiles` line 50; `pageData` lines 700–712)
- Modify: `internal/web/dashboard.go` (`handleDashboard`, `handleItems`; add `answeredWaiting`)
- Modify: `internal/web/sites.go` (`handleSites`)
- Create: `internal/web/waiting_test.go`
- Modify: `internal/web/dashboard_test.go` (`TestNoRenderedHTMLNeedsUnsafeInline`)

**Interfaces:**

- Consumes: `snapshot.Snapshot.Fetching` (Task 2); CSS classes and `logo-large.jpg` (Task 3); test helpers `newColdServerWith`, `getSettled`, `waitingMarker` (Task 2).
- Produces: `waitingView{Repos int; Path string}`; `pageData.Waiting *waitingView`; `(*Server).answeredWaiting(w, r) bool`; constant `waitingPollID = "waiting"`.

- [ ] **Step 1: Write the failing tests**

Create `internal/web/waiting_test.go`:

```go
package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/snapshot"
)

// blockingSource answers only once released, so a test can hold a fetch in flight for as long
// as it needs and observe what the pages do meanwhile.
type blockingSource struct {
	release chan struct{}
	items   []domain.Item
}

func (s *blockingSource) Fetch(ctx context.Context) ([]domain.Item, error) {
	select {
	case <-s.release:
		return s.items, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// coldServer is a server whose first fetch will block until release is closed.
func coldServer(t *testing.T) (h http.Handler, release chan struct{}) {
	t.Helper()
	release = make(chan struct{})
	src := &blockingSource{release: release, items: []domain.Item{ghItem(1, "An issue", testNow.Add(-time.Hour))}}
	s := newColdServerWith(t, func(o *Options) {
		o.Config.GitHub.Repos = append([]string{"org/repo"}, representativeRepos()...)
		o.Cache = snapshot.New(src, time.Hour, o.Clock)
	})
	return s.Handler(), release
}

func getWith(h http.Handler, path string, c *http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(c)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(rec, req)
	return rec
}

// QS-2.6: a page view never waits for GitHub. With the source blocked, GET / answers within the
// budget — with the wait page, not the list.
func TestPageNeverWaitsForGitHub(t *testing.T) {
	h, release := coldServer(t)
	defer close(release)
	c := signIn(t, h)

	start := time.Now()
	rec := getAs(h, "/", c)
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("GET / took %v with the source blocked; it must not wait for GitHub (QS-2.6)", elapsed)
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), waitingMarker) {
		t.Fatalf("GET / = %d, body lacks %s", rec.Code, waitingMarker)
	}
}

// FR-1.9 AC1, AC2, AC4: what the wait page holds.
func TestWaitPageWhileFetching(t *testing.T) {
	h, release := coldServer(t)
	defer close(release)
	c := signIn(t, h)

	rec := getAs(h, "/?kind=pr", c)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	for _, want := range []string{
		`class="orbit-stage"`,
		`/static/logo-large.jpg?`,
		`class="orbit-beam"`,
		`class="polling-pulse"`,
		`<p class="waiting-status" role="status" aria-live="polite">Asking GitHub about 11 repositories…</p>`,
		`<div id="waiting" hx-get="/?kind=pr" hx-trigger="every 500ms" hx-target="main" hx-swap="outerHTML" hx-select="main"></div>`,
		`<noscript><meta http-equiv="refresh" content="2"></noscript>`,
		`<script src="/static/htmx.min.js?`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("wait page lacks %s", want)
		}
	}
	for _, forbidden := range []string{`id="items"`, `class="filter"`, `class="tiles"`, "Mark all seen"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("wait page shows %s; it must show nothing of the list while the fetch runs (FR-1.9 AC1)", forbidden)
		}
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store: a wait page must never be served from a browser cache", cc)
	}
	if rec.Header().Get("Content-Security-Policy") != contentSecurityPolicy {
		t.Error("the wait page does not carry the Content-Security-Policy every page carries")
	}
}

// The poll during a fetch is answered 204, so htmx swaps nothing and the animation keeps running.
func TestPollAnswers204WhileFetching(t *testing.T) {
	h, release := coldServer(t)
	defer close(release)
	c := signIn(t, h)

	rec := getWith(h, "/", c, map[string]string{"HX-Request": "true", "HX-Trigger": waitingPollID})
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("poll = %d with %d bytes, want 204 and no body", rec.Code, rec.Body.Len())
	}
}

// Once the fetch has landed, the poll receives the page: a title for htmx to apply, a main to
// swap in, and the list.
func TestPollReceivesThePageAfterTheFetch(t *testing.T) {
	h, release := coldServer(t)
	c := signIn(t, h)
	getAs(h, "/", c) // starts the fetch
	close(release)

	rec := getSettled(t, h, "/", c)
	body := rec.Body.String()
	for _, want := range []string{"<title>", "<main>", `id="items"`, "An issue"} {
		if !strings.Contains(body, want) {
			t.Errorf("the settled page lacks %s", want)
		}
	}
	poll := getWith(h, "/", c, map[string]string{"HX-Request": "true", "HX-Trigger": waitingPollID})
	if poll.Code != http.StatusOK || !strings.Contains(poll.Body.String(), `id="items"`) {
		t.Errorf("poll after the fetch = %d; want the page with the list", poll.Code)
	}
}

// FR-1.9 AC5: the filter form's htmx request during a fetch is served from the current snapshot,
// never with the wait page — an hx-select="#items" against a wait page would empty the list.
func TestFilterRequestDuringFetchIsServedFromTheSnapshot(t *testing.T) {
	h, release := coldServer(t)
	defer close(release)
	c := signIn(t, h)

	rec := getWith(h, "/?kind=pr", c, map[string]string{"HX-Request": "true"})
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `id="items"`) {
		t.Fatalf("htmx GET / during a fetch = %d, body lacks the list", rec.Code)
	}
	if strings.Contains(body, waitingMarker) {
		t.Error("the filter request was answered with the wait page")
	}
	frag := getWith(h, "/items", c, map[string]string{"HX-Request": "true"})
	if frag.Code != http.StatusOK || strings.Contains(frag.Body.String(), waitingMarker) {
		t.Errorf("GET /items during a fetch = %d, or carried the wait page", frag.Code)
	}
}

// The Sites view waits the same way and polls its own path.
func TestSitesViewWaitsToo(t *testing.T) {
	h, release := coldServer(t)
	defer close(release)
	c := signIn(t, h)

	body := getAs(h, "/sites", c).Body.String()
	if !strings.Contains(body, `hx-get="/sites"`) || !strings.Contains(body, waitingMarker) {
		t.Errorf("GET /sites during a fetch is not the wait page polling /sites")
	}
}

// FR-1.9 AC3: a fetch that fails ends the waiting the ordinary way — the page with the notice.
func TestAFailedFetchEndsTheWaiting(t *testing.T) {
	src := &fakeSource{err: errTestUpstream}
	s := newColdServerWith(t, func(o *Options) { o.Cache = snapshot.New(src, time.Hour, o.Clock) })
	h := s.Handler()
	c := signIn(t, h)

	rec := getSettled(t, h, "/", c)
	body := rec.Body.String()
	if !strings.Contains(body, "GitHub unreachable since") || !strings.Contains(body, "Fetched never") {
		t.Errorf("after a failed first fetch the page lacks the notice or 'Fetched never'")
	}
	if src.CallCount() != 1 {
		t.Errorf("fetches = %d, want 1: a failure is not retried within the TTL", src.CallCount())
	}
}

// QS-2.3, the wait page's own measure: HTML and static assets on the wire.
func TestWaitPageStaysInsideItsBudget(t *testing.T) {
	h, release := coldServer(t)
	defer close(release)
	c := signIn(t, h)

	page := getAs(h, "/", c).Body.String()
	if n := len(page); n > 20*1024 {
		t.Errorf("wait page is %d bytes of HTML, budget is 20 kB", n)
	}
	assets := linkedStaticAssets(page)
	if len(assets) < 3 {
		t.Fatalf("found %d static assets on the wait page (%v); the stylesheet, htmx and the mark are all linked", len(assets), assets)
	}
	total := 0
	for _, asset := range assets {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, asset, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", asset, rec.Code)
		}
		total += rec.Body.Len()
		t.Logf("%s: %d bytes on the wire", asset, rec.Body.Len())
	}
	if total > 100*1024 {
		t.Errorf("the wait page's static assets are %d bytes on the wire, budget is 100 kB", total)
	}
}
```

`errTestUpstream` — check `internal/web/*_test.go` for an existing error value used by `TestAFailingSourceShowsTheNoticeAndKeepsThePreviousList`; if none exists, declare `var errTestUpstream = errors.New("upstream on fire")` at the top of `waiting_test.go` (and import `errors`).

In `internal/web/dashboard_test.go`, extend `TestNoRenderedHTMLNeedsUnsafeInline`: after the `pages` map is built, add the wait page:

```go
	// The wait page (FR-1.9) is swept too: it is the one page a cold start shows.
	wh, release := coldServer(t)
	pages["GET / (waiting)"] = getAs(wh, "/", signIn(t, wh)).Body.String()
	close(release)
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/web/ -run 'TestPageNeverWaitsForGitHub|TestWaitPage|TestPoll|TestFilterRequestDuringFetch|TestSitesViewWaitsToo|TestAFailedFetchEndsTheWaiting' 2>&1 | head`

Expected: compile error, `waitingPollID` undefined.

- [ ] **Step 3: The template and the head block**

In `internal/web/templates/layout.html`, insert `{{block "head" .}}{{end}}` on its own line immediately before `</head>`, with this comment above it:

```html
{{/* A page may add to the head. Only the wait page does: a meta refresh inside noscript, so a
     browser without JavaScript still gets the page once the fetch has ended (FR-1.9 AC2). The
     element is permitted inside head and may hold meta, so it applies only when script is off. */}}
```

Create `internal/web/templates/waiting.html`:

```html
{{define "head"}}<noscript><meta http-equiv="refresh" content="2"></noscript>{{end}}
{{define "content"}}
{{with .Waiting}}
{{/* The wait page (FR-1.9): shown in place of the list or the tiles while a fetch runs. The
     mark, large, with the scanning orbit and the heartbeat drawn by app.css alone (QS-4.4); a
     status line, so the signal is never only colour or motion (AC4); and a poll that asks for
     this page again every half second and swaps main in once the fetch has ended (AC2). While
     the fetch still runs the poll is answered 204, so nothing is swapped and the animation never
     restarts. The polling element sits inside main, so the swap that brings the page removes it. */}}
<section class="waiting" aria-busy="true">
  <div class="orbit-stage" data-state="refreshing">
    <img class="orbit-mark" src="{{$.Asset "logo-large.jpg"}}" width="512" height="512" alt="" decoding="async">
    <svg class="orbit-ring" viewBox="0 0 100 100" aria-hidden="true" focusable="false">
      <circle class="orbit-track" cx="50" cy="50" r="46.5" pathLength="100"></circle>
      <circle class="orbit-beam" cx="50" cy="50" r="46.5" pathLength="100"></circle>
    </svg>
    <span class="polling-pulse" aria-hidden="true"></span>
  </div>
  <p class="waiting-status" role="status" aria-live="polite">Asking GitHub about {{.Repos}} repositories…</p>
  <div id="waiting" hx-get="{{.Path}}" hx-trigger="every 500ms" hx-target="main" hx-swap="outerHTML" hx-select="main"></div>
</section>
{{end}}
{{end}}
```

Note `$.Asset`: inside `{{with .Waiting}}` the dot is the view, and `Asset` is a method of the page data.

- [ ] **Step 4: The view, the page list and the handlers**

In `internal/web/server.go`:

- `pageFiles` becomes `[]string{"login.html", "dashboard.html", "sites.html", "waiting.html"}`.
- In `pageData`, after the `Sites *sitesView` field, add:

```go
	// Waiting is set only by answeredWaiting. waiting.html reads it for the repository count and
	// the path to poll; every other page leaves it nil.
	Waiting *waitingView
```

In `internal/web/dashboard.go`, add after the `fragmentItemsTemplate` constant:

```go
// waitingPollID is the id of the wait page's polling element. htmx sends it as the HX-Trigger
// header of every poll, which is how a poll is told from the filter form's own htmx request:
// the poll is answered 204 while the fetch runs, the filter is answered from the snapshot.
const waitingPollID = "waiting"

// waitingView is what waiting.html renders: how many repositories are being asked, and the
// page's own path and query, which the poll asks for again.
type waitingView struct {
	Repos int
	Path  string
}

// answeredWaiting is the first thing a page handler does (FR-1.9). It asks the cache — which
// starts a fetch when one is due and never waits for it (QS-2.6) — and, while a fetch is in
// flight, answers the request itself and reports so:
//
//   - an ordinary page view gets the wait page;
//   - the wait page's own poll gets 204, so htmx swaps nothing and the animation keeps running;
//   - any other htmx request — the filter form — is not answered here at all, and the caller
//     renders it from the current snapshot (AC5), since a wait page selected for #items would
//     empty the list.
//
// When no fetch is in flight it answers nothing and the caller renders as it always has.
func (s *Server) answeredWaiting(w http.ResponseWriter, r *http.Request) bool {
	snap := s.cache.Get(r.Context())
	if !snap.Fetching {
		return false
	}
	if r.Header.Get("HX-Request") != "true" {
		// A wait page must never come back out of a browser cache: it is only ever right now.
		w.Header().Set("Cache-Control", "no-store")
		s.execute(w, r, http.StatusOK, "waiting.html", pageData{
			Waiting: &waitingView{Repos: len(s.cfg.GitHub.Repos), Path: r.URL.RequestURI()},
		})
		return true
	}
	if r.Header.Get("HX-Trigger") == waitingPollID {
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	return false
}
```

Change `handleDashboard` to:

```go
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if s.answeredWaiting(w, r) {
		return
	}
	s.render(w, r, "dashboard.html")
}
```

Leave `handleItems` and `render` as they are: the fragment is never the wait page, and `render`'s own `s.cache.Get` simply returns the current snapshot (a second `Get` while a fetch is in flight starts nothing).

In `internal/web/sites.go`, at the top of `handleSites`, before `sess, _ := s.session(r)`:

```go
	if s.answeredWaiting(w, r) {
		return
	}
```

Update the comment on `render` in `dashboard.go` that says "It contacts no upstream service itself (FR-1.1): the only thing it reaches for is the cache, which fetches on its own schedule" to add: "— and never waits for that fetch; answeredWaiting has already dealt with a fetch in flight before render runs."

- [ ] **Step 5: Run the new tests, then the whole package under the race detector**

Run: `go test -race ./internal/web/ -run 'TestPageNeverWaitsForGitHub|TestWaitPage|TestPoll|TestFilterRequestDuringFetch|TestSitesViewWaitsToo|TestAFailedFetchEndsTheWaiting|TestNoRenderedHTMLNeedsUnsafeInline' -v 2>&1 | grep -E '^(---|FAIL|ok)'`

Expected: all PASS. If `TestWaitPageWhileFetching` reports the poller line differs only by attribute order or escaping (`&amp;`), match the template to the test, not the other way round — the exact attribute set is the contract with htmx.

Then: `go test -race ./internal/web/ 2>&1 | tail -3` — expected `ok`. A previously green test that now fails is one that reads `/` right after Refresh or a clock move without `getSettled`; fix it as Task 2 Step 6 describes.

- [ ] **Step 6: Whole-module check and commit**

Run: `go build ./... && go vet ./... && go test -race ./...`

```bash
git add internal/web/templates/waiting.html internal/web/templates/layout.html internal/web/server.go internal/web/dashboard.go internal/web/sites.go internal/web/waiting_test.go internal/web/dashboard_test.go
git commit -m "feat(web): the wait page — the animated mark while a fetch runs, an htmx poll answered 204 until it lands, a noscript refresh without JavaScript (FR-1.9, QS-2.6, QS-2.3)

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 5: Requirements, ADR-0011, glossary, README and the comments that said "sequential"

Model tier: cheap (every text is given below; the work is placing it).

**Files:**

- Modify: `docs/requirements/04-functional-requirements.md` (E‑1 table: a row after FR‑1.8)
- Modify: `docs/requirements/05-quality-requirements.md` (quality tree line for QG‑2; §5.3 table: QS‑2.3 amended, QS‑2.6 and QS‑2.7 added)
- Create: `docs/decisions/0011-request-triggered-fetch-never-a-ticker.md`
- Modify: `docs/decisions/README.md` (table row)
- Modify: `docs/decisions/0010-stateless-no-database.md` (the "first view" consequence, lines 53–55)
- Modify: `docs/requirements/06-glossary.md` (Cold start row amended; Wait page row added after it)
- Modify: `README.md` (lines 10–15)
- Modify: `docs/concepts/configuration.md` (line 39)
- Modify: `cmd/zorgscope/main.go` (package comment), `config/zorgscope.yaml` (the QS‑3.5 comment), `deploy/fly.toml` (header comment)

- [ ] **Step 1: Functional requirement FR‑1.9**

In `docs/requirements/04-functional-requirements.md`, insert after the FR‑1.8 row (line 26), one line:

```markdown
| FR‑1.9 | M | As the user I see that GitHub is being asked, and the page arrives on its own. | AC1 While a fetch is running, `GET /` and `GET /sites` show the wait page — the mark, animated, with a status line naming how many repositories are being asked — and not a list, filters or tiles. AC2 The page the visitor asked for, with its query, replaces the wait page without any action: with JavaScript by polling every 500 ms and swapping the page in once the fetch has ended, without it by reloading every two seconds. AC3 A fetch that fails ends the waiting the same way, with the page's error notice and the previous or empty list. AC4 With reduced motion requested the mark neither rotates nor scales, and the status line is shown regardless; colour is never the only signal. AC5 An htmx request other than the poll — the filter form — is answered from the current list during a fetch, never with the wait page. |
```

- [ ] **Step 2: Quality scenarios**

In `docs/requirements/05-quality-requirements.md`:

- In the quality tree, change the QG‑2 line to `├── QG‑2 Speed                  page weight, pagination that cannot hang, a page that never waits for GitHub`.
- In §5.3's QS‑2.3 row, append this sentence to the Measure cell, before the closing pipe: "The wait page (FR‑1.9) has its own measure: at most 20 kB of HTML and at most 100 kB of static assets on the wire, asserted by `TestWaitPageStaysInsideItsBudget` (`internal/web/waiting_test.go`)."
- After the QS‑2.5 row, add:

```markdown
| QS‑2.6 | A list that is empty or stale | A page view while GitHub has not answered | The page answers without waiting for GitHub: the wait page, and the fetch runs on | With a source that blocks until the test releases it, `GET /` answers within 200 ms, asserted by `TestPageNeverWaitsForGitHub` (`internal/web/waiting_test.go`); `Get` of the cache returns at once with `Fetching` set, asserted by `TestFirstGetReturnsAtOnceWithNothingWhileFetching` (`internal/snapshot`). |
| QS‑2.7 | The representative configuration (10 repositories) | A fetch runs against a source that answers each request after 200 ms | The twenty requests run side by side | The fetch completes within 1 s, asserted by `TestFetchRunsRepositoriesSideBySide` (`internal/adapters/github`); the result keeps configuration order, asserted by `TestFetchKeepsConfigurationOrder`. |
```

- [ ] **Step 3: ADR‑0011**

Create `docs/decisions/0011-request-triggered-fetch-never-a-ticker.md`:

```markdown
# 0011. The fetch runs in a goroutine a request started, never a ticker; pages never wait for it

* Status: accepted
* Date: 2026-09-16
* Requirements: FR‑1.9, QS‑2.6, QS‑2.7, C‑3

## Context and problem statement

Measured on 2026-09-16, a cold visit took about eleven seconds, nine of them eighteen GraphQL
requests made one after the other while the page handler waited for all of them. The page was
blank for as long as GitHub took. C‑3 forbids a process that works between requests, so the
usual answer — a background loop that keeps the list warm — is not available. The question is
where the fetch runs and what the visitor sees while it does.

## Considered options

* Block the page until the fetch has returned, as before, but fetch the repositories side by side.
* Run the fetch in a goroutine the request starts, detached from that request and bounded by the
  existing budget, and show a wait page that polls until the page is ready.
* Serve the stale list at once and refetch behind it (stale-while-revalidate).
* A background ticker that refetches on a schedule.

## Decision outcome

Chosen: **a request-started, budget-bounded goroutine with a wait page**, together with the
side-by-side fetch. The page answers in milliseconds whatever GitHub does, the machine still does
nothing between requests, and the visitor is told what is happening rather than shown a blank tab.

`internal/snapshot.Cache.Get` never waits: when a fetch is due and none is in flight it starts one
under `context.WithoutCancel` and `fetchBudget`, marks it in flight, and returns what it has with
`Fetching` set. The two page handlers answer a request during a fetch with the wait page, its poll
with `204`, and every other htmx request from the current snapshot (design
`docs/superpowers/specs/2026-09-16-fast-first-view-design.md`, §4 and §5).

### Consequences

* Good: a page view answers within 200 ms with the source blocked (QS‑2.6); a cold visit is about
  the machine's own wake plus one round trip to GitHub.
* Good: nothing runs unless a request asked for it, and the goroutine lives at most sixty seconds;
  Fly's proxy keeps the machine awake for the polling requests that wait on it anyway, so C‑3's
  scale-to-zero is untouched.
* Bad: a stale list is withheld for the length of a fetch, by decision — the wait page appears on
  every fetch, an expired TTL and the Refresh button included, rather than only on a cold start.
* Neutral: the goroutine can outlive the request that started it by at most the fetch budget; a
  visitor closing the tab does not cancel a fetch everyone else is waiting for.

## Pros and cons of the options

### Block the page, fetch side by side

* Good: the smallest change; the side-by-side fetch alone takes nine seconds down to under one.
* Bad: the page is still blank for as long as the slowest request takes, and a slow GitHub still
  turns into a slow page.

### A request-started goroutine with a wait page

* Good: see Decision outcome above.
* Bad: see Consequences above.

### Stale-while-revalidate

* Good: a returning visitor sees a list at once, however old.
* Bad: what is on screen is silently replaced a moment later; the header would have to say the
  list is being replaced; and it does nothing at all for the cold start, which has no stale list.
  Rejected in conversation on 2026-09-16.

### A background ticker

* Good: the list is always warm.
* Bad: contradicts C‑3 directly and would keep the machine running; rejected for the same reason
  as in [ADR‑0003](0003-fly-scale-to-zero-external-cron.md).
```

In `docs/decisions/README.md`, add after the 0010 row:

```markdown
| 0011 | The fetch runs in a goroutine a request started, never a ticker; pages never wait for it | `0011-request-triggered-fetch-never-a-ticker.md` | accepted |
```

In `docs/decisions/0010-stateless-no-database.md`, replace the consequence at lines 53–55 with:

```markdown
* Bad: the first view after the Fly Machine scales to zero pays one fetch rather than reading a
  warm cache; this is the price a refresh button charges in place of a cron-warmed database, and it
  is accepted as such. Since [ADR‑0011](0011-request-triggered-fetch-never-a-ticker.md) the fetch
  runs side by side and the page does not wait for it: the visitor sees the wait page for about a
  second rather than a blank tab for nine.
```

- [ ] **Step 4: Glossary, README, concept, comments**

`docs/requirements/06-glossary.md`: replace the Cold start row with

```markdown
| **Cold start** | The first request after the Fly Machine has been stopped; it includes starting the machine and the process and, when the snapshot is empty or stale, the fetch that follows — shown as the wait page, not waited for. |
| **Wait page** | The page shown in place of the list or the tiles while a fetch is running: the mark, animated, a status line naming how many repositories are being asked, and a poll that replaces it with the page once the fetch has ended. |
```

`README.md`: replace lines 10–15 (the "One Go binary runs…" paragraph) with:

```markdown
One Go binary runs on a Fly.io Machine that is **stopped whenever nothing is happening**. There is
no database and no background scheduler: the backend fetches straight from GitHub on demand, every
repository side by side, keeps the last fetched list in memory for a few minutes, and a "Refresh"
button fetches immediately when that is not fresh enough. While a fetch runs the page shows the
zorgscope mark, animated, and replaces it with the list on its own. The container itself is
disposable — it remembers nothing between restarts except what a signed-in visitor's own browser
carries in its session cookie (design [ADR‑0010](docs/decisions/0010-stateless-no-database.md),
[ADR‑0011](docs/decisions/0011-request-triggered-fetch-never-a-ticker.md)).
```

`docs/concepts/configuration.md` line 39: change "before a page view triggers a fetch" to "before a page view triggers a fetch — shown as the wait page while it runs (FR‑1.9)".

`cmd/zorgscope/main.go`, package comment: replace "and the first page view after a cold start pays the one fetch (design §5)." with "and the first page view after a cold start starts the one fetch and shows the wait page until it lands (ADR-0011)."

`config/zorgscope.yaml`: after the sentence "so this list may hold at most ten entries.", add this comment line, indented like its neighbours:

```yaml
  # The repositories are fetched side by side, so the list's length costs no time, only requests.
```

`deploy/fly.toml` header, second line: replace "and the first page view after a cold start pays the one fetch (design §5)." with "and the first page view after a cold start shows the wait page while the one fetch runs (ADR-0011)."

- [ ] **Step 5: Lint the documents and run the full gate**

Run:

```bash
docker run --rm -v "$PWD:/work" -w /work davidanson/markdownlint-cli2:latest "docs/**/*.md" "README.md"
make check
```

Expected: markdownlint `0 issues`; `make check` green end to end (vet, golangci-lint, race tests, domain coverage ≥ 90 %, markdownlint, fly config validate).

- [ ] **Step 6: Commit**

```bash
git add docs/requirements/04-functional-requirements.md docs/requirements/05-quality-requirements.md docs/decisions/0011-request-triggered-fetch-never-a-ticker.md docs/decisions/README.md docs/decisions/0010-stateless-no-database.md docs/requirements/06-glossary.md README.md docs/concepts/configuration.md cmd/zorgscope/main.go config/zorgscope.yaml deploy/fly.toml
git commit -m "docs: FR-1.9 the wait page, QS-2.6 and QS-2.7, ADR-0011 request-triggered fetch, glossary and README (FR-1.9, QS-2.6, QS-2.7)

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 6: Real GitHub, deploy, measure (Gernot, or with his go-ahead)

Not a subagent task: it needs a fresh token, a Fly login and a judgement about what the browser shows.

- [ ] **Step 1: A token that works.** The `GITHUB_TOKEN` in the local `.env` is expired (GitHub answers 401). Create a fine-grained token with no repository access and no expiry at `https://github.com/settings/personal-access-tokens/new?name=zorgscope&expires_in=none`, put it in `.env`, and if the Fly secret is the same expired token: `flyctl secrets set GITHUB_TOKEN=<value> -a zorgscope`.
- [ ] **Step 2: Local run.** `make backend`, then open `http://localhost:8080`: sign in, press Refresh, and watch the wait page appear and give way to the list on its own. Try it once with JavaScript disabled (the page reloads every two seconds) and once with "reduce motion" on in the OS.
- [ ] **Step 3: Deploy and measure.** `make deploy`; wait for the machine to stop (`fly status -a zorgscope` shows `stopped`, about eight minutes idle); then time a cold visit in the browser's network panel: the first document should arrive in about two seconds and the list about a second later. `fly logs -a zorgscope --no-tail` should show one `listening` and no `fetch failed`.
- [ ] **Step 4: Merge.** `git checkout main && git merge --ff-only feat/fast-first-view && git push`.
