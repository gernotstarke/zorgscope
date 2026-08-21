# Handover — zorgscope v1

Written 2026-08-17 for the session that takes over the implementation.

## Where things stand

The project was reset on 2026-08-17. Everything the previous iteration had built was removed, not
adapted: it assumed an always-on Fly Machine with a persistent volume, an in-process scheduler, daily
snapshots, per-item dismissals, a runtime configuration API and passkeys, none of which survive the
move to a scale-to-zero machine with Turso. Two commits on branch
`feat/fly-shared-backend-config-api` carry the reset:

| Commit | Contents |
|--------|----------|
| `docs: reset requirements, design and plan to the v1 scope` | The rewritten `docs/requirements/`, the design spec, the implementation plan; removal of the arc42 chapters, ADR‑0001…0014, the old handover and plans, and the client-neutral API guide |
| `feat(infra): module, container, Compose, Fly config and make targets` | Task 1 of the plan, plus removal of all previous application code |

**Read these two, in this order, before touching anything:**

1. [The design](../specs/2026-08-17-zorgscope-reset-design.md) — what is being built and why, including
   what was deliberately cut.
2. [The implementation plan](2026-08-17-zorgscope-v1.md) — twenty tasks, each with its files, its
   tests and the command that proves it done.

The requirements they argue from are in [`docs/requirements/`](../../requirements/README.md).

## What is done

**Task 1 only.** The repository builds, tests, lints and runs:

- `go.mod` at `github.com/gernotstarke/zorgscope`, Go 1.26, no dependencies yet.
- `cmd/zorgscope` serves `GET /healthz` and shuts down gracefully; it has a test.
- `.golangci.yml` carries the `depguard` rules that enforce the architecture. They are already active,
  so an import that breaks the layering fails `make lint` from the first adapter onwards.
- `deploy/Dockerfile` produces a 7.7 MB distroless non-root image — the QS‑3.4 budget is 25 MB.
- `deploy/compose.yml` runs the backend plus `libsql-server`; `deploy/fly.toml` scales to zero.
- The `Makefile` has `backend`, `client`, `fakes`, `test`, `test-unit`, `test-domain`, `lint`, `check`,
  `db-up`, `db-shell`, `db-reset`, `image`, `build` and the `fly-*` family.
- CI is rewritten as three jobs — `check`, `docs`, `image` — inside the QS‑5.3 three-minute budget.
- `internal/web/static/htmx.min.js` is vendored, so Task 14 needs no network.
- `docs/decisions/` has its index and the MADR template; the eight records are Task 19.

Verified by hand, not just asserted: `make test-unit`, `make lint` and `make check` pass; the built
image answers `/healthz`; the `libsql-server` container answers the Hrana pipeline protocol that
`libsql-client-go` speaks, so Task 5 has a working target.

## What is next

**Task 2 onwards.** The order in the plan is a dependency order, with one deliberate exception: Task 6
(fake sources) comes before the adapters so each adapter can be built against it.

Do not skip ahead to the interesting parts. Task 5 — the store — is where the product's one real
invariant lives (`first_seen_at` is written on insert and never updated), and Tasks 13 and 14 assume
it holds.

## How to work

```sh
cp deploy/env.example .env        # fill in ZORGSCOPE_TOKEN and REFRESH_SECRET
make test                         # starts the database container itself
make check                        # what CI runs
```

`make test` brings up the `db` service and shares its network namespace with the Go container, so the
store tests reach libSQL at `localhost:8080` inside the container and `TEST_TURSO_URL` is set for
them. `make test-unit` skips the database entirely; the store tests skip themselves when
`TEST_TURSO_URL` is unset.

Nothing is installed on the host. If a step seems to need a local Go, Node or database client, it is
the wrong step — see C‑2 in [the constraints](../../requirements/03-constraints.md).

## Things that will bite

- **`first_seen_at`.** The upsert writes every column *except* that one on conflict. It is one line of
  SQL and the entire correctness of `NEW` depends on it. `TestReplaceItemsPreservesFirstSeen` is the
  guard; do not weaken it.
- **No scheduler.** There is nowhere to put a `time.Ticker`. If a task seems to need background work,
  it belongs in the refresh run.
- **Two independent credentials.** `ZORGSCOPE_TOKEN` signs in a browser; `REFRESH_SECRET` authorises
  `POST /api/refresh`. The cron service must never be able to read the dashboard, and the session
  cookie must never authorise a refresh. There is a test for both directions.
- **`make check` before every commit.** The `depguard` rules mean a layering mistake surfaces as a lint
  failure rather than a review comment, which is only useful if the linter actually runs.
- **The plan's code blocks are illustrative where they say so and literal where they show a test.**
  Write the tests as given, then make them pass; do not paste an implementation sketch verbatim
  without reading what it needs to do.

## Plans written but not implemented

Two features have a plan and no code. Each is self-contained and can be picked up on its own; read
the plan before the code, because both make decisions that the code cannot show.

| Plan | What it adds | Written |
|------|--------------|---------|
| [Configuration UI](2026-08-18-config-ui.md) | Editing the watched repository list in the browser, verified against GitHub on save, with the configuration moving into the database | 2026‑08‑18 |
| [GitHub details page](2026-08-21-github-details-page.md) | `/github`: every open issue and pull request grouped by repository, behind a strip of small per-repository counters | 2026‑08‑21 |

## Deliberately deferred

FR‑2.4 (GitHub mentions and review requests), FR‑6.2 (email notifier), FR‑8.4 (configuration editing
in the browser) and FR‑6.3 (traffic-change notifications) are v2 and have no task. FR‑1.6 (htmx
polling) is prepared by the tile-fragment routes in Task 14 but is not switched on.

Production is not yet created: there is no Fly app, no Turso database and no cron-job.org job. Task 18
creates all three, and it needs Gernot — the accounts and their credentials are his.

## Kickoff prompt

> Take over the zorgscope implementation on branch `feat/fly-shared-backend-config-api`.
> Read `docs/superpowers/plans/HANDOVER.md`, then the design at
> `docs/superpowers/specs/2026-08-17-zorgscope-reset-design.md`, then the plan at
> `docs/superpowers/plans/2026-08-17-zorgscope-v1.md`. Task 1 is done. Execute Task 2 onwards with the
> `superpowers:subagent-driven-development` skill — one fresh subagent per task, review between tasks,
> `make check` green before each commit. Stop and ask before Task 18, which needs my Fly, Turso and
> cron-job.org accounts.
