# Implementation plans

Plans are task lists written for engineers/agents with **zero project context**: every task names files,
gives the test first, the minimal implementation, the command to run, and the commit. Execute with
`superpowers:subagent-driven-development` (fresh subagent per task, review between tasks) or
`superpowers:executing-plans`.

## Milestones

| Milestone | Outcome (working software at the end) | Plan |
|-----------|----------------------------------------|------|
| **M1 – Walking skeleton** ([preview](2026-08-16-m1-preview.png)) | `make app` shows the dashboard (header, Attention tile, Repositories tile) fed by GitHub — real token or `cmd/fakesources`; daily snapshots, NEW/UNANSWERED/BUILD FAILED detection, dismiss, refresh, staleness, dev auth, SQLite, Docker image, CI (lint, unit, e2e smoke). Covers FR‑1.1–1.5, E‑2 (M), E‑3, E‑7, FR‑8.1–8.3, FR‑9.4, E‑10 (M) except deploy. | [2026-08-16-m1-walking-skeleton.md](2026-08-16-m1-walking-skeleton.md) |
| **M2 – All sources** | Plausible, Todoist, Feeds and Watch (credentials/health/auth‑failed) adapters + tiles + fakes; token‑expiry auto‑detection surfaces in the Watch tile; `docs/guides/adding-a-source.md`. Covers E‑4, E‑5, E‑6 (M), E‑11, FR‑8.4. | written after M1 |
| **M3 – Hosted & secured** | Passkey enrolment/login/account, sessions, CSRF hardening, fly.io deploy from CI, `/status`, backups guide, browser matrix e2e (Chromium/Firefox/WebKit, viewports, axe), design polish pass. Covers E‑9, FR‑10.4/10.5, QS‑2.x/3.x/5.x/6.x. | written after M2 |
| **M4 – Should/Could items** | Attention filters, since‑last‑visit marker, keyword filters, grouping, hot reload, Slack notifier, org PAT listing. | backlog |

## Verification status

The M1 plan's code blocks were extracted verbatim and run on 2026‑08‑16: `go vet`, `go test -race ./...`,
`golangci-lint` (0 issues) and the Docker Compose e2e stack (4/4 Playwright tests) all pass. Executors
should still follow the TDD steps — the point is the process — but should not hit surprises.

## Conventions for executors

* Only Docker + make: run tests with `make test`, lint with `make lint` (never install Go locally to
  "speed things up" — CI runs exactly these targets). For a single package: `make go ARGS="test ./internal/domain/..."`.
* TDD: write the failing test, run it, implement, run again, commit. Never skip the "verify it fails" step.
* Commit messages: `<type>(<scope>): <summary>` (`feat`, `fix`, `test`, `docs`, `chore`, `refactor`), body
  references requirement ids (`FR-2.3`, `QS-1.2`) — e.g. `feat(domain): attention rules (FR-2.2, FR-2.3, QS-1.2)`.
* Package boundaries are enforced by `depguard` (`.golangci.yml`); if lint fails on an import, the design is
  wrong — do not loosen the rule.
* Do not add third‑party dependencies beyond those listed in the plan's Tech Stack without an ADR.
* Every new source kind needs: adapter, fake, tile template, config section, registry entry (QS‑4.2).

## Deviations

Departures from the M1 plan text made while executing it, with the reason. The plan stays as written;
this table is the record of where the code intentionally differs.

| # | Task | Plan says | What we do instead | Why |
|---|------|-----------|--------------------|-----|
| D‑1 | 7, 15 | `config/zorgscope.yaml` ships with `plausible.enabled: true` and `todoist.enabled: true` | Both set to `false` with a "enabled in M2" comment | `Validate()` requires `PLAUSIBLE_API_KEY`/`TODOIST_TOKEN` when a source is enabled, so `make app` with only a `GITHUB_TOKEN` exits 2. M1 builds no adapter for either source. |
| D‑2 | 1 | `.golangci.yml` defines depguard rules for `domain`, `ports`, `app`, `adapters` | Adds a `server` rule (`$gostd` + domain, ports, app, config, server, web) | arc42 ch. 5 presents the whole import table as depguard‑enforced; the plan never revisits the file, so `internal/server` would go unchecked for all of M1. `cmd/*` stays unconstrained by design. |
| D‑3 | 2 | A domain test ends with `_ = time.Now // keep import used in later edits` | Line omitted; `time` import dropped if unused | Global constraint: no `time.Now()` in `internal/domain`. |
| D‑4 | 4 | `Evaluate` suppresses dismissed items in the `raise()` closure and again in a second `if ev.Dismissed` after the Issue/PR switch | Stale/Aged routed through the same `raise()` closure | One rule, one code path; behaviour and tests unchanged. |
| D‑5 | 7, 13 | `config.indexOf()` and `github.splitRepo()` hand‑roll `strings.Index` / `strings.Cut` | Use the standard library | No behavioural difference; std lib is allowed in every package. |
| D‑6 | 6, 7 | `(*RateLimitedError).Error` and `(*ValidationError).Error` have no doc comments | Doc comments added | `revive`'s `exported` rule is enabled, so `make lint` would fail. |
| D‑7 | 14, 15 | `fmt.Fprintln(os.Stderr, …)` and `sb.Write(…)` return values ignored | `_, _ =` at both call sites | golangci‑lint v2 applies no `exclusions.presets`, so `errcheck` flags them. Matches the plan's own idiom in `internal/server/health.go`. |
| D‑8 | 14 | `web/embed.go` written in step 1, templates in step 4; `web/templates/page.html` listed for creation | Templates written together with `embed.go`; `page.html` not created | The embed pattern cannot resolve before the templates exist; nothing defines or references a `page` block, and an empty file breaks `template.ParseFS`. |
| D‑9 | 15 | `logging.New` redacts only `slog.KindString` attribute values | Redaction applies to the attribute's rendered string form, with a test covering an `error` value | Every `"err", err` log site passes a `KindAny` value, so secrets in error text would reach stdout (QS‑3.3). |
| D‑10 | 15 | CI runs `lint`, `test`, `test-domain`, `image`, `e2e` | Adds a `docs-check` job | `make check` includes `docs-check` and this README claims CI runs exactly these targets. |
| D‑11 | 4, 7 | `domain.Rules.Me` is populated from config but read by no rule | `Me` wired into `IsUnanswered`: activity by `Me` on an item `Me` authored counts as an answer, so an item where I spoke last is no longer UNANSWERED | Decided by the maintainer. arc42 ch. 5 lists `Me` as an `IsUnanswered` input while ch. 8's pseudocode omits it; the dashboard exists to show what needs *my* attention, and if I was the last to speak there is nothing for me to do. With `Me` empty the rule is byte-for-byte the old behaviour, so every existing test case is unaffected. |
| D‑12 | 7 | `Validate()` also resolves defaults and derived fields (timezone `Location`, snapshot `Hour`/`Minute`, `ExpiresAt`, `ExpectStatus`, `PollInterval`) | Behaviour kept, documented in the doc comment | Tasks 10, 11 and 15 depend on those side effects; splitting it would break `Parse`'s contract. |
| D‑13 | — | `make go ARGS="test -race …"` is the documented way to run the race detector | `CGO_ENABLED ?= 0` added and `GO_RUN` passes `$(CGO_ENABLED)`, so `make go CGO_ENABLED=1 ARGS="test -race …"` works | The Go container ran with `CGO_ENABLED=0` and the race detector requires cgo, so *every* such invocation failed with `-race requires cgo`. `make test` was unaffected because it overrides inline. Defaults and the `test`/`test-domain` targets are unchanged. |
| D‑15 | 15 | `deploy/Dockerfile` builds a distroless `nonroot` image with `VOLUME ["/data"]` and no ownership handling for it; `deploy/compose.e2e.yml`'s `zorgscope` service uses `tmpfs: ["/data"]` | `deploy/Dockerfile` pre-creates `/data` and `chown`s it to `65532:65532` (the distroless `nonroot` uid:gid) before the final stage; `deploy/compose.e2e.yml` pins the mount to `tmpfs: ["/data:mode=1777"]` | The distroless `nonroot` image runs as uid:gid `65532:65532` with no shell (no `HEALTHCHECK`/`chown` possible at container start). As written, a Docker named volume mounted at `/data` (the `compose.yml`/`make app` path) is created root-owned on first use, so `sqlite.Open` failed with `unable to open database file` — `make app` did not actually start. Chowning `/data` in the image fixes the named-volume case (Docker copies a mount point's pre-existing ownership into a *new* named volume on first use), but Docker's tmpfs mount preserves a pre-existing mount point's *mode* bits while resetting *ownership* to root, so once `/data` was chowned in the image an unqualified `tmpfs: ["/data"]` in the e2e/demo stack became unwritable by the same fix. `mode=1777` (world-writable + sticky, like `/tmp`) makes that mount writable regardless of what the image bakes at that path; it is scoped to `compose.e2e.yml`'s disposable e2e/demo stack only and does not touch the production named-volume path in `compose.yml`. |

Accepted as‑is after review (no code change): the four `time.Now()` sites outside `clock`/`cmd`
(sqlite migration timestamp, GitHub rate‑limit header parsing, the fake GitHub server, request‑latency
middleware) are infrastructure timestamps where a `Clock` buys no testability.
