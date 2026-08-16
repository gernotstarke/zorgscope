# 10. Quality requirements

Quality tree and scenarios QS‑1.x … QS‑7.x are maintained in
[requirements ch. 5](../requirements/05-quality-requirements.md). Traceability from scenario to
architectural means:

| Scenario group | Realised by |
|----------------|-------------|
| QS‑1.9/1.10 credentials | `watch` adapter, `CredentialSink`, `ErrAuth` → `AuthFailed` ([8.2](08-crosscutting-concepts.md#82-attention-rules-the-heart-of-qg1), [8.7](08-crosscutting-concepts.md#87-error-handling)) |
| QS‑1.x reliability | domain rules ([8.2](08-crosscutting-concepts.md#82-attention-rules-the-heart-of-qg1)), scheduler isolation/backoff ([6.1](06-runtime-view.md)), snapshot catch‑up ([6.4](06-runtime-view.md)), staleness UI |
| QS‑2.x performance | cache‑only read path ([6.2](06-runtime-view.md)), self‑hosted tiny assets, ETag fragments |
| QS‑3.x security/ops | [8.6](08-crosscutting-concepts.md#86-security), ADR‑0006, ADR‑0005, distroless image, CI |
| QS‑4.x flexibility | config schema ([8.3](08-crosscutting-concepts.md#83-configuration-configzorgscopeyaml)), `SourceFetcher` port + registry ([5](05-building-block-view.md)) |
| QS‑5.x compatibility | server rendering + progressive enhancement (ADR‑0003), responsive grid ([8.5](08-crosscutting-concepts.md#85-ui-and-design-system)), Playwright matrix |
| QS‑6.x usability | design tokens, explicit states, badges with text ([8.5](08-crosscutting-concepts.md#85-ui-and-design-system)) |
| QS‑7.x maintainability | package rules ([5](05-building-block-view.md)), ADR‑0010, ADR‑0012, plans |
