# 4. Functional requirements

Organised as epics (E‑x) with user stories (FR‑x). Each story has acceptance criteria (AC) that a test can
verify. Priority: **M** must (v1), **S** should, **C** could, **W** won't (now).

"The user" is always Gernot (S‑1). "Attention item" is defined in the [glossary](07-glossary.md).

---

## E‑1 Dashboard shell

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑1.1 | M | As the user I open a new browser tab and see the dashboard immediately, so that I don't have to navigate anywhere. | AC1 `GET /` renders the complete dashboard from cached data without waiting for any upstream call. AC2 The page is usable as a browser new‑tab/homepage URL (no query parameters required, no interstitials after login). |
| FR‑1.2 | M | As the user I see the dashboard as a grid of tiles with a header, so that information is grouped and scannable. | AC1 Header shows current date, weekday, "data as of hh:mm" and one staleness indicator per source. AC2 Tiles: Attention, Repositories, Sites, Todoist, News, Watch (credentials & health) — order configurable, default as listed. AC3 Layout adapts from 1 column (≤ 640 px) to 4 columns (≥ 1600 px). |
| FR‑1.3 | M | As the user I want each tile to refresh itself in the background, so that the page stays current while it stays open. | AC1 Each tile is an htmx fragment polled every *n* seconds (configurable, default 60 s) from `GET /tiles/{name}`. AC2 A failed poll leaves the last content and shows a warning badge; it never blanks the tile. |
| FR‑1.4 | M | As the user I can trigger an immediate refresh of all sources, so that I don't have to wait for the next poll. | AC1 A "refresh" control issues `POST /refresh` which enqueues a fetch of every source (respecting a min‑interval of 30 s per source) and returns 202. AC2 Header shows "refreshing…" until all fetches finish. |
| FR‑1.5 | S | As the user I want light and dark appearance following my OS, so that the tab is comfortable day and night. | AC1 `prefers-color-scheme` switches themes; no flash of wrong theme. |
| FR‑1.6 | C | As the user I want to reorder or hide tiles. | Configuration‑only in v1 (`tiles:` order list in config); drag & drop is W. |

## E‑2 GitHub attention (core, G‑1)

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑2.1 | M | As the user I see every **open issue and PR** of every monitored repository, so that I have the full picture. | AC1 For each configured repo all open issues and PRs (any author) are fetched incl. number, title, url, author, created, updated, labels, comment count, last comment author & time, draft flag (PR), review decision (PR). AC2 Pagination is handled (repos with > 100 open items). |
| FR‑2.2 | M | As the user I see items that are **new** since the previous snapshot highlighted, so that I never miss them. | AC1 An item is *new* iff its id is absent from the previous daily snapshot of that source (see E‑7). AC2 New items carry a `NEW` badge and are sorted first within their age bucket. AC3 On the very first run nothing is marked new except items younger than 24 h. |
| FR‑2.3 | M | As the user I see items that are **unanswered**, so that contributors get a quick reaction. | AC1 An open issue/PR is *unanswered* iff it has no comment/review from anyone other than its author **and** it is older than the configured grace period (default 4 h). AC2 Comments/reviews by the configured "me" login or by any repo collaborator with write access count as answers; comments by bots (`[bot]` suffix) do not. AC3 Badge `UNANSWERED`, colour distinct from `NEW`. |
| FR‑2.4 | M | As the user I see the **age** of each item at a glance. | AC1 Age buckets: `< 24 h`, `< 7 d`, `< 30 d`, `≥ 30 d`; each with its own colour token. AC2 Items with no activity for ≥ 30 d are additionally marked `STALE` and listed in a collapsed section. |
| FR‑2.5 | M | As the user I see one **Attention tile** aggregating everything that needs me across all repos, so that I look in one place. | AC1 Contains all items with level `new` or `unanswered` plus mentions/review requests (FR‑2.6), grouped by repo or flat (config), newest first. AC2 Each row: repo short name, `#number`, title, author, age, badges, dismiss control. AC3 Empty state text "Nothing needs your attention 🎉". AC4 Row count is capped (default 30) with "+ n more" link to the repo. |
| FR‑2.6 | M | As the user I see **mentions and review requests** addressed to me anywhere on GitHub, so that requests outside the monitored repos are not lost. | AC1 GitHub notifications with reason `mention`, `review_requested`, `assign`, `author`(replies) for the configured login are fetched and shown as attention items with source "GitHub mentions". AC2 Deduplicated against items already present from monitored repos. |
| FR‑2.7 | M | As the user I can **dismiss** an attention item or a whole tile ("seen"), so that highlights disappear before the next snapshot. | AC1 `POST /dismiss` with item id stores a dismissal with timestamp and the item's `updated_at`. AC2 A dismissed item loses `NEW`/`UNANSWERED` badges and leaves the Attention tile. AC3 If the item's `updated_at` later changes (new comment, edit) the dismissal expires and the item reappears with the applicable badges. AC4 "Dismiss all" on a tile dismisses every currently shown item of that tile. |
| FR‑2.8 | S | As the user I want to filter the Attention tile to issues only / PRs only / a repo. | Client‑side toggle; state kept in URL fragment. |
| FR‑2.9 | C | As the user I want to see my own open PRs' review status. | Included in FR‑2.1 data (`reviewDecision`), rendered as small icon. |

## E‑3 Repository overview and CI status

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑3.1 | M | As the user I see one card per monitored repository with open counts, so that I know where activity is. | AC1 Card shows: short name (link), open issues, open PRs, count of new, count of unanswered. AC2 Cards ordered by attention count desc, then name. |
| FR‑3.2 | M | As the user I see the **build status** of each repository, so that broken site builds are noticed. | AC1 Latest completed workflow run on the default branch: conclusion (success/failure/cancelled/…), workflow name, finished‑at age; rendered as green/red/grey dot with tooltip and link. AC2 A run in progress is shown as pulsing dot with the previous conclusion. AC3 Failure counts as an attention item (`BUILD FAILED`) once per run id. |

## E‑4 Site statistics (Plausible)

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑4.1 | M | As the user I see visitors and pageviews for each configured site for the last 7 and 30 days, so that I notice trends. | AC1 Per site: visitors 7 d, visitors 30 d, pageviews 30 d, each with % change vs previous period (↑/↓, colour). AC2 A daily‑visitors sparkline for the last 30 days. |
| FR‑4.2 | S | As the user I see the top pages of each site (last 7 days). | AC1 Top 3 pages by visitors with counts, expandable to 10. |
| FR‑4.3 | S | As the user I want site tiles ordered by 7‑day visitors desc. | AC1 Ordering configurable: by visitors, by config order. |
| FR‑4.4 | C | As the user I want a combined "all arc42 sites" total. | Sum row when ≥ 2 sites share a group label in config. |

## E‑5 Todoist

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑5.1 | M | As the user I see all tasks due in the next 7 days grouped by day, so that I can plan. | AC1 Groups: Overdue, Today, Tomorrow, then weekday names; empty groups hidden except Today. AC2 Task row: content, project name, priority colour (p1‑p4), due time if any, labels; click opens task in Todoist. AC3 Recurring tasks show only their next occurrence. |
| FR‑5.2 | M | As the user I see **overdue** tasks first and clearly marked. | AC1 Overdue group is first, red accent, count in tile title. |
| FR‑5.3 | W | Complete / reschedule from the dashboard. | Explicitly read‑only in v1 (write scope not requested). |

## E‑6 News feeds

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑6.1 | M | As the user I see the newest articles from a configurable list of RSS/Atom feeds, so that I follow selected topics without a feed reader. | AC1 Feeds configured with url, display name, optional topic tag, optional max items. AC2 Items merged, deduplicated by canonical URL, sorted by published desc; default 20 shown. AC3 Row: title (link), source name, topic tag, age; `NEW` badge per E‑7 rules; dismissable. |
| FR‑6.2 | S | As the user I want simple include/exclude keyword filters per feed or globally. | AC1 Case‑insensitive substring match on title + summary; filtered items are not stored. |
| FR‑6.3 | S | As the user I want to group the news tile by topic tag. | AC1 Toggle grouped/flat; config default. |
| FR‑6.4 | W | LLM summarisation / ranking. | Design keeps a hook (`Enricher` port) but no implementation. |

## E‑7 New‑detection, snapshots and dismissals (cross‑cutting)

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑7.1 | M | As the system I take a **daily snapshot** per source of all item ids currently present, so that "new since yesterday" is well‑defined. | AC1 Snapshot time configurable (default 03:00, timezone configurable, default Europe/Berlin). AC2 If the app was down at snapshot time, the snapshot is taken at next start (catch‑up), at most one per calendar day. AC3 Snapshots are retained for a configurable number of days (default 30) and pruned. |
| FR‑7.2 | M | As the system I define **new** as "present now, absent in the most recent snapshot older than the current one" and this rule is identical for issues, PRs, mentions, feed items and workflow runs. | AC1 Implemented once in the domain package, covered by property‑style tests (see QS‑1.x). |
| FR‑7.3 | M | As the user I want dismissals to survive restarts and to expire when an item changes (see FR‑2.7). | AC1 Persisted in SQLite. AC2 Dismissal keyed by (source, external id, updated‑at). |
| FR‑7.4 | S | As the user I want to see what changed since I last looked, i.e. a "since last visit" marker in addition to the daily snapshot. | AC1 The time of the last authenticated page load is stored; items created after it get a subtle dot even if not `NEW` by snapshot. |

## E‑8 Configuration and extensibility (QG‑4)

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑8.1 | M | As the operator I configure sources, intervals and display options in one YAML file, so that adding a repo/site/feed needs no code. | AC1 `config/zorgscope.yaml` schema documented and validated at start; invalid config aborts start with a message naming the offending key. AC2 Sections: `github` (login, repos, poll interval, grace period), `plausible` (sites, interval), `todoist` (interval, horizon days), `feeds` (list, interval), `snapshot` (time, tz, retention), `ui` (tile poll seconds, attention cap, theme), `server` (port, base url). AC3 Per‑source `poll_interval` overrides the kind default. |
| FR‑8.2 | M | As the operator I supply secrets **only** via environment variables. | AC1 `GITHUB_TOKEN`, `PLAUSIBLE_API_KEY`, `TODOIST_TOKEN`, `SESSION_SECRET`, `ENROLL_TOKEN`; missing required secret → clear startup error (unless the source is disabled). AC2 Secrets are never written to logs, HTML, or the database (verified by tests). |
| FR‑8.3 | M | As the operator I can disable any source kind without removing its config. | AC1 `enabled: false` hides the tile and skips fetching. |
| FR‑8.4 | S | As a developer I can add a new **kind** of source by implementing one interface and registering it. | AC1 `SourceFetcher` port + adapter package + tile template + fake in `cmd/fakesources`; documented in `docs/guides/adding-a-source.md` (written during implementation). |
| FR‑8.5 | S | As the operator I want the app to hot‑reload the YAML config on SIGHUP or on file change, so that changes locally are instant. | AC1 Reload validates first; on error keeps old config and logs. (On fly.io changes still go through deploy.) |

## E‑9 Authentication and session

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑9.1 | M | As the user I log in with a **passkey**, so that the hosted dashboard is private without passwords. | AC1 WebAuthn registration and authentication ceremonies (platform and roaming authenticators, discoverable credentials preferred). AC2 Successful login sets a session cookie (HttpOnly, Secure, SameSite=Lax, configurable lifetime, default 90 d). AC3 Unauthenticated `GET /` redirects to `/login`; `/tiles/*`, `/dismiss`, `/refresh` return 401. |
| FR‑9.2 | M | As the user I **enrol** my first passkey using a one‑time enrolment token, so that nobody else can register. | AC1 `GET /enroll?token=…` valid only if token equals `ENROLL_TOKEN` (constant‑time compare) **and** no credential exists yet, or the user is already logged in (adding another passkey). AC2 Multiple passkeys can be enrolled and listed/removed on `/account`. AC3 Rotating `ENROLL_TOKEN` + deleting credentials (documented CLI/`make` recipe) is the recovery path. |
| FR‑9.3 | M | As the user I can log out and revoke all sessions. | AC1 `POST /logout` deletes the current session; `/account` offers "log out everywhere". |
| FR‑9.4 | M | Local development must not require passkeys. | AC1 `AUTH_MODE=dev` (only honoured when `server.base_url` is `http://localhost*`) auto‑authenticates; production refuses `dev` mode. |
| FR‑9.5 | C | Password + TOTP as fallback. | Not planned; passkeys only (see ADR‑0006). |

## E‑10 Operations and observability

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑10.1 | M | As the operator I see per‑source health in the UI and on a status endpoint. | AC1 `GET /status` (authenticated) lists each source with last success, last error, next run, items count. AC2 `GET /healthz` (liveness, unauthenticated, no data) and `GET /readyz` (ready when config loaded and DB open). |
| FR‑10.2 | M | As the operator I get structured logs. | AC1 JSON logs (slog), level configurable, secrets redacted, request logs with duration and status. |
| FR‑10.3 | M | As the operator I run everything locally with `make app`. | AC1 `make app` builds the image, starts app + SQLite volume, prints URL. AC2 `make test`, `make lint`, `make e2e`, `make deploy` work with only Docker + make installed. |
| FR‑10.4 | M | As the operator I want CI to run lint, tests, e2e and deploy on `main`. | AC1 GitHub Actions workflow per [ADR‑0010](../architecture/decisions/ADR-0010-testing-strategy.md); deploy needs `FLY_API_TOKEN` repo secret. |
| FR‑10.5 | S | As the operator I want the SQLite database backed up. | AC1 fly volume daily snapshots enabled; `make fly ARGS="ssh sftp get …"` recipe documented. |
| FR‑10.6 | C | Slack notification for new attention items. | `Notifier` port; adapter later. |

## E‑11 Credential expiry and health watch (G‑6)

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑11.1 | M | As the operator I register credentials (tokens, API keys, certificates, domains) with their expiry date, so that I am warned before they expire and break an app. | AC1 Config `watch.credentials`: list of `{name, expires: YYYY-MM-DD, warn_days (default 14), used_by (free text, e.g. "status.arc42.org"), url (optional link to renewal page)}`. AC2 The Watch tile lists them sorted by remaining days with a countdown; `EXPIRING` badge when `remaining ≤ warn_days`, `EXPIRED` when past. AC3 `EXPIRING`/`EXPIRED` are attention items (appear in the Attention tile), dismissable like others (dismissal keyed to the expiry date, so a renewed date revives nothing wrongly). |
| FR‑11.2 | M | As the operator I want zorgscope's **own** GitHub token expiry detected automatically, so that the dashboard does not silently go dark. | AC1 The GitHub adapter reads the `GitHub-Authentication-Token-Expiration` response header on every call and stores the date as a synthetic credential "zorgscope GitHub token". AC2 Same rules as FR‑11.1 (warn ≥ 14 d). AC3 If the header is absent (non‑expiring token) the entry shows "no expiry". |
| FR‑11.3 | M | As the user I see **authentication failures** of any source immediately, so that a revoked/expired token is noticed the same hour. | AC1 An `ErrAuth` (401/403 non‑rate‑limit) from any adapter sets the source's status to `auth_failed` and creates an attention item `AUTH FAILED: <source>` (once per failure streak). AC2 The tile of that source shows a red badge with the error and the age of last good data. |
| FR‑11.4 | S | As the operator I register URLs of apps I run (e.g. `https://status.arc42.org`) for a simple health check, so that a broken app is noticed. | AC1 Config `watch.urls`: `{name, url, expect_status (default 200), expect_body_contains (optional), poll_interval}`; HEAD/GET with 10 s timeout. AC2 Down or unexpected → attention item `DOWN: <name>` after 2 consecutive failures; tile shows last OK time and response time. AC3 TLS certificate expiry of each URL is recorded and treated like FR‑11.1 (`warn_days` 14). |
| FR‑11.5 | C | Automatic listing of fine‑grained PATs granted to the `arc42` org (`GET /orgs/arc42/personal-access-tokens`) with their expiry. | Needs org‑admin token; optional later. |
