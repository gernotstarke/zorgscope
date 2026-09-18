# Search, Contributors and the Top Bar Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the view switch and the actions into the top bar, retire NEW and the seen mark, name every tile through configuration, add a ranked server-side search with a Cmd‑K box and its own results page, and add a Contributors page.

**Architecture:** Two new pure functions in `internal/domain` — `Search` and `BuildContributors` — over the snapshot the cache already holds; two new session routes in `internal/web` rendering two new page templates; the top bar's chrome drawn by `layout.html` from one new `pageData` field; one vendored script of our own for the shortcut. NEW is removed from the domain first, then from the web layer, so every later task builds on the smaller code. Configuration and docs are their own tasks.

**Tech Stack:** Go 1.26 module (host Go 1.27), `html/template`, htmx 2.0.4 (vendored), hand-written CSS with `light-dark()` and `color-mix()`, a 20-line plain script. Gate: `make check`, or its native equivalent while Docker is down.

**Spec:** `docs/superpowers/specs/2026-09-18-search-and-contributors-design.md`

## Global Constraints

- No inline `<script>`, `<style>` or `style=` attribute; `contentSecurityPolicy` unchanged; the only script files are `htmx.min.js` and the new `search.js`, both same-origin (QS‑4.4).
- `TestGraphQLRequestBudget` keeps counting exactly 20 requests for 10 repositories (QS‑3.5). The shipped configuration now has exactly ten repositories.
- Reserved search words, exactly: `issue`, `issues` → `KindIssue`; `pr`, `prs`, `pull` → `KindPR`. Scores, exactly: title at a word start 4, title elsewhere 2 (only when no word-start match), contributor 3, label 3 (once per word), repository 1, summary 1. Every word must match; the kind filters and scores nothing. Order: score desc, `UpdatedAt` desc, `Repo` asc, `Number` asc.
- Contributors order: open items desc, `LastActive` desc, login case-insensitively asc; the empty login last.
- The session cookie payload is the expiry alone; a payload containing `:` is refused.
- Every string a template prints is computed in Go; nothing is ever `template.HTML`. `<mark>` is template markup around escaped runs.
- QS‑2.3 budgets hold: ≤ 150 kB HTML per page with the representative fixture; the static budget test includes `search.js`.
- No new configuration key, no new Make target, no database, no ticker.
- Every task ends with `go build ./... && go vet ./... && go test -race -timeout 120s ./...` green on the host and one commit (Task 2 may make two). Task 9 also ends with the full gate. If Docker is up, the gate is `make check`; if not, run natively: `go test -race -timeout 120s -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1`; `go test -coverprofile=domain.out ./internal/domain/... && go tool cover -func=domain.out | tail -1` (≥ 90 %); `GOFLAGS=-mod=mod go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.0 run ./...` (0 issues); `npx --yes markdownlint-cli2@0.23.2 "docs/**/*.md" "README.md"` (0 issues); `fly config validate --strict --app zorgscope --config deploy/fly.toml`. Never run a `docker` command when Docker is down (it hangs).
- Stage files explicitly. Never `git add -A`. Never commit `.agent/`, `.agents/`, `.claude/`, `_bmad/`, `.env`, `coverage.out` or `domain.out`.
- Commit messages name the requirement ids they touch and end with a blank line and `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`.
- Comments use British spelling (golangci `misspell` locale UK); `revive` runs: exported identifiers carry doc comments starting with their name; `gofmt` is a golangci formatter.
- Requirement ids in Go comments use a plain hyphen (`FR-12.1`); in `docs/` they use U+2011 (`FR‑12.1`), as every existing id there does.
- Branch: `feat/search-and-contributors` off `main` (spec at `6fd24b7`).

---

### Task 1: NEW leaves the domain

**Files:**

- Modify: `internal/domain/item.go`, `internal/domain/dashboard.go`, `internal/domain/site.go`
- Test: `internal/domain/item_test.go`, `internal/domain/dashboard_test.go`, `internal/domain/site_test.go`

**Interfaces:**

- Produces: `SortItems(items []Item)`; `RepoGroup{Repo, Items, Total}`; `DashboardInput{Now, Items, Repos, Filter}`; `Dashboard{GeneratedAt, Total, Shown, Filter, Groups}`; `SiteTilesInput{Items, Sites, MaxPRs, MaxIssues}`; `SiteTile` without `NewCount`. `IsNew` and `CountNew` no longer exist.

- [ ] **Step 1: Rewrite the tests first**

In `item_test.go` delete `TestIsNew` and `TestCountNew`; replace `TestSortItemsPutsNewFirstThenNewestUpdate` with:

```go
func TestSortItemsPutsTheNewestUpdateFirst(t *testing.T) {
	items := []Item{
		{Number: 1, UpdatedAt: testNow.Add(-3 * time.Hour)},
		{Number: 2, UpdatedAt: testNow.Add(-time.Hour)},
		{Number: 3, UpdatedAt: testNow.Add(-2 * time.Hour)},
	}
	SortItems(items)
	if got := []int{items[0].Number, items[1].Number, items[2].Number}; !reflect.DeepEqual(got, []int{2, 3, 1}) {
		t.Errorf("order = %v, want [2 3 1] (most recently updated first)", got)
	}
}
```

Keep `TestSortItemsIsStableForEqualKeys`, dropping its second argument. In `dashboard_test.go` rename `TestBuildDashboardAppliesTheFilterButCountsNewUnfiltered` to `TestBuildDashboardAppliesTheFilterButCountsTotalsUnfiltered`, delete every `LastVisitAt`, `NewTotal` and `NewCount` assertion, keep the `Total` and `Shown` ones; delete `TestBuildDashboardEchoesLastVisitAt`. In `site_test.go` remove `LastVisitAt` from every input and every `NewCount` assertion; where a test relied on "new first", assert "most recently updated first" instead. Use whatever `testNow` helper the files already have.

- [ ] **Step 2: Run the tests, expect compile failures naming `IsNew`, `CountNew`, `LastVisitAt`, `NewCount`**

Run: `go test ./internal/domain/...`

- [ ] **Step 3: Remove NEW from the domain**

`item.go`: delete `IsNew` and `CountNew`; `SortItems` becomes:

```go
// SortItems orders items most recently updated first. The sort is stable, so items with equal
// update times keep their original relative order.
func SortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
}
```

`dashboard.go`: `RepoGroup` keeps `Repo`, `Items`, `Total` (comment: "Total is taken over every item this repository holds, before the filter is applied"); `DashboardInput` loses `LastVisitAt`; `Dashboard` loses `LastVisitAt` and `NewTotal`; `BuildDashboard` and `groupByRepo` lose the `lastVisit` parameter and the `NewCount` increment; `SortItems(g.Items)`. Reword the comments that cite FR-1.2 to cite FR-2.1 AC3 ("the filter never changes a repository's total").

`site.go`: `SiteTilesInput` loses `LastVisitAt`; `SiteTile` loses `NewCount` and its comment reads "PRs and Issues are sorted most recently updated first, and cut to MaxPRs and MaxIssues"; `SortItems(prs)`, `SortItems(issues)`; delete the `NewCount` line.

- [ ] **Step 4: Run the domain tests with coverage, expect PASS and ≥ 90 %**

Run: `go test -race -coverprofile=domain.out ./internal/domain/... && go tool cover -func=domain.out | tail -1`

`go build ./...` will fail in `internal/web` — that is Task 2's job; `go vet ./internal/domain/...` must pass.

- [ ] **Step 5: Commit**

```bash
git add internal/domain/item.go internal/domain/dashboard.go internal/domain/site.go internal/domain/item_test.go internal/domain/dashboard_test.go internal/domain/site_test.go
git commit -m "refactor(domain): NEW and the seen mark leave the domain; lists sort by last update (FR-1.2 retired)"
```

---

### Task 2: NEW leaves the web layer

**Files:**

- Modify: `internal/web/auth.go`, `internal/web/dashboard.go`, `internal/web/sites.go`, `internal/web/server.go`, `internal/web/signin.go`, `internal/web/templates/layout.html`, `internal/web/templates/fragments/header.html`, `internal/web/templates/fragments/items.html`, `internal/web/templates/sites.html`, `internal/web/static/app.css`
- Test: `internal/web/auth_test.go`, `internal/web/signin_test.go`, `internal/web/dashboard_test.go`, `internal/web/sites_test.go` and any helper file defining `mintSessionSeenAt`

**Interfaces:**

- Consumes: Task 1's signatures.
- Produces: `session{Expiry}`; `headerView{FetchedAt, Error, Return}`; `func (s *Server) headerView(snap snapshot.Snapshot, r *http.Request) headerView`; `newItemView(it domain.Item, now time.Time) itemView`; no `POST /seen`; `pageData` without `NewCount`.

- [ ] **Step 1: Rewrite the tests**

`auth_test.go`: `TestSessionCodecRoundTripsExpiryAndSeen` → `TestSessionCodecRoundTripsTheExpiry` (mint `session{Expiry: …}`, assert only `Expiry`); `TestACookieWithSeenAlteredByOneCharacterFails` → `TestACookieWithTheExpiryAlteredByOneCharacterFails` (flip the last digit of the whole payload, keep the signature, expect `decode` false). Add:

```go
// A payload minted before 2026-09-18 carried "expiry:seen". It proves nothing this codec signs
// now, and is refused like the pre-reset format was: the visitor signs in once more.
func TestTheOldTwoIntegerPayloadIsRefused(t *testing.T) {
	codec := newSessionCodec(testClientSecret)
	payload := strconv.FormatInt(testNow.Add(sessionTTL).Unix(), 10) + ":0"
	value := sessionEncoding.EncodeToString([]byte(payload)) + "." + sessionEncoding.EncodeToString(codec.sign(payload))
	if _, ok := codec.decode(value, testNow); ok {
		t.Error("a two-integer payload decoded; the seen mark is gone and its format with it")
	}
}
```

`signin_test.go`: rename `TestTheSessionCookieCarriesOnlyAnExpirySeenMarkAndASignature` to `TestTheSessionCookieCarriesOnlyAnExpiryAndASignature`; after decoding the payload assert `!strings.Contains(string(payload), ":")` and that the whole payload parses as an integer.

`dashboard_test.go`: delete `TestNewMarkerAppearsOnlyForItemsCreatedAfterSeen`, `TestTabTitleCarriesTheNewCount`, `TestMarkSeenClearsTheNewBadgeOnTheNextGet`, `TestMarkSeenUsesTheFetchedAtOfTheSnapshotShownNotTheClick`, `TestTheMarkSeenFormCarriesTheSnapshotsFetchedAt`, `TestTheMarkSeenFormOmitsSeenAtBeforeTheFirstFetch`, `TestSeenAtFallsBackToNowWhenMissingInvalidOrFuture`, `TestMarkSeenNeverMovesTheSeenMarkBackwards`. Rename `TestTabTitleHasNoPrefixWhenNothingIsNew` to `TestTabTitleIsTheSiteName` asserting `<title>zorgscope</title>`. Rewrite `TestAPartialFetchKeepsTheFailingRepositoryAndTheSeenAtOfTheGoodFetch` as `TestAPartialFetchKeepsTheFailingRepositoryAndTheFetchedAtOfTheGoodFetch`: keep every assertion about items and the Fetched line, drop the seen ones (FR-1.4 AC4 still holds). Rename `TestTheNewBadgeIgnoresTheFilter` to `TestTheRepositoryTotalIgnoresTheFilter`, asserting the group's count line reads "1 of 3" (or whatever the fixture gives) under a filter. Rename `TestSeenAndRefreshReturnToThePageTheyWerePressedOn` to `TestRefreshReturnsToThePageItWasPressedOn`, dropping the `/seen` half. Add:

```go
// FR-1.2 retired 2026-09-18: nothing on the page is marked new, and the route that moved the
// seen mark is gone.
func TestNothingIsMarkedNewAndSeenIsNoRoute(t *testing.T) {
	h := dashHandlerWith(t, threeItems()) // whatever helper gives a list with items
	body := getAuthed(t, h, "/").Body.String()
	for _, gone := range []string{"badge-new", "is-new", "NEW", "Mark all seen", "seen_at"} {
		if strings.Contains(body, gone) {
			t.Errorf("the page still carries %q", gone)
		}
	}
	if rec := postAuthed(t, h, "/seen", nil); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /seen = %d, want 404 or 405", rec.Code)
	}
}
```

Use the helpers the file already has (`getAuthed`, a post helper, item fixtures); adapt names. Replace every `mintSessionSeenAt(x)` with a `mintSession()` helper minting `session{Expiry: testNow.Add(sessionTTL)}`. `sites_test.go`: replace `TestSitesPageCountsNewLikeTheList` with `TestSitesPageOrdersTileRowsByLastUpdate` (two items in one repository, the later-updated one listed first).

- [ ] **Step 2: Run `go vet ./internal/web/` and expect failures naming the removed identifiers**

- [ ] **Step 3: Remove NEW from the web layer**

`auth.go`:

```go
// session is what the cookie proves: that the visitor signed in, and until when. It carries no
// identity and, since 2026-09-18, no seen mark (ADR-0012).
type session struct {
	Expiry time.Time
}
```

`mint` writes `strconv.FormatInt(s.Expiry.Unix(), 10)` as the whole payload. `decode`: after the signature check, `if strings.Contains(string(payload), ":") { return session{}, false }` with the comment "A payload of the 2026-09-15 to 2026-09-18 format, expiry:seen, or of the pre-reset format, proves nothing this codec signs now"; parse the whole payload as the expiry. Update the type comment above `sessionCodec` ("base64(expiry) + "." + base64(HMAC…)").

`signin.go`: the comment at the `setSession` call reads "The cookie carries the expiry and nothing else (ADR-0012)."

`server.go`: delete the `/seen` route line; delete `pageData.NewCount` and its comment. `dashboard.go`: delete `handleSeen`, `seenAt` and their comments; `render` builds `DashboardInput{Now, Items, Repos, Filter}` and passes `pageData{Dashboard: &view}`; its comment loses "It is not a visit…"; `headerView` loses `FetchedAtUnix` and `NewTotal` (and `headerView()` its `newTotal` parameter); `groupView` loses `NewCount`; `itemView` loses `New`; `newItemView(it, now)`; `itemsView`'s comment reads "sorts each group most recently updated first". `sites.go`: `tileView` loses `NewCount`, `tileItemView` loses `New`; `handleSites` builds `SiteTilesInput{Items, Sites, MaxPRs, MaxIssues}`, calls `s.headerView(snap, r)`, passes `pageData{Title: "Sites", Sites: &view}`; `newTileView(n, tile, now)`, `tileItems(items, now)`.

Templates: `layout.html` title becomes `<title>{{with .Title}}{{.}} · {{end}}zorgscope</title>` and its FR-1.2 comment goes; `header.html` loses the NEW badge span and the `/seen` form and the `seen_at` comment (the Refresh and Log out forms stay for now — Task 3 moves them); `items.html` loses the `new-count` span, the `is-new` class and the NEW badge; `sites.html` loses `tile-new` and the badge in `tileitem`, and `is-new`.

`app.css`: delete `.new-count`, `.is-new .item-title`, `.badge-new`, `.tile-new`; delete `.badge` too if `grep -rn 'class="badge' internal/web/templates` finds nothing; update the comments that mention NEW or "the one thing this dashboard exists to say".

- [ ] **Step 4: Build, vet, test**

Run: `go build ./... && go vet ./... && go test -race -timeout 120s ./...` — PASS. Then `grep -rn -i "seen\|IsNew\|NewCount\|NewTotal" internal/ --include=*.go --include=*.html --include=*.css` and remove every remaining reference (comments included) except the rate limiter's "least recently seen" and unrelated English.

- [ ] **Step 5: Commit**

```bash
git add internal/web/auth.go internal/web/dashboard.go internal/web/sites.go internal/web/server.go internal/web/signin.go internal/web/templates/layout.html internal/web/templates/fragments/header.html internal/web/templates/fragments/items.html internal/web/templates/sites.html internal/web/static/app.css internal/web/auth_test.go internal/web/signin_test.go internal/web/dashboard_test.go internal/web/sites_test.go
git commit -m "refactor(web): NEW, the seen mark and POST /seen removed; the cookie carries only its expiry (FR-1.2 retired, QS-4.1)"
```

(Add any helper file you changed to the `git add`.)

---

### Task 3: The top bar carries the chrome (FR‑1.11)

**Files:**

- Modify: `internal/web/server.go` (`pageData`), `internal/web/dashboard.go`, `internal/web/sites.go`, `internal/web/templates/layout.html`, `internal/web/templates/fragments/header.html`, `internal/web/templates/dashboard.html`, `internal/web/templates/sites.html`, `internal/web/static/app.css`
- Delete: `internal/web/templates/fragments/viewswitch.html`
- Test: `internal/web/chrome_test.go`, `internal/web/sites_test.go`, `internal/web/dashboard_test.go`

**Interfaces:**

- Consumes: Task 2's `headerView`.
- Produces: `pageData.Chrome *chromeView`; `type chromeView struct{ View, Query, Return string }`; `func chromeFor(r *http.Request) *chromeView`; `headerView{FetchedAt, Error}` (Return gone). Tasks 6 and 8 set `Chrome: chromeFor(r)` and rely on `chromeFor` reading `q` only on `/search`.

- [ ] **Step 1: Write the failing tests**

In `chrome_test.go` (read it first; it already tests the top bar):

```go
// FR-1.11: the view switch, the search box, Refresh and Log out live in the top bar of every
// signed-in page, above the rainbow band, and nowhere on the sign-in page.
func TestTopBarCarriesTheChromeOnlyWhenSignedIn(t *testing.T) {
	h := dashHandlerWith(t, nil)
	body := getAuthed(t, h, "/").Body.String()
	header := body[strings.Index(body, "<header"):strings.Index(body, "</header>")]
	for _, want := range []string{`class="view-switch"`, `href="/contributors"`, `role="search"`, `action="/refresh"`, `action="/logout"`, `data-search`} {
		if !strings.Contains(header, want) {
			t.Errorf("the top bar lacks %s", want)
		}
	}
	if strings.Index(body, "</header>") > strings.Index(body, `class="rainbow"`) {
		t.Error("the rainbow band is not below the top bar")
	}
	main := body[strings.Index(body, "<main"):]
	for _, gone := range []string{`class="view-switch"`, `action="/refresh"`, `action="/logout"`} {
		if strings.Contains(main, gone) {
			t.Errorf("the page body still carries %s", gone)
		}
	}
	login := get(h, "/login").Body.String() // unauthenticated
	for _, gone := range []string{`class="view-switch"`, `role="search"`, `action="/refresh"`, `action="/logout"`} {
		if strings.Contains(login, gone) {
			t.Errorf("the sign-in page carries %s", gone)
		}
	}
}

func TestTheSearchBoxEchoesTheQueryOnlyOnTheResultsPage(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?q=header&kind=pr", nil)
	if c := chromeFor(r); c.View != "list" || c.Query != "" || c.Return != "/?q=header&kind=pr" {
		t.Errorf("chromeFor(list) = %+v", *c)
	}
	r = httptest.NewRequest(http.MethodGet, "/search?q=+bug+", nil)
	if c := chromeFor(r); c.View != "search" || c.Query != "bug" {
		t.Errorf("chromeFor(search) = %+v", *c)
	}
	r = httptest.NewRequest(http.MethodGet, "/sites", nil)
	if c := chromeFor(r); c.View != "sites" || c.Return != "/sites" {
		t.Errorf("chromeFor(sites) = %+v", *c)
	}
}
```

Adapt `TestViewSwitchMarksTheCurrentView` in `sites_test.go` to look inside `<header>` and to expect three links. Use the existing request helpers; if there is no unauthenticated `get`, use `httptest.NewRecorder` with `h.ServeHTTP`.

- [ ] **Step 2: Run them, expect failures (`chromeFor` undefined)**

- [ ] **Step 3: Implement**

`server.go`, in `pageData`:

```go
	// Chrome is the signed-in top bar: which view is current, what the search box shows and
	// where Refresh comes back to (FR-1.11). nil on the sign-in and refusal pages, which have no
	// session to act for, so layout.html draws only the brand and the appearance control there.
	Chrome *chromeView
```

`dashboard.go`:

```go
// chromeView is what layout.html draws between the brand and the appearance control.
type chromeView struct {
	// View is "list", "sites", "contributors" or "search"; the switch marks the first three.
	View string
	// Query is the search box's text: the query on the results page, empty everywhere else —
	// the list's own filter also uses q, and its text is not a search.
	Query string
	// Return is the page the Refresh form comes back to, path and query (FR-1.8 AC4). It is what
	// the browser asked for, so handleRefresh only ever uses it through safeReturn.
	Return string
}

// views maps a page's path to the name the switch marks.
var views = map[string]string{"/": "list", "/sites": "sites", "/contributors": "contributors", "/search": "search"}

// chromeFor is the top bar for the signed-in page answering r.
func chromeFor(r *http.Request) *chromeView {
	c := &chromeView{View: views[r.URL.Path], Return: r.URL.RequestURI()}
	if r.URL.Path == "/search" {
		c.Query = strings.TrimSpace(r.URL.Query().Get("q"))
	}
	return c
}
```

`headerView` loses `Return`; `render` and `handleSites` pass `Chrome: chromeFor(r)`; `answeredWaiting` passes `Chrome: chromeFor(r)` alongside `Waiting`. `layout.html`: insert the chrome markup from spec §3 between `</a>` of the brand and `<nav class="topbar-nav">`, and add a comment: "The chrome (FR-1.11). The search form carries htmx so a keystroke anywhere fetches the results page and swaps main; with script off Enter submits the same GET. Refresh and Log out are plain posts, as before." `header.html` keeps only the Fetched line and the error notice. Delete `viewswitch.html` and the two `{{template "viewswitch" …}}` lines. Delete `.dash-actions` rules.

`app.css`: `.topbar` gains `flex-wrap: wrap;` and `row-gap: 0.5rem`; change `.topbar nav a` to `.topbar-nav a` (both rules) so the switch keeps its own colours; `.view-switch { margin: 0 }`; add:

```css
/* ---------------------------------------------------------------- the top bar's chrome (FR-1.11)

   The search box takes the free width between the switch and the actions, and gives it up first
   when the bar wraps on a phone. */
.search-form { flex: 1 1 12rem; min-width: 8rem; margin: 0; display: flex; }
.search-form input[type="search"] {
  width: 100%;
  margin: 0;
  padding: 0.3rem 0.6rem;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--bg);
  color: var(--text);
  font: inherit;
  font-size: 0.9rem;
}
.search-form input[type="search"]:focus { outline: 2px solid var(--accent); outline-offset: 1px; }
.topbar-actions { display: inline-flex; align-items: center; gap: 0.4rem; }
.topbar-actions form { margin: 0; }
.topbar-actions button { margin: 0; padding: 0.3rem 0.7rem; font-size: 0.9rem; }
```

Keep `.action button`.

- [ ] **Step 4: Build, vet, test — PASS. Check the wait page still renders (`waiting_test.go`).**

- [ ] **Step 5: Commit**

```bash
git add internal/web/server.go internal/web/dashboard.go internal/web/sites.go internal/web/templates/layout.html internal/web/templates/fragments/header.html internal/web/templates/dashboard.html internal/web/templates/sites.html internal/web/static/app.css internal/web/chrome_test.go internal/web/sites_test.go internal/web/dashboard_test.go
git rm -q internal/web/templates/fragments/viewswitch.html
git commit -m "feat(web): the view switch, search box, Refresh and Log out move into the top bar (FR-1.11, FR-1.8)"
```

---

### Task 4: Ten sites, and version 0.4.0

**Files:**

- Modify: `config/zorgscope.yaml`, `docs/concepts/configuration.md`, `internal/version/version.go`

- [ ] **Step 1: Edit the YAML**

Replace the `repos` list with, in this order: `arc42/arc42-template`, `arc42/arc42.org-site`, `arc42/arc42.de-site`, `arc42/quality.arc42.org-site`, `arc42/docs.arc42.org-site`, `arc42/faq.arc42.org-site`, `arc42/examples.arc42.org-site`, `arc42/trainings.arc42.org-site`, `arc42/arc42-generator`, `gernotstarke/zorgscope`. Change the comment "It holds nine." to "It holds ten: the budget exactly." Reorder `sites` to the same order, keeping the seven existing entries verbatim and inserting:

```yaml
    - name: arc42-template
      url: https://github.com/arc42/arc42-template
      repo: arc42/arc42-template
      hue: slate
```

first, and before the end:

```yaml
    - name: arc42-generator
      url: https://github.com/arc42/arc42-generator
      repo: arc42/arc42-generator
      hue: slate
    - name: zorgscope
      url: https://github.com/gernotstarke/zorgscope
      repo: gernotstarke/zorgscope
      hue: slate
```

Update the comment above `sites`: "Each site names exactly one of the repositories above; a repository no site names would share an Other tile, which the shipped list leaves empty. The three without a brand hue take slate." `docs/concepts/configuration.md`: update the example list and the sentence about Other so it says the shipped configuration claims every repository.

- [ ] **Step 2: Version**

`internal/version/version.go`: `Version = "0.4.0"`.

- [ ] **Step 3: Verify the file loads**

Run: `go test -race ./internal/config/... ./cmd/... && go run ./cmd/zorgscope --help 2>/dev/null; CONFIG_PATH=config/zorgscope.yaml go test -race -run 'Config|Load|Example' ./...` — whatever the config tests are named, they must pass; additionally: `npx --yes markdownlint-cli2@0.23.2 "docs/**/*.md" "README.md"`.

- [ ] **Step 4: Commit**

```bash
git add config/zorgscope.yaml docs/concepts/configuration.md internal/version/version.go
git commit -m "config: ten sites, arc42-template first and zorgscope last, arc42-generator watched; v0.4.0 (FR-1.8, QS-3.5)"
```

---

### Task 5: The search in the domain (FR‑12.1)

**Files:**

- Create: `internal/domain/search.go`, `internal/domain/search_test.go`

**Interfaces:**

- Produces: `ParseQuery(q string) Query`; `Search(items []Item, q Query) []Hit`; `Query{Words []string; Kind Kind}`; `Hit{Item Item; Score int; Matched []string; TitleSpans [][2]int}`.

- [ ] **Step 1: Write the failing tests**

```go
package domain

import (
	"reflect"
	"testing"
	"time"
)

func TestParseQuery(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Query
	}{
		{"", Query{}},
		{"  Header   Bug ", Query{Words: []string{"header", "bug"}}},
		{"issue gernot", Query{Words: []string{"gernot"}, Kind: KindIssue}},
		{"PRs", Query{Kind: KindPR}},
		{"pull issues", Query{Kind: KindIssue}},
	} {
		if got := ParseQuery(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseQuery(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func searchItem(n int, title, author, repo, summary string, labels ...string) Item {
	return Item{Kind: KindIssue, Number: n, Title: title, Author: author, Repo: repo, Summary: summary, Labels: labels, UpdatedAt: time.Date(2026, 9, 1, 0, n, 0, 0, time.UTC)}
}

func TestSearchScoresFields(t *testing.T) {
	for _, tc := range []struct {
		name    string
		item    Item
		q       string
		score   int
		matched []string
	}{
		{"title word start", searchItem(1, "Fix the header", "", "o/r", ""), "header", 4, []string{"title"}},
		{"title elsewhere", searchItem(1, "Subheader", "", "o/r", ""), "header", 2, []string{"title"}},
		{"contributor", searchItem(1, "x", "gernot", "o/r", ""), "gern", 3, []string{"contributor"}},
		{"label once", searchItem(1, "x", "", "o/r", "", "bug", "bugfix"), "bug", 3, []string{"label"}},
		{"repository", searchItem(1, "x", "", "arc42/faq.arc42.org-site", ""), "faq", 1, []string{"repository"}},
		{"summary", searchItem(1, "x", "", "o/r", "about the header"), "header", 1, []string{"summary"}},
		{"sums across fields", searchItem(1, "Bug in header", "bugsy", "o/r", "a bug", "bug"), "bug", 4 + 3 + 3 + 1, []string{"title", "contributor", "label", "summary"}},
		{"sums across words", searchItem(1, "Fix the header", "gernot", "o/r", ""), "header gernot", 7, []string{"title", "contributor"}},
	} {
		hits := Search([]Item{tc.item}, ParseQuery(tc.q))
		if len(hits) != 1 {
			t.Fatalf("%s: %d hits, want 1", tc.name, len(hits))
		}
		if hits[0].Score != tc.score || !reflect.DeepEqual(hits[0].Matched, tc.matched) {
			t.Errorf("%s: score %d matched %v, want %d %v", tc.name, hits[0].Score, hits[0].Matched, tc.score, tc.matched)
		}
	}
}

func TestSearchRequiresEveryWord(t *testing.T) {
	items := []Item{searchItem(1, "Fix the header", "gernot", "o/r", "")}
	if hits := Search(items, ParseQuery("header nobody")); len(hits) != 0 {
		t.Errorf("an item missing one word was found: %+v", hits)
	}
	if hits := Search(items, ParseQuery("")); hits != nil {
		t.Errorf("an empty query found %d hits, want nil", len(hits))
	}
}

func TestSearchKindFiltersAndScoresNothing(t *testing.T) {
	pr := searchItem(1, "Header", "", "o/r", "")
	pr.Kind = KindPR
	items := []Item{pr, searchItem(2, "Header", "", "o/r", "")}
	hits := Search(items, ParseQuery("pr header"))
	if len(hits) != 1 || hits[0].Item.Number != 1 || hits[0].Score != 4 {
		t.Errorf("pr header = %+v", hits)
	}
	if hits := Search(items, ParseQuery("issues")); len(hits) != 1 || hits[0].Item.Number != 2 || hits[0].Score != 0 || hits[0].Matched != nil {
		t.Errorf("issues alone = %+v", hits)
	}
}

func TestSearchOrdersByScoreThenUpdateThenRepoThenNumber(t *testing.T) {
	a := searchItem(1, "header", "", "b/r", "") // score 4, updated 00:01
	b := searchItem(2, "subheader", "", "a/r", "") // score 2, updated 00:02
	c := searchItem(3, "header", "", "a/r", "") // score 4, updated 00:03
	d := searchItem(4, "header", "", "a/r", "")
	d.UpdatedAt = c.UpdatedAt // tie with c: repo a/r < b/r? both a/r → number
	hits := Search([]Item{a, b, c, d}, ParseQuery("header"))
	var got []int
	for _, h := range hits {
		got = append(got, h.Item.Number)
	}
	if want := []int{3, 4, 1, 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestSearchTitleSpansMergeAndMapBack(t *testing.T) {
	hits := Search([]Item{searchItem(1, "Header headers ahead", "", "o/r", "")}, ParseQuery("head header"))
	if want := [][2]int{{0, 6}, {7, 13}, {15, 19}}; !reflect.DeepEqual(hits[0].TitleSpans, want) {
		t.Errorf("spans = %v, want %v", hits[0].TitleSpans, want)
	}
	// "İ" lower-cases to three bytes from two, so byte offsets cannot be mapped back.
	hits = Search([]Item{searchItem(1, "İstanbul header", "", "o/r", "")}, ParseQuery("header"))
	if len(hits) != 1 || hits[0].TitleSpans != nil {
		t.Errorf("a title whose lower-casing changes length: hits %+v", hits)
	}
}

func TestSearchNeverMutates(t *testing.T) {
	items := []Item{searchItem(1, "b", "", "o/r", ""), searchItem(2, "a", "", "o/r", "")}
	before := append([]Item(nil), items...)
	Search(items, ParseQuery("a b"))
	if !reflect.DeepEqual(items, before) {
		t.Error("Search reordered or changed the caller's items")
	}
}
```

Check the span expectation by hand before trusting it: "Header headers ahead" lower-cased is "header headers ahead"; "head" matches at 0, 7, 16; "header" at 0, 7; merged: [0,6], [7,13], [16,20]. Correct the test to `{{0, 6}, {7, 13}, {16, 20}}` — the value above was a deliberate trap; compute, do not copy.

- [ ] **Step 2: Run, expect compile failure**

- [ ] **Step 3: Implement `search.go`**

```go
package domain

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Query is a search as the matcher reads it: the words to find, and the kind the reserved words
// asked for (FR-12.1).
type Query struct {
	// Words are the free words, lower-cased, in the order typed; never empty strings.
	Words []string
	// Kind is set when the query held a reserved kind word; "" means both kinds.
	Kind Kind
}

// ParseQuery splits q on whitespace, lower-cases every word, and takes the reserved words out:
// "issue" and "issues" set Kind to KindIssue, "pr", "prs" and "pull" set it to KindPR. A query
// with both kinds keeps the last one typed. Everything else is a word to find.
func ParseQuery(q string) Query {
	var out Query
	for _, w := range strings.Fields(strings.ToLower(q)) {
		switch w {
		case "issue", "issues":
			out.Kind = KindIssue
		case "pr", "prs", "pull":
			out.Kind = KindPR
		default:
			out.Words = append(out.Words, w)
		}
	}
	return out
}

// Hit is one item the query matched, with the evidence.
type Hit struct {
	Item  Item
	Score int
	// Matched names the fields that matched, in the order of matchedFields and without repeats.
	// nil when only the kind matched.
	Matched []string
	// TitleSpans are the byte ranges [start, end) of Item.Title the words matched, merged where
	// they touch or overlap, in order — what a page wraps in <mark>. nil when lower-casing the
	// title changes its byte length, since then the ranges could not be mapped back.
	TitleSpans [][2]int
}

// The score each field contributes per word. A title match at a word start is worth most; the
// summary and the repository name are tie-breakers rather than reasons.
const (
	scoreTitleWordStart = 4
	scoreTitleElsewhere = 2
	scoreContributor    = 3
	scoreLabel          = 3
	scoreRepository     = 1
	scoreSummary        = 1
)

// matchedFields is the fixed order of Hit.Matched.
var matchedFields = []string{"title", "contributor", "label", "repository", "summary"}

// Search ranks items against q. Every word must match at least one field; an item that fails a
// word is out. The kind, when set, filters and scores nothing. A query with no words and no kind
// returns nil. Items are never mutated.
func Search(items []Item, q Query) []Hit {
	if len(q.Words) == 0 && q.Kind == "" {
		return nil
	}
	var hits []Hit
	for _, it := range items {
		if q.Kind != "" && it.Kind != q.Kind {
			continue
		}
		if h, ok := match(it, q.Words); ok {
			hits = append(hits, h)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if !a.Item.UpdatedAt.Equal(b.Item.UpdatedAt) {
			return a.Item.UpdatedAt.After(b.Item.UpdatedAt)
		}
		if a.Item.Repo != b.Item.Repo {
			return a.Item.Repo < b.Item.Repo
		}
		return a.Item.Number < b.Item.Number
	})
	return hits
}

// match scores one item against every word, and reports false as soon as a word matches nothing.
func match(it Item, words []string) (Hit, bool) {
	h := Hit{Item: it}
	title := strings.ToLower(it.Title)
	mappable := len(title) == len(it.Title)
	author := strings.ToLower(it.Author)
	repo := strings.ToLower(it.Repo)
	summary := strings.ToLower(it.Summary)
	labels := make([]string, len(it.Labels))
	for i, l := range it.Labels {
		labels[i] = strings.ToLower(l)
	}
	matched := make(map[string]bool, len(matchedFields))
	var spans [][2]int
	for _, w := range words {
		score := 0
		if s := titleScore(title, w); s > 0 {
			score += s
			matched["title"] = true
			if mappable {
				spans = append(spans, occurrences(title, w)...)
			}
		}
		if strings.Contains(author, w) {
			score += scoreContributor
			matched["contributor"] = true
		}
		for _, l := range labels {
			if strings.Contains(l, w) {
				score += scoreLabel
				matched["label"] = true
				break
			}
		}
		if strings.Contains(repo, w) {
			score += scoreRepository
			matched["repository"] = true
		}
		if strings.Contains(summary, w) {
			score += scoreSummary
			matched["summary"] = true
		}
		if score == 0 {
			return Hit{}, false
		}
		h.Score += score
	}
	for _, f := range matchedFields {
		if matched[f] {
			h.Matched = append(h.Matched, f)
		}
	}
	if mappable {
		h.TitleSpans = mergeSpans(spans)
	}
	return h, true
}

// titleScore is scoreTitleWordStart when w occurs at a word start of title, scoreTitleElsewhere
// when it occurs only inside words, and 0 when it does not occur.
func titleScore(title, w string) int {
	best := 0
	for _, span := range occurrences(title, w) {
		if atWordStart(title, span[0]) {
			return scoreTitleWordStart
		}
		best = scoreTitleElsewhere
	}
	return best
}

// atWordStart reports whether the byte at is the start of the string or follows something that
// is neither a letter nor a digit.
func atWordStart(s string, at int) bool {
	if at == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:at])
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

// occurrences is every [start, end) at which w occurs in s, overlapping ones included, in order.
func occurrences(s, w string) [][2]int {
	var out [][2]int
	for from := 0; from <= len(s); {
		i := strings.Index(s[from:], w)
		if i < 0 {
			return out
		}
		at := from + i
		out = append(out, [2]int{at, at + len(w)})
		from = at + 1
	}
	return out
}

// mergeSpans sorts spans and joins the ones that touch or overlap; nil for none.
func mergeSpans(spans [][2]int) [][2]int {
	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	out := [][2]int{spans[0]}
	for _, s := range spans[1:] {
		last := &out[len(out)-1]
		if s[0] <= last[1] {
			last[1] = max(last[1], s[1])
			continue
		}
		out = append(out, s)
	}
	return out
}
```

- [ ] **Step 4: Run the domain tests with coverage — PASS, ≥ 90 %; `gofmt -l internal/domain` prints nothing**

- [ ] **Step 5: Commit**

```bash
git add internal/domain/search.go internal/domain/search_test.go
git commit -m "feat(domain): ranked search over titles, contributors, labels, repositories and summaries, with reserved kind words (FR-12.1)"
```

---

### Task 6: The results page, live search and Cmd‑K (FR‑12.1)

**Files:**

- Create: `internal/web/search.go`, `internal/web/search_test.go`, `internal/web/templates/search.html`, `internal/web/static/search.js`
- Modify: `internal/web/server.go` (route, `pageFiles`, `pageData.Search`), `internal/web/templates/layout.html` (script tag), `internal/web/static/app.css`, the static budget test (`TestStaticAssetsFitTheirBudgetOnTheWire` in `dashboard_test.go` or wherever it lives)

**Interfaces:**

- Consumes: `domain.ParseQuery`, `domain.Search`, `chromeFor`, `s.headerView(snap, r)`, `hueForRepo`, `labelViews`, `newTimeView`, `quantity`, `answeredWaiting`.
- Produces: `GET /search` (`authSessionPage`); `pageData.Search *searchView`.

- [ ] **Step 1: Write the failing tests** (`search_test.go`; reuse the helpers of `dashboard_test.go`: a handler with a fake source and items, `getAuthed`, `getSettled`, `coldServer`, `blockingSource`)

```go
// FR-12.1 AC1: the results page ranks the snapshot and marks the matched text.
func TestSearchPageRendersHitsWithMarks(t *testing.T) {
	h := dashHandlerWith(t, []domain.Item{
		{Kind: domain.KindIssue, Repo: "arc42/arc42-template", Number: 7, Title: "Fix the <header> & footer", Author: "gernot", Labels: []string{"bug"}, UpdatedAt: testNow},
		{Kind: domain.KindPR, Repo: "arc42/arc42-template", Number: 8, Title: "Unrelated", Author: "alice", UpdatedAt: testNow},
	})
	body := getAuthed(t, h, "/search?q=header+gernot").Body.String()
	for _, want := range []string{
		"1 result for “header gernot”",
		"Fix the &lt;<mark>header</mark>&gt; &amp; footer",
		"matched: title, contributor",
		`class="hit hue-slate"`, "label-bug", "#7",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("results page lacks %s", want)
		}
	}
	if strings.Contains(body, "Unrelated") {
		t.Error("an item matching no word was listed")
	}
	if !strings.Contains(body, `value="header gernot"`) {
		t.Error("the top-bar box does not echo the query")
	}
}

func TestSearchPageReflectsNothingUnescaped(t *testing.T) {
	h := dashHandlerWith(t, nil)
	body := getAuthed(t, h, "/search?q=%3Cscript%3Ealert(1)%3C/script%3E").Body.String()
	if strings.Contains(body, "<script>alert") {
		t.Error("the query is reflected unescaped")
	}
	if !strings.Contains(body, "Nothing matches") {
		t.Error("no count line for a query without hits")
	}
}

func TestSearchWithoutQueryShowsTheHint(t *testing.T) {
	body := getAuthed(t, dashHandlerWith(t, nil), "/search").Body.String()
	if !strings.Contains(body, `class="search-hint"`) || strings.Contains(body, `class="hits"`) {
		t.Error("an empty query should show the hint and no list")
	}
}

// The top-bar form asks for the page with HX-Request and selects main; the answer is the whole
// page, which htmx cuts down. A poll from the wait page is a different matter (waiting_test.go).
func TestSearchAnswersAnHtmxRequestWithThePage(t *testing.T) {
	h := dashHandlerWith(t, []domain.Item{{Kind: domain.KindIssue, Repo: "o/r", Number: 1, Title: "Header", UpdatedAt: testNow}})
	req := authedRequest(t, http.MethodGet, "/search?q=header")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<main") || !strings.Contains(rec.Body.String(), "<mark>Header</mark>") {
		t.Errorf("htmx search = %d %s", rec.Code, firstLineContaining(rec.Body.String(), "result"))
	}
}

// FR-1.9 AC1 and AC5 hold for /search as for /: a navigation during a fetch waits, an htmx
// request is answered from the current list.
func TestSearchDuringAFetchWaitsForNavigationAndAnswersHtmx(t *testing.T) {
	h, release := coldServer(t)
	defer release()
	if body := getAuthed(t, h, "/search?q=x").Body.String(); !strings.Contains(body, waitingMarker) || !strings.Contains(body, `hx-get="/search?q=x"`) {
		t.Error("a navigation to /search during a fetch did not get the wait page polling itself")
	}
	req := authedRequest(t, http.MethodGet, "/search?q=x")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), waitingMarker) {
		t.Errorf("an htmx search during a fetch = %d, body has wait page: %v", rec.Code, strings.Contains(rec.Body.String(), waitingMarker))
	}
}

func TestSearchCountLine(t *testing.T) {
	for _, tc := range []struct {
		q    string
		n    int
		want string
	}{
		{"x", 0, "Nothing matches “x”."},
		{"x", 1, "1 result for “x”"},
		{"a b", 7, "7 results for “a b”"},
	} {
		if got := searchCountLine(tc.q, tc.n); got != tc.want {
			t.Errorf("searchCountLine(%q, %d) = %q, want %q", tc.q, tc.n, got, tc.want)
		}
	}
}

func TestTitleRuns(t *testing.T) {
	got := titleRuns("Fix the header", [][2]int{{8, 14}})
	want := []titleRun{{Text: "Fix the ", Mark: false}, {Text: "header", Mark: true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("titleRuns = %+v, want %+v", got, want)
	}
	if got := titleRuns("plain", nil); !reflect.DeepEqual(got, []titleRun{{Text: "plain"}}) {
		t.Errorf("titleRuns(no spans) = %+v", got)
	}
}
```

Adjust helper names to what `dashboard_test.go` and `waiting_test.go` actually define (read them first; `authedRequest` may be called something else — use what exists, do not add a duplicate). Extend the static budget test's asset list with `search.js`. Also assert in `TestSearchPageRendersHitsWithMarks` that `<script src=` for `search.js` is present in the page and that the CSP header is unchanged (the existing CSP test covers the header; only check the tag).

- [ ] **Step 2: Run, expect failures**

- [ ] **Step 3: Implement**

`server.go`: add `"search.html"` to `pageFiles`; route `{http.MethodGet, "/search", authSessionPage, s.handleSearch, ""}` after `/sites` with the comment "The results page (FR-12.1): a page, so an anonymous visitor is redirected to sign in."; `pageData` gains `Search *searchView` ("set only by handleSearch"). `layout.html`: after the htmx script, `<script src="{{.Asset "search.js"}}" defer></script>` with the comment "Cmd-K (FR-12.1 AC3). A file of our own, since htmx's event filters would need unsafe-eval, which the policy does not grant."

`search.go`:

```go
package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/snapshot"
)

// handleSearch renders the results page (FR-12.1). Like the list, it reads only the snapshot;
// answeredWaiting handles a fetch in flight exactly as it does for the list (FR-1.9).
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	snap, waiting := s.answeredWaiting(w, r)
	if waiting {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	view := s.searchView(snap, q, s.clock.Now(), r)
	s.execute(w, r, http.StatusOK, "search.html", pageData{
		Title:  "Search",
		Chrome: chromeFor(r),
		Search: &view,
	})
}

// searchView is the results page.
type searchView struct {
	headerView
	// Query is the text as typed, trimmed; empty shows the hint instead of results.
	Query string
	// CountLine says what was found — computed here, never in the template.
	CountLine string
	Hits      []hitView
}

// hitView is one result row.
type hitView struct {
	Kind, Repo string
	// Hue is the repository's site colour, as the list's groups carry it.
	Hue    string
	Number int
	// Title is the title cut into runs, each marked or plain, so the template can wrap the
	// matched runs in <mark> without ever holding HTML.
	Title   []titleRun
	URL     string
	Labels  []labelView
	Author  string
	Updated timeView
	Quiet   bool
	// Matched is the evidence line, "matched: title, label", or "" when only the kind matched.
	Matched string
}

// titleRun is a run of the title: marked when the query matched it.
type titleRun struct {
	Text string
	Mark bool
}

func (s *Server) searchView(snap snapshot.Snapshot, q string, now time.Time, r *http.Request) searchView {
	hits := domain.Search(snap.Items, domain.ParseQuery(q))
	v := searchView{
		headerView: s.headerView(snap, r),
		Query:      q,
		CountLine:  searchCountLine(q, len(hits)),
		Hits:       make([]hitView, 0, len(hits)),
	}
	for _, h := range hits {
		v.Hits = append(v.Hits, hitView{
			Kind:    kindLabel(h.Item.Kind),
			Repo:    h.Item.Repo,
			Hue:     hueForRepo(s.cfg.GitHub, h.Item.Repo),
			Number:  h.Item.Number,
			Title:   titleRuns(h.Item.Title, h.TitleSpans),
			URL:     h.Item.URL,
			Labels:  labelViews(h.Item.Labels),
			Author:  h.Item.Author,
			Updated: newTimeView(h.Item.UpdatedAt, now),
			Quiet:   h.Item.IsQuiet(now),
			Matched: matchedLine(h.Matched),
		})
	}
	return v
}

// searchCountLine is the line above the results: how many, for what.
func searchCountLine(q string, n int) string {
	if n == 0 {
		return "Nothing matches “" + q + "”."
	}
	return quantity(n, "result") + " for “" + q + "”"
}

// matchedLine spells out which fields matched, or "" for none.
func matchedLine(fields []string) string {
	if len(fields) == 0 {
		return ""
	}
	return "matched: " + strings.Join(fields, ", ")
}

// titleRuns cuts title at the spans, which are sorted, merged and inside the title (the domain
// guarantees all three); a title with no spans is one plain run.
func titleRuns(title string, spans [][2]int) []titleRun {
	var runs []titleRun
	at := 0
	for _, sp := range spans {
		if sp[0] > at {
			runs = append(runs, titleRun{Text: title[at:sp[0]]})
		}
		runs = append(runs, titleRun{Text: title[sp[0]:sp[1]], Mark: true})
		at = sp[1]
	}
	if at < len(title) || len(runs) == 0 {
		runs = append(runs, titleRun{Text: title[at:]})
	}
	return runs
}
```

Check that `headerView`'s signature after Task 3 is `(snap, r)`; if Task 3 dropped `r`, drop it here. If `quantity` does not exist with that shape, use whatever helper `kindsLine` uses.

`templates/search.html`: the markup from spec §6.2 verbatim, with a leading comment "The results page (FR-12.1). Every string is computed in search.go; the runs are escaped text and <mark> is this template's own markup."

`static/search.js`: the script from spec §6.3 verbatim.

`app.css`, appended:

```css
/* ---------------------------------------------------------------- search results (FR-12.1) */

.search-hint { color: var(--muted); max-width: 40rem; }
.search-hint code { font-size: 0.9em; }
.hits { list-style: none; margin: 0; padding: 0; }
.hit {
  padding: 0.5rem 0 0.5rem 0.75rem;
  border-bottom: 1px solid var(--border);
  border-left: 4px solid var(--tile-hue, var(--hue-slate));
}
.hit:last-child { border-bottom: 0; }
.hit-kind, .hit-repo { color: var(--muted); font-size: 0.8rem; }
mark { background: #fde68a; color: #1f2937; border-radius: 2px; padding: 0 0.1em; }
@supports (color: light-dark(#000, #fff)) and (color: color-mix(in srgb, #000 50%, #fff)) {
  .hit { border-left-color: light-dark(var(--tile-hue, var(--hue-slate)), color-mix(in srgb, var(--tile-sig, var(--hue-slate-sig)) 70%, #ffffff)); }
  mark { background: color-mix(in srgb, var(--accent) 25%, transparent); color: inherit; }
}
```

Copy the exact `@supports` condition and the stripe expression the `.repo-group` rule uses, so the two stripes can never differ.

- [ ] **Step 4: Build, vet, test — PASS; static budget test green; `grep -c "unsafe" internal/web/server.go` unchanged**

- [ ] **Step 5: Commit**

```bash
git add internal/web/search.go internal/web/search_test.go internal/web/templates/search.html internal/web/static/search.js internal/web/server.go internal/web/templates/layout.html internal/web/static/app.css internal/web/dashboard_test.go
git commit -m "feat(web): the results page, live search from the top bar and Cmd-K (FR-12.1, FR-1.9, QS-2.3)"
```

---

### Task 7: Contributors in the domain (FR‑12.2)

**Files:**

- Create: `internal/domain/contributors.go`, `internal/domain/contributors_test.go`

**Interfaces:**

- Produces: `Contributor{Login string; PRs, Issues int; Repos []string; LastActive time.Time}`; `BuildContributors(items []Item, repos []string) []Contributor`.

- [ ] **Step 1: Write the failing test**

```go
package domain

import (
	"reflect"
	"testing"
	"time"
)

func TestBuildContributorsGroupsAndOrders(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 9, 1, h, 0, 0, 0, time.UTC) }
	items := []Item{
		{Kind: KindIssue, Repo: "o/b", Author: "Zed", UpdatedAt: at(1)},
		{Kind: KindPR, Repo: "o/a", Author: "amy", UpdatedAt: at(5)},
		{Kind: KindIssue, Repo: "o/b", Author: "amy", UpdatedAt: at(2)},
		{Kind: KindIssue, Repo: "o/c", Author: "", UpdatedAt: at(9)},
		{Kind: KindPR, Repo: "o/a", Author: "bob", UpdatedAt: at(3)},
		{Kind: KindIssue, Repo: "o/a", Author: "bob", UpdatedAt: at(4)},
		{Kind: KindIssue, Repo: "o/a", Author: "", UpdatedAt: at(1)},
	}
	got := BuildContributors(items, []string{"o/a", "o/b"})
	want := []Contributor{
		{Login: "amy", PRs: 1, Issues: 1, Repos: []string{"o/a", "o/b"}, LastActive: at(5)},
		{Login: "bob", PRs: 1, Issues: 1, Repos: []string{"o/a"}, LastActive: at(4)},
		{Login: "Zed", Issues: 1, Repos: []string{"o/b"}, LastActive: at(1)},
		{Login: "", Issues: 2, Repos: []string{"o/a", "o/c"}, LastActive: at(9)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildContributors =\n%+v\nwant\n%+v", got, want)
	}
	if BuildContributors(nil, nil) != nil {
		t.Error("no items should give nil")
	}
}

func TestBuildContributorsBreaksTiesByLoginCaseInsensitively(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	got := BuildContributors([]Item{
		{Kind: KindIssue, Repo: "o/a", Author: "bob", UpdatedAt: now},
		{Kind: KindIssue, Repo: "o/a", Author: "Alice", UpdatedAt: now},
	}, nil)
	if got[0].Login != "Alice" || got[1].Login != "bob" {
		t.Errorf("order = %s, %s", got[0].Login, got[1].Login)
	}
}
```

- [ ] **Step 2: Run, expect compile failure**

- [ ] **Step 3: Implement**

```go
package domain

import (
	"slices"
	"sort"
	"strings"
	"time"
)

// Contributor is one person who opened at least one open item (FR-12.2).
type Contributor struct {
	// Login is the GitHub login; "" when GitHub no longer knows the author.
	Login       string
	PRs, Issues int
	// Repos are the repositories they opened something in: the ones repos names, in that order,
	// then any it does not, in first-seen order.
	Repos []string
	// LastActive is the latest UpdatedAt among their items.
	LastActive time.Time
}

// BuildContributors groups items by author. The order is by open items descending, then
// LastActive descending, then Login case-insensitively; the unknown author, if present, is last.
// It is a pure function and never modifies items. nil for no items.
func BuildContributors(items []Item, repos []string) []Contributor {
	byLogin := make(map[string]*Contributor)
	var order []string
	for _, it := range items {
		c, ok := byLogin[it.Author]
		if !ok {
			c = &Contributor{Login: it.Author}
			byLogin[it.Author] = c
			order = append(order, it.Author)
		}
		switch it.Kind {
		case KindPR:
			c.PRs++
		case KindIssue:
			c.Issues++
		default:
			// A third kind, should one arrive, is neither; the person is still listed.
		}
		if it.UpdatedAt.After(c.LastActive) {
			c.LastActive = it.UpdatedAt
		}
		if !slices.Contains(c.Repos, it.Repo) {
			c.Repos = append(c.Repos, it.Repo)
		}
	}
	if len(order) == 0 {
		return nil
	}
	out := make([]Contributor, 0, len(order))
	for _, login := range order {
		c := *byLogin[login]
		c.Repos = orderRepos(c.Repos, repos)
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Login == "") != (b.Login == "") {
			return b.Login == ""
		}
		if ta, tb := a.PRs+a.Issues, b.PRs+b.Issues; ta != tb {
			return ta > tb
		}
		if !a.LastActive.Equal(b.LastActive) {
			return a.LastActive.After(b.LastActive)
		}
		return strings.ToLower(a.Login) < strings.ToLower(b.Login)
	})
	return out
}

// orderRepos puts the configured repositories first, in configuration order, then the rest in
// the order given.
func orderRepos(seen, configured []string) []string {
	out := make([]string, 0, len(seen))
	for _, r := range configured {
		if slices.Contains(seen, r) {
			out = append(out, r)
		}
	}
	for _, r := range seen {
		if !slices.Contains(configured, r) {
			out = append(out, r)
		}
	}
	return out
}
```

- [ ] **Step 4: Domain tests with coverage — PASS, ≥ 90 %**

- [ ] **Step 5: Commit**

```bash
git add internal/domain/contributors.go internal/domain/contributors_test.go
git commit -m "feat(domain): contributors grouped from the open items, ordered by how much they opened (FR-12.2)"
```

---

### Task 8: The Contributors page (FR‑12.2)

**Files:**

- Create: `internal/web/contributors.go`, `internal/web/contributors_test.go`, `internal/web/templates/contributors.html`
- Modify: `internal/web/server.go` (route, `pageFiles`, `pageData.Contributors`), `internal/web/static/app.css`

**Interfaces:**

- Consumes: `domain.BuildContributors`, `chromeFor`, `s.headerView`, `newTimeView`, `quantity`, `answeredWaiting`.
- Produces: `GET /contributors` (`authSessionPage`); `pageData.Contributors *contributorsView`.

- [ ] **Step 1: Write the failing tests**

```go
// FR-12.2: one row per author, the busiest first, each linking to GitHub and to the search for
// their login; the unknown author is named and links nowhere.
func TestContributorsPageListsAuthors(t *testing.T) {
	h := dashHandlerWith(t, []domain.Item{
		{Kind: domain.KindPR, Repo: "o/a", Number: 1, Author: "amy", UpdatedAt: testNow},
		{Kind: domain.KindIssue, Repo: "o/b", Number: 2, Author: "amy", UpdatedAt: testNow.Add(-time.Hour)},
		{Kind: domain.KindIssue, Repo: "o/a", Number: 3, Author: "", UpdatedAt: testNow},
	})
	body := getAuthed(t, h, "/contributors").Body.String()
	for _, want := range []string{
		"<title>Contributors · zorgscope</title>", "2 contributors",
		`href="https://github.com/amy"`, `href="/search?q=amy"`, "o/a, o/b",
		`class="contributor-unknown"`, `aria-current="page"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("contributors page lacks %s", want)
		}
	}
	if strings.Index(body, "github.com/amy") > strings.Index(body, "contributor-unknown") {
		t.Error("the unknown author is not last")
	}
	if strings.Contains(body, `href="https://github.com/"`) || strings.Contains(body, `q=">`) {
		t.Error("the unknown author links somewhere")
	}
}

func TestContributorsPageWithNothingOpen(t *testing.T) {
	body := getAuthed(t, dashHandlerWith(t, nil), "/contributors").Body.String()
	if !strings.Contains(body, "No contributors yet.") || strings.Contains(body, "<table") {
		t.Error("an empty snapshot should say so and draw no table")
	}
}

func TestContributorsPageStaysInsideItsBudget(t *testing.T) — copy the shape of TestSitesPageStaysInsideItsBudget against the representative fixture, 150 kB.
```

- [ ] **Step 2: Run, expect failures**

- [ ] **Step 3: Implement**

`server.go`: `"contributors.html"` in `pageFiles`; route `{http.MethodGet, "/contributors", authSessionPage, s.handleContributors, ""}`; `pageData.Contributors *contributorsView`.

`contributors.go`:

```go
package web

import (
	"net/http"
	"net/url"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// handleContributors renders the Contributors page (FR-12.2): the people who opened the open
// items, busiest first. It reads only the snapshot, through answeredWaiting like every page.
func (s *Server) handleContributors(w http.ResponseWriter, r *http.Request) {
	snap, waiting := s.answeredWaiting(w, r)
	if waiting {
		return
	}
	now := s.clock.Now()
	people := domain.BuildContributors(snap.Items, s.cfg.GitHub.Repos)
	view := contributorsView{
		headerView: s.headerView(snap, r),
		CountLine:  contributorsCountLine(len(people)),
		Rows:       make([]contributorView, 0, len(people)),
	}
	for _, c := range people {
		row := contributorView{
			Login: c.Login, Unknown: c.Login == "",
			PRs: c.PRs, Issues: c.Issues, Repos: c.Repos,
			LastActive: newTimeView(c.LastActive, now),
		}
		if row.Unknown {
			row.Login = "unknown"
		} else {
			row.ProfileURL = "https://github.com/" + url.PathEscape(c.Login)
			row.ItemsURL = "/search?" + url.Values{"q": {c.Login}}.Encode()
		}
		view.Rows = append(view.Rows, row)
	}
	s.execute(w, r, http.StatusOK, "contributors.html", pageData{
		Title: "Contributors", Chrome: chromeFor(r), Contributors: &view,
	})
}

// contributorsView is the Contributors page.
type contributorsView struct {
	headerView
	// CountLine: "12 contributors" or "No contributors yet."
	CountLine string
	Rows      []contributorView
}

// contributorView is one row of the table.
type contributorView struct {
	// Login is the login, or "unknown" for the author GitHub no longer knows; Unknown says which.
	Login   string
	Unknown bool
	// ProfileURL is the GitHub profile and ItemsURL the search for the login; both empty when
	// Unknown, so the template draws no link.
	ProfileURL, ItemsURL string
	PRs, Issues          int
	Repos                []string
	LastActive           timeView
}

// contributorsCountLine is the line above the table.
func contributorsCountLine(n int) string {
	if n == 0 {
		return "No contributors yet."
	}
	return quantity(n, "contributor")
}
```

`templates/contributors.html`: the markup from spec §7.2 verbatim, with a leading comment "The Contributors page (FR-12.2). A table, because it is one: five facts per person, compared down the columns."

`app.css`, appended:

```css
/* ---------------------------------------------------------------- contributors (FR-12.2) */

.table-scroll { overflow-x: auto; }
.contributor-table { border-collapse: collapse; width: 100%; font-size: 0.9rem; }
.contributor-table th, .contributor-table td { padding: 0.4rem 0.6rem; border-bottom: 1px solid var(--border); text-align: left; vertical-align: baseline; }
.contributor-table thead th { color: var(--muted); font-weight: 500; font-size: 0.8rem; }
.contributor-table tbody th { font-weight: 600; }
.contributor-table .num { text-align: right; font-variant-numeric: tabular-nums; }
.contributor-table a { color: var(--text); }
.contributor-table a:hover { color: var(--accent); }
.contributor-unknown { color: var(--muted); font-style: italic; }
```

- [ ] **Step 4: Build, vet, test — PASS; `TestEveryRouteIsEitherDeliberatelyPublicOrRefusesAnonymousAccess` green without edits**

- [ ] **Step 5: Commit**

```bash
git add internal/web/contributors.go internal/web/contributors_test.go internal/web/templates/contributors.html internal/web/server.go internal/web/static/app.css
git commit -m "feat(web): the Contributors page (FR-12.2)"
```

---

### Task 9: Documentation, ADR‑0012 and the full gate

**Files:**

- Modify: `docs/requirements/01-goals.md`, `docs/requirements/04-functional-requirements.md`, `docs/requirements/05-quality-requirements.md`, `docs/requirements/06-glossary.md`, `docs/decisions/README.md`, `docs/decisions/0010-stateless-no-database.md`, `docs/concepts/security-and-tokens.md`, `README.md` (only if it mentions "Mark all seen" or NEW)
- Create: `docs/decisions/0012-no-seen-mark.md`

- [ ] **Step 1: Requirements** — apply spec §9 exactly. The new rows, in the table's own format (`| id | M | story | ACs |`):

`FR‑1.11`: "As the user I reach every view and action from the top bar." AC1 The top bar of every signed-in page carries, above the rainbow band, the List | Sites | Contributors switch marking the current view, the search box, Refresh and Log out; the sign-in page carries only the brand and the appearance control. AC2 The page body keeps the fetched time and the error notice above its content. AC3 Refresh returns to the page it was pressed on, query included, through the same validation as before.

`FR‑12.1`: "As the user I find an item by a word from its title, its contributor, a label or its repository, from any page, in seconds." AC1 `GET /search?q=` ranks the snapshot: every word must match; a word matches case-insensitively as a substring of the title, the author login, a label, the repository name or the summary; `issue`, `issues`, `pr`, `prs` and `pull` are reserved and narrow the kind; scores are title at a word start 4, title elsewhere 2, contributor 3, label 3, repository 1, summary 1, summed; ties break by last update. AC2 The results page shows the count, and per hit the kind, repository, number, title with the matched text marked, labels, author, last update and which fields matched; an empty query shows a hint; nothing is reflected unescaped. AC3 The box in the top bar fetches results as the user types, 300 ms after the last keystroke, and pushes the results URL; Enter and JavaScript-off reach the same page; Cmd‑K or Ctrl‑K focuses the box, Escape leaves it. AC4 During a fetch the page behaves as FR‑1.9 prescribes.

`FR‑12.2`: "As the user I see who opened the open issues and pull requests." AC1 `GET /contributors` lists one row per author with login linking to their GitHub profile, open pull requests, open issues, the repositories in configuration order, last activity, and a link to the search for their login; ordered by open items, then last activity, then login; an author GitHub no longer knows is listed last as "unknown" and links nowhere. AC2 The page carries the switch marking it and the shared header.

Epic heading: `## E‑12 Search and people` before "Explicitly out of scope", with one intro sentence. Add "closed items, and NEW with its seen mark (retired 2026-09-18, ADR‑0012)" to the out-of-scope paragraph.

- [ ] **Step 2: Goals, quality, glossary, concepts** — spec §9. In `05-quality-requirements.md` also update QS‑2.3's measure text to "including the vendored htmx and `search.js`".

- [ ] **Step 3: ADR‑0012** at `docs/decisions/0012-no-seen-mark.md`, from the template: title "No seen mark: the page shows what is open, not what is new"; Status accepted; Date 2026-09-18; Requirements FR‑1.2 (retired), FR‑1.11, QS‑4.1. Context: "Mark all seen" was the only thing that moved the mark; Gernot asked for it to go; a mark nothing moves is dead. Options: (A) remove the button and leave the machinery; (B) remove NEW altogether; (C) move seen automatically on every visit (last-visit semantics, rejected in 0006 for good reasons). Chosen B. Consequences: good — smaller cookie, fewer routes, no badge that lies; bad — G‑1's "recognises at a glance what is new" is gone, and returning it is a design of its own; neutral — every session signs in once more. Add the row to `docs/decisions/README.md`; set 0010's status line to "accepted; its seen-mark half superseded by [0012](0012-no-seen-mark.md)".

- [ ] **Step 4: Full gate** — `make check` if Docker is up, else the native sequence from Global Constraints; all green, domain coverage ≥ 90 %.

- [ ] **Step 5: Commit**

```bash
git add docs/requirements/01-goals.md docs/requirements/04-functional-requirements.md docs/requirements/05-quality-requirements.md docs/requirements/06-glossary.md docs/decisions/README.md docs/decisions/0010-stateless-no-database.md docs/decisions/0012-no-seen-mark.md docs/concepts/security-and-tokens.md
git commit -m "docs: FR-1.11, E-12 with FR-12.1 and FR-12.2, FR-1.2 retired, QG-1 reworded, ADR-0012 (FR-1.2, FR-1.11, FR-12.1, FR-12.2)"
```

---

## Self-review

- **Spec coverage:** §3 → Task 3; §4 → Tasks 1, 2; §5 → Task 4; §6.1 → Task 5; §6.2, §6.3 → Task 6; §7 → Tasks 7, 8; §8 → the tests in Tasks 2, 6, 8; §9 → Task 9; §10 → every task's tests.
- **Placeholders:** the Task 5 span test deliberately asks the implementer to compute the expectation rather than copy it; everything else is literal.
- **Type consistency:** `headerView(snap, r)` is produced in Task 2 and consumed in Tasks 6 and 8; `chromeFor` produced in Task 3, consumed in 6 and 8; `titleRun`/`hitView` defined once in Task 6; `quantity(n, noun)` is the existing helper `kindsLine` uses.
