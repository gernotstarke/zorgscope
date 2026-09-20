# Warm start Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A page view during a fetch shows the list it already has, marked refreshing, instead of the wait page — and on a genuinely cold Machine the wait page shows the visitor's own last list from `localStorage` behind it.

**Architecture:** Two disjoint layers. Layer 1 moves one predicate in `answeredWaiting` so the wait page is chosen by *having nothing* rather than by *fetching*, and the items fragment grows a self-terminating htmx poll that asks `GET /items` until a fragment without the poll replaces it. Layer 2 adds a static script that saves the rendered `#items` HTML to `localStorage` on the dashboard and restores it on the wait page, gated by an epoch derived from the OAuth client secret so a rotation invalidates every stored copy.

**Tech Stack:** Go 1.x standard library only in `internal/domain`; `html/template`; htmx 1.x vendored at `internal/web/static/htmx.min.js`; no new dependencies. Docker-only toolchain via `make`.

**Spec:** `docs/superpowers/specs/2026-09-20-warm-start-design.md`

## Global Constraints

- **Branch:** `feat/warm-start`, already created off `main` at `7ba8a61`. The spec is committed at `fc4f179`.
- **No new dependencies.** `go.mod` is not touched.
- **CSP:** `default-src 'self'; … script-src 'self'` with no `'unsafe-inline'` (`internal/web/server.go:88`). Every script is a file under `internal/web/static/`. Values reach scripts as `data-` attributes, never inline `<script>`.
- **No `template.HTML` for upstream text.** Issue titles, repository names, summaries and error text are attacker-influenceable (QS‑4.3, QS‑4.4). Every string a template prints is computed in Go in a view type.
- **Domain purity:** `internal/domain` imports nothing but the standard library (QS‑5.1) and calls no `time.Now()`. Nothing in this plan touches `internal/domain`.
- **Budgets that must stay green:** `TestRenderedPageStaysInsideItsBudget`, `TestStaticAssetsFitTheirBudgetOnTheWire`, `TestWaitPageStaysInsideItsBudget` (150 kB HTML / 50 kB static for pages; 20 kB HTML / 100 kB static for the wait page).
- **Verification command for everything below:** `make check` (vet, golangci-lint, race tests, ≥90 % domain coverage, markdownlint, `fly config validate`). Individual tests run as `docker compose -f deploy/compose.yml run --rm dev go test ./internal/web/ -run '<name>' -v` — or whatever `GO_RUN` expands to in the `Makefile`; check it once at the start and reuse.
- **Attribution:** every commit ends with `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.
- **Prose style:** comments explain *why*, in full sentences, British spelling (`colour`, `behaviour`). Match the density of the file being edited — this codebase comments heavily and the reviewer expects it.

---

## File Structure

| File | Responsibility | Task |
|---|---|---|
| `internal/web/dashboard.go` | `answeredWaiting` predicate; `itemsView` gains `Fetching`/`Repos`; `itemsView` builder sets them | 1, 2 |
| `internal/web/templates/fragments/items.html` | The refreshing status line and its self-terminating poll | 2 |
| `internal/web/waiting_test.go` | The two inverted tests plus the new layer-1 tests | 1, 2 |
| `internal/web/cache_epoch.go` (new) | `cacheEpochContext`, `newCacheEpoch` — one small unit, no other responsibility | 3 |
| `internal/web/cache_epoch_test.go` (new) | Epoch derivation and its domain separation from the session key | 3 |
| `internal/web/server.go` | `Server.cacheEpoch` field, set in `New`; `pageData.CacheEpoch`, set in `execute` | 3 |
| `internal/web/templates/layout.html` | `data-cache-epoch` on `<html>`; `warm-start.js` script tag | 3, 4 |
| `internal/web/static/warm-start.js` (new) | Save, restore, clear. No application logic. | 4 |
| `internal/web/templates/waiting.html` | The placeholder mount point the script fills | 4 |
| `internal/web/static/app.css` | `.refreshing` and `.placeholder-note` styling | 2, 4 |
| `internal/web/warm_start_test.go` (new) | The script is linked, versioned, and no page carries an inline script | 4 |
| `docs/requirements/04-functional-requirements.md` | FR‑1.9 AC1/AC2 rewritten, AC6/AC7 added | 5 |
| `docs/decisions/0013-stale-while-revalidating.md` (new) | The ADR | 5 |
| `internal/version/version.go` | `0.4.0` → `0.5.0` | 5 |

Layer 1 (tasks 1–2) ships working software on its own: if layer 2 were abandoned, the product is strictly better than today. Layer 2 (tasks 3–4) is additive and touches no layer-1 behaviour.

---

### Task 1: The wait page is chosen by having nothing, not by fetching

**Files:**

- Modify: `internal/web/dashboard.go:61-84` (`answeredWaiting`)
- Test: `internal/web/waiting_test.go:219-287` (two existing tests inverted)

**Interfaces:**

- Consumes: `snapshot.Snapshot{Items []domain.Item, FetchedAt time.Time, Err error, ErrAt time.Time, Fetching bool}`
- Produces: `answeredWaiting(w, r) (snapshot.Snapshot, bool)` — unchanged signature; the `bool` now reports "answered with the wait page", which is true only for an empty snapshot.

- [ ] **Step 1: Invert the two tests that assert the old policy**

Replace `TestRefreshShowsTheWaitPageEvenThoughAListIsAlreadyHeld` (`waiting_test.go:219`) and `TestAnExpiredTTLShowsTheWaitPageNotTheOldList` (`waiting_test.go:256`) with these. Keep `refetchBlockSource`, `warmCache`, `getSettled` and the rest of the file untouched.

```go
// FR-1.9 AC1: a list already held is shown while the refetch is on its way, marked, rather than
// taken away. Refresh is covered by the same rule as the TTL — see the design, §4.3: POST
// /refresh invalidates and redirects, and the redirected GET cannot tell an invalidated snapshot
// from one the TTL aged out, nor should it.
func TestRefreshKeepsTheListAndMarksItRefreshing(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	src := &refetchBlockSource{
		items:   []domain.Item{ghItem(1, "Held already", testNow.Add(-time.Hour))},
		release: release,
	}
	s, cache := newColdServerWith(t, func(o *Options) {
		o.Config.GitHub.Repos = append([]string{"org/repo"}, representativeRepos()...)
		o.Cache = snapshot.New(src, time.Hour, o.Clock)
	})
	warmCache(t, cache)
	h := s.Handler()
	c := signIn(t, h)

	rec := postAs(h, "/refresh", nil, c)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /refresh = %d, want %d", rec.Code, http.StatusSeeOther)
	}

	body := getAs(h, "/", c).Body.String()
	if strings.Contains(body, waitingMarker) {
		t.Error("GET / after Refresh showed the wait page instead of the list it already held")
	}
	if !strings.Contains(body, "Held already") {
		t.Error("GET / after Refresh did not show the list it already held")
	}
	if !strings.Contains(body, refreshingMarker) {
		t.Error("the list was shown without saying that a fetch is running")
	}

	release <- struct{}{}
	body = getSettled(t, h, "/", c).Body.String()
	if !strings.Contains(body, "Held already") {
		t.Error("once the refetch landed the list did not come back")
	}
	if strings.Contains(body, refreshingMarker) {
		t.Error("the settled list still claims a fetch is running")
	}
}

// FR-1.9 AC1: the same rule for a TTL that has expired. The visitor did nothing here — the clock
// did — so taking their list away is even less defensible than it is after Refresh.
func TestAnExpiredTTLShowsTheOldListMarkedRefreshing(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	src := &refetchBlockSource{
		items:   []domain.Item{ghItem(1, "Held already", testNow.Add(-time.Hour))},
		release: release,
	}
	var clock *ports.FixedClock
	s, cache := newColdServerWith(t, func(o *Options) {
		o.Config.GitHub.Repos = append([]string{"org/repo"}, representativeRepos()...)
		clock = o.Clock.(*ports.FixedClock)
		o.Cache = snapshot.New(src, time.Minute, o.Clock)
	})
	warmCache(t, cache)
	h := s.Handler()
	c := signIn(t, h)

	clock.Advance(2 * time.Minute) // past the one-minute TTL

	body := getAs(h, "/", c).Body.String()
	if strings.Contains(body, waitingMarker) {
		t.Error("GET / past the TTL showed the wait page instead of the list it already held")
	}
	if !strings.Contains(body, "Held already") {
		t.Error("GET / past the TTL did not show the list it already held")
	}
	if !strings.Contains(body, refreshingMarker) {
		t.Error("the list was shown without saying that a fetch is running")
	}

	release <- struct{}{}
	body = getSettled(t, h, "/", c).Body.String()
	if !strings.Contains(body, "Held already") {
		t.Error("once the refetch landed the list did not come back")
	}
}
```

Add beside `waitingMarker` in `internal/web/auth_test.go:901`:

```go
// refreshingMarker is the list's own "a fetch is running" element, the layer-1 counterpart of
// waitingMarker. A test asserting the list is shown during a fetch has to assert this too:
// showing a stale list without saying it is stale is the one outcome FR-1.9 rules out.
const refreshingMarker = `class="refreshing"`
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `make check` — or, faster while iterating, the package alone:
`go test ./internal/web/ -run 'TestRefreshKeepsTheList|TestAnExpiredTTLShowsTheOldList' -v`

Expected: FAIL. Both report "showed the wait page instead of the list it already held", because the predicate has not moved yet. `refreshingMarker` is also absent — that is task 2's half, and it is expected to keep failing after this task's step 4.

- [ ] **Step 3: Move the predicate**

In `internal/web/dashboard.go`, in `answeredWaiting`, replace:

```go
	snap := s.cache.Get(r.Context())
	if !snap.Fetching {
		return snap, false
	}
```

with:

```go
	snap := s.cache.Get(r.Context())
	// A fetch in flight is not by itself a reason to withhold the page. What is, is having
	// nothing to put on it: an empty snapshot can only draw an empty list, and "nothing is open"
	// is a wrong answer rather than a stale one — which is the whole reason the wait page exists.
	// With items in hand the page is drawn from them and says that a fetch is running (FR-1.9
	// AC1, AC6), and the poll inside the list swaps the fresh one in when it lands.
	//
	// This is deliberately blind to why the snapshot is stale. POST /refresh invalidates and
	// redirects, so the redirected GET sees exactly what an aged-out TTL leaves behind, and
	// telling them apart would need a field in Snapshot that nothing else wants (design §4.3).
	if !snap.Fetching || len(snap.Items) > 0 {
		return snap, false
	}
```

Then update the function's doc comment: its second paragraph currently begins "While a fetch is in flight it also answers the request itself and reports so". Change that opening clause to "While a fetch is in flight **and the snapshot is empty** it also answers the request itself and reports so", and leave the three bullets below it as they are — they still describe the empty case exactly.

- [ ] **Step 4: Run the tests to verify the wait page is gone**

Run: `go test ./internal/web/ -run 'TestRefreshKeepsTheList|TestAnExpiredTTLShowsTheOldList' -v`

Expected: both still FAIL, but now only on `refreshingMarker` — "the list was shown without saying that a fetch is running". The two "showed the wait page" assertions must now pass. If a "showed the wait page" assertion still fails, the predicate is wrong; do not proceed.

Also run the neighbours to see what task 2 must not break:
`go test ./internal/web/ -run 'TestWaitPageWhileFetching|TestSitesViewWaitsToo|TestPollAnswers204|TestPollReceivesThePage|TestFilterRequestDuringFetch|TestAFailedFetchEndsTheWaiting' -v`

Expected: `TestWaitPageWhileFetching` and `TestSitesViewWaitsToo` may now FAIL, because they warm the cache and then expect a wait page. That is correct and task 2 step 5 fixes them by giving them an empty snapshot. The other four must PASS.

- [ ] **Step 5: Do not commit yet**

Layer 1 is half-built: the list is shown but says nothing about being stale, which is worse than either endpoint. Task 2 completes it and commits both together.

---

### Task 2: The list says it is refreshing, and finishes by itself

**Files:**

- Modify: `internal/web/dashboard.go` (`itemsView` struct ~line 205; `itemsView` builder ~line 367; `dashboardView` builder ~line 352; `render` ~line 108)
- Modify: `internal/web/templates/fragments/items.html`
- Modify: `internal/web/static/app.css`
- Modify: `internal/web/waiting_test.go` (two neighbours given an empty snapshot; two new tests)

**Interfaces:**

- Consumes: `answeredWaiting` from task 1, returning `false` for a stale-but-populated snapshot.
- Produces: `itemsView{Query template.URL, Groups []groupView, Filtered bool, ShownLine string, Fetching bool, Repos int}` — the two new fields are what the fragment reads, and `GET /items` serves the same struct, so page and fragment draw one poll from one source.

- [ ] **Step 1: Write the failing tests**

Append to `internal/web/waiting_test.go`:

```go
// The poll lives inside the fragment it replaces, which is what makes it stop without anything
// counting: the first fragment rendered after the fetch has landed carries no poll and takes the
// running one away with it. If a fragment ever carried the poll unconditionally, the list would
// ask for itself every 500 ms for ever.
func TestTheItemsFragmentCarriesThePollOnlyWhileFetching(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	src := &refetchBlockSource{
		items:   []domain.Item{ghItem(1, "Held already", testNow.Add(-time.Hour))},
		release: release,
	}
	var clock *ports.FixedClock
	s, cache := newColdServerWith(t, func(o *Options) {
		clock = o.Clock.(*ports.FixedClock)
		o.Cache = snapshot.New(src, time.Minute, o.Clock)
	})
	warmCache(t, cache)
	h := s.Handler()
	c := signIn(t, h)

	settled := getAs(h, "/items", c).Body.String()
	if strings.Contains(settled, refreshingMarker) {
		t.Error("a fragment drawn with no fetch in flight carries the refresh poll; it would never stop")
	}

	clock.Advance(2 * time.Minute)
	fetching := getAs(h, "/items", c).Body.String()
	if !strings.Contains(fetching, refreshingMarker) {
		t.Error("a fragment drawn during a fetch carries no refresh poll, so the list would never update")
	}
	// It has to ask for the fragment, and replace the section it lives in. Targeting anything
	// else would leave the poll behind and start a second one on every swap.
	for _, want := range []string{`hx-get="/items"`, `hx-target="#items"`, `hx-swap="outerHTML"`} {
		if !strings.Contains(fetching, want) {
			t.Errorf("the refresh poll is missing %s", want)
		}
	}

	release <- struct{}{}
	getSettled(t, h, "/", c)
}

// FR-1.9 AC6: a stale list is honest about its age. It states the time its items were fetched at,
// which is the header's own line, and does not claim to be current.
func TestAStaleListStatesWhenItWasFetched(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	src := &refetchBlockSource{
		items:   []domain.Item{ghItem(1, "Held already", testNow.Add(-time.Hour))},
		release: release,
	}
	var clock *ports.FixedClock
	s, cache := newColdServerWith(t, func(o *Options) {
		clock = o.Clock.(*ports.FixedClock)
		o.Cache = snapshot.New(src, time.Minute, o.Clock)
	})
	warmCache(t, cache)
	h := s.Handler()
	c := signIn(t, h)

	clock.Advance(2 * time.Minute)
	body := getAs(h, "/", c).Body.String()
	if !strings.Contains(body, "fetched") {
		t.Error("the stale page does not say when its items were fetched")
	}
	if strings.Contains(body, "never") {
		t.Error("the stale page says its items were never fetched, though it is showing some")
	}

	release <- struct{}{}
	getSettled(t, h, "/", c)
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/web/ -run 'TestTheItemsFragmentCarriesThePoll|TestAStaleListStatesWhenItWasFetched' -v`

Expected: FAIL. `TestTheItemsFragmentCarriesThePoll` fails on "a fragment drawn during a fetch carries no refresh poll" — the template has no such element yet.

- [ ] **Step 3: Add the two fields and set them**

In `internal/web/dashboard.go`, add to the `itemsView` struct, after `ShownLine`:

```go
	// Fetching says a fetch is in flight, so the list draws the poll that will replace it and
	// states that it is not current (FR-1.9 AC2, AC6). It lives on the items view rather than on
	// the page, because GET /items renders this struct alone: putting it on the page would mean
	// the swapped-in fragment could not carry the poll, and the list would stop updating after
	// the first swap.
	Fetching bool
	// Repos is how many repositories the running fetch is asking, for the status line. The wait
	// page names the same number, from the same configuration.
	Repos int
```

Change `itemsView`'s builder signature and body (~line 367):

```go
func (s *Server) itemsView(d domain.Dashboard, snap snapshot.Snapshot, now time.Time) itemsView {
	v := itemsView{
		Query:     template.URL(queryString(d.Filter)), // #nosec G203 -- see the field's comment
		Groups:    make([]groupView, 0, len(d.Groups)),
		Filtered:  !d.Filter.Empty(),
		ShownLine: shownLine(d),
		Fetching:  snap.Fetching,
		Repos:     len(s.cfg.GitHub.Repos),
	}
```

…leaving the rest of the function exactly as it is. Then update its one caller in `dashboardView` (~line 359):

```go
		Items:      s.itemsView(d, snap, now),
```

`dashboardView` already receives `snap`, so nothing above it changes.

- [ ] **Step 4: Add the status line to the fragment**

In `internal/web/templates/fragments/items.html`, immediately after the opening `<section class="items-section" id="items">` and before the `<p class="source-line">`:

```html
  {{/* While a fetch runs, the list says so and asks for itself again (FR-1.9 AC2, AC6). The
       polling element sits inside the section it replaces, so the first fragment rendered after
       the fetch has landed carries no poll and takes this one away with it: the poll stops
       because the thing that polls is gone, not because anything counted.

       role="status" with aria-live="polite" announces it without interrupting, and the text
       says it in words — motion and colour are never the only signal (FR-1.9 AC4). */}}
  {{if .Fetching}}
  <p class="refreshing" role="status" aria-live="polite"
     hx-get="/items" hx-trigger="every 500ms" hx-target="#items" hx-swap="outerHTML">
    Asking GitHub about {{.Repos}} repositories…
  </p>
  {{end}}
```

- [ ] **Step 5: Give the two wait-page neighbours an empty snapshot**

`TestWaitPageWhileFetching` (`waiting_test.go:79`) and `TestSitesViewWaitsToo` (`waiting_test.go:180`) warm the cache and then expect the wait page, which is no longer what a warm cache produces. Remove the `warmCache(t, cache)` call from each, so the snapshot is genuinely empty and the wait page is genuinely right, and add above each:

```go
// The wait page is now for an empty snapshot only — a cold Machine — so this test no longer warms
// the cache first. A warm one shows its list, marked; that is TestAnExpiredTTLShowsTheOldList...
```

If removing `warmCache` leaves `cache` unused in either test, drop it from the `newColdServerWith` destructuring with `_`.

- [ ] **Step 6: Style the status line**

In `internal/web/static/app.css`, beside the existing `.source-line` rules:

```css
/* The list's own "a fetch is running" line. It reads as secondary to the list below it — this is
   a note about the list's age, not a headline — and it never relies on colour alone: the words
   say it, and the dot beside them is decorative. */
.refreshing {
  margin: 0 0 var(--space-2);
  font-size: 0.875rem;
  color: var(--fg-muted);
}
.refreshing::before {
  content: "";
  display: inline-block;
  width: 0.5rem;
  height: 0.5rem;
  margin-right: 0.5ch;
  border-radius: 50%;
  background: var(--fg-muted);
  animation: refreshing-pulse 1.2s ease-in-out infinite;
}
@keyframes refreshing-pulse {
  50% { opacity: 0.25; }
}
/* QS-4.4: a visitor who asks for less motion gets none. The dot stays, so the line still has its
   mark; it simply holds still. */
@media (prefers-reduced-motion: reduce) {
  .refreshing::before { animation: none; }
}
```

Check `--space-2` and `--fg-muted` against the custom properties actually defined at the top of `app.css` and substitute the file's own names if they differ. `contrast_test.go` reads this file and asserts contrast ratios; if it fails, pick the muted foreground token it already accepts.

- [ ] **Step 7: Run the whole package**

Run: `go test ./internal/web/ -v`

Expected: PASS, including the four tests from tasks 1 and 2 and all of `waiting_test.go`. `TestRenderedPageStaysInsideItsBudget` must still pass — the status line is one short paragraph.

- [ ] **Step 8: Run the full check**

Run: `make check`

Expected: clean. `golangci-lint` will flag `itemsView`'s new parameter if any other caller was missed; there is exactly one.

- [ ] **Step 9: Commit layer 1**

```bash
git add internal/web/dashboard.go internal/web/templates/fragments/items.html \
        internal/web/static/app.css internal/web/waiting_test.go internal/web/auth_test.go
git commit -F - <<'EOF'
feat(web): a fetch no longer takes the list away — stale while revalidating (FR-1.9)

The wait page is now chosen by having nothing to show rather than by a fetch being in flight, so
the common case — a warm Machine whose snapshot has merely crossed the five-minute TTL — keeps
the visitor's list on screen, marked, and swaps the fresh one in when it lands. The poll lives
inside the fragment it replaces, so it stops because the thing that polls is gone.

Refresh is covered by the same rule: POST /refresh invalidates and redirects, and the redirected
GET cannot tell that from an aged-out TTL, nor should it (design §4.3).

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

### Task 3: The cache epoch

**Files:**

- Create: `internal/web/cache_epoch.go`
- Create: `internal/web/cache_epoch_test.go`
- Modify: `internal/web/server.go` (`Server` struct ~line 115, `New` ~line 206, `pageData` ~line 706, `execute`)
- Modify: `internal/web/templates/layout.html:6`

**Interfaces:**

- Produces: `newCacheEpoch(secret string) string` — eight lowercase hex characters. `Server.cacheEpoch string`. `pageData.CacheEpoch string`, read by `layout.html` as `data-cache-epoch`.

- [ ] **Step 1: Write the failing test**

Create `internal/web/cache_epoch_test.go`:

```go
package web

import (
	"strings"
	"testing"
)

// The epoch is what makes rotating the OAuth client secret reach a copy of the list sitting in a
// browser's localStorage. If it did not change with the secret, a rotation — this product's only
// sign-out beyond the Log out button (FR-8.3 AC4) — would leave every stored list readable for
// ever, which is precisely the state the rotation was performed to end.
func TestTheEpochChangesWithTheSecret(t *testing.T) {
	if newCacheEpoch("one") == newCacheEpoch("two") {
		t.Error("two secrets produced the same epoch; a rotation would not invalidate a stored list")
	}
	if newCacheEpoch("one") != newCacheEpoch("one") {
		t.Error("the epoch is not stable for one secret; every page view would discard the cache")
	}
}

// The epoch is published in the page's markup, and the session signing key is derived from the
// same secret. A reader of the epoch must learn nothing about that key, which is what the separate
// context string buys: two hashes of the same secret under different domains.
func TestTheEpochSaysNothingAboutTheSigningKey(t *testing.T) {
	const secret = "the-client-secret"
	epoch := newCacheEpoch(secret)
	key := newSessionCodec(secret).key

	if strings.Contains(string(key[:]), epoch) {
		t.Error("the epoch appears inside the session signing key")
	}
	// The epoch must not be a prefix of the key under any encoding a careless refactor might
	// reach for: it is a different hash, not a truncation of the same one.
	if epoch == newCacheEpoch(sessionKeyContext+secret) {
		t.Error("the epoch is derivable by hashing the session key's own input")
	}
}

// Eight hex characters, so the attribute is short and a mismatch is decidable by string equality
// in the browser. 32 bits is not a secret and does not need to be: it is a version tag.
func TestTheEpochIsEightHexCharacters(t *testing.T) {
	e := newCacheEpoch("whatever")
	if len(e) != 8 {
		t.Errorf("epoch = %q, want 8 characters", e)
	}
	if strings.Trim(e, "0123456789abcdef") != "" {
		t.Errorf("epoch = %q, want lowercase hex only", e)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/web/ -run TestTheEpoch -v`

Expected: FAIL to compile — `undefined: newCacheEpoch`.

- [ ] **Step 3: Write the implementation**

Create `internal/web/cache_epoch.go`:

```go
// How a rotated client secret reaches a list sitting in a browser's localStorage.
//
// requireSession sets Cache-Control: no-store on every session route precisely so that rotating
// GITHUB_OAUTH_CLIENT_SECRET is a real revocation — a page the browser stored cannot outlive the
// credential it was served against (FR-8.3 AC4). A copy the page itself put in localStorage would
// escape that entirely: no header reaches it, and the server has no way to clear it. The epoch is
// the answer. It is stamped on the document, the browser stores it beside the list, and the list
// is restored only when the two agree. Rotate the secret and every stored list, in every browser,
// becomes unreadable at once — without a session table, a revocation list, or anything else that
// would have to survive the Machine being stopped.
package web

import (
	"crypto/sha256"
	"encoding/hex"
)

// cacheEpochContext domain-separates this value from the session signing key, which is derived
// from the same secret. The epoch is published in the page's markup; the signing key must never be
// derivable from it, and a distinct context string is what guarantees one hash says nothing about
// the other. It says v1 because no epoch has been minted under another.
const cacheEpochContext = "zorgscope-cache-epoch-v1"

// cacheEpochLen is how many bytes of the hash become the epoch. Four — eight hex characters — is
// a version tag rather than a secret: it only has to differ between secrets, which 32 bits does
// with a margin nothing here will ever test.
const cacheEpochLen = 4

// newCacheEpoch returns the epoch for secret: a short, stable, one-way tag that changes when and
// only when the secret does.
func newCacheEpoch(secret string) string {
	sum := sha256.Sum256([]byte(cacheEpochContext + secret))
	return hex.EncodeToString(sum[:cacheEpochLen])
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/web/ -run TestTheEpoch -v`

Expected: PASS, all three.

- [ ] **Step 5: Carry it to the page**

In `internal/web/server.go`, add to the `Server` struct, after the `codec` field:

```go
	// cacheEpoch is stamped on every page so the browser can tell a stored list minted under the
	// current client secret from one minted before a rotation. See cache_epoch.go.
	cacheEpoch string
```

In `New`, in the `&Server{…}` literal, after the `codec:` line:

```go
		cacheEpoch:       newCacheEpoch(o.Config.Secrets.OAuthClientSecret),
```

Add to `pageData`, after the `Version` field:

```go
	// CacheEpoch is the document's data-cache-epoch: which client secret this page was rendered
	// under. execute fills it in for every page, the way it fills in Theme and Version, because a
	// page that silently lost it would have its stored list refused for ever — the failure would
	// look like the cache simply not working.
	CacheEpoch string
```

In `execute`, wherever it sets `d.Version` and `d.Theme`, add alongside them:

```go
	d.CacheEpoch = s.cacheEpoch
```

- [ ] **Step 6: Stamp it on the document**

In `internal/web/templates/layout.html`, line 6, extend the `<html>` tag:

```html
<html lang="en"{{with .ThemeAttr}} data-theme="{{.}}"{{end}} data-cache-epoch="{{.CacheEpoch}}">
```

Add to the comment block above it, as a new paragraph:

```text
     data-cache-epoch travels the same way and for the same reason the theme does: the CSP grants
     no 'unsafe-inline', so there is no inline script to put a value in, and an attribute is what
     a static file can read. See cache_epoch.go for what it is for.
```

- [ ] **Step 7: Run the package**

Run: `go test ./internal/web/ -v`

Expected: PASS. Nothing reads the attribute yet; this step only proves it renders and breaks no budget or CSP assertion.

- [ ] **Step 8: Commit**

```bash
git add internal/web/cache_epoch.go internal/web/cache_epoch_test.go \
        internal/web/server.go internal/web/templates/layout.html
git commit -F - <<'EOF'
feat(web): the cache epoch, so a rotated secret reaches browser storage (FR-8.3)

A one-way tag derived from the OAuth client secret under its own context string, stamped on every
document as data-cache-epoch. Nothing reads it yet; the script that stores the list does, and
refuses any stored copy whose epoch disagrees — which is how rotating the secret invalidates every
stored list in every browser at once.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

### Task 4: The browser's own last view

**Files:**

- Create: `internal/web/static/warm-start.js`
- Create: `internal/web/warm_start_test.go`
- Modify: `internal/web/templates/layout.html` (script tag beside `search.js`)
- Modify: `internal/web/templates/waiting.html` (the mount point)
- Modify: `internal/web/static/app.css` (`.placeholder-note`)

**Interfaces:**

- Consumes: `data-cache-epoch` on `<html>` from task 3; `#items` as rendered by task 2.
- Produces: `localStorage["zorgscope.cache.v1"] = {"epoch","savedAt","fetchedAt","html"}`; the wait page's `#placeholder` mount point.

- [ ] **Step 1: Write the failing test**

Create `internal/web/warm_start_test.go`:

```go
package web

import (
	"strings"
	"testing"
)

// The script is a file under /static, versioned like every other asset. It cannot be inline: the
// Content-Security-Policy grants no 'unsafe-inline' (see contentSecurityPolicy), so an inline
// script would simply not run, and the failure would be silent.
func TestTheWarmStartScriptIsLinkedAndVersioned(t *testing.T) {
	s := newTestServer(t)
	if _, ok := s.assets["warm-start.js"]; !ok {
		t.Fatal("warm-start.js is not among the embedded assets")
	}
	h := s.Handler()
	page := getAuthed(t, h, "/").Body.String()
	if !strings.Contains(page, `<script src="/static/warm-start.js?`) {
		t.Error("the dashboard does not link warm-start.js with a version")
	}
}

// Every page stamps the epoch, because every page either saves, restores or clears, and all three
// need to know which client secret they are working under. A page that silently lost the attribute
// would have its stored list refused for ever, and the failure would look like the cache simply
// not working.
func TestEveryPageStampsTheEpoch(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	for _, path := range []string{"/", "/sites", "/contributors"} {
		page := getAuthed(t, h, path).Body.String()
		if !strings.Contains(page, `data-cache-epoch="`+s.cacheEpoch+`"`) {
			t.Errorf("%s carries no data-cache-epoch, so the script cannot tell a stale copy from a current one", path)
		}
	}
}

// The sign-in page clears rather than saves: reaching it means there is no session, whether Log
// out was pressed or the cookie expired, and a stored list must not outlive one.
func TestTheSignInPageTellsTheScriptToClear(t *testing.T) {
	h := newTestServer(t).Handler()
	if !strings.Contains(get(h, "/login").Body.String(), `id="signed-out"`) {
		t.Error("the sign-in page does not tell warm-start.js to clear the stored list")
	}
}
```

Add a third test that drives the wait page itself, using the existing cold-server helpers:

```go
// The wait page — the only page that restores — carries the mount point and the epoch.
func TestTheWaitPageRestoresInto(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	src := &refetchBlockSource{release: release} // no items: a genuinely cold Machine
	s, _ := newColdServerWith(t, func(o *Options) {
		o.Cache = snapshot.New(src, time.Hour, o.Clock)
	})
	h := s.Handler()
	c := signIn(t, h)

	body := getAs(h, "/", c).Body.String()
	if !strings.Contains(body, waitingMarker) {
		t.Fatal("a cold Machine did not show the wait page")
	}
	if !strings.Contains(body, `id="placeholder"`) {
		t.Error("the wait page has nowhere to restore a stored list into")
	}
	if !strings.Contains(body, `<script src="/static/warm-start.js?`) {
		t.Error("the wait page does not link the script that would restore one")
	}

	release <- struct{}{}
	getSettled(t, h, "/", c)
}
```

Add the imports `time` and the `snapshot` package path to the file's import block.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/web/ -run 'TestTheWarmStart|TestTheWaitPage' -v`

Expected: FAIL — `warm-start.js is not among the embedded assets`.

- [ ] **Step 3: Write the script**

Create `internal/web/static/warm-start.js`:

```js
// The visitor's own last list, kept in localStorage so that a cold Machine has something to show
// while it asks GitHub (design §5). It is a placeholder and never a source of truth: the page that
// arrives replaces it whole, and nothing here merges anything — the server already does that, and
// it is the only party that knows which repositories failed.
//
// Everything is wrapped in try/catch. localStorage throws rather than returning null in a Safari
// private window and wherever site data is blocked, and a placeholder that cannot be read must
// degrade to the plain wait page rather than take the page down with it.
(function () {
  "use strict";

  var KEY = "zorgscope.cache.v1";
  // Seven days. Safari's Intelligent Tracking Prevention evicts script-writable storage after
  // seven days of browser non-use regardless of what we ask for, so a longer expiry here would be
  // a promise the platform does not keep.
  var MAX_AGE_MS = 7 * 24 * 60 * 60 * 1000;

  function epoch() {
    return document.documentElement.getAttribute("data-cache-epoch") || "";
  }

  function read() {
    try {
      var raw = window.localStorage.getItem(KEY);
      if (!raw) { return null; }
      var v = JSON.parse(raw);
      // The epoch is the revocation: a stored list minted under a client secret that has since
      // been rotated must not be shown, whatever else it says (FR-8.3 AC4).
      if (!v || v.epoch !== epoch() || !v.html) { return null; }
      if (!v.savedAt || (Date.now() - v.savedAt) > MAX_AGE_MS) { return null; }
      return v;
    } catch (e) { return null; }
  }

  function clear() {
    try { window.localStorage.removeItem(KEY); } catch (e) { /* nothing to do */ }
  }

  // Save whatever the server just rendered, verbatim. Storing the server's own HTML is what keeps
  // a second renderer out of the browser: this file never builds markup, it moves a string.
  function save() {
    var items = document.getElementById("items");
    if (!items) { return; }
    // A list drawn during a fetch carries the refresh poll. Storing that would restore a
    // placeholder that immediately starts polling on a page whose poll is already running.
    if (items.querySelector(".refreshing")) { return; }
    var stamp = document.querySelector("[data-fetched-at]");
    try {
      window.localStorage.setItem(KEY, JSON.stringify({
        epoch: epoch(),
        savedAt: Date.now(),
        fetchedAt: stamp ? stamp.getAttribute("data-fetched-at") : "",
        html: items.outerHTML
      }));
    } catch (e) { /* a full or blocked store simply means no placeholder next time */ }
  }

  function restore() {
    var mount = document.getElementById("placeholder");
    if (!mount) { return; }
    var v = read();
    if (!v) { return; }
    var note = document.createElement("p");
    note.className = "placeholder-note";
    note.setAttribute("role", "status");
    // textContent, not innerHTML: fetchedAt came back out of storage and is treated as text.
    note.textContent = v.fetchedAt
      ? "Showing your last view, fetched at " + v.fetchedAt + ", while GitHub is asked."
      : "Showing your last view while GitHub is asked.";
    mount.appendChild(note);
    // v.html is the server's own rendered fragment, escaped by html/template when it was written.
    var holder = document.createElement("div");
    holder.innerHTML = v.html;
    // The restored copy must not carry the live list's id: the wait page's own poll swaps <main>,
    // and two #items on one document is invalid and makes getElementById ambiguous.
    var restored = holder.firstElementChild;
    if (restored) { restored.removeAttribute("id"); }
    mount.appendChild(holder);
  }

  // Exactly one of the three applies to any page: the sign-in page clears, the wait page
  // restores, and a page with a list saves.
  if (document.getElementById("signed-out")) {
    clear();
  } else if (document.getElementById("placeholder")) {
    restore();
  } else {
    save();
  }
})();
```

- [ ] **Step 4: Link it and add the mount point**

In `internal/web/templates/layout.html`, after the `search.js` script tag:

```html
{{/* The visitor's last list, saved here and restored on the wait page (design §5). A file of our
     own for the same reason search.js is one: the policy grants no 'unsafe-inline'. */}}
<script src="{{.Asset "warm-start.js"}}" defer></script>
```

In `internal/web/templates/waiting.html`, immediately after the `<p class="waiting-status">` line and before the polling `<div id="waiting">`:

```html
  {{/* Where warm-start.js puts the visitor's own last list, when they have one and its epoch
       still matches. Empty in the markup and empty without JavaScript: the placeholder is
       advisory, and its absence is exactly the wait page as it was before it existed. */}}
  <div id="placeholder"></div>
```

In `internal/web/templates/fragments/header.html`, add the machine-readable stamp to the line that already prints the fetched time:

```html
<p class="fetched-line" data-fetched-at="{{.FetchedAt}}">Fetched {{.FetchedAt}}</p>
```

In `internal/web/templates/login.html`, add the marker that tells the script it is looking at a signed-out page:

```html
{{/* Reaching the sign-in page means there is no session — whether Log out was pressed or the
     cookie simply expired — and a stored list has no business outliving one. warm-start.js
     clears it here rather than on a click, so a session that ends by expiring is covered too,
     and so that nothing depends on catching a form submission. */}}
<div id="signed-out" hidden></div>
```

- [ ] **Step 5: Style the note**

In `internal/web/static/app.css`:

```css
/* The banner over a restored list. It has to read as a caveat rather than as part of the list, so
   it is set apart and muted; the words carry the meaning, never the colour alone. */
.placeholder-note {
  margin: var(--space-3) 0;
  padding: var(--space-2);
  border-left: 3px solid var(--fg-muted);
  font-size: 0.875rem;
  color: var(--fg-muted);
}
```

Substitute the file's own custom-property names if these differ, as in task 2 step 6.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/web/ -v`

Expected: PASS, including the three new tests and every budget test. `TestWaitPageStaysInsideItsBudget` covers the wait page's 100 kB static allowance; `warm-start.js` is a couple of kilobytes and htmx plus the stylesheet leave ample room. `TestStaticAssetsFitTheirBudgetOnTheWire` covers the dashboard's 50 kB — check its log line for the new total, and if it is close to the limit, say so rather than raising the budget.

- [ ] **Step 7: Run the full check**

Run: `make check`

Expected: clean.

- [ ] **Step 8: Commit**

```bash
git add internal/web/static/warm-start.js internal/web/warm_start_test.go \
        internal/web/templates/layout.html internal/web/templates/waiting.html \
        internal/web/templates/fragments/header.html internal/web/static/app.css
git commit -F - <<'EOF'
feat(web): the wait page shows the visitor's own last list while GitHub is asked (FR-1.9)

A cold Machine has nothing to serve, so layer 1 cannot help it and the wait page still appears.
Behind it, the list the visitor last saw is restored from localStorage: the server's own rendered
fragment, stored verbatim, labelled with the time it was fetched, replaced whole by the page that
arrives. Nothing is merged and nothing is rendered in the browser.

A stored copy is refused when its epoch disagrees with the document's, or when it is older than
seven days — which is as long as Safari keeps script-writable storage anyway. Log out clears it.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

### Task 5: Requirements, the ADR and the version

**Files:**

- Modify: `docs/requirements/04-functional-requirements.md` (the FR‑1.9 row)
- Create: `docs/decisions/0013-stale-while-revalidating.md`
- Modify: `docs/decisions/README.md` (the index)
- Modify: `internal/version/version.go:20`

**Interfaces:** none — documentation and the version constant.

- [ ] **Step 1: Rewrite FR‑1.9's acceptance criteria**

In `docs/requirements/04-functional-requirements.md`, the FR‑1.9 row: replace AC1, replace AC2, and append AC6 and AC7. Copy the non-breaking hyphens from the neighbouring ids. The row is one long table cell; edit it in place.

AC1 becomes:

```text
AC1 While a fetch is running **and the snapshot holds no items**, `GET /`, `GET /sites`, `GET /contributors` and `GET /search` show the wait page — the mark, animated, with a status line naming how many repositories are being asked. While a fetch is running and the snapshot holds items, each shows its ordinary page, drawn from that snapshot.
```

AC2 becomes:

```text
AC2 The page arrives without any action. From the wait page: with JavaScript by polling every 500 ms and swapping the page in once the fetch has ended, without it by reloading every two seconds. From a stale page: the list polls `GET /items` every 500 ms from an element inside the fragment, which the arriving fragment removes.
```

AC6 (new):

```text
AC6 A stale list states the time its items were fetched at and that a fetch is running; the status line is announced politely and colour is never the only signal.
```

AC7 (new):

```text
AC7 On the wait page a stored copy of the visitor's last list may be shown as a labelled placeholder. It is replaced by the arriving page; it is refused when its epoch does not match the document's or when it is older than seven days; and its absence leaves the wait page exactly as it is without it.
```

Leave AC3, AC4 and AC5 as they are — all three still hold word for word.

- [ ] **Step 2: Write the ADR**

Create `docs/decisions/0013-stale-while-revalidating.md`, following the shape of `0012-no-seen-mark.md` exactly: `# 0013. Stale while revalidating: the page shows what it has, and says it is refreshing`, then `* Status: accepted`, `* Date: 2026-09-20`, `* Requirements: FR‑1.9, FR‑8.3, QS‑2.3, QS‑2.6`, then **Context and problem statement**, **Considered options**, **Decision outcome**, **Consequences**, **Pros and cons of the options**.

The three options to record: **A** keep the wait page and add only browser storage; **B** the server serves stale data and browser storage covers the cold Machine (chosen); **C** persist the snapshot server-side on a Fly Volume — rejected as reopening [ADR‑0010](0010-stateless-no-database.md), whose whole subject was that persistence is what broke the first deploy.

Consequences to record, both directions:

- Good: the common wait — a warm Machine past its five-minute TTL — disappears for every visitor, JavaScript or not, without storing anything anywhere.
- Good: ADR‑0002, ‑0003, ‑0010 and ‑0011 all stand. No database, no volume, no ticker, no second renderer.
- Bad: pressing Refresh now shows the list being replaced rather than a wait page, so the visitor sees their old list for a moment after explicitly asking for a new one (design §4.3).
- Bad: a list in `localStorage` is readable by anything with script access to the origin, which the epoch bounds in time but does not prevent. The CSP's `script-src 'self'` is what keeps that set empty.
- Neutral: on iOS the placeholder is gone after seven days of not opening the browser, and the visitor simply sees the wait page as before.

Add the row to `docs/decisions/README.md`'s index in the same format as its neighbours.

- [ ] **Step 3: Bump the version**

In `internal/version/version.go:20`: `const Version = "0.5.0"`.

- [ ] **Step 4: Run the full check**

Run: `make check`

Expected: clean, markdownlint included. If markdownlint objects to the ADR, fix the ADR rather than the configuration — `MD040` (fenced blocks need a language) and `MD032` (lists need blank lines around them) are the two that bite in this repo.

- [ ] **Step 5: Commit**

```bash
git add docs/requirements/04-functional-requirements.md \
        docs/decisions/0013-stale-while-revalidating.md \
        docs/decisions/README.md internal/version/version.go
git commit -F - <<'EOF'
docs: FR-1.9 reworded, ADR-0013, version 0.5.0 (FR-1.9, FR-8.3)

FR-1.9 AC1 said the wait page appears while a fetch runs and shows "not a list". It now turns on
having nothing to show, and AC2 gains the stale page's own route to arriving. AC6 and AC7 state
what a stale list and a restored placeholder owe the visitor.

ADR-0013 records why the server serves what it has, why the browser copy is a placeholder rather
than a cache of record, and why persisting the snapshot server-side was not the answer.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

## Verification before calling this done

- [ ] `make check` is clean from a cold start (`make clean` first).
- [ ] `make backend` and `make client`: the dashboard loads, and pressing Refresh keeps the list on screen with the refreshing line, which disappears when the fetch lands.
- [ ] With DevTools open on the dashboard, `localStorage.getItem("zorgscope.cache.v1")` returns an object whose `epoch` matches the `data-cache-epoch` attribute on `<html>`.
- [ ] Restart the backend so the snapshot is empty, then reload: the wait page appears with the previous list beneath its note, and the real page replaces it.
- [ ] Hand-edit the stored `epoch` to `deadbeef` and reload a cold backend: no placeholder, plain wait page.
- [ ] Press Log out: the key is gone.
- [ ] A private window on a cold backend: plain wait page, no console error.
