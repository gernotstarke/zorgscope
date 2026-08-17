# 1. Introduction and goals

zorgscope is a single-user status system. An always-on fly.io backend aggregates GitHub
issues/PRs/mentions/Actions status, Plausible statistics, credential/API-key expiry and TLS-certificate
expiry, highlighting what needs attention. A future Wails macOS client, same-origin browser client, or
both consume its client-neutral JSON API.

* Requirements overview: [docs/requirements](../requirements/README.md), functional epics E‑1 … E‑11.
* Quality goals (ordered): QG‑1 reliability of detection, QG‑2 perceived performance, QG‑3 security & low
  operating cost, QG‑4 flexibility of sources, QG‑5 compatibility, QG‑6 usability & aesthetics — see
  [requirements ch. 1](../requirements/01-goals.md) and scenarios in [ch. 5](../requirements/05-quality-requirements.md).
* Stakeholders: [requirements ch. 2](../requirements/02-stakeholders.md).

## Architectural consequences of the quality goals

| Goal | Consequence |
|------|-------------|
| QG‑1 | Detection logic is one pure function set in `internal/domain`, tested exhaustively; every fetch is isolated per source with backoff; failures are first‑class data (`FetchStatus`) shown in the UI. |
| QG‑2 | Read path never calls upstream: `/api/v1/dashboard` is assembled from SQLite; background polling pre-fetches; ETag avoids unchanged transfers. |
| QG‑3 | Runtime secrets are write-only and encrypted at rest under a Fly-provided master key; deployment trust-root values stay in environment/Fly secrets; redacting logger, TLS, non-root image and one fly machine. |
| QG‑4 | Source kinds sit behind `SourceFetcher`; runtime instances and all adjustable behaviour are revisioned through `/api/v1/config`; visual clients share one contract. |
| QG‑5 | Versioned JSON DTOs and contract tests isolate the core from Wails/browser presentation choices. |
| QG‑6 | API provides explicit empty/error/stale states; each visual client owns a polished, accessible design system. |
