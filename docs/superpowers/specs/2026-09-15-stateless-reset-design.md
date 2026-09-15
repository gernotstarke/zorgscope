# Stateless reset — design

Date: 2026-09-15. Status: approved by Gernot in conversation (storage, features, deploy and docs
decisions below are his). Supersedes the 2026-08-17 reset design and the 2026-09-14 sign-in design
wherever they disagree; the sign-in flow itself is carried over unchanged.

## 1. What went wrong, and the goal

zorgscope is one page: the open issues and pull requests of the arc42 repositories, grouped by
repository, behind a GitHub sign-in. The implementation behind that page is ~21,000 lines of Go plus
~13,000 lines of tests, and it needs a Turso database, embedded migrations, a refresh pipeline, an
external cron job with its own secret, a fake-sources server and a CI job that boots a libSQL server.
Every one of those pieces failed separately on the first production deploy.

All of it exists to serve one idea from August: NEW is computed from a `first_seen_at` row per item,
so the server has to remember every item it ever saw. Drop that idea and the database goes, and with
the database the refresh pipeline, the cron trigger, the notifier and most of the fakes.

**Goal:** the same page, served by a process that remembers nothing between requests. Three secrets,
one deploy command, one CI job.

## 2. Decisions (Gernot, 2026-09-15)

| Question | Decision |
|---|---|
| Storage | Stateless. No database. NEW is "created after my previous *mark seen*", carried in the session cookie. |
| Features kept | The grouped list with filters, the GitHub sign-in with the push-access check, NEW markers, a local fake GitHub for offline development. |
| Features dropped | Build status per repository, Slack notifications, the refresh endpoint and cron-job.org, the in-app docs pages, the stop control, htmx polling. |
| Deploy | `make deploy` from the laptop wrapping `fly deploy`. CI only tests. |
| Docs | Slim rewrite: one ADR records the reset, requirements and concepts cut to what the app does, old plans and specs deleted. |
| Secrets | The two OAuth Apps, their client id/secret pairs and `GITHUB_TOKEN` are reused unchanged. Nothing has to be re-registered. |

## 3. Architecture

Same shape, fewer parts: Go modular monolith, hexagonal, `internal/domain` on the standard library
only (depguard stays), `html/template` + vendored htmx, one Fly Machine that scales to zero.

```text
browser ──GET /──▶ web ──▶ snapshot cache ──(older than ttl)──▶ github adapter ──▶ GitHub GraphQL
                    │                                                              (GITHUB_TOKEN)
                    └──▶ session cookie: signed {expiry, last-seen}
```

Packages after the reset:

| Package | Role |
|---|---|
| `internal/domain` | `Item`, `Kind`, `Filter`, `RepoGroup`, `BuildDashboard`. Nothing else. |
| `internal/ports` | `Source`, `AccessChecker`, `Clock`. |
| `internal/snapshot` | New. Holds the last fetched item list in memory and refetches it when it is older than a TTL. |
| `internal/adapters/github` | `IssueFetcher` (GraphQL issues + PRs, unchanged) and `AccessChecker` (unchanged). |
| `internal/config` | YAML for the non-secret part, environment for the three secrets. |
| `internal/web` | Sign-in (unchanged), one page, one fragment, three POSTs. |
| `internal/fakesources` | Fake GitHub for `make fakes`: OAuth endpoints, the GraphQL issues query, fixtures. |
| `cmd/zorgscope`, `cmd/fakesources` | Binaries. |

Deleted: `internal/adapters/libsql`, `internal/adapters/slack`, `internal/refresh`, `cmd/migrate`,
the REST/badge/control parts of the fakes, and in `web` everything about refresh runs, problems,
builds, docs pages, stopping and polling.

## 4. Sign-in (carried over, one addition)

Unchanged from ADR‑0009: "Sign in with GitHub" via an OAuth App per environment, no scopes, one
`GET /repos/{auth_repo}` with the visitor's token, push or admin admits; state cookie, constant-time
state check, rate limit charged at the top of the callback; session key =
`SHA256("zorgscope-session-v2" + GITHUB_OAUTH_CLIENT_SECRET)`.

**The one addition:** the signed session payload becomes two integers, `expiry` and `seen`, both Unix
seconds. `seen` is the moment of the visitor's last *mark seen*; on sign-in it is 0 (nothing is NEW
until the first mark). `POST /seen` re-mints the cookie with `seen = now` and the same expiry.
Because the value sits inside the HMAC-signed payload, nobody can tamper with it, and because it sits
in the cookie, the server keeps no state.

Consequence to accept: signing out, or the cookie expiring, forgets the mark. For one user this is
the right trade against a database.

## 5. Data: fetch, cache, NEW

`ports.Source` is `Fetch(ctx) ([]domain.Item, error)`. The GitHub adapter implements it as today
(two GraphQL queries per repository, pagination, page cap, per-repository failure kept as an error,
good repositories returned).

`snapshot.Cache` wraps a Source:

- `Get(ctx) Snapshot` returns `{Items, FetchedAt, Err, ErrAt}`. If the snapshot is older than the
  configured TTL (or has never been fetched), it fetches first. The fetch runs under the cache's
  mutex, so concurrent requests wait for the one fetch rather than start their own.
- On a failed fetch the previous items are kept and `Err`/`ErrAt` are set; the page shows the old
  list with a notice ("GitHub unreachable since 12:04, showing the list from 11:50"). A first fetch
  that fails yields an empty list with the notice.
- `Invalidate()` makes the next `Get` fetch. `POST /refresh` calls it and redirects to `/`.

The TTL is `github.cache_ttl` in the YAML, default `5m`. After the Machine scales to zero the cache
is empty and the first visit pays one fetch (~16 GraphQL calls for eight repositories, a few
seconds). Acceptable; it is what a refresh button costs anyway.

`Item` loses `Source` and `FirstSeenAt`; `IsNew(seen)` becomes `CreatedAt.After(seen)`, with the
zero `seen` meaning "nothing is new". `BuildDashboard` keeps grouping by configured repository order
(silent repositories still appear), `NewCount`/`Total` per group before the filter, `NewTotal`
over everything.

## 6. The page

Routes, all behind the session except the first four:

| Route | Purpose |
|---|---|
| `GET /healthz` | Fly's health check. |
| `GET /login`, `GET /auth/github`, `GET /auth/callback` | Sign-in, unchanged. |
| `GET /static/…` | CSS, htmx, logo. |
| `GET /{$}` | The page: header (fetched-at, error notice, NEW total), filter form, grouped list. |
| `GET /items` | The list fragment the filter form swaps in (htmx, unchanged mechanics: `hx-get="/" hx-select="#items" hx-target="#items" hx-swap="outerHTML" hx-push-url="true"`). |
| `POST /seen` | Re-mint the cookie with `seen = now`, redirect to `/`. |
| `POST /refresh` | Invalidate the cache, redirect to `/`. |
| `POST /logout` | Clear the session cookie, redirect to `/login`. |

Filters stay as they are: `repo`, `kind`, `since`, `q`, parsed by `parseFilter`. No polling. The
theme toggle (`zorgscope_theme` cookie) stays; it is a few lines of CSS and Go and costs nothing.

Templates: `layout.html`, `login.html`, `dashboard.html`, `fragments/items.html`. The rest is
deleted.

## 7. Local development

`make backend` runs the container against real GitHub. `.env` needs three lines:

```text
GITHUB_OAUTH_CLIENT_ID=      # the local OAuth App, callback http://localhost:8080/auth/callback
GITHUB_OAUTH_CLIENT_SECRET=
GITHUB_TOKEN=                # any PAT of yours; public repositories need no scope
```

`make fakes` serves a fake GitHub on :9090 (OAuth endpoints + the GraphQL issues query over fixture
files) for offline work; `.env` then also sets `GITHUB_BASE_URL` and `GITHUB_OAUTH_BASE_URL` to
`http://host.docker.internal:9090`, as documented in `deploy/env.example`. The fake's REST, badge and
control endpoints are deleted.

Compose loses the `db` service and its volume.

## 8. Configuration and secrets

`config/zorgscope.yaml`:

```yaml
timezone: Europe/Berlin
github:
  auth_repo: gernotstarke/zorgscope   # push access here admits a visitor
  cache_ttl: 5m                       # how old the fetched list may be before a page view refetches
  repos:
    - arc42/arc42.org-site
    - …
```

Environment: `GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET`, `GITHUB_TOKEN` (all three
required, the process refuses to start without them), optional `GITHUB_BASE_URL`,
`GITHUB_OAUTH_BASE_URL` (fakes only, https unless loopback/host.docker.internal), `PORT`,
`LOG_LEVEL`, `CONFIG_PATH`.

Gone: `REFRESH_SECRET`, `SLACK_WEBHOOK_URL`, `TURSO_URL`, `TURSO_AUTH_TOKEN`, `GITHUB_BADGE_BASE_URL`.
The Fly app keeps the two OAuth secrets already set; `GITHUB_TOKEN` is added; `REFRESH_SECRET` may
stay set, it is ignored.

## 9. Deploy and CI

- `make deploy` = `fly deploy --remote-only --config deploy/fly.toml`. Needs the laptop's `fly`
  login and nothing else. Seven make targets: help, backend, client, fakes, check, deploy, clean.
- `make check` = vet, golangci-lint, race tests, domain coverage ≥ 90 %, markdownlint,
  `fly config validate --strict`. No libSQL server, no `-p 1`, no lychee.
- CI (`ci.yml`): one job — vet, lint, race tests, domain coverage gate. `deploy.yml` is deleted.
- `fly.toml` unchanged except its comment (no cron warmer); scale to zero stays.

## 10. Documentation

- ADR‑0010 "Stateless: no database, no refresh pipeline" — supersedes 0004, 0005, 0006 outright and
  the cron half of 0003 (scale to zero stays). 0007 was already superseded by 0009.
- Requirements: goals to the one goal; functional requirements to sign-in, list, filters, NEW,
  refresh; quality requirements pruned to what still has a test or a lint behind it; constraints
  updated. Requirement ids keep working in commit messages.
- Concepts: `data-storage.md` deleted; `configuration.md` and `security-and-tokens.md` rewritten
  to §8 and §4.
- `docs/superpowers/plans/` and `specs/` keep only this spec and its plan; `HANDOVER.md` is deleted.
- README rewritten: what it is, the three `.env` lines, the seven targets, deploy.

## 11. Testing

- `domain`: table tests for `Filter.Match`, `IsNew`, `BuildDashboard` (coverage gate stays).
- `snapshot`: TTL respected, fetch error keeps the old items and reports it, `Invalidate` forces a
  fetch, concurrent `Get`s produce one fetch (counting fake source).
- `web`: the existing sign-in tests stay; page tests use an in-process fake Source; cookie tests
  cover `seen` round-trip and tamper rejection; `POST /refresh` invalidates; filters unchanged.
- `github`: existing issue-fetch and access tests stay; build tests go.
- No test needs a database or an external process.

## 12. Out of scope, deliberately

History, notifications, build status, item-level dismissals, background refresh. Each would bring
state back. If one is wanted later, a Fly volume with SQLite is the cheapest way in — not Turso,
not cron.
