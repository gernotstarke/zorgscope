# 2. Stakeholders

| Id | Stakeholder | Interest |
|----|-------------|----------|
| S‑1 | **Gernot Starke** ("zorg") | Owner, sole user and operator. Decides scope, priorities and providers; owns every credential and the Fly account. |
| S‑2 | **Implementing agents and contributors** | Need small self-contained tasks, unambiguous acceptance criteria and tests they can run with Docker alone. |
| S‑3 | **External services** | GitHub and Fly.io. Impose rate limits, API versions and free-tier limits. |
| S‑4 | **Invited members** | A handful of iSAQB people with write access on the members repository. They get a personal view of public repositories they choose; they decide nothing about scope, hold no credential of the deployment, and their token lives only in their own cookie. |

Readers of the repository are not a stakeholder group with requirements of their own; the
requirements, decisions and concepts documents are public and legible on their own account, not
because a goal says so — G‑6, which used to state that, was retired in the 2026-09-15 stateless
reset along with every goal but G‑1 (see [01-goals.md](01-goals.md)).
