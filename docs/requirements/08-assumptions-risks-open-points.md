# 8. Assumptions, risks, open points

## Assumptions

| Id | Assumption | If wrong … |
|----|------------|-----------|
| A‑1 | The GitHub token has read access to all monitored repos incl. the private one and to notifications. | Private repo tile shows an auth error; fix by token scope. |
| A‑2 | Plausible sites are on plausible.io cloud and one API key covers all of them. | Config supports per‑site key override. |
| A‑3 | **Retired:** Todoist was removed from product scope. | No impact; identifier retained for traceability. |
| A‑4 | A passkey device-authorisation flow can use the system browser and PKCE for a future Wails client. | Keep bootstrap bearer authentication longer or ship the same-origin browser client first. |
| A‑5 | One fly.io shared‑cpu‑1x/256 MB machine suffices (QS‑2.3). | Scale to 512 MB; still cheap. |
| A‑7 | GitHub returns the `GitHub-Authentication-Token-Expiration` header for expiring tokens (fine‑grained and classic with expiry). | Fall back to a manual `watch.credentials` entry. |
| A‑6 | The owner accepts that deployment-only trust-root changes still require fly.io/environment access; all ordinary runtime changes use the config API. | Reclassify only values that can be safely remotely mutable; never expose the master key or bootstrap auth control through the API. |
| A‑8 | `ZORGSCOPE_CONFIG_KEY`, supplied as a Fly secret, can protect all runtime secrets with authenticated encryption and can be backed up/rotated safely. | Introduce key-version metadata and a staged rotation procedure before storing production secrets. |

## Risks

| Id | Risk | Prob. | Impact | Mitigation |
|----|------|-------|--------|------------|
| R‑1 | Detection bug marks a new issue as seen → contributor waits (violates G‑1). | low | high | Domain rule implemented once, property tests, e2e injection tests, "since last visit" secondary marker (FR‑7.4). |
| R‑2 | Upstream API changes (GitHub GraphQL/REST deprecations, Plausible v2). | medium | medium | Adapters isolated (QS‑7.4); contract fixtures; visible staleness; optional live smoke job. |
| R‑3 | Rate‑limit exhaustion by too‑eager polling / manual refresh spam. | low | medium | Per‑source min interval, refresh throttling, backoff, budget check against GitHub `rateLimit` field. |
| R‑4 | Passkey lock‑out (lost devices). | low | medium | Multiple passkeys, documented recovery via `ENROLL_TOKEN` rotation + credential reset. |
| R‑5 | Cheap agents produce inconsistent code across tasks. | medium | medium | Strict lint, architecture tests (`depguard`), small tasks with explicit interfaces, review checkpoints. |
| R‑6 | fly.io machine restarts lose in‑memory state. | high | low | Everything relevant in SQLite on volume; startup catch‑up snapshot. |
| R‑7 | SQLite corruption/volume loss. | low | medium | WAL mode, fly volume snapshots, cache is re‑creatable; only dismissals & snapshots are precious (30 d). |
| R‑8 | A configuration API bug or stolen bootstrap token could expose or replace upstream credentials. | low | high | Write-only responses, authenticated encryption, least-privilege provider tokens, strict auth/rate limits, revision audit, secret canary tests and rapid bootstrap-token rotation. |
| R‑9 | Losing or rotating `ZORGSCOPE_CONFIG_KEY` incorrectly makes stored upstream secrets unreadable. | low | high | Document backup and key-versioned rotation; fail closed; never silently overwrite undecryptable values. |
| R‑10 | Bootstrap bearer auth is weaker and less revocable than passkey-backed device auth. | medium | medium | Treat it as an explicit bootstrap stage, store client token in Keychain where applicable, rate limit, rotate easily, and implement FR-9.2/9.3 before broader access. |

## Open points

| Id | Question | Owner | Needed by |
|----|----------|-------|-----------|
| O‑1 | **Closed:** news feeds are not part of zorgscope. | S‑1 | Closed 2026-08-16. |
| O‑2 | Slack notifications: which channel, which triggers? | S‑1 | v2. |
| O‑3 | Should "unanswered" also consider reactions (👍) as answers? | S‑1 | before implementing FR‑2.3 — default: no. |
| O‑4 | Exact grace period for "unanswered" (default 4 h) and stale threshold (30 d). | S‑1 | config default; adjustable. |
| O‑6 | Which credentials/API keys and TLS endpoints should be seeded beyond zorgscope's own tokens? | S‑1 | before production; editable through the config API. |
| O‑5 | Custom domain instead of `zorgscope.fly.dev`? | S‑1 | any time; fly certs. |
| O‑7 | Which visual clients ship first: Wails macOS, same-origin browser, or both? | S‑1 | after backend API/config vertical slice. |
| O‑8 | Exact passkey device-authorisation protocol and credential lifetimes? | S‑1 | before FR-9.2 implementation; bootstrap bearer token is interim only. |
