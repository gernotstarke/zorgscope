# 3. Constraints

| Id | Constraint | Consequence |
|----|------------|-------------|
| C‑1 | Implementation language is **Go**. | One statically linked binary; no runtime to install. |
| C‑2 | **Docker and GNU make are the only tools installed locally** ([ADR‑0008](../decisions/0008-docker-and-make-only.md)). No Go toolchain, no Node, no database client on the host. | Every `make` target runs in a container; the build must not need cgo. |
| C‑3 | Hosting is **Fly.io, scaled to zero** (`min_machines_running = 0`). | No process survives between requests; nothing may rely on an in-process scheduler or on a local disk still holding what it held last time. |
| C‑10 | The client signs in through a **GitHub OAuth App registered per environment** ([ADR‑0009](../decisions/0009-github-sign-in-push-access.md)). | Local and production need their own client id/secret pair, because an OAuth App has exactly one callback URL; the two pairs must not be crossed. |

C‑4 through C‑9 (persistence, the external cron trigger, and the cost/publication constraints that
assumed a database) were retired in the 2026-09-15 stateless reset; see
[ADR‑0010](../decisions/0010-stateless-no-database.md). Their ids are not reused. The facts some of
them stated are still true in substance — the repository is still public under MIT with secrets
only as Fly secrets and a local `.env`, and the client is still server-rendered `html/template` plus
htmx — they are simply no longer formalised as constraints with their own id and consequence.
