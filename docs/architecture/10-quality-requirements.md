# 10. Quality requirements

Quality tree and scenarios QS‑1.x … QS‑7.x are maintained in
[requirements ch. 5](../requirements/05-quality-requirements.md). Traceability from scenario to
architectural means:

| Scenario group | Realised by |
|----------------|-------------|
| QS‑1.9/1.10 credentials | `watch` adapter, `CredentialSink`, `ErrAuth` → `AuthFailed` ([8.2](08-crosscutting-concepts.md#82-attention-rules-the-heart-of-qg1), [8.7](08-crosscutting-concepts.md#87-error-handling)) |
| QS‑1.x reliability | domain rules ([8.2](08-crosscutting-concepts.md#82-attention-rules-the-heart-of-qg1)), scheduler isolation/backoff ([6.1](06-runtime-view.md)), snapshot catch‑up ([6.4](06-runtime-view.md)), staleness UI |
| QS‑2.x performance | cache-only JSON read path ([6.2](06-runtime-view.md)), ETag/304 and background prefetch |
| QS‑3.x security/ops | write-only authenticated encryption and bearer/passkey boundary ([8.6](08-crosscutting-concepts.md#86-security)), ADR‑0014, ADR‑0005, distroless image, CI |
| QS‑4.x flexibility | revisioned config API ([8.3](08-crosscutting-concepts.md#83-configuration)), `SourceFetcher` port + registry ([5](05-building-block-view.md)), transactional activation ([6.7](06-runtime-view.md#67-runtime-configuration-update)) |
| QS‑5.x compatibility | versioned client-neutral JSON, stable DTOs and contract tests (ADR-0014) |
| QS‑6.x usability | semantic explicit states from API; client-specific polished design systems ([8.5](08-crosscutting-concepts.md#85-client-presentation)) |
| QS‑7.x maintainability | package rules ([5](05-building-block-view.md)), ADR‑0010, ADR‑0012, plans |
