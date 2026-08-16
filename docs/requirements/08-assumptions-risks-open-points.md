# 8. Assumptions, risks, open points

## Assumptions

| Id | Assumption | If wrong … |
|----|------------|-----------|
| A‑1 | The GitHub token has read access to all monitored repos incl. the private one and to notifications. | Private repo tile shows an auth error; fix by token scope. |
| A‑2 | Plausible sites are on plausible.io cloud and one API key covers all of them. | Config supports per‑site key override. |
| A‑3 | Todoist's current API can filter "overdue OR due before +7 days" server‑side. | Fetch all active tasks and filter locally (still cheap for a personal account). |
| A‑4 | Passkeys sync via iCloud Keychain across the owner's Apple devices; Arc and Vivaldi (Chromium) support WebAuthn platform authenticators. | Enrol a second passkey per device; roaming key as fallback. |
| A‑5 | One fly.io shared‑cpu‑1x/256 MB machine suffices (QS‑2.3). | Scale to 512 MB; still cheap. |
| A‑6 | Owner accepts that config changes on fly.io go through git push + CI deploy (minutes). | Add hot reload from a mounted volume file (FR‑8.5 covers local). |

## Risks

| Id | Risk | Prob. | Impact | Mitigation |
|----|------|-------|--------|------------|
| R‑1 | Detection bug marks a new issue as seen → contributor waits (violates G‑1). | low | high | Domain rule implemented once, property tests, e2e injection tests, "since last visit" secondary marker (FR‑7.4). |
| R‑2 | Upstream API changes (Todoist v1 migration, GitHub GraphQL deprecations, Plausible v2). | medium | medium | Adapters isolated (QS‑7.4); contract fixtures; visible staleness; optional live smoke job. |
| R‑3 | Rate‑limit exhaustion by too‑eager polling / manual refresh spam. | low | medium | Per‑source min interval, refresh throttling, backoff, budget check against GitHub `rateLimit` field. |
| R‑4 | Passkey lock‑out (lost devices). | low | medium | Multiple passkeys, documented recovery via `ENROLL_TOKEN` rotation + credential reset. |
| R‑5 | Cheap agents produce inconsistent code across tasks. | medium | medium | Strict lint, architecture tests (`depguard`), small tasks with explicit interfaces, review checkpoints. |
| R‑6 | fly.io machine restarts lose in‑memory state. | high | low | Everything relevant in SQLite on volume; startup catch‑up snapshot. |
| R‑7 | SQLite corruption/volume loss. | low | medium | WAL mode, fly volume snapshots, cache is re‑creatable; only dismissals & snapshots are precious (30 d). |

## Open points

| Id | Question | Owner | Needed by |
|----|----------|-------|-----------|
| O‑1 | Which news feeds / providers for the News tile? | S‑1 | before enabling the tile in prod (config only). |
| O‑2 | Slack notifications: which channel, which triggers? | S‑1 | v2. |
| O‑3 | Should "unanswered" also consider reactions (👍) as answers? | S‑1 | before implementing FR‑2.3 — default: no. |
| O‑4 | Exact grace period for "unanswered" (default 4 h) and stale threshold (30 d). | S‑1 | config default; adjustable. |
| O‑5 | Custom domain instead of `zorgscope.fly.dev`? | S‑1 | any time; fly certs. |
