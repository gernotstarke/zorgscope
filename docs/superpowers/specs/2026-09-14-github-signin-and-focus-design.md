# GitHub sign-in and a GitHub-only dashboard — design

Written 2026-09-14. Approved in conversation before this file was written. It changes three things
the [reset design](2026-08-17-zorgscope-reset-design.md) decided, and leaves everything else in that
document standing: the runtime (§3), the structure (§4), the store and the first-seen invariant (§5),
the refresh trigger, and the documentation served at `/docs`.

## 1. What changes and why

1. **Sign-in becomes "sign in with GitHub", admitting only people with push access to the zorgscope
   repository.** The shared token of ADR‑0007 worked, but it is a 32-character secret to paste into a
   phone, and it names nobody. GitHub already knows who may push to `gernotstarke/zorgscope`; that is
   exactly the set of people who should see the dashboard, and it is maintained in one place that is
   not this repository.
2. **Plausible and Todoist are removed, from the backend and the client.** After a month of use the
   page is opened for one question — which issues and pull requests of the arc42 sites need
   attention — and the other two tiles were glanced past. Removing them is not deferring them: the
   adapters, fixtures, tables, tiles and requirements go, and adding either back later is a new
   source under QS‑5.5, not a revert.
3. **The front page becomes one list, grouped by repository, with filters.** With a single source the
   tile grid no longer earns its layout. The page shows every open issue and pull request, a section
   per repository, new items first, and four filters — repository, kind, created since, and text —
   so that "the PRs on the quality site opened this month" is a URL rather than a scroll.

Slack notifications are untouched. The refresh trigger, the refresh secret and cron-job.org are
untouched. The GitHub data token (`GITHUB_TOKEN`) stays a backend secret: the cron-triggered refresh
runs when nobody is signed in, so it cannot borrow a visitor's token. That was the one fork in this
design and it was decided in favour of the simpler option.

## 2. Sign-in with GitHub

### The flow

```text
GET  /login              public   the sign-in page: one button, "Sign in with GitHub"
POST /auth/github        public   sets a state cookie, redirects to GitHub's authorize URL
GET  /auth/callback      public   verifies state, exchanges the code, checks push access,
                                  sets the session cookie, redirects to /
```

* `POST /auth/github` generates 32 random bytes, stores them base64-encoded in a cookie named
  `zorgscope_oauth_state` (HttpOnly, Secure, SameSite=Lax, ten minutes), and redirects to GitHub's
  authorization endpoint with `client_id`, `state` and **no scopes**. zorgscope is a public
  repository, and reading a public repository's metadata — including the authenticated user's own
  permission on it — needs no scope. The App therefore asks the user for nothing beyond their
  identity.
* `redirect_uri` is **not sent**. GitHub then uses the callback registered on the OAuth App. A GitHub
  OAuth App has exactly one callback URL, which is why there is one App per environment (§6), and it
  is also why the backend never has to know its own public URL or trust a `Host` header.
* `GET /auth/callback` compares the `state` query parameter with the cookie in constant time, clears
  the cookie, and exchanges `code` for an access token at GitHub's token endpoint using
  `golang.org/x/oauth2` (already a dependency).
* With that token the backend makes **one** request: `GET {api}/repos/{auth_repo}`. GitHub's
  response carries a `permissions` object for the authenticated user (`admin`, `maintain`, `push`,
  `triage`, `pull`). The visitor is admitted when `push` or `admin` is true. Anything else —
  `pull` only, a missing `permissions` block, a non-200 response — is refused with a page that says
  "This dashboard is for collaborators of `gernotstarke/zorgscope`" and sets no session. The
  visitor's token is used for that request and then dropped; it is never stored, logged or rendered.
* A refused callback, a bad state, or a failed exchange counts against the same in-memory rate
  limiter that failed token sign-ins did (QS‑4.2), keyed by client IP, and is logged without the
  code, the state or the token.

### The session

The cookie keeps its shape from ADR‑0007: `base64(expiry) + "." + base64(HMAC-SHA256(key, expiry))`,
HttpOnly, Secure, SameSite=Lax, thirty days. Only the key changes:

```text
key = SHA256("zorgscope-session-v2" + GITHUB_OAUTH_CLIENT_SECRET)
```

Rotating the client secret on GitHub and on Fly signs every browser out at once, which is the
property ADR‑0007 valued and the property this design keeps. There is no session table and no
sign-out control; the cookie holds no identity, because the product has no per-user state and no
audit need beyond "a collaborator signed in", which is logged at the callback together with the
permission level and the client IP, and never with a login name the visitor did not choose to
publish.

### Configuration

| Setting | Where | Meaning |
|---|---|---|
| `GITHUB_OAUTH_CLIENT_ID` | environment | the OAuth App's client id; required |
| `GITHUB_OAUTH_CLIENT_SECRET` | environment | the OAuth App's client secret; required, and the session key's seed |
| `GITHUB_OAUTH_BASE_URL` | environment, optional | where the authorize and token endpoints live; `""` means `https://github.com`; `make fakes` sets it |
| `github.auth_repo` | `config/zorgscope.yaml` | the repository whose push access admits a visitor, `owner/name`; required |

`ZORGSCOPE_TOKEN` is removed from the configuration, the environment template, the Makefile's
start-up check and the Fly secrets. `web.New` refuses to start without both OAuth values, as it
refused to start without the token.

## 3. Removing Plausible and Todoist

Removed outright, with their tests: `internal/adapters/plausible`, `internal/adapters/todoist`, the
Plausible and Todoist handlers and fixtures in `internal/fakesources`, `domain.Metric`,
`domain.KindTask`, the `DueAt` and `Priority` fields of `domain.Item`, `FetchResult.Metrics`,
`Store.UpsertMetrics` and `Store.Metrics`, the `sites` and `tasks` tiles and their templates, the
`plausible:` and `todoist:` sections of the YAML file, and the `PLAUSIBLE_*` and `TODOIST_*`
environment variables.

Migration `0005_github_only.sql` drops the `metrics` table, deletes items whose source is `todoist`
and source-state rows for `plausible` and `todoist`, and drops the `due_at` and `priority` columns
from `items`. It is a one-way migration, like every other one here (ADR‑0005).

`Item.Repo` now means exactly what its name says. The comment that apologised for it holding a
Todoist project name goes with the field's second meaning.

The fake sources server keeps its GitHub handlers and its control routes, and gains one page:
`GET /` answers with plain text listing every path it serves. Opening `http://localhost:9090` in a
browser then explains the server instead of returning 404, which is what happened before and what
prompted the question.

## 4. The front page

### Domain

`BuildDashboard` no longer assembles tiles. `Dashboard` gains:

```go
type Dashboard struct {
    // … header fields, Builds, Problems, ProblemCount as before …
    NewTotal int          // new items across every stored item, unfiltered
    Total    int          // stored items, unfiltered
    Shown    int          // items that passed the filter
    Filter   Filter       // the filter that was applied, echoed back for the form
    Groups   []RepoGroup  // one per repository that has at least one item passing the filter
    Source   SourceHealth // the GitHub source: disabled, failing (with the error), stale or ok
}

type SourceHealth struct {
    Disabled bool
    Stale    bool
    Error    string    // the last error when the source is failing, otherwise ""
    LastOKAt time.Time // zero when it never succeeded
}

type RepoGroup struct {
    Repo     string
    Items    []Item // new first, then most recently updated (SortItems)
    NewCount int
    Total    int    // items of this repository before filtering
}

type Filter struct {
    Repo         string    // "" means every repository
    Kind         Kind      // "" means issues and pull requests
    CreatedSince time.Time // zero means no lower bound
    Text         string    // "" means no text filter
}

func (f Filter) Match(it Item) bool
func (f Filter) Empty() bool
```

`Match` is the only rule: repository equality, kind equality, `CreatedAt` not before
`CreatedSince`, and a case-insensitive substring match of `Text` against title and summary. Groups
follow configuration order (`DashboardInput.Repos`), with any repository that has items but is no
longer configured after them, so the page reads in the order the operator wrote. `NewTotal` is
counted over every item regardless of the filter: the tab title and the summary line state what is
new, not what is visible, and a filter must never make the badge lie.

`Tile`, `tileOrder`, `tileSource`, `tileTitle`, `githubTileLimit`, `firstN`, `sortTasks` and
`siteMetrics` are deleted. `sourceOrder` shrinks to `github`, `builds` and the notifier. There is no
truncation any more: the page shows every open item that matches, and the count beside each
repository name says how many that is.

### Routes and templates

| Route | Auth | What |
|---|---|---|
| `GET /` | page | the dashboard, filters read from the query string |
| `GET /items` | fragment | the list section alone, same query string, plus the out-of-band title, summary, alert and build status the tile fragment already carried |

`GET /tile/{source}` is removed. The list section polls `GET /items?<current query>` at the
configured interval, exactly as a tile polled before, so a page left open still updates and still
never counts as a visit (FR‑1.6 AC2).

The filter bar is a `<form method="get" action="/">` above the list with four controls:

| Control | Query parameter | Form |
|---|---|---|
| Repository | `repo` | a `<select>` of the configured repositories plus "all" |
| Kind | `kind` | three radio buttons: all, issues, pull requests |
| Created since | `since` | `<input type="date">`, `YYYY-MM-DD`, interpreted in the configured timezone |
| Text | `q` | `<input type="search">` |

It works with JavaScript disabled: submitting reloads `/` with the parameters in the URL, and every
filtered view is a bookmarkable address. htmx enhances it: the form carries `hx-get="/items"`,
`hx-trigger="change, keyup changed delay:400ms from:[name=q]"`, `hx-target="#items"`,
`hx-swap="outerHTML"` and `hx-push-url="true"`, so changing a control swaps the list and updates the
address bar without a visible reload. The CSP is unchanged: htmx is vendored, the attributes are
markup, and there is no inline script.

An unparseable `since` or an unknown `kind` is ignored, not an error: the page renders unfiltered on
that axis and the control shows its empty state. A `repo` that is not configured is kept — it may
name a repository that still has stored items — and simply matches nothing if there are none.

The templates `tiles/tile.html`, `tiles/github.html`, `tiles/sites.html` and `tiles/tasks.html` are
replaced by `fragments/items.html` (the list section: filter echo, source-health notice, groups) and
the surviving shared blocks (`counts.html`, `alert.html`, `build-status.html`) move to
`templates/fragments/`. The source-health line that the GitHub tile's header carried — disabled,
failing with the error text, stale, or the age of the data — sits once above the list, in the same
words.

The `.impeccable.md` design context applies: a compact instrument, the lime focus point as the one
accent, new / stale / failing readable in a three-second glance, light and dark, no colour as the
only signal.

## 5. Documentation

* **Requirements.** The vision names GitHub only. G‑2 and G‑3 are struck through in the goals table
  with the date, not deleted. E‑3 and E‑4 are removed and their ids retired; FR‑1.1 describes the
  list and the filters; FR‑1.2 AC2 counts per repository; FR‑8.1, FR‑8.2 and FR‑9.2 lose the two
  sources; FR‑8.3 becomes GitHub sign-in with the push-access rule; FR‑9.1 and FR‑9.3 match the
  six-target Makefile. The glossary's *Item*, *Source* and *Fake sources* entries follow. S‑3 and
  C‑8 lose the two services. The out-of-scope list gains "site statistics and task lists".
* **Decisions.** ADR‑0009 "GitHub sign-in gated on push access to the repository" supersedes
  ADR‑0007, which is marked superseded and left in place. The index gains the row.
* **Concepts.** `security-and-tokens.md` describes the OAuth pair, the derived session key, what the
  callback logs, and rotation of the client secret. `configuration.md` shows the reduced YAML, the
  new environment table, and the registration steps for the two OAuth Apps.
* **README.** The feature list and the package table lose the two sources; the quick start names
  the OAuth pair.
* The v1 plan and the handover are history and are not edited; this spec and its plan say what
  changed.

## 6. What the operator does once

Register two GitHub OAuth Apps under the account that owns `gernotstarke/zorgscope`
(Settings → Developer settings → OAuth Apps → New):

| | Local | Production |
|---|---|---|
| Homepage URL | `http://localhost:8080` | `https://zorgscope.fly.dev` |
| Authorization callback URL | `http://localhost:8080/auth/callback` | `https://zorgscope.fly.dev/auth/callback` |

Copy the client id and a generated client secret into `.env` locally, and set them as Fly secrets
for production. Remove `ZORGSCOPE_TOKEN` from both. Nothing else changes for cron-job.org.

## 7. Testing

* **Domain.** Table tests for `Filter.Match` on every axis and their combination, for group order
  and counts, and for `NewTotal` staying unfiltered. The 90 % domain coverage gate stays.
* **Fake sources.** The fake GitHub gains `GET /login/oauth/authorize` (redirects straight back to
  the callback with `code=fake-code` and the given `state`), `POST /login/oauth/access_token`
  (answers a fixed token for `fake-code`, 400 otherwise), and `GET /repos/{owner}/{repo}`
  (answers the repository with a `permissions` block). A control route
  `POST /_control/oauth-user?permission={admin|push|pull|none}` decides what the next sign-in is,
  so one server exercises the collaborator, the stranger and the missing block.
* **Web.** The sign-in tests are rewritten against an in-process fake GitHub: a collaborator gets
  the cookie, a `pull`-only user does not, a mismatched state does not, a failed exchange does not,
  refusals are rate-limited, and no response or log line ever contains the code, the state, the
  token or the client secret (the existing canary test extends to the new secrets). The route
  table test still proves every route is either deliberately public or refuses anonymous access.
  The dashboard tests cover the filters through the query string, the fragment route, and the
  out-of-band blocks.
* **Store.** Migration 0005 is applied to a database holding a Todoist item and a metrics row, and
  afterwards neither exists and the GitHub items are untouched, first-seen times included.
* `make check` is green at the end of every task.

## 8. Risks

* **The `permissions` block on an unscoped token.** GitHub documents the block on the repository
  response for an authenticated user; if a production sign-in ever comes back without it, the
  callback refuses (fail closed) and logs that the block was missing, and the fix is to request the
  `read:org` or `repo` scope. The plan's last task verifies the real flow by hand before this is
  merged.
* **Two OAuth Apps to keep straight.** A local `.env` holding the production App's values would
  send a local sign-in to the production callback. Mitigated by the `.env` template naming which
  App each value comes from, and by the configuration concept listing both Apps side by side.
* **A collaborator with `maintain` but not `push`.** GitHub's `maintain` implies `push` in the
  permissions block, so the rule holds; the test fixture includes that case.
