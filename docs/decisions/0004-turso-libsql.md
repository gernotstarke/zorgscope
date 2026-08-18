# 0004. Turso and libSQL for persistence, `libsql-server` locally

* Status: accepted
* Date: 2026-08-17
* Requirements: C‑3, C‑4, C‑8, QS‑3.1, QS‑3.2

## Context and problem statement

C‑3's `min_machines_running = 0` means "nothing may live on a local disk" — a Fly Machine that is
stopped between requests cannot be trusted to still have its filesystem contents next time it
starts, and even where a Fly Volume would survive a stop, it ties the machine to one host and
complicates the "any instance can start cold" model scale-to-zero relies on. Design §1 names this
directly as the second consequence of the new constraints: "Turso removes the volume. With no
local disk, all state — items, metrics, run history, last-visit time — lives in the database."
Something outside the machine has to hold every row zorgscope has, and local development needs the
same thing without a network dependency on that external service.

## Considered options

* Turso (managed libSQL) in production, `libsql-server` in a local container — one SQL dialect
  (SQLite) and one pure-Go driver for both.
* A Fly Volume with a local SQLite file — what the previous design used.
* A managed relational database over the network (Fly Postgres, or an external Postgres provider).

## Decision outcome

Chosen: **Turso and libSQL**, with `libsql-server` as the identical-protocol local stand-in, because
it is the only option that removes the volume C‑3 forbids while keeping exactly one SQL dialect and
one driver across both environments.

`internal/adapters/libsql/store.go` is the only package permitted to import
`github.com/tursodatabase/libsql-client-go` (enforced by the `db-driver` `depguard` rule from
[ADR‑0001](0001-go-modular-monolith.md)), and its `Open` function takes the same `rawURL` and
`authToken` shape whether the URL points at Turso or at a local `libsql-server`:

```go
func Open(rawURL, authToken string) (*Store, error) {
	dsn, err := dsn(rawURL, authToken)
	...
	db, err := sql.Open("libsql", dsn)
	...
}
```

`deploy/compose.yml` runs the identical image production would talk to, `libsql-server`, as the
local `db` service, and overrides `TURSO_URL` to point at it inside the Compose network — "so a
production URL cannot be hit from a local run", per its own comment — with `TURSO_AUTH_TOKEN` empty,
because a local `libsql-server` needs none. Nothing in the adapter branches on which one it is
talking to; the same code path is exercised by `make test` (against `libsql-server`) and by
production (against Turso).

### Consequences

* Good: local tests exercise the real SQL dialect and the real driver, not a substitute — the store
  tests (`store_test.go`, `store_more_test.go`) run against a container, not an in-memory fake, so a
  behaviour that only shows up in SQLite's actual `ON CONFLICT` semantics (which is exactly what
  ADR‑0006 depends on) is caught before it reaches Turso.
* Good: Turso's free tier is a genuine fit for C‑8's budget and QS‑3.2's ≤ 50 MB / ≤ 1 million row
  reads per month for a single-user tool refreshed every fifteen minutes.
* Bad: production data now depends on a third party's availability and pricing, not on infrastructure
  Fly alone controls. Design §10 names this risk explicitly and its mitigation is architectural, not
  contractual: "the store is one adapter behind `ports.Store`, and the dialect is plain SQLite;
  moving to a Fly volume or another libSQL host is an adapter change" — a real mitigation, but one
  that would still be a migration, not a configuration flag.
* Neutral: every timestamp is stored as an RFC 3339 UTC string rather than a native temporal type,
  because that is what both libSQL's SQLite dialect and simple lexicographic comparison (used by the
  refresh lease, ADR‑0006's neighbour) need; this is a consequence of the dialect choice, not of
  Turso specifically.

## Pros and cons of the options

### Turso and libSQL, `libsql-server` locally

* Good: see Decision outcome above.
* Bad: see Consequences above.

### Fly Volume with a local SQLite file

* Bad: this is what the previous design did, and it is exactly what C‑3 rules out: a volume ties
  state to one Machine instance, which conflicts with "nothing may live on a local disk" and with a
  cold start that may land on a differently provisioned Machine. It was a reasonable choice for an
  always-on machine (ADR‑0003's rejected alternative); once scale-to-zero was fixed, it stopped
  being available at all rather than merely being worse.
* Good: none over the chosen option once C‑3 is taken as given — a local file is simpler
  infrastructure, but only in a runtime model this project no longer has.

### Managed relational database over the network (Postgres)

* Good: mature tooling, wide operational familiarity, and a real free tier exists on more than one
  provider.
* Bad: a second dialect and a second driver to hold in mind alongside the SQLite semantics that
  `libsql-server` already gives local development for free; nothing in the schema (§5 of the
  design) needs anything Postgres offers that SQLite does not. Choosing it would trade the "one
  dialect, one driver, both environments" property this decision is built around for tooling
  maturity the project's scale does not need.
