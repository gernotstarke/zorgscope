# 1. Goals and vision

## Vision

One page that answers "does anything need me right now?" — open it, glance, close it. zorgscope
collects the open issues and pull requests of the arc42 sites' repositories on GitHub, marks what is
new since the last look, and costs almost nothing to run.

## Goals

| Id | Goal |
|----|------|
| G‑1 | The user sees open issues and pull requests of the configured GitHub repositories, with build status, and recognises at a glance which of them are new. |
| G‑2 | ~~The user sees visitor numbers of the configured Plausible sites and how they moved.~~ — *retired 2026-09-14, see [the design](../superpowers/specs/2026-09-14-github-signin-and-focus-design.md)* |
| G‑3 | ~~The user sees the Todoist tasks that are overdue or due today.~~ — *retired 2026-09-14, see [the design](../superpowers/specs/2026-09-14-github-signin-and-focus-design.md)* |
| G‑4 | The user is notified out-of-band when something interesting appears, without having to open the page. |
| G‑5 | Running zorgscope costs at most a euro a month and needs no routine maintenance. |
| G‑6 | The repository is readable as a worked example of req42 requirements and MADR decisions; the running system serves that documentation. |

## Quality goals

| Id | Quality goal | Why |
|----|--------------|-----|
| QG‑1 | **Correctness of "new"** — an item is marked new exactly while the user has not seen it. | A dashboard that cries wolf, or silently hides something, is worse than no dashboard. |
| QG‑2 | **Speed** — the page is usable within a moment of opening, even after the machine has been asleep. | It is opened many times a day, for seconds at a time. |
| QG‑3 | **Frugality** — hosting and storage stay inside the free/cheap tiers. | The value of the tool does not justify a subscription. |
| QG‑4 | **Confidentiality** — upstream tokens never leave the backend and never appear in output. | The tokens grant write access to private repositories. |
| QG‑5 | **Maintainability** — small, testable units; the domain has no infrastructure dependencies. | The system is built and extended largely by LLM agents working from this documentation. |

G‑4 is priority **S** (v2); every other goal is **M** (v1).
