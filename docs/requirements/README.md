# zorgscope — Requirements

Structured after [req42](https://req42.de). Every requirement has a stable id referenced from the
design, the implementation plan, tests and commit messages.

| # | Chapter | Ids |
|---|---------|-----|
| 1 | [Goals and vision](01-goals.md) | G‑x, QG‑x |
| 2 | [Stakeholders](02-stakeholders.md) | S‑x |
| 3 | [Constraints](03-constraints.md) | C‑x |
| 4 | [Functional requirements](04-functional-requirements.md) | E‑x (epics), FR‑x (stories) |
| 5 | [Quality requirements](05-quality-requirements.md) | QS‑x (scenarios) |
| 6 | [Glossary](06-glossary.md) | — |

Priorities use MoSCoW: **M**ust (v1), **S**hould (v2), **W**on't (explicitly out of scope).

These requirements were reset twice: on 2026-08-17, when a considerably larger set was cut back to
what the single user actually asked for (that design is removed, see git history), and again on
2026-09-15, when everything that assumed a database — build status, refresh runs, notifications,
the in-app documentation pages — was retired along with the database itself. See
[ADR‑0010](../decisions/0010-stateless-no-database.md) and
`docs/superpowers/specs/2026-09-15-stateless-reset-design.md` for the reasoning behind the second
reset.
