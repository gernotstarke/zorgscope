# 6. Constraints

| Id | Constraint | Background |
|----|------------|------------|
| C‑1 | **Backend language: Go** (current stable, currently 1.26). | Owner preference; single static binary; strong standard library for HTTP and concurrency. |
| C‑2 | **Local toolchain: Docker + GNU make only.** Every developer/agent task (build, test, lint, e2e, docs check, deploy) runs inside containers via `make <target>`. No Go, Node, Playwright or flyctl installation on the host is assumed. | Reproducibility; cheap agents in sandboxes; owner's explicit requirement. |
| C‑3 | **Hosting: fly.io**, single always‑on machine, persistent volume, secrets via `fly secrets`. Local run and hosted run use the same image. | Owner has a fly.io account; cheap; simple. |
| C‑4 | **Source hosting: GitHub**, repository `gernotstarke/zorgscope`, CI on GitHub Actions. | Owner's account; free CI minutes for the repo. |
| C‑5 | **Single user**, no multi-tenancy. Bootstrap authentication uses one bearer token; passkey-backed, revocable device authentication is the target. | Personal status service; upstream data and runtime configuration are private. |
| C‑6 | **Read‑only** towards all upstream systems in v1. | Avoid accidental changes; least‑privilege tokens. |
| C‑7 | **Visual clients never call upstream providers directly**; GitHub/Plausible/API-key credentials remain on fly.io. An optional browser client self-hosts its assets and uses strict CSP. | One security boundary, consistent polling, privacy and performance. |
| C‑8 | **Documentation**: English; requirements after req42, architecture after arc42, decisions as MADR; Markdown in the repo, rendered by GitHub. Stable ids everywhere. | Owner is arc42/req42 co‑author; docs must be exemplary. |
| C‑9 | **Repository layout**: `go.mod` at root; backend source in `cmd/`, `internal/`; clients, `docs/`, `test/`, `deploy/`, `config/` remain separated (see [ADR‑0012](../architecture/decisions/ADR-0012-repository-layout.md)). Go unit tests are colocated with the code. | Go idiom + owner's separation-of-concerns requirement. |
| C‑10 | **Tests on every layer** (domain, adapters, http/app, UI/e2e) and in CI; TDD for implementation tasks. | G‑4; plan tasks are written test‑first. |
| C‑11 | **Licence MIT**, public repository (private data such as tokens, API keys and collected private-repository data are never committed; monitored public repo names are public knowledge). | Owner decision. |
| C‑12 | **Rate limits** of GitHub (5 000 GraphQL points/h, notifications REST) and Plausible (600 req/h) must never be exceeded; default intervals leave >= 10x headroom. | Provider terms. |
| C‑13 | **Timezone Europe/Berlin** for snapshot times and day grouping; configurable. | Owner location. |
| C‑14 | Implementation may be carried out by **less capable LLM agents**: tasks must be small, explicit, verifiable, and must not rely on implicit knowledge. | Owner requirement. |
| C‑15 | **All runtime configuration is mutable through the authenticated `/api/v1/config` family.** Deployment-only bootstrap, storage, network, logging and master-encryption values remain environment/Fly secrets. | Visual clients must be able to administer the service completely without a deploy, while avoiding remote mutation of the service's trust root. |
| C‑16 | Runtime upstream secrets are **write-only and encrypted at rest** with authenticated encryption under a master key supplied by fly.io secret/environment. | A database/volume leak must not disclose provider credentials; clients never receive stored plaintext. |
| C‑17 | The backend API is **client-neutral and versioned**. Wails macOS and same-origin browser clients are both valid future consumers; neither is selected as the only UI yet. | Preserve the option to ship one or both clients without duplicating business logic. |
