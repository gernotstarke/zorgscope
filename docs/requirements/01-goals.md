# 1. Goals and vision

## Vision

One page that answers "does anything need me right now?" — open it, glance, close it. zorgscope
collects the open issues and pull requests of the arc42 sites' repositories on GitHub, finds any of
them, and the people behind them, in seconds, and costs almost nothing to run.

## Goals

| Id | Goal |
|----|------|
| G‑1 | The user sees the open issues and pull requests of the configured GitHub repositories, and finds any of them, and the people behind them, in seconds. |

G‑2 through G‑6 were retired in the 2026-09-15 stateless reset (site statistics and task lists had
already gone on 2026-09-14; build status, out-of-band notification, the in-app documentation pages
and the cost/maintainability goals that assumed a database went with the reset itself). See
[ADR‑0010](../decisions/0010-stateless-no-database.md) for the reasoning; their ids are not reused.

## Quality goals

| Id | Quality goal | Why |
|----|--------------|-----|
| QG‑1 | **Completeness** — every open item of every watched repository is shown, and a failing repository never hides the others. | A dashboard that silently hides an open item, or a whole failing repository, is worse than no dashboard. |
| QG‑2 | **Speed** — the page stays light and a misbehaving upstream cannot hang it. | It is opened many times a day, for seconds at a time, from whatever connection is at hand. |
| QG‑3 | **Frugality** — hosting stays inside the free tier and GitHub's rate limit is never a constraint. | The value of the tool does not justify a subscription or a throttled dashboard. |
| QG‑4 | **Confidentiality** — upstream tokens never leave the backend and never appear in output. | The tokens grant write access to private repositories. |
| QG‑5 | **Maintainability** — small, testable units; the domain has no infrastructure dependencies. | The system is built and extended largely by LLM agents working from this documentation. |

QG‑2's scenarios changed shape in the 2026-09-15 reset: cold-start latency measured a database
warmed by an external cron ping (ADR‑0003), which is gone along with the database, so that scenario
was retired. It was not left empty, though — a 2026-09-16 review found the code still enforcing and
testing two other speed properties (page weight, and a pagination loop that cannot hang), so QG‑2
was kept for those; see [chapter 5](05-quality-requirements.md).

Every goal is priority **M** (v1).
