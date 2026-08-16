# ADR-0002: Go modular monolith with hexagonal package structure

* Status: accepted
* Date: 2026-08-16
* Deciders: Gernot Starke
* Related: QG-1, QG-4, QS-7.x, C-1, C-14

## Context and problem statement

The system talks to four kinds of upstream APIs, keeps state, runs background jobs and serves a UI. It must be
easy for cheap agents to extend (new source kinds), and its detection logic must be testable in isolation.

## Decision drivers

* Testability of the "new/unanswered" rules without I/O (QG‑1).
* Adding a source kind must touch a minimal, predictable set of packages (QG‑4).
* Small deployable, single process on one machine (QG‑3).
* Agents work best with clear, enforced boundaries (C‑14).

## Considered options

1. Modular monolith, hexagonal (ports & adapters): `domain` ← `app` ← {`adapters`, `http`}.
2. Flat package layout ("just a Go web app") with adapters called directly from handlers.
3. Separate services (poller + web) communicating via DB/queue.

## Decision outcome

**Chosen option: 1.** One binary, dependencies point inward, interfaces in `internal/ports`, boundaries
enforced by `depguard`. Standard library first (`net/http`, `html/template`, `slog`, `database/sql`);
third‑party only where it clearly pays: `modernc.org/sqlite`, `github.com/go-webauthn/webauthn`,
`github.com/mmcdole/gofeed`, `gopkg.in/yaml.v3` (or `github.com/goccy/go-yaml`), a thin GraphQL HTTP call
(no code generation), `pgregory.net/rapid` for property tests.

### Consequences

* Good: domain 100 % pure and fast to test; each adapter swappable/fakeable; scheduler and HTTP share one
  process and one SQLite file (no coordination problem).
* Bad: a bit more ceremony (interfaces, view models) than a flat layout; acceptable and documented.
* Option 3 rejected: two deployables + shared DB semantics for a single user is pure overhead.
