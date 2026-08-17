# 4. Functional requirements

Organised as epics (E‑x) with user stories (FR‑x). Each story has acceptance criteria (AC) that a test can
verify. Priority: **M** must (v1), **S** should, **C** could, **W** won't (now).

"The user" is always Gernot (S‑1). "Attention item" is defined in the [glossary](07-glossary.md).

---

## E‑1 Client-neutral dashboard API

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑1.1 | M | As a visual client I obtain the complete current dashboard immediately, so that no upstream request delays presentation. | AC1 Authenticated `GET /api/v1/dashboard` returns one presentation-ready JSON snapshot from SQLite only. AC2 The response contains `generated_at`, per-source freshness/error state, attention, repositories, Plausible sites and watch entries. AC3 Stable identifiers and explicit schema version make the contract usable by Wails and browser clients. |
| FR‑1.2 | M | As the user I see information grouped and scannable in any supported client. | AC1 The API exposes sections for Attention, Repositories, Sites and Watch; no Todoist or News section. AC2 Each section has explicit content, empty, stale and error states. AC3 Layout is a client concern; no domain rule depends on tiles or HTML. |
| FR‑1.3 | M | As a client I keep the view current without losing cached content. | AC1 The dashboard supports `ETag`/`If-None-Match` and a configurable client polling hint. AC2 A later event stream may signal invalidation but never carries the sole copy of state. AC3 A backend refresh failure retains the last successful content and its error/freshness metadata. |
| FR‑1.4 | M | As the user I can trigger an immediate refresh of all sources. | AC1 Authenticated `POST /api/v1/refresh` enqueues every enabled source, respects the configured minimum interval and returns 202. AC2 Subsequent dashboard/status responses expose refresh progress. |
| FR‑1.5 | S | As the user I want light and dark visual appearances. | AC1 Each implemented visual client follows the OS appearance without a flash of the wrong theme. |
| FR‑1.6 | C | As the user I want to reorder or hide dashboard sections. | The order and visibility are runtime configuration exposed through E-8; each client applies them appropriately. |

## E‑2 GitHub attention (core, G‑1)

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑2.1 | M | As the user I see every **open issue and PR** of every monitored repository, so that I have the full picture. | AC1 For each configured repo all open issues and PRs (any author) are fetched incl. number, title, url, author, created, updated, labels, comment count, last comment author & time, draft flag (PR), review decision (PR). AC2 Pagination is handled (repos with > 100 open items). |
| FR‑2.2 | M | As the user I see items that are **new** since the previous snapshot highlighted, so that I never miss them. | AC1 An item is *new* iff its id is absent from the previous daily snapshot of that source (see E‑7). AC2 New items carry a `NEW` badge and are sorted first within their age bucket. AC3 On the very first run nothing is marked new except items younger than 24 h. |
| FR‑2.3 | M | As the user I see items that are **unanswered**, so that contributors get a quick reaction. | AC1 An open issue/PR is *unanswered* iff it has no comment/review from a human other than its author **and** it is older than the configured grace period (default 4 h). AC2 Activity by the configured "me" login counts as an answer even on self-authored items; comments by the opener or configured bot patterns do not. AC3 Badge `UNANSWERED`, colour distinct from `NEW`. |
| FR‑2.4 | M | As the user I see the **age** of each item at a glance. | AC1 Age buckets: `< 24 h`, `< 7 d`, `< 30 d`, `≥ 30 d`; each with its own colour token. AC2 Items with no activity for ≥ 30 d are additionally marked `STALE` and listed in a collapsed section. |
| FR‑2.5 | M | As the user I see one **Attention section** aggregating everything that needs me across all repos, so that I look in one place. | AC1 API data contains all items with level `new` or `unanswered` plus mentions/review requests (FR‑2.6), grouped by repo or flat (config), newest first. AC2 Each item includes repo, number, title, author, age, semantic badges and dismissal identity. AC3 Empty state carries "Nothing needs your attention". AC4 Response count is capped (default 30) with overflow count/link. |
| FR‑2.6 | M | As the user I see **mentions and review requests** addressed to me anywhere on GitHub, so that requests outside the monitored repos are not lost. | AC1 GitHub notifications with reason `mention`, `review_requested`, `assign`, `author`(replies) for the configured login are fetched and shown as attention items with source "GitHub mentions". AC2 Deduplicated against items already present from monitored repos. |
| FR‑2.7 | M | As the user I can **dismiss** an attention item or the current attention set ("seen"), so that highlights disappear before the next snapshot. | AC1 Authenticated `POST /api/v1/dismiss` with item id and `updated_at` stores a dismissal. AC2 A dismissed item loses its attention state and leaves Attention. AC3 If meaningful item state changes, the dismissal expires. AC4 `POST /api/v1/dismiss-all` dismisses the complete attention set returned by the current configuration. |
| FR‑2.8 | S | As the user I want to filter Attention to issues only, PRs only or one repo. | Client-side state; no backend configuration mutation required. |
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

## E‑5 Todoist — retired

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑5.1 | W | Todoist due-task aggregation. | **Removed from scope on 2026-08-16.** Identifier retained; no adapter, API representation or client section shall be built. |
| FR‑5.2 | W | Todoist overdue highlighting. | **Removed from scope on 2026-08-16.** |
| FR‑5.3 | W | Complete / reschedule from zorgscope. | Remains explicitly out of scope. |

## E‑6 News feeds — retired

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑6.1 | W | RSS/Atom aggregation. | **Removed from scope on 2026-08-16.** Identifier retained; no adapter, API representation or client section shall be built. |
| FR‑6.2 | W | Feed keyword filters. | **Removed from scope on 2026-08-16.** |
| FR‑6.3 | W | Feed topic grouping. | **Removed from scope on 2026-08-16.** |
| FR‑6.4 | W | LLM summarisation / ranking. | Remains explicitly out of scope. |

## E‑7 New‑detection, snapshots and dismissals (cross‑cutting)

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑7.1 | M | As the system I take a **daily snapshot** per source of all item ids currently present, so that "new since yesterday" is well‑defined. | AC1 Snapshot time configurable (default 03:00, timezone configurable, default Europe/Berlin). AC2 If the app was down at snapshot time, the snapshot is taken at next start (catch‑up), at most one per calendar day. AC3 Snapshots are retained for a configurable number of days (default 30) and pruned. |
| FR‑7.2 | M | As the system I define **new** as "present now, absent in the most recent snapshot older than the current one" and this rule is identical for issues, PRs, mentions and workflow runs. | AC1 Implemented once in the domain package, covered by property-style tests (see QS-1.x). |
| FR‑7.3 | M | As the user I want dismissals to survive restarts and to expire when an item changes (see FR‑2.7). | AC1 Persisted in SQLite. AC2 Dismissal keyed by (source, external id, updated‑at). |
| FR‑7.4 | S | As the user I want to see what changed since I last looked, in addition to the daily snapshot. | Client-owned follow-up: each visual client records its own last-open time and derives this highlight from stable item timestamps; background API polling must not count as a human visit. |

## E‑8 Runtime configuration and extensibility (QG-4)

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑8.1 | M | As the user I can read and change **every runtime-configurable property** through the visual client, so that no source or presentation change needs a deploy. | AC1 Authenticated `GET /api/v1/config` returns the complete non-secret runtime configuration and metadata (`revision`, `schema_version`, effective/default values). AC2 Authenticated `PUT /api/v1/config` atomically replaces the complete editable document; partial PATCH is deliberately unsupported. AC3 Runtime sections cover GitHub (login, repositories, mentions, intervals, grace/stale/bot rules and API base), Plausible (sites, intervals/order and API base), snapshots (time/timezone/retention), watch (credential metadata, TLS endpoints, warning days/intervals), and client presentation hints (order, visibility, attention cap, polling hint, refresh minimum gap). AC4 Malformed/unknown JSON returns 400 and semantic validation returns a structured 422 naming the field; the previous revision remains active. AC5 Optimistic concurrency via `If-Match`/revision rejects lost updates with 409. |
| FR‑8.2 | M | As the user I can set or rotate upstream secrets through the same configuration API without ever reading them back. | AC1 `PUT /api/v1/config/secrets/{github_token\|plausible_api_key}` sets/replaces a value and `DELETE` clears it; GET config returns only configured/source status for those two secrets. AC2 Values are AES-256-GCM encrypted before persistence in a 0600 file on the Fly volume using deployment secret `ZORGSCOPE_CONFIG_KEY`; plaintext exists only transiently in process memory. AC3 Each secret operation requires the current configuration revision and returns the effective revision; an actual state change creates a new one. AC4 Validation prevents enabling a source without its credential and prevents clearing a credential while that source is enabled; unrelated disabled sources require no secret. |
| FR‑8.3 | M | As the operator I can disable any source kind without removing its config. | AC1 `enabled: false` stops fetching and omits that source's cached rows/cards from API sections. |
| FR‑8.4 | S | As a developer I can add a new **kind** of source by implementing one interface and registering it. | AC1 One `SourceFetcher` adapter, config schema fragment, response mapping and fake are sufficient; no client-specific business rule is required. |
| FR‑8.5 | M | As the user I expect accepted configuration changes to take effect safely at runtime. | AC1 The backend persists a new revision and rebuilds the source scheduler/dashboard without restart. AC2 Removed/disabled sources stop after any in-flight fetch and their cached rows disappear from API views. AC3 Runtime YAML and encrypted provider secrets survive process/deployment restarts on the Fly volume. |
| FR‑8.6 | M | As the operator I keep deployment-only values outside runtime configuration. | AC1 Listen port, database path, public base URL, `ZORGSCOPE_CONFIG_KEY`, bootstrap `ZORGSCOPE_API_TOKEN`, auth mode and platform/logging controls are environment/Fly settings and are not mutable through `/api/v1/config`. AC2 Config GET may show non-secret effective deployment metadata as read-only; deployment secret values and even their presence are never exposed. AC3 Documentation explicitly classifies every property as runtime or deployment-only. |

## E‑9 Authentication and client sessions

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑9.1 | M | As a bootstrap client I authenticate every `/api/v1/*` request, so that private status and configuration never become public. | AC1 Initial implementation accepts a constant-time-checked bearer token supplied only as Fly secret/environment. AC2 Missing/invalid credentials return 401 without data. AC3 TLS is mandatory in production and auth failures are rate limited and logged without token material. |
| FR‑9.2 | S | As the user I enrol passkeys and authorise individual native/browser clients. | Planned device-authorisation flow: external browser performs WebAuthn; native clients use PKCE and receive revocable per-device credentials. Detailed acceptance criteria will supersede the bootstrap scheme before implementation. |
| FR‑9.3 | S | As the user I can list and revoke client sessions/devices. | Planned with FR-9.2; each device is independently revocable and stored token material is hashed/encrypted as appropriate. |
| FR‑9.4 | M | Local development does not require production passkey infrastructure. | AC1 A dedicated development token works only for localhost/test configuration; production rejects development auth mode. |
| FR‑9.5 | W | Password + TOTP as fallback. | Not planned; passkey-backed device authentication is preferred. |

## E‑10 Operations and observability

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑10.1 | M | As the operator I see per-source health through the API. | AC1 Authenticated `GET /api/v1/status` lists each source with last success, last error, next run and item count. AC2 `GET /healthz` (liveness, unauthenticated, no data) and `GET /readyz` (ready when deployment config loaded, DB open and runtime config readable). |
| FR‑10.2 | M | As the operator I get structured logs. | AC1 JSON logs (slog), level configurable, secrets redacted, request logs with duration and status. |
| FR‑10.3 | M | As the operator I run everything locally with `make app`. | AC1 `make app` builds the image, starts app + SQLite volume, prints URL. AC2 `make test`, `make lint`, `make e2e`, `make deploy` work with only Docker + make installed. |
| FR‑10.4 | M | As the operator I want CI to run lint, tests, e2e and deploy on `main`. | AC1 GitHub Actions workflow per [ADR‑0010](../architecture/decisions/ADR-0010-testing-strategy.md); deploy needs `FLY_API_TOKEN` repo secret. |
| FR‑10.5 | S | As the operator I want the SQLite database backed up. | AC1 fly volume daily snapshots enabled; `make fly ARGS="ssh sftp get …"` recipe documented. |
| FR‑10.6 | C | Slack notification for new attention items. | `Notifier` port; adapter later. |

## E‑11 Credential expiry and health watch (G‑6)

| Id | Prio | Story | Acceptance criteria |
|----|------|-------|---------------------|
| FR‑11.1 | M | As the operator I register credentials and API keys with expiry metadata, so that I am warned before they break an app. | AC1 Runtime config contains `{name, expires, warn_days, used_by, url}`; provider values needed by zorgscope use the separate write-only secret routes. AC2 Watch API data is sorted by remaining days; `EXPIRING` when `remaining <= warn_days`, `EXPIRED` when past. AC3 Both are dismissable attention items keyed to the expiry date. |
| FR‑11.2 | M | As the operator I want zorgscope's **own** GitHub token expiry detected automatically, so that the service does not silently go dark. | AC1 The GitHub adapter reads the `GitHub-Authentication-Token-Expiration` response header on every call and stores the date as a synthetic credential "zorgscope GitHub token". AC2 Same rules as FR‑11.1 (warn >= 14 d). AC3 If the header is absent (non-expiring token) the entry shows "no expiry". |
| FR‑11.3 | M | As the user I see **authentication failures** of any source immediately, so that a revoked/expired token is noticed the same hour. | AC1 An `ErrAuth` (401/403 non-rate-limit) sets source status `auth_failed` and creates `AUTH FAILED: <source>` once per failure streak. AC2 API data carries semantic danger status, safe error detail and age of last good data. |
| FR‑11.4 | M | As the operator I register TLS endpoints, so that certificate expiry cannot surprise me. | AC1 Runtime config accepts `{name, url, expect_status, expect_body_contains, poll_interval}`; URL includes any non-default port and the watch-level `warn_days` controls certificate warnings. AC2 HTTPS checks never disable verification and report the served leaf certificate's `not_after`, issuer, hostname-verification result and last check. AC3 Expiring/expired certificates and endpoints that fail twice consecutively are attention items; the running fetcher retains last-known certificate metadata through transient errors. |
| FR‑11.5 | C | Automatic listing of fine‑grained PATs granted to the `arc42` org (`GET /orgs/arc42/personal-access-tokens`) with their expiry. | Needs org‑admin token; optional later. |
