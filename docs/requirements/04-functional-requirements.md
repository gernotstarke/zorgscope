# 4. Functional requirements

Epics (E‑x) with user stories (FR‑x) and acceptance criteria (AC) a test can check.
Priority: **M** must (v1), **S** should (v2), **W** won't.

"The user" is always Gernot (S‑1). Terms in *italics* are defined in the [glossary](06-glossary.md).

The requirements below were renumbered in the 2026-09-15 stateless reset: everything that assumed
a database — build status, refresh runs, Slack notifications, the in-app documentation pages, the
stop control, htmx polling — was retired, and the ids of the stories that described it are not
reused. See [ADR‑0010](../decisions/0010-stateless-no-database.md) for why, and the design's own
"out of scope, deliberately" (`docs/superpowers/specs/2026-09-15-stateless-reset-design.md`, §12)
for what would have to change to bring any of it back.

---

## E‑1 The dashboard

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑1.1 | M | As the user I open one page and see everything at once, so that a glance is enough. | AC1 The dashboard lists the open issues and pull requests of the configured repositories, one section per repository that currently holds at least one item, in configuration order; a repository no longer configured but still holding items from an earlier snapshot appears after the configured ones. AC2 The list is read from an in-memory snapshot at most `github.cache_ttl` old; two page views inside that window share one fetch rather than each fetching on its own. AC3 Each item shows its title, number, URL, author, kind and the age of its creation and last update. AC4 The header states when the snapshot's items were fetched, or "never" before the first fetch has returned anything. |
| FR‑1.2 | M | As the user I see immediately what arrived since I last marked everything seen. | AC1 An item carries `NEW` when it was created after the visitor's seen mark; the mark is zero on a fresh sign-in, so a first visit shows nothing as new. AC2 The header's `NEW` total and each repository's `NEW` count are taken before any filter is applied; narrowing the list never changes either, and the browser tab title is prefixed with the total when it is greater than zero. AC3 "Mark all seen" (`POST /seen`) sets the seen mark to the fetched-at time of the list the visitor was actually shown, not to the moment of the click, clamped to now; a missing, unparsable or future value falls back to now; and the mark never moves backwards, so pressing the button in a tab showing an older list keeps the later mark the session already carries. AC4 After marking seen, no item is `NEW` until something created after the new mark arrives; the mark lives inside the signed session cookie, so it needs no JavaScript and is never usable to move itself later than the click. |
| FR‑1.3 | M | As the user I can fetch immediately rather than wait for the cache to go stale. | AC1 "Refresh" (`POST /refresh`) invalidates the cached list and redirects to the page, which then fetches instead of serving the cache. AC2 The action is a plain form behind the session, so it works without JavaScript and is refused to an anonymous visitor. |
| FR‑1.4 | M | As the user I notice when the data is stale or GitHub is failing, without losing what I already had. | AC1 A failed fetch keeps the previous items; the list is never blanked by an upstream failure. AC2 A first fetch that fails — before anything has ever been fetched — yields an empty list with the notice rather than an error page. AC3 The notice states since when GitHub has been failing and, once there is one, when the list being shown was last fetched; the upstream error text is scrubbed of every configured secret before it is shown. AC4 A repository that fails on its own does not blank the others: the repositories that did fetch are shown current, alongside the notice for the one that did not, and the one that did not keeps its previous items; such a partial fetch does not advance the list's fetched-at time (unless nothing had been fetched before), so "Mark all seen" never acknowledges items that failed to arrive. |
| FR‑1.5 | M | As the user I want the page to work in light and dark appearance. | AC1 The page follows the operating-system appearance by default; a control cycles system → light → dark → system and is remembered in a cookie for a year. AC2 The control returns the visitor to the local path it was pressed on; an absolute URL, a protocol-relative URL, or any other value that is not an ordinary local path falls back to the dashboard rather than being followed, so the control cannot be turned into an open redirect. |
| FR‑1.8 | M | As the user I see each site's open pull requests and issues at a glance. | AC1 `/sites` shows one tile per site configured in `github.sites`, in configuration order, and — when some watched repository is claimed by no site — a last tile called Other holding those repositories; a site with nothing open keeps its tile. AC2 A tile lists at most three pull requests and four issues, new first, then most recently updated; its totals and new count are taken before that cut. AC3 When a tile has cut something, it links to the list filtered to its repository — on Other, one link per repository. AC4 Both views carry a List \| Sites switch marking the view being shown, and "Mark all seen" and "Refresh" return to the view they were pressed on. AC5 Each tile carries its site's colour from the arc42 brand registry, text on and inside a tile keeps a contrast of at least 4.5:1 in both appearances, and colour is never the only way a site or a new item is told apart. |

## E‑2 Filters

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑2.1 | M | As the user I narrow the list by repository, kind, creation date and text, without changing what counts as new. | AC1 The list can be narrowed by repository, by kind (issue or pull request), by items created since a date, and by a case-insensitive text search over title and summary; every axis is optional and the axes combine. AC2 The filter is carried in the URL's query string, works as a plain GET form without JavaScript, and the htmx fragment the same form also drives (`GET /items`) renders identically to the page's own list. AC3 The filter never changes the `NEW` total or a repository's `NEW` count, including when it matches nothing in that repository. AC4 A date is read in the configured timezone rather than UTC, so "since today" means the visitor's own day. |

## E‑8 Configuration and access

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑8.1 | M | As the operator I configure what is watched in one YAML file and supply every secret through the environment. | AC1 `config/zorgscope.yaml` lists the timezone, the repositories to watch, the repository whose push access admits a visitor, and how long a fetched list may be reused before a page view refetches it; it contains no secret value. AC2 `GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET` and `GITHUB_TOKEN` come from the environment (Fly secrets in production); the process refuses to start without any one of them, naming which is missing. AC3 An invalid file — an unknown field, an empty repository list, a malformed `cache_ttl`, a repository not in `owner/name` form — aborts start-up with a message naming the offending field, never a value. AC4 No secret value is ever logged, returned in a response, or otherwise rendered. AC5 `github.sites` is optional; a site with an empty or duplicate name, an address that is not an absolute `https` URL, a repository not in `owner/name` form, not listed in `github.repos` or claimed by a second site, a colour outside the fixed palette, or a tag longer than three characters aborts start-up with a message naming the offending field. |
| FR‑8.3 | M | As the user I sign in with my GitHub account, once per browser, and only a collaborator with push access is admitted. | AC1 An unauthenticated browser request to the dashboard is redirected to a sign-in page, not answered with a bare 401; a non-navigation request (an htmx fragment or a form action) is refused with 401 instead, since redirecting it would paint the sign-in page into a tile. AC2 Signing in with GitHub through the registered OAuth App, with no scopes requested, issues a signed, HttpOnly, SameSite=Lax session cookie whose payload is nothing but an expiry and the seen mark; the visitor's GitHub token is used for one permission check and never stored. AC3 Only a GitHub user with push or admin permission on the configured `auth_repo` is admitted; anyone else is refused with a page that says so and receives no session. AC4 The cookie's signing key derives from the OAuth client secret, so rotating the secret invalidates every session immediately. AC5 Refused sign-in callbacks are rate-limited by an in-memory bucket keyed by client IP, and are logged with the reason but never the authorization code, the state or the visitor's token. |

## Explicitly out of scope

Build status per repository, Slack notifications, item-level dismissals, history of any kind,
background refresh, a runtime configuration page, and everything else a database would be needed
for — see [ADR‑0010](../decisions/0010-stateless-no-database.md). News and RSS feeds, "unanswered"
detection, daily snapshots, credential and TLS expiry watching, passkey authentication, a native or
Wails client, and a write-capable public JSON API were already out of scope before the stateless
reset. Site statistics (Plausible) and task lists (Todoist) were removed 2026-09-14 after a month of
not being looked at.
