# 11. Risks and technical debt

Project‑level risks R‑1 … R‑7 are tracked in [requirements ch. 8](../requirements/08-assumptions-risks-open-points.md).
Architecture‑specific risks and known debt:

| Id | Risk / debt | Mitigation / plan |
|----|-------------|-------------------|
| AR‑1 | Multiple clients polling the full snapshot multiply requests. | Cache-only JSON reads and ETag/304 are cheap for one user; add an invalidation-only SSE stream later if needed. |
| AR‑2 | Single machine = no zero‑downtime deploy (few seconds outage). | Acceptable; fly rolling strategy with a second machine would need LiteFS/Postgres — not worth it. |
| AR‑3 | Future WebAuthn RP ID is bound to the host; moving domains after passkey enrolment requires re-enrolment. | Decide domain before FR-9.2; bootstrap auth is unaffected. |
| AR‑4 | GraphQL query cost grows with repos; > ~40 repos might need batching. | `rateLimit` field monitored; registry supports per‑repo intervals. |
| AR‑5 | **Retired:** Todoist was removed from scope. | Identifier retained for traceability. |
| AR‑6 | Config persistence and scheduler replacement cross file/runtime boundaries. | Mitigated by one serialized manager transaction: build before replacing the old generation and restore exact prior files/state/revision on activation failure; tests cover rollback and ordering. |
| AR‑7 | No LLM/summarisation hooks implemented; `Enricher` port only sketched. | Deliberate YAGNI. |
| AR‑8 | Loss, reuse or incorrect rotation of `ZORGSCOPE_CONFIG_KEY` can expose or strand provider secrets. | Independent high-entropy key, secure backup, key-version metadata, documented staged rotation, fail closed. |
| AR‑9 | Bootstrap bearer token has broad authority over data and config. | Rate limit, redact, rotate, store in Keychain, and replace with passkey-backed revocable device credentials. |
| AR‑10 | API/schema drift can split Wails and browser behaviour. | Versioned DTOs, compatibility policy and shared contract tests; breaking changes require `/api/v2`. |
