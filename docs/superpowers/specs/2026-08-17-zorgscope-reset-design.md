# zorgscope reset — design

Date: 2026-08-17
Status: approved
Supersedes: everything under the former `docs/architecture/` (arc42 chapters and ADR‑0001…0014)

## 1. Why the reset

The previous design solved a larger problem than the one that exists. It assumed an always-on Fly
Machine with a persistent volume, an in-process scheduler, daily snapshots, per-item dismissals, a
runtime-writable configuration API, passkey authentication, credential and TLS expiry watching, and a
client-neutral API for a native client that was never built. None of that is needed to answer "does
anything need me right now?".

What the user actually asked for is: GitHub issues and PRs of configured repositories, Plausible
statistics, Todoist items due — all priority 1 — and notifications by Slack or email at priority 3;
new things marked as new; as cheap as possible; comfortable local development with make and Docker.

Three consequences of the new constraints drove this design:

1. **Scale-to-zero cannot poll.** With `min_machines_running = 0` there is no process between
   requests, so the refresh must be triggered from outside. cron-job.org calls an authenticated
   endpoint, exactly as `status.arc42.org-site` already does for its availability prober.
2. **Turso removes the volume.** With no local disk, all state — items, metrics, run history,
   last-visit time — lives in the database. Configuration that is not secret lives in a YAML file in
   the image.
3. **The cron ping is also a warmer.** Because the machine is woken every refresh interval, a user
   opening the dashboard usually meets a warm machine. That is what makes scale-to-zero acceptable
   for an interactive page.

## 2. Scope

**v1 (must).** GitHub open issues and PRs for configured repositories with `created_at`/`updated_at`
and a `NEW` badge; GitHub Actions build status per repository; Plausible visitors and pageviews over 7
and 30 days with change against the preceding period; Todoist tasks overdue or due today; rendered
requirements, decisions and concepts served by the app; token sign-in producing a session cookie.

**v2 (should).** GitHub mentions and review requests; Slack notification on newly seen issues and PRs;
email as a second notifier; a configuration page in the browser; htmx polling while the page is open.

**Cut permanently.** News and RSS feeds, "unanswered" detection, per-item dismissals, daily snapshots,
credential and TLS expiry watching, passkeys, the twelve-chapter arc42 set, the write-capable
`/api/v1/*` configuration API, and a Wails or other native client. Traffic-change notifications are
deferred, not cut.

Requirements: [`docs/requirements/`](../../requirements/README.md).

## 3. Runtime

One Go binary on one Fly Machine in `fra`, `min_machines_running = 0`, `auto_stop_machines = "stop"`,
`shared-cpu-1x` with 256 MB. State lives entirely in Turso.

Two things wake the machine:

- **cron-job.org** issues `POST /api/refresh` with `Authorization: Bearer <REFRESH_SECRET>` at the
  configured interval. The handler fetches every enabled source, writes items and metrics, records a
  refresh run, and — in v2 — posts notifications. This is the only writer of upstream data.
- **A browser request** to any page. The handler reads stored data and renders; it never contacts an
  upstream service, so a cold open costs a machine start plus a few queries.

If a refresh takes longer than the machine's idle timeout, it still completes: the request is in
flight, so Fly does not stop the machine. Each source commits its own transaction, so a machine that
dies mid-run leaves the sources it already wrote intact and the rest simply stale, recorded as failed
in the run record.

Only one refresh runs at a time. Concurrency is rejected with 409 rather than queued, using a lease
row in the database so the guarantee survives a restart.

```text
cron-job.org ──POST /api/refresh──┐
                                  ▼
browser ──GET /──────────────► Fly Machine (Go binary) ──► Turso (libSQL over HTTP)
                                  │
                                  └──► GitHub GraphQL + REST, Plausible, Todoist, Slack
```

## 4. Structure

A modular monolith with a hexagonal core. Package layout:

| Package | Contents | May import |
|---------|----------|------------|
| `internal/domain` | Item, Metric, Source, RefreshRun, the new-detection rule, tile assembly, sorting and age bucketing | standard library only |
| `internal/ports` | `SourceFetcher`, `Store`, `Notifier`, `Clock` | `internal/domain` |
| `internal/adapters/github` | GraphQL for issues and PRs, REST for workflow runs | `shurcooL/githubv4`, ports, domain |
| `internal/adapters/plausible` | Aggregate statistics for two windows plus their comparisons | net/http, ports, domain |
| `internal/adapters/todoist` | REST v2 tasks filtered to overdue and due today | net/http, ports, domain |
| `internal/adapters/slack` | Webhook notifier (v2) | net/http, ports, domain |
| `internal/adapters/libsql` | `Store` implementation, migrations, the refresh lease | `tursodatabase/libsql-client-go`, `database/sql`, ports, domain |
| `internal/config` | YAML file plus environment secrets, validation | gopkg.in/yaml.v3, domain |
| `internal/refresh` | Orchestrates a run over the enabled fetchers | ports, domain |
| `internal/web` | Router, handlers, templates, static assets, docs rendering, auth | ports, domain, goldmark |
| `cmd/zorgscope` | Wiring and start-up | everything |
| `cmd/fakesources` | Fixture-backed stand-ins for GitHub, Plausible, Todoist | standard library |

`depguard` enforces the import column: the domain imports nothing external, and upstream client
libraries appear only in their own adapter.

The external dependency set is deliberately four modules: `shurcooL/githubv4` with `golang.org/x/oauth2`
for the GitHub GraphQL schema, `tursodatabase/libsql-client-go` for the database driver, `yuin/goldmark`
for the documentation pages and `gopkg.in/yaml.v3` for the configuration. Plausible, Todoist and Slack
are a handful of JSON requests each; writing them against `net/http` is less code than a client library
and lets a test point them at the fake-sources server through one base-URL field.

The dashboard view is one Go struct that marshals cleanly to JSON. Nothing in v1 exposes it as JSON,
but keeping the render input separable costs nothing and leaves a read-only API or a different client
cheap to add later.

## 5. Data

SQLite dialect on libSQL. Numbered migrations embedded with `go:embed` and applied at start-up inside
a transaction, tracked in a `schema_migrations` table. Times are stored as RFC 3339 UTC strings.

```sql
items(
  source TEXT, external_id TEXT, kind TEXT, repo TEXT, number INTEGER,
  title TEXT, url TEXT, author TEXT, state TEXT,
  created_at TEXT, updated_at TEXT, due_at TEXT, priority INTEGER,
  first_seen_at TEXT NOT NULL, last_fetched_at TEXT NOT NULL, payload TEXT,
  PRIMARY KEY (source, external_id))

builds(repo TEXT PRIMARY KEY, workflow TEXT, conclusion TEXT, status TEXT,
       run_url TEXT, finished_at TEXT, fetched_at TEXT)

metrics(site TEXT, window_days INTEGER, visitors INTEGER, pageviews INTEGER,
        prev_visitors INTEGER, prev_pageviews INTEGER, fetched_at TEXT,
        PRIMARY KEY (site, window_days))

refresh_run(id INTEGER PRIMARY KEY AUTOINCREMENT, started_at TEXT, finished_at TEXT,
            trigger TEXT, ok INTEGER, detail TEXT)

source_state(source TEXT PRIMARY KEY, last_success_at TEXT, last_error TEXT,
             last_error_at TEXT, item_count INTEGER)

app_state(key TEXT PRIMARY KEY, value TEXT)   -- holds last_visit_at and the refresh lease

notified(key TEXT PRIMARY KEY, sent_at TEXT)  -- v2, one row per announced item
```

The upsert is the only subtle statement: it writes every column except `first_seen_at`, which is set
on insert and left alone by `ON CONFLICT DO UPDATE`. That single rule is what makes `NEW` correct.
Items no longer returned by a source are deleted within that source's transaction, so an item that
returns later is genuinely new again (FR‑5.3 AC3).

`NEW` is `first_seen_at > last_visit_at`. "Mark all seen" writes `now` to `app_state`. There is no
per-item dismissal and no snapshot job.

## 6. Client

Server-rendered `html/template` with vendored htmx and hand-written CSS; no build step, no Node.

The dashboard is a grid of tiles. Each tile is rendered by its own template fragment addressable at
`/tile/{name}`, so htmx can replace one tile without a full reload (FR‑1.6) and so a slow or failing
source cannot hold up the page. Light and dark come from CSS custom properties switched by
`prefers-color-scheme`; no theme flash because there is no client-side theme script.

Documentation is served at `/docs`. The Markdown under `docs/` is embedded with `go:embed` and
rendered at request time with goldmark; link rewriting maps `../requirements/01-goals.md` to
`/docs/requirements/01-goals`. This replaces the previous pre-rendering step and its
`docs-html-check` gate.

Authentication: `GET` of a page without a session cookie redirects to `/login`. Posting the value of
`ZORGSCOPE_TOKEN` sets a signed, HttpOnly, SameSite=Lax cookie whose HMAC key is derived from the
token, so rotating the token invalidates every session. `POST /api/refresh` uses the separate
`REFRESH_SECRET` bearer and never accepts the cookie; the two credentials are independent so the cron
service holds only the ability to trigger a refresh.

## 7. Configuration

Non-secret settings live in `config/zorgscope.yaml`, baked into the image and overridable by
`CONFIG_PATH`:

```yaml
timezone: Europe/Berlin
refresh:
  interval: 15m          # what cron-job.org is set to; used for freshness display only
github:
  login: gernotstarke
  repos: [arc42/arc42.org-site, arc42/quality.arc42.org, gernotstarke/zorgscope]
plausible:
  sites: [arc42.org, quality.arc42.org]
todoist:
  filter: "overdue | today"
notifications:
  slack: {enabled: false}
```

Secrets come from the environment only: `GITHUB_TOKEN`, `PLAUSIBLE_API_KEY`, `TODOIST_TOKEN`,
`SLACK_WEBHOOK_URL`, `ZORGSCOPE_TOKEN`, `REFRESH_SECRET`, `TURSO_URL`, `TURSO_AUTH_TOKEN`. A source
whose secret is missing is disabled and says so on its tile, rather than failing every run. An invalid
YAML file aborts start-up naming the field. The browser configuration page (FR‑8.4, v2) edits the same
structure, then persisted in `app_state`, with the file as the default.

## 8. Development and testing

Everything runs in containers. `make backend` brings up the backend and a `libsql-server` container
with Compose; `make client` opens the browser at it; `make fakes` runs `cmd/fakesources` and points
the API base URLs at it. `make test`, `make lint`, `make check`, `make db-migrate`, `make db-shell`,
`make fly-*` complete the set. No Node, no Playwright, no local Go toolchain.

Testing, layer by layer:

- **Domain** — table-driven and property-style tests for the new rule, sorting and age bucketing.
  ≥ 90 % statement coverage, gated in `make test-domain`.
- **Adapters** — against `cmd/fakesources` and recorded fixtures, including pagination, 500s,
  rate-limit responses and timeouts.
- **Store** — against the `libsql-server` container: the upsert's first-seen invariant, deletion and
  reappearance, per-source transactions, the lease.
- **Web** — `httptest` handler tests for routing, authentication on every route, security headers,
  and rendering of empty, populated, stale and error states.
- **End to end** — the binary against fake sources: refresh, then assert the rendered HTML.

The previous Playwright suite is not carried over; it needed Node and tested the browser more than the
product.

## 9. Documentation to be written

`docs/requirements/` (done as part of this reset), `docs/decisions/` as MADR, and `docs/concepts/`:

| ADR | Subject |
|-----|---------|
| 0001 | Go modular monolith with a hexagonal core |
| 0002 | Server-rendered html/template plus htmx, no JavaScript build |
| 0003 | Fly.io scaled to zero with an external cron trigger |
| 0004 | Turso and libSQL for persistence, libsql-server locally |
| 0005 | Embedded SQL migrations rather than a schema tool |
| 0006 | First-seen versus last-visit as the definition of new |
| 0007 | Token sign-in with a derived session cookie |
| 0008 | Docker and make as the only local toolchain |

Concepts: **security and token handling**, **data storage**, **configuration**.

## 10. Risks

| Risk | Mitigation |
|------|------------|
| Cold start is slower than QS‑2.1 allows | The cron ping keeps the machine warm; if that is not enough, shorten the interval or accept `min_machines_running = 1` at a few euros a month. |
| Turso's free tier changes | The store is one adapter behind `ports.Store`, and the dialect is plain SQLite; moving to a Fly volume or another libSQL host is an adapter change. |
| cron-job.org is unavailable | The dashboard shows the age of the last successful run, so a silent stall is visible. A user-triggered refresh is always available. |
| GitHub GraphQL quota with many repositories | QS‑3.5 budgets the run; the fetch is one query per repository with pagination only where needed. |
| The Fly machine stops mid-refresh | Per-source transactions; the run record marks unreached sources as failed and the next run retries them. |
