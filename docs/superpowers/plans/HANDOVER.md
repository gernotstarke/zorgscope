# Handover — 2026-09-15, stateless reset

Written for the next session. Read this, then the spec, then the plan. This file is deleted by the
plan's Task 6; nothing in it needs to outlive the reset.

## Where things stand

- **Branch:** `reset/stateless`, one commit ahead of `main` (`c820205`), not pushed. It carries only
  two documents:
  - `docs/superpowers/specs/2026-09-15-stateless-reset-design.md` — the design of record.
  - `docs/superpowers/plans/2026-09-15-stateless-reset.md` — seven tasks; the first six are for
    subagents, the seventh is Gernot's.
- **`main`** holds the 2026-09-14 GitHub-only version (merge `0c41ea0`) plus two CI fixes
  (`golangci-lint-action@v9`, `go test -p 1`). CI is green on main. The app it builds crash-loops on
  Fly for lack of `REFRESH_SECRET` and `TURSO_URL`; do not spend effort on it, the reset replaces it.
- **Nothing of the reset is implemented yet.**

## What was decided, and by whom

Gernot, in conversation on 2026-09-15, after the first production deploy failed on every stateful
piece in turn:

| Decision | Choice |
|---|---|
| Storage | None. NEW = created after the visitor's last "mark seen", carried in the signed session cookie. In-memory cache with a TTL. |
| Kept | Grouped list with filters, GitHub OAuth sign-in with push-access check, NEW markers, a slim local fake GitHub. |
| Dropped | Build status, Slack, refresh endpoint and cron-job.org, in-app docs, stop control, polling, Turso, migrations. |
| Deploy | `make deploy` from the laptop. CI only tests. The Makefile grows to seven targets. |
| Docs | Slim rewrite: ADR‑0010, requirements and concepts cut down, old plans and specs deleted. |
| Secrets | Both OAuth Apps, their pairs and `GITHUB_TOKEN` are reused unchanged. Nothing is re-registered. |

Two calls made by the previous session without asking, open to veto: the theme toggle stays; the
seventh make target is `deploy`.

## Facts the code cannot tell you

- The Fly app is `zorgscope` in `fra`. `fly.toml` lives in `deploy/`, so every `fly` command needs
  `-a zorgscope` or `--config deploy/fly.toml`. The app currently holds `GITHUB_OAUTH_CLIENT_ID`,
  `GITHUB_OAUTH_CLIENT_SECRET` and a `REFRESH_SECRET` (unused after the reset). `GITHUB_TOKEN` is
  not set there yet — Task 7.
- The two OAuth Apps exist: local callback `http://localhost:8080/auth/callback`, production
  `https://zorgscope.fly.dev/auth/callback`. Gernot's `.env` has the local pair and a `GITHUB_TOKEN`,
  plus a dozen stale lines the new config ignores.
- Real-GitHub sign-in has **never been exercised**. If the callback logs `why="no permissions block"`,
  request the `read:org` scope in `Server.oauthConfig` (`internal/web/signin.go`). Everything else in
  the sign-in code is tested against the in-process fake and was reviewed twice.
- Go 1.27 is installed on the host for quick loops; the gate is `make check` (Docker). CI runs the
  same checks natively.
- The repository has no GitHub secrets (`gh secret list` is empty). The deploy workflow that needed
  `FLY_API_TOKEN` is deleted by Task 5; do not create the secret.

## How to run the plan

Use `superpowers:subagent-driven-development` on the plan file. Model tiers are in the plan's task
headers: Tasks 1, 4, 5, 6 cheap; Tasks 2 and 3 standard; the final whole-branch review on the most
capable model. Every task ends with `go build ./... && go vet ./... && go test -race ./...` green
and one commit. Task 2 is the cut-over and is large by design — do not split it across agents,
the tree only compiles again at its end.

Stop and ask Gernot only for: a push, a merge to main, a `fly` command that changes the app, or a
plan defect where every path forward is a guess. Everything else is a ruling in the ledger.

## Kickoff prompt

> Take over zorgscope on branch `reset/stateless`. Read `docs/superpowers/plans/HANDOVER.md`, then
> `docs/superpowers/specs/2026-09-15-stateless-reset-design.md`, then
> `docs/superpowers/plans/2026-09-15-stateless-reset.md`. Execute Tasks 1–6 with
> superpowers:subagent-driven-development. Stop before Task 7 and tell me what to do on my laptop.
