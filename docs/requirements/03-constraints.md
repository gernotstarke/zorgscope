# 3. Constraints

| Id | Constraint | Consequence |
|----|------------|-------------|
| C‑1 | Implementation language is **Go**. | One statically linked binary; no runtime to install. |
| C‑2 | **Docker and GNU make are the only tools installed locally.** No Go toolchain, no Node, no database client on the host. | Every target runs in a container; the build must not need cgo. |
| C‑3 | Hosting is **Fly.io**, scaled to zero (`min_machines_running = 0`). | No process survives between requests; nothing may live on a local disk, and nothing may rely on an in-process scheduler. |
| C‑4 | Persistence is **Turso** (libSQL) in production and a **libsql-server container** locally. | One pure-Go driver for both; SQL dialect is SQLite. |
| C‑5 | Refresh is triggered **externally by cron-job.org**, as at `status.arc42.org-site`. | The refresh is an authenticated HTTP endpoint, not a background job; its interval is configuration, not code. |
| C‑6 | The client is **server-rendered `html/template` plus htmx**; no JavaScript build step. | Vendored htmx, hand-written CSS. |
| C‑7 | Documentation is **Markdown**: req42 requirements, MADR decisions, prose concepts. | Rendered by the running system; no separate site generator. |
| C‑8 | Total operating cost **≤ 1 €/month**. | Free tiers of Fly, Turso and cron-job.org. |
| C‑9 | The repository is **public** under MIT; secrets exist only as Fly secrets and a local `.env`. | No credential, host name of a private system, or personal data in the repository. |
