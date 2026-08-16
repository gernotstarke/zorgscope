# 6. Constraints

| Id | Constraint | Background |
|----|------------|------------|
| C‑1 | **Backend language: Go** (current stable, currently 1.26). | Owner preference; single static binary; strong std lib for HTTP/templates. |
| C‑2 | **Local toolchain: Docker + GNU make only.** Every developer/agent task (build, test, lint, e2e, docs check, deploy) runs inside containers via `make <target>`. No Go, Node, Playwright or flyctl installation on the host is assumed. | Reproducibility; cheap agents in sandboxes; owner's explicit requirement. |
| C‑3 | **Hosting: fly.io**, single always‑on machine, persistent volume, secrets via `fly secrets`. Local run and hosted run use the same image. | Owner has a fly.io account; cheap; simple. |
| C‑4 | **Source hosting: GitHub**, repository `gernotstarke/zorgscope`, CI on GitHub Actions. | Owner's account; free CI minutes for the repo. |
| C‑5 | **Single user**, no multi‑tenancy. Authentication by passkey (WebAuthn). | Personal dashboard; owner asked for 2FA‑grade privacy. |
| C‑6 | **Read‑only** towards all upstream systems in v1. | Avoid accidental changes; least‑privilege tokens. |
| C‑7 | **No third‑party runtime requests from the browser**: all CSS/JS/fonts self‑hosted; strict CSP. | Privacy, offline‑robustness of the tab, performance. |
| C‑8 | **Documentation**: English; requirements after req42, architecture after arc42, decisions as MADR; Markdown in the repo, rendered by GitHub. Stable ids everywhere. | Owner is arc42/req42 co‑author; docs must be exemplary. |
| C‑9 | **Repository layout**: `go.mod` at root; source in `cmd/`, `internal/`, `web/`; `docs/`, `test/`, `deploy/`, `config/` strictly separated (see [ADR‑0012](../architecture/decisions/ADR-0012-repository-layout.md)). Go unit tests are colocated with the code. | Go idiom + owner's separation‑of‑concerns requirement. |
| C‑10 | **Tests on every layer** (domain, adapters, http/app, UI/e2e) and in CI; TDD for implementation tasks. | G‑4; plan tasks are written test‑first. |
| C‑11 | **Licence MIT**, public repository (private data such as tokens or personal Todoist content never committed; monitored repo names are public knowledge). | Owner decision. |
| C‑12 | **Rate limits** of GitHub (5 000 GraphQL points/h, notifications REST), Plausible (600 req/h), Todoist (~450 req/15 min for REST) must never be exceeded; default intervals leave ≥ 10× headroom. | Provider terms. |
| C‑13 | **Timezone Europe/Berlin** for snapshot times and day grouping; configurable. | Owner location. |
| C‑14 | Implementation may be carried out by **less capable LLM agents**: tasks must be small, explicit, verifiable, and must not rely on implicit knowledge. | Owner requirement. |
