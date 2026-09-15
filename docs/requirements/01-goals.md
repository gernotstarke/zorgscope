# 1. Goals and vision

## Vision

One page that answers "does anything need me right now?" — open it, glance, close it. zorgscope
collects the open issues and pull requests of the arc42 sites' repositories on GitHub, marks what is
new since the last look, and costs almost nothing to run.

## Goals

| Id | Goal |
|----|------|
| G‑1 | The user sees open issues and pull requests of the configured GitHub repositories, and recognises at a glance which of them are new. |

G‑2 through G‑6 were retired in the 2026-09-15 stateless reset (site statistics and task lists had
already gone on 2026-09-14; build status, out-of-band notification, the in-app documentation pages
and the cost/maintainability goals that assumed a database went with the reset itself). See
[ADR‑0010](../decisions/0010-stateless-no-database.md) for the reasoning; their ids are not reused.

## Quality goals

| Id | Quality goal | Why |
|----|--------------|-----|
| QG‑1 | **Correctness of "new"** — an item is marked new exactly while the user has not marked it seen. | A dashboard that cries wolf, or silently hides something, is worse than no dashboard. |
| QG‑3 | **Frugality** — hosting stays inside the free tier and GitHub's rate limit is never a constraint. | The value of the tool does not justify a subscription or a throttled dashboard. |
| QG‑4 | **Confidentiality** — upstream tokens never leave the backend and never appear in output. | The tokens grant write access to private repositories. |
| QG‑5 | **Maintainability** — small, testable units; the domain has no infrastructure dependencies. | The system is built and extended largely by LLM agents working from this documentation. |

QG‑2 (Speed) was retired 2026-09-15: its scenarios measured a database warmed by an external cron
ping (ADR‑0003), which is gone along with the database. Nothing replaces it — a stateless process
either answers from its in-memory cache or pays one fetch, and neither is currently held to a
number.

Every remaining goal is priority **M** (v1).
