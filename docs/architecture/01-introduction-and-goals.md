# 1. Introduction and goals

zorgscope is a single‑user web dashboard that aggregates GitHub issues/PRs/CI status, Plausible statistics,
Todoist tasks and news feeds into tiles, highlighting what needs attention. It is the browser new‑tab page of
its owner.

* Requirements overview: [docs/requirements](../requirements/README.md), functional epics E‑1 … E‑10.
* Quality goals (ordered): QG‑1 reliability of detection, QG‑2 perceived performance, QG‑3 security & low
  operating cost, QG‑4 flexibility of sources, QG‑5 compatibility, QG‑6 usability & aesthetics — see
  [requirements ch. 1](../requirements/01-goals.md) and scenarios in [ch. 5](../requirements/05-quality-requirements.md).
* Stakeholders: [requirements ch. 2](../requirements/02-stakeholders.md).

## Architectural consequences of the quality goals

| Goal | Consequence |
|------|-------------|
| QG‑1 | Detection logic is one pure function set in `internal/domain`, tested exhaustively; every fetch is isolated per source with backoff; failures are first‑class data (`FetchStatus`) shown in the UI. |
| QG‑2 | Read path never calls upstream: page and tiles render from SQLite cache; background scheduler pre‑fetches; tiny, self‑hosted assets, ETag on fragments. |
| QG‑3 | Secrets only via env, redacting logger, strict CSP, passkeys, distroless non‑root image, single fly machine, CI deploy. |
| QG‑4 | Source kinds behind one port (`SourceFetcher`), registry maps YAML sections to adapters; tiles are templates keyed by kind. |
| QG‑5 | Server‑rendered HTML + progressive enhancement via htmx; responsive CSS grid; no browser‑specific APIs except WebAuthn. |
| QG‑6 | Design tokens (CSS custom properties), explicit empty/error/stale states, badges with text. |
