# Handover: coordinating the M1 build in a fresh session

Everything a new coordinating session (Claude Code or another LLM) needs is
in this repository. Nothing lives only in a previous chat.

## What exists

| Where | What |
|---|---|
| `docs/requirements/` | req42 requirements (FR-x, QS-x, C-x, open points O-1…O-6) |
| `docs/architecture/` | arc42 chapters 1–12, `decisions/ADR-0001…0013` |
| `docs/plans/README.md` | roadmap M1–M4, executor conventions, commit format |
| `docs/plans/2026-08-16-m1-walking-skeleton.md` | the M1 plan: 15 TDD tasks, full code, verified end to end |
| `docs/plans/2026-08-16-m1-preview.png` | what the finished M1 looks like |
| branch `reference/m1-verified` | the extracted implementation that passed vet, `-race` tests, lint and Playwright e2e — a diff target for reviewers, **not** for merging |
| `Makefile`, `config/`, `deploy/` | Docker-only toolchain, config schema, compose/env |

Local prerequisites: Docker Desktop and `make`. Nothing else.

## How to start the new session

1. `cd ~/projects/privat/zorgscope && claude` (project memory in
   `~/.claude/projects/-Users-gernotstarke-projects-privat-zorgscope/memory/`
   is loaded automatically).
2. Paste the kickoff prompt below.
3. Stay in the loop at the checkpoints the coordinator announces
   (after each task's review, and at Task 8 / Task 15).

## Kickoff prompt (paste verbatim)

```text
You are the coordinator for building milestone M1 of zorgscope, a personal
dashboard (Go, hexagonal monolith, html/template + htmx, SQLite, Docker-only
toolchain). Requirements, architecture and a fully worked implementation plan
already exist in this repo — do not redesign anything.

Read first, in this order:
1. docs/plans/README.md            (roadmap, executor conventions, commit format)
2. docs/plans/2026-08-16-m1-walking-skeleton.md   (the plan: 15 tasks)
3. docs/architecture/05-building-block-view.md and 08-crosscutting-concepts.md
   (import rules, domain model, config, testing matrix)

Then execute the plan with superpowers:subagent-driven-development:
- one fresh subagent per task, in task order (tasks build on each other);
  give each subagent its task text verbatim plus the plan's header and
  "Global Constraints" section, and tell it to read the same two arc42
  chapters;
- two-stage review after every task (spec compliance, then code quality);
- all commands go through `make` (Docker); never assume a local Go or Node.
  `make go ARGS="..."` runs any go command in the container (added in Task 1);
- one commit per task on branch `m1-walking-skeleton`, message format
  `<type>(<scope>): <summary>` referencing FR-/QS- ids as in the plan;
- gates before calling M1 done: `make check` green (test, test-domain ≥90 %,
  lint 0 issues, docs-check) and `make e2e` 4/4 Playwright tests passing.

Branch `reference/m1-verified` holds an implementation extracted from this
plan that already passes every gate. Reviewers may diff against it when a
task's result looks wrong; implementers must not copy from it — the point
is that the plan alone is sufficient.

Checkpoints where you stop and report to me: after Task 1 (toolchain works),
after Task 8 (domain + store + scheduler complete), after Task 15 (open a PR
against main). If a subagent hits something the plan does not cover, do not
improvise silently — record it under "Deviations" in docs/plans/README.md
and tell me.

Not in scope: M2–M4, passkeys, fly.io deploy, any new dependency
(needs an ADR first).
```

## After M1

Ask the same session (or a new one, same recipe) to write the M2 plan
(Plausible, Todoist, feeds, Watch tiles) using superpowers:writing-plans,
grounded in the real M1 code. Open points O-1…O-6 in
`docs/requirements/08-assumptions-risks-open-points.md` should be answered
before M2 (feeds list, credentials/URLs to watch).
