# 2. Stakeholders

| Id | Stakeholder | Role / interest | Involvement |
|----|-------------|-----------------|-------------|
| S‑1 | **Gernot Starke** ("zorg") | Owner, only user, operator, product manager. Wants a fast, reliable, pretty new‑tab page; decides priorities and providers. | Approves requirements, plans, UI; owns all credentials and the fly.io account. |
| S‑2 | **arc42 / esabuch / site contributors** | Indirect: people who open issues and PRs in the monitored repositories. They benefit from faster answers. | None; never see the dashboard. |
| S‑3 | **Implementing agents & developers** | LLM agents (possibly cheaper models) and occasional human contributors executing the implementation plan. Need small, self‑contained tasks, unambiguous acceptance criteria, tests, and a Docker‑only toolchain. | Execute plans, extend sources. |
| S‑4 | **External service providers** | GitHub, Plausible, Todoist, feed publishers, fly.io. Impose rate limits, API versions, terms of use. | Interfaces documented in [chapter 3](03-scope-and-context.md). |
| S‑5 | **Reviewers / readers of the repository** | People who look at zorgscope as an example of arc42/req42 usage and agent‑driven development. | Read documentation; may open issues. |
