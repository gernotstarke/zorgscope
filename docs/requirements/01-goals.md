# 1. Goals and vision

## Vision

> One glance at zorgscope tells Gernot everything that needs his attention today — and nothing that
> doesn't, regardless of which supported client he uses.

zorgscope is a **single-user, read-mostly status service** with an always-on backend on fly.io and one or
more visual clients. It aggregates GitHub activity and Actions status, Plausible statistics, and expiry
information for credentials/API keys and TLS certificates. It exists because this information is spread
over many pages and notifications, and important changes can otherwise go unnoticed for days.

## Business goals

| Id | Goal | Rationale / measure |
|----|------|---------------------|
| G‑1 | **Never miss a new issue, PR, mention or review request** in the monitored repositories | Every such item is visibly highlighted until acknowledged; contributors receive a first reaction fast (target: within one working day). |
| G‑2 | **Compress** the daily status check of repositories, builds, sites and expiries into one pleasing visual client | One client replaces the regularly visited GitHub, Actions, Plausible and credential/certificate pages. |
| G‑3 | **Configurable in minutes**: adding or changing a repository, site, credential or TLS endpoint must not require code changes or a deploy | Every runtime property is editable through the authenticated configuration API and takes effect after validation. |
| G‑4 | Serve as a **showcase for solid, agent‑friendly software engineering**: strict separation of concerns, tests on every layer, CI, documented decisions | The repo can be handed to an unfamiliar (human or LLM) developer who can add a source from the docs alone. |
| G‑5 | **Cheap and low‑maintenance**: one small cloud machine, no manual operations, no local toolchain beyond Docker + make | Hosting cost in the single‑digit‑euro range per month; zero recurring ops tasks. |
| G‑6 | **Never be surprised by an expiring or broken credential** — for zorgscope itself and for other apps the owner runs (e.g. status.arc42.org) | Every registered credential shows its remaining validity; warnings appear ≥ 14 days ahead; authentication failures of any source are highlighted immediately. |

## Top quality goals (ordered)

Detailed scenarios in [chapter 5](05-quality-requirements.md).

| Rank | Id | Quality goal | One‑liner |
|------|----|--------------|-----------|
| 1 | QG‑1 | **Reliability of detection** | New / unanswered items are detected correctly and completely; failures are visible, never silent. |
| 2 | QG‑2 | **Perceived performance** | A client shows meaningful cached content in < 1 s; upstream work never blocks reads. |
| 3 | QG‑3 | **Security & low operating cost** | Upstream secrets never leak; stored secrets are encrypted at rest; one tiny always-on service needs no routine operations. |
| 4 | QG‑4 | **Flexibility of sources and clients** | Runtime configuration is client-neutral; new source instances need no code or deploy; new clients use the same API. |
| 5 | QG‑5 | **Client compatibility** | The API supports a future macOS Wails client and an optional same-origin browser client without client-specific domain logic. |
| 6 | QG‑6 | **Visual quality & usability** | Supported visual clients are calm, structured and highly pleasing; attention and failure states are unmistakable. |

Non-goals: multi-tenancy, team features, Todoist, news feeds, writing to upstream providers, and choosing
the final visual client technology in this increment.
