# ADR-0004: SQLite via pure-Go driver for cached product state

* Status: accepted
* Date: 2026-08-16
* Deciders: Gernot Starke
* Related: FR-7.x, FR-9.x, QG-2, QG-3, R-7

## Context and problem statement

State to keep: cached items (re-creatable), daily snapshots, dismissals and fetch status. Single writer
process, single small machine. Runtime configuration and provider-secret overrides were later assigned to
separate volume files by ADR-0014; future passkey credential storage is not decided yet.

## Considered options

1. SQLite file on a fly volume, `modernc.org/sqlite` (pure Go, no CGO).
2. SQLite via `mattn/go-sqlite3` (CGO).
3. Managed Postgres (fly Postgres / Neon).
4. Files (JSON) on the volume.

## Decision outcome

**Chosen option: 1.** WAL mode, `busy_timeout`, embedded SQL migrations, one `Store` type implementing all
store ports. Static binary keeps the distroless image and cross‑compilation trivial.

### Consequences

* Good: zero infrastructure, sub‑millisecond reads (QG‑2), simple backup (fly volume snapshots or copy file),
  tests on temp files.
* Bad: single machine only (no horizontal scaling — irrelevant for one user); `modernc` is slightly slower
  than CGO SQLite (irrelevant at this size).
* Postgres rejected: cost, ops, latency for a personal tool. Files rejected: concurrent updates and queries get messy.
