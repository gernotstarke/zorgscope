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
