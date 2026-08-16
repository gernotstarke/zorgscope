# 11. Risks and technical debt

Project‑level risks R‑1 … R‑7 are tracked in [requirements ch. 8](../requirements/08-assumptions-risks-open-points.md).
Architecture‑specific risks and known debt:

| Id | Risk / debt | Mitigation / plan |
|----|-------------|-------------------|
| AR‑1 | htmx polling of 5 tiles every 60 s from several open tabs multiplies requests. | Fragments are cheap (cache read + template), ETag/304; acceptable for one user. Could switch to SSE later. |
| AR‑2 | Single machine = no zero‑downtime deploy (few seconds outage). | Acceptable; fly rolling strategy with a second machine would need LiteFS/Postgres — not worth it. |
| AR‑3 | WebAuthn RP ID is bound to the host (`zorgscope.fly.dev`); moving to a custom domain requires re‑enrolment. | Documented in guides; decide domain early (O‑5). |
| AR‑4 | GraphQL query cost grows with repos; > ~40 repos might need batching. | `rateLimit` field monitored; registry supports per‑repo intervals. |
| AR‑5 | Todoist API in transition (REST v2 → unified v1). | Adapter isolated; contract fixtures; verify at implementation. |
| AR‑6 | Config baked into image means "add a repo" needs a deploy (~5 min CI). | Acceptable per QS‑4.1; hot reload exists locally; could mount config from volume later. |
| AR‑7 | No LLM/summarisation hooks implemented; `Enricher` port only sketched. | Deliberate YAGNI. |
