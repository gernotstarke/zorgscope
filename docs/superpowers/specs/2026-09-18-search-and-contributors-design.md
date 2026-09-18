# Search, contributors and the top bar — design

Date: 2026-09-18. Status: approved by Gernot in conversation (server-side search, the reserved
kind words and labels as "status", ten configured sites with arc42-template first, "Mark all seen"
removed outright — his choices). Builds on the list sugar (`2026-09-17-list-sugar-design.md`) and
the fast first view (`2026-09-16-fast-first-view-design.md`) and changes none of their decisions:
no database, no ticker, colour only as CSS classes, no inline script or style, the 20-request
budget of QS‑3.5 untouched.

## 1. Goal

Four things Gernot asked for on one screenshot. The actions and the view switch move out of the
page body into the top bar, above the rainbow band. A search box in that top bar, reached with
Cmd‑K, finds items by title, contributor, label, repository and kind, and has a results page of
its own. A third page beside List and Sites lists the contributors — the people who opened the
open issues and pull requests. And the Sites view names every tile: arc42-template first, the
generator and zorgscope at the end, no Other tile.

The screenshot also crossed out "Mark all seen". Removing that button removes the only thing that
ever moves the seen mark, so this design takes the whole idea of NEW out with it (§4) rather than
leave a badge that can never appear.

## 2. Decisions (Gernot, 2026-09-18)

| Question | Decision |
|---|---|
| Where search runs | On the server, in Go, over the in-memory snapshot. Not lunr.js: the corpus is at most ~150 items, a Go matcher works with JavaScript off, needs no new dependency and no second renderer in the browser. |
| What "status" means to search | The kind, as the reserved words `issue`, `issues`, `pr`, `prs`; and the labels. Not "new", not "quiet", not "draft". |
| The last tile | Every watched repository becomes a configured site, so the Other tile disappears. arc42-template first, then arc42.org and arc42.de, the existing sites in their order, then arc42-generator (newly watched) and zorgscope last. |
| "Mark all seen" | Removed entirely, and with it NEW: the badges, the counts, the tab-title prefix, `POST /seen` and the seen mark in the cookie. FR‑1.2 is retired. |
| Delivery | Branch `feat/search-and-contributors` off `main` at `12d0579`; version 0.4.0. |

## 3. The top bar (FR‑1.11)

`layout.html` draws the top bar for every page. Signed-in pages add the chrome — the view switch,
the search box, Refresh and Log out — between the brand and the appearance control; the sign-in
page and the refusal page show only the brand and the appearance control. `pageData` gains one
field for it:

```go
// Chrome is the signed-in top bar: which view is current, what the search box shows, and where
// Refresh comes back to. nil on the sign-in and refusal pages, which have no session to act for.
Chrome *chromeView

// chromeView is what layout.html draws between the brand and the appearance control.
type chromeView struct {
	// View is "list", "sites", "contributors" or "search"; the switch marks the first three.
	View string
	// Query is the search box's current text: the query on the results page, empty elsewhere.
	Query string
	// Return is the page the Refresh form comes back to, path and query (FR‑1.8 AC4), used only
	// through safeReturn.
	Return string
}
```

The four session pages — `/`, `/sites`, `/contributors`, `/search` — and the wait page set it.
The markup, after the brand and before `<nav class="topbar-nav">`:

```html
{{with .Chrome}}
<nav class="view-switch" id="view-switch" aria-label="View">
  <a href="/"{{if eq .View "list"}} aria-current="page"{{end}}>List</a>
  <a href="/sites"{{if eq .View "sites"}} aria-current="page"{{end}}>Sites</a>
  <a href="/contributors"{{if eq .View "contributors"}} aria-current="page"{{end}}>Contributors</a>
</nav>
<form class="search-form" id="topbar-search" role="search" method="get" action="/search"
      hx-get="/search" hx-trigger="input changed delay:300ms, submit"
      hx-target="main" hx-select="main" hx-select-oob="#view-switch"
      hx-swap="outerHTML" hx-push-url="true">
  <input type="search" name="q" value="{{.Query}}" placeholder="Search  ⌘K" autocomplete="off"
         aria-label="Search titles, contributors, labels and repositories"
         title="Cmd-K or Ctrl-K" data-search>
</form>
<div class="topbar-actions">
  <form class="action" method="post" action="/refresh"><input type="hidden" name="return" value="{{.Return}}"><button type="submit">Refresh</button></form>
  <form class="action" method="post" action="/logout"><button type="submit">Log out</button></form>
</div>
{{end}}
```

The two ids earn their keep on the server and in the response. The form's is what htmx sends as
`HX-Trigger`, which is how `answeredWaiting` tells a whole-page search from a fragment (§6.2). The
switch's is what `hx-select-oob` swaps: the switch sits outside `main` so the box keeps its focus
and text across a swap, and without the out-of-band copy it would go on marking the page left
behind — `/search` reached by typing would say List, `/search` reached by navigating says nothing.

`fragments/viewswitch.html` is deleted; `fragments/header.html` keeps only the Fetched line and
the error notice, which stay in the page body above the list, the tiles, the contributors and the
results. The `.dash-actions` block and its CSS go. The top bar wraps (`flex-wrap: wrap`) so a phone
gets two rows rather than a horizontal scroll; the search box takes the free width
(`flex: 1 1 12rem`, `min-width: 8rem`) and the view switch loses its bottom margin.

## 4. NEW retired

With no way to move the seen mark, a fresh sign-in's zero mark is permanent, and zero means
nothing is new: NEW could never appear. Dead machinery is worse than none, so all of it goes.

Domain (`internal/domain`):

* `Item.IsNew` and `CountNew` are deleted.
* `SortItems(items []Item)` sorts by `UpdatedAt` descending, stable; the `lastVisit` parameter
  goes.
* `RepoGroup.NewCount`, `DashboardInput.LastVisitAt`, `Dashboard.LastVisitAt` and
  `Dashboard.NewTotal` are deleted; `Total` and `Shown` stay.
* `SiteTilesInput.LastVisitAt` and `SiteTile.NewCount` are deleted; a tile's rows are the most
  recently updated first.

Web (`internal/web`):

* `session` is `{Expiry time.Time}`. The cookie payload is the expiry alone, as decimal Unix
  seconds; `decode` rejects a payload containing `:` exactly as it rejected the pre-reset format,
  so every session minted before this change signs in once more and that is all.
* `POST /seen`, `handleSeen` and `seenAt` are deleted, along with `headerView.NewTotal`,
  `headerView.FetchedAtUnix`, `headerView.Return` (the Refresh form now reads `chromeView.Return`), `pageData.NewCount`, `itemView.New`, `groupView.NewCount`,
  `tileView.NewCount` and `tileItemView.New`.
* Templates lose the NEW badges, the `new-count` and `tile-new` spans, the `is-new` class and the
  tab-title prefix; `app.css` loses `.badge-new`, `.new-count`, `.tile-new` and every `.is-new`
  rule.

Documentation (§9) retires FR‑1.2, rewords G‑1 and QG‑1, and records the decision in ADR‑0012.

## 5. The sites (configuration only)

`config/zorgscope.yaml`:

```yaml
  repos:
    - arc42/arc42-template
    - arc42/arc42.org-site
    - arc42/arc42.de-site
    - arc42/quality.arc42.org-site
    - arc42/docs.arc42.org-site
    - arc42/faq.arc42.org-site
    - arc42/examples.arc42.org-site
    - arc42/trainings.arc42.org-site
    - arc42/arc42-generator
    - gernotstarke/zorgscope
```

Ten repositories: exactly the QS‑3.5 budget, and the comment above the list says so ("It holds
ten"). `sites` lists the same ten in the same order. The seven existing entries keep their hue and
tag; the three new ones have no brand hue and take `slate`, with their GitHub address as the link:

```yaml
    - name: arc42-template
      url: https://github.com/arc42/arc42-template
      repo: arc42/arc42-template
      hue: slate
    - name: arc42-generator
      url: https://github.com/arc42/arc42-generator
      repo: arc42/arc42-generator
      hue: slate
    - name: zorgscope
      url: https://github.com/gernotstarke/zorgscope
      repo: gernotstarke/zorgscope
      hue: slate
```

The list (FR‑1.1 AC1) follows the same order, arc42-template first. The Other tile's code stays:
it is what the Sites view does for a repository no site claims, and a future edit of the file may
leave one unclaimed. `docs/concepts/configuration.md` shows the new list.

## 6. Search (FR‑12.1)

### 6.1 The domain

`internal/domain/search.go`, pure, standard library only (QS‑5.1):

```go
// Query is a search as the matcher reads it: the words to find, and the kind the reserved words
// asked for.
type Query struct {
	// Words are the free words, lower-cased, in the order typed; never empty strings.
	Words []string
	// Kind is set when the query held a reserved kind word; "" means both kinds.
	Kind Kind
}

// ParseQuery splits q on whitespace, lower-cases every word, and takes the reserved words out:
// "issue" and "issues" set Kind to KindIssue, "pr", "prs" and "pull" set it to KindPR. A query
// with both kinds keeps the last one typed. Everything else is a word to find.
func ParseQuery(q string) Query

// Hit is one item the query matched, with the evidence.
type Hit struct {
	Item  Item
	Score int
	// Matched names the fields that matched, in this fixed order and without repeats: "title",
	// "contributor", "label", "repository", "summary". Empty when only the kind matched.
	Matched []string
	// TitleSpans are the byte ranges [start, end) of Item.Title the words matched, merged where
	// they touch or overlap, in order — what the page wraps in <mark>. nil when lower-casing the
	// title changes its byte length, since then the ranges could not be mapped back.
	TitleSpans [][2]int
}

// Search ranks items against q. Every word must match at least one field; an item that fails a
// word is out. A query with no words and no kind returns nil.
func Search(items []Item, q Query) []Hit
```

Matching is a case-insensitive substring match: word and field are both lower-cased with
`strings.ToLower`. Per word, the fields and their scores are:

| Field | Score | Note |
|---|---|---|
| Title, at a word start | 4 | The byte before the match is the start of the title or not a letter or digit. |
| Title, elsewhere | 2 | Only when no word-start match exists for that word. |
| Contributor (`Author`) | 3 | |
| A label | 3 | Once per word, however many labels match. |
| Repository (`Repo`) | 1 | Matches `arc42/faq.arc42.org-site` and so `faq` or `arc42`. |
| Summary | 1 | |

A word's score is the sum over the fields it matched; the hit's score is the sum over the words.
The kind, when set, filters and scores nothing. Hits are ordered by score descending, then
`UpdatedAt` descending, then `Repo`, then `Number`. Items are never mutated.

### 6.2 The page and the request

`GET /search?q=…` is a session route like `/` (QS‑4.1: an anonymous navigation is redirected to
sign-in, anything else gets 401). It goes through `answeredWaiting` like the other pages
(FR‑1.9 AC1, AC5): a navigation during a fetch gets the wait page, which polls `/search?q=…`
and swaps the results in; an htmx request for a fragment during a fetch is answered from the
current list. The top-bar search is not such a fragment — its form replaces the whole page body —
so it too gets the wait page: `answeredWaiting` recognises it by `HX-Trigger: topbar-search`
(the form's id), htmx selects the wait page's own `main`, and the poll inside it brings the
results in when the fetch ends. Answering it from the current list would swap that poll away and
leave "Nothing matches" on screen with nothing left to fetch the data.

`handleSearch` reads `q` (trimmed), parses it, searches `snap.Items`, and renders
`search.html` with `Chrome{View: "search", Query: q, Return: "/search?q=…"}`: (`pageData` gains `Search *searchView` and `Contributors *contributorsView`, set only by their handlers, like `Sites`)

```go
// searchView is the results page.
type searchView struct {
	headerView
	// Query is the text as typed, trimmed; empty shows the hint instead of results.
	Query string
	// CountLine says what was found: "7 results for “gernot bug”", "1 result for “x”", or
	// "Nothing matches “x”." — computed in Go, never in the template.
	CountLine string
	Hits      []hitView
}

// hitView is one result row.
type hitView struct {
	Kind, Repo string
	// Hue is the repository's site colour, as the list's groups carry it (hueForRepo).
	Hue    string
	Number int
	// Title is the title cut into runs, each either marked or plain, so the template can wrap
	// the matched runs in <mark> without ever holding HTML.
	Title  []titleRun
	URL    string
	Labels []labelView
	Author string
	Updated timeView
	Quiet   bool
	// Matched is the evidence line: "matched: title, label", built from Hit.Matched with
	// "contributor" and "repository" spelled out as they are.
	Matched string
}

// titleRun is a run of the title: marked when the query matched it.
type titleRun struct {
	Text string
	Mark bool
}
```

`search.html`:

```html
{{define "content"}}
{{with .Search}}
{{template "header" .}}
<section class="search-results" id="results">
  <h1>Search</h1>
  {{if not .Query}}
  <p class="search-hint">Type a word from a title, a contributor's login, a label or a repository
    name. <code>issue</code> or <code>pr</code> narrows to that kind: <code>pr gernot</code> is the
    pull requests gernot opened.</p>
  {{else}}
  <p class="source-line">{{.CountLine}}</p>
  <ol class="hits">
    {{range .Hits}}
    <li class="hit hue-{{.Hue}}{{if .Quiet}} is-quiet{{end}}">
      <p class="item-line">
        <span class="hit-kind">{{.Kind}}</span>
        <span class="hit-repo">{{.Repo}}</span>
        {{if .Number}}<span class="item-number">#{{.Number}}</span>{{end}}
        <a class="item-title" href="{{.URL}}" target="_blank" rel="noopener noreferrer">{{range .Title}}{{if .Mark}}<mark>{{.Text}}</mark>{{else}}{{.Text}}{{end}}{{end}}</a>
        {{if .Labels}}<span class="visually-hidden">Labels:</span>{{end}}
        {{range .Labels}}<span class="label label-{{.Key}}">{{.Name}}</span>{{end}}
      </p>
      <p class="item-meta">{{if .Author}}{{.Author}} · {{end}}{{if .Updated.Known}}updated <time datetime="{{.Updated.Absolute}}">{{.Updated.Relative}}</time>{{else}}updated at an unknown time{{end}}{{if .Quiet}}, quiet{{end}}{{with .Matched}} · {{.}}{{end}}</p>
    </li>
    {{end}}
  </ol>
  {{end}}
</section>
{{end}}
{{end}}
```

A hit carries the same stripe as a list group (`.hit { border-left: 4px solid var(--tile-hue,
var(--hue-slate)) }` with the `@supports` override the groups have), and `<mark>` is drawn with
the page's accent at low opacity behind the text colour, so the highlighted text keeps its
contrast: `mark { background: color-mix(in srgb, var(--accent) 25%, transparent); color: inherit;
}` with a plain `background: #fde68a; color: #1f2937` fallback outside `@supports`.

### 6.3 Live results and Cmd‑K

The top-bar form carries the htmx attributes above, so on any page a keystroke in the box fetches
`/search?q=…` 300 ms after the last one, swaps the whole `<main>` — the results page's own
`<main>` — and pushes the URL, so the address bar always names the page on screen and a reload
shows the same results. The box is outside `<main>`, so it keeps its focus and its text across the
swap. Enter submits through htmx the same way; with JavaScript off the plain GET does the same
with a full page.

The existing list filter (FR‑2.1), its own text box included, stays as it is: narrowing the list
is not searching. Its one change is the scope of its trigger: `from:` is a selector htmx resolves
against the whole document, so `from:[name=q]` matched this new box as well and fired the filter
on every keystroke in it; the filter now says `from:find [name=q]`, which is its own input.

Cmd‑K needs a key handler, and htmx's event filters need `unsafe-eval`, which the policy does not
grant. So one script of our own, `internal/web/static/search.js`, served like `htmx.min.js`
under `script-src 'self'` and loaded from `layout.html` with `defer`:

```js
// Cmd-K or Ctrl-K focuses the search box and selects its text; Escape leaves it (FR-12.1 AC3).
// Nothing else: the search itself is a form the server answers.
(function () {
  "use strict";
  // Ctrl-K inside another text field is the kill-line macOS text fields honour, so it is left to
  // them; Cmd-K is ours everywhere.
  function typingElsewhere(el, box) {
    return !!el && el !== box &&
      (el.isContentEditable || el.tagName === "INPUT" || el.tagName === "TEXTAREA");
  }
  document.addEventListener("keydown", function (e) {
    var box = document.querySelector("input[data-search]");
    if (!box) { return; }
    if ((e.metaKey || e.ctrlKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === "k") {
      if (!e.metaKey && typingElsewhere(document.activeElement, box)) { return; }
      e.preventDefault();
      box.focus();
      box.select();
    } else if (e.key === "Escape" && document.activeElement === box) {
      box.blur();
    }
  });
})();
```

The file is under 1 kB and counts against QS‑2.3's static budget with the other assets.

## 7. Contributors (FR‑12.2)

### 7.1 The domain

`internal/domain/contributors.go`:

```go
// Contributor is one person who opened at least one open item.
type Contributor struct {
	// Login is the GitHub login; "" when GitHub no longer knows the author.
	Login       string
	PRs, Issues int
	// Repos are the repositories they opened something in, in the order of repos, then any the
	// configuration does not name, in first-seen order.
	Repos []string
	// LastActive is the latest UpdatedAt among their items.
	LastActive time.Time
}

// BuildContributors groups items by author. The order is by open items descending, then
// LastActive descending, then Login case-insensitively; the unknown author, if present, is last.
func BuildContributors(items []Item, repos []string) []Contributor
```

### 7.2 The page

`GET /contributors` is a session route through `answeredWaiting`, rendering `contributors.html`
with `Chrome{View: "contributors", Return: "/contributors"}` and:

```go
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
	// ProfileURL is https://github.com/<login>; empty when Unknown.
	ProfileURL string
	// ItemsURL is the search for this login: "/search?q=<login>"; empty when Unknown.
	ItemsURL    string
	PRs, Issues int
	Repos       []string
	LastActive  timeView
}
```

```html
{{define "content"}}
{{with .Contributors}}
{{template "header" .}}
<section class="contributors">
  <h1>Contributors</h1>
  <p class="source-line">{{.CountLine}}</p>
  {{if .Rows}}
  <div class="table-scroll">
  <table class="contributor-table">
    <thead><tr><th scope="col">Contributor</th><th scope="col" class="num">Pull requests</th><th scope="col" class="num">Issues</th><th scope="col">Repositories</th><th scope="col">Last active</th><th scope="col"><span class="visually-hidden">Items</span></th></tr></thead>
    <tbody>
    {{range .Rows}}
    <tr>
      <th scope="row">{{if .Unknown}}<span class="contributor-unknown">unknown</span>{{else}}<a href="{{.ProfileURL}}" target="_blank" rel="noopener noreferrer">{{.Login}}</a>{{end}}</th>
      <td class="num">{{.PRs}}</td>
      <td class="num">{{.Issues}}</td>
      <td>{{range $i, $r := .Repos}}{{if $i}}, {{end}}{{$r}}{{end}}</td>
      <td>{{if .LastActive.Known}}<time datetime="{{.LastActive.Absolute}}">{{.LastActive.Relative}}</time>{{else}}unknown{{end}}</td>
      <td>{{with .ItemsURL}}<a href="{{.}}">items →<span class="visually-hidden"> of this contributor</span></a>{{end}}</td>
    </tr>
    {{end}}
    </tbody>
  </table>
  </div>
  {{end}}
</section>
{{end}}
{{end}}
```

No avatars: `img-src` allows only this site, and relaxing the policy for a decoration is not
worth it. `.table-scroll { overflow-x: auto }` keeps a phone from scrolling the whole page
sideways; the numeric columns are right-aligned in tabular figures.

## 8. Security

Nothing new reaches the page unescaped: the title runs, the count line and every field are
strings `html/template` escapes; `<mark>` is template markup around escaped text. `q` is reflected
only through the same escaping, into an attribute value and the count line. The policy is
unchanged: no inline script or style, `search.js` is a same-origin file (QS‑4.4). `/search` and
`/contributors` are in the route table with the session flag, so
`TestEveryRouteIsEitherDeliberatelyPublicOrRefusesAnonymousAccess` covers them without being
told. The cookie shrinks to its expiry; nothing a visitor submits reaches it any more.

## 9. Documentation

* `04-functional-requirements.md`: FR‑1.2's row becomes the note "FR‑1.2 (NEW and the seen mark)
  was retired on 2026-09-18, see ADR‑0012; the id is not reused." FR‑1.8 AC2 reads "most recently
  updated first; its totals are taken before that cut"; AC4 reads "Both views carry the top bar's
  List | Sites | Contributors switch marking the view being shown, and Refresh returns to the
  view it was pressed on"; AC5 drops "or a new item". FR‑2.1's story drops "without changing what
  counts as new" and AC3 reads "The filter never changes a repository's total." New rows FR‑1.11
  (the top bar) and, under a new epic **E‑12 Search and people**, FR‑12.1 (search) and FR‑12.2
  (contributors), each with the acceptance criteria this design states. The out-of-scope
  paragraph gains "closed items, and NEW (retired 2026-09-18)".
* `01-goals.md`: G‑1 reads "The user sees the open issues and pull requests of the configured
  GitHub repositories, and finds any of them, and the people behind them, in seconds." QG‑1 is
  reworded, not retired, because its two scenarios still hold: "**Completeness** — every open
  item of every watched repository is shown, and a failing repository never hides the others."
* `05-quality-requirements.md`: the quality tree's QG‑1 line reads "Completeness"; QS‑4.1's list
  of session-only POSTs loses `/seen`; QS‑2.3's static budget names `search.js` beside htmx.
* `06-glossary.md`: "Seen mark" and "New" removed; "Other tile" kept with "unused by the shipped
  configuration since 2026-09-18"; new rows "Search", "Reserved word", "Contributor".
* `docs/decisions/0012-no-seen-mark.md`: "No seen mark: the page shows what is open, not what is
  new", superseding the NEW half of 0010; 0010's status line and the index say so.
* `docs/concepts/security-and-tokens.md`: the cookie section describes the expiry-only payload.
* `docs/concepts/configuration.md`: the ten sites and repositories.

## 10. Testing

Domain (`internal/domain`, coverage stays ≥ 90 %): `TestParseQuery` (reserved words, both kinds,
empty), `TestSearchScoresFields` (a table: word start 4, elsewhere 2, contributor 3, label 3,
repository 1, summary 1, sums), `TestSearchRequiresEveryWord`, `TestSearchKindFilters`,
`TestSearchOrdersByScoreThenUpdated`, `TestSearchTitleSpansMergeAndMapBack` (touching and
overlapping spans, a title whose lower-casing changes length gives nil), `TestSearchNeverMutates`;
`TestBuildContributorsGroupsAndOrders` (counts, repos in configuration order, LastActive, the
unknown author last, ties by login case-insensitively). `SortItems` and the tiles' tests lose
their new-first cases and gain updated-first ones.

Web (`internal/web`): `TestSearchPageRendersHitsWithMarks` (a `<mark>` around the matched run,
escaped text intact), `TestSearchPageReflectsNothingUnescaped` (`q` of `<script>` appears only
escaped), `TestSearchWithoutQueryShowsHint`, `TestSearchHtmxSwapReturnsMain`,
`TestSearchDuringFetchShowsWaitPage` and `...AnswersHtmxFromCurrentList`,
`TestContributorsPageListsAuthors`, `TestTopBarCarriesChromeOnlyWhenSignedIn`,
`TestSessionCookieCarriesOnlyItsExpiry` (and the old two-integer payload is refused),
`TestStaticAssetsFitTheirBudgetOnTheWire` extended with `search.js`, the CSP test unchanged and
still passing. `TestEveryRouteIsEitherDeliberatelyPublicOrRefusesAnonymousAccess` picks up the two
routes on its own. The fixture `org-repo.json` gets an item by a second author so the
contributors page has two rows to order.

## 11. Out of scope, deliberately

Closed items and anything not in the snapshot; stemming, fuzzy matching, quoted phrases or a
query language; searching full bodies beyond the stored summary; avatars; a contributor's page
of their own; and NEW in any form — if it returns, it returns as its own design.
