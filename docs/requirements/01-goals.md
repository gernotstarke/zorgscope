# 1. Goals and vision

## Vision

> One glance at a new browser tab tells Gernot everything that needs his attention today —
> and nothing that doesn't.

zorgscope is a **single‑user, read‑mostly dashboard** that aggregates data from GitHub, Plausible,
Todoist and news feeds into a grid of tiles. It exists because this information is currently spread over
a dozen browser tabs and mail notifications, and issues opened by arc42 contributors occasionally go
unnoticed for days.

## Business goals

| Id | Goal | Rationale / measure |
|----|------|---------------------|
| G‑1 | **Never miss a new issue, PR, mention or review request** in the monitored repositories | Every such item is visibly highlighted until acknowledged; contributors receive a first reaction fast (target: within one working day). |
| G‑2 | **Compress** the daily status check of sites, tasks and news into one page | The new‑tab page replaces at least 6 regularly visited pages (GitHub notifications, Plausible ×n, Todoist, feed reader). |
| G‑3 | **Extensible in minutes**: adding a repository, site or feed must not require code changes | One configuration entry + deploy. |
| G‑4 | Serve as a **showcase for solid, agent‑friendly software engineering**: strict separation of concerns, tests on every layer, CI, documented decisions | The repo can be handed to an unfamiliar (human or LLM) developer who can add a source from the docs alone. |
| G‑5 | **Cheap and low‑maintenance**: one small cloud machine, no manual operations, no local toolchain beyond Docker + make | Hosting cost in the single‑digit‑euro range per month; zero recurring ops tasks. |
| G‑6 | **Never be surprised by an expiring or broken credential** — for zorgscope itself and for other apps the owner runs (e.g. status.arc42.org) | Every registered credential shows its remaining validity; warnings appear ≥ 14 days ahead; authentication failures of any source are highlighted immediately. |

## Top quality goals (ordered)

Detailed scenarios in [chapter 5](05-quality-requirements.md).

| Rank | Id | Quality goal | One‑liner |
|------|----|--------------|-----------|
| 1 | QG‑1 | **Reliability of detection** | New / unanswered items are detected correctly and completely; failures are visible, never silent. |
| 2 | QG‑2 | **Perceived performance** | A new tab shows meaningful content in < 1 s — everything is pre‑fetched and served from cache. |
| 3 | QG‑3 | **Security & low operating cost** | Secrets never leak; runs on one tiny always‑on machine; no manual ops. |
| 4 | QG‑4 | **Flexibility of sources** | New repo / site / feed in minutes, purely by configuration; new *kinds* of sources by adding one adapter. |
| 5 | QG‑5 | **Cross‑browser & device compatibility** | Works in Arc, Vivaldi, Firefox and Safari, on desktop and phone, without extensions. |
| 6 | QG‑6 | **Visual quality & usability** | Calm, structured, pleasing tile layout; highlights are unmistakable; nothing needs explanation. |

Non‑goals: multi‑tenancy, team features, real‑time push updates, mobile app, offline mode.
