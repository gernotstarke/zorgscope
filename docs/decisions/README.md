# Architecture decisions

Decisions are recorded in [MADR](https://adr.github.io/madr/) format, one file per decision, numbered
and never renumbered. A decision that is later reversed gets a new record that supersedes the old one;
the old record stays, because the reasoning is the point.

| # | Decision | File | Status |
|---|----------|------|--------|
| 0001 | Go modular monolith with a hexagonal core | `0001-go-modular-monolith.md` | accepted |
| 0002 | Server-rendered `html/template` plus htmx, no JavaScript build | `0002-server-rendered-htmx.md` | accepted |
| 0003 | Fly.io scaled to zero, refreshed by an external cron trigger | `0003-fly-scale-to-zero-external-cron.md` | accepted |
| 0004 | Turso and libSQL for persistence, `libsql-server` locally | `0004-turso-libsql.md` | accepted |
| 0005 | Embedded SQL migrations rather than a schema tool | `0005-embedded-sql-migrations.md` | accepted |
| 0006 | First-seen versus last-visit as the definition of "new" | `0006-first-seen-versus-last-visit.md` | accepted |
| 0007 | Token sign-in with a session cookie derived from the token | `0007-token-sign-in-derived-cookie.md` | superseded |
| 0008 | Docker and make as the only local toolchain | `0008-docker-and-make-only.md` | accepted |
| 0009 | GitHub sign-in gated on push access to the repository | `0009-github-sign-in-push-access.md` | accepted |

The records are kept current with the design specs. A spec that decides something — the
[reset](../superpowers/specs/2026-08-17-zorgscope-reset-design.md), the
[GitHub sign-in and focus](../superpowers/specs/2026-09-14-github-signin-and-focus-design.md) — is
followed by a record for each decision it made, so that the reasoning can be read on its own rather
than excavated from the document that happened to occasion it.

The previous decision set (ADR‑0001…0014 under the former `docs/architecture/decisions/`) was removed
on 2026-08-17 rather than superseded one by one. It described an always-on machine with a persistent
volume, an in-process scheduler, daily snapshots, per-item dismissals, a runtime configuration API and
passkey authentication — a system that no longer exists in any part. Its reasoning survives in git
history.

Use [`adr-template.md`](adr-template.md) for new records.
