# 0005. Embedded SQL migrations rather than a schema tool

* Status: superseded by [0010](0010-stateless-no-database.md) — the stateless reset removed the
  schema this record migrated, so there is nothing left to apply a migration to.
* Date: 2026-08-17
* Requirements: C‑2, C‑4, QG‑5

## Context and problem statement

The schema has to reach a fresh Turso database and a fresh local `libsql-server` container the same
way, from a process that may start cold on any Fly Machine at any time (ADR‑0003) with no dedicated
deploy step configured in `deploy/fly.toml` (there is no `release_command`). The design's dependency
list (§4) is deliberately four modules — `shurcooL/githubv4`, `tursodatabase/libsql-client-go`,
`yuin/goldmark`, `gopkg.in/yaml.v3` — and a schema-migration tool would be a fifth kind of thing to
learn, run and keep working under C‑2's "Docker and make only" constraint. The question is whether
that fifth thing earns its place for a schema with, at the time of writing, one migration file.

## Considered options

* Numbered SQL files embedded with `go:embed`, applied at start-up inside a transaction, tracked in
  a `schema_migrations` table the code itself creates and reads.
* Atlas (declarative, schema-as-code migrations with drift detection).
* A migration framework such as `golang-migrate` or `goose`, driven by a CLI subcommand or a
  `Makefile` target.

## Decision outcome

Chosen: **embedded SQL migrations applied at start-up**, because it needs no tool beyond the Go
standard library and the driver already required by ADR‑0004, and because "safe to call on every
startup" is a real property this project needs given there is no separate release phase to run a
migration step in.

`internal/adapters/libsql/migrate.go`'s `Migrate` method is the whole mechanism:

```go
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, bootstrapSQL); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	applied, err := s.appliedVersions(ctx)
	...
	for _, name := range names {
		version, err := versionOf(name)
		...
		if applied[version] {
			continue
		}
		body, err := migrationFS.ReadFile(path.Join("migrations", name))
		...
		if err := s.apply(ctx, version, string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}
	return nil
}
```

Files live under `internal/adapters/libsql/migrations/` (`0001_initial.sql` through
`0005_github_only.sql` today),
embedded with `//go:embed migrations/*.sql`, applied in file-name order inside one transaction per
file together with its `schema_migrations` row — "a half-applied migration cannot be recorded as
done", per the code's own comment — and `recordVersion` uses `INSERT ... ON CONFLICT(version) DO
NOTHING` because "two machines starting at the same time may both apply an idempotent migration;
only the row must not be duplicated." There is no make target for migrating: the server applies
every migration on start-up, so an ordinary deploy is the migration. `cmd/migrate` calls the same
method without starting the binary, for the one case start-up does not cover — preparing a fresh
Turso database by hand before the first deploy — and it is run the way the SQL shell described in
`docs/concepts/data-storage.md` (removed in the stateless reset, see git history) was, through a
`docker run` of the Go image with `TURSO_URL` and `TURSO_AUTH_TOKEN` in its environment.

### Consequences

* Good: `Migrate` being "safe to call on every startup" (the method's own doc comment) means the
  binary can call it unconditionally wherever it opens the store, with no operator action and no
  deploy-time hook, which matters specifically because C‑3 gives this project no reliable moment —
  no persistent host, no guaranteed release phase — to run a separate migration step in. At the
  time of writing `cmd/zorgscope/main.go` does not yet call `config.Load`, `libsql.Open` or
  `Migrate` — that wiring is Task 12's work — so this benefit is a property of the mechanism, not
  yet an observed behaviour of `make backend` or a Fly start.
* Good: zero new dependencies; the migration mechanism is `database/sql`, `embed` and the driver
  ADR‑0004 already requires.
* Bad: no drift detection, no down-migrations, no dry-run diff against the live schema — everything
  a dedicated tool would add for free. For a schema this size (seven tables, one migration file) the
  cost of writing each `ALTER` by hand is currently small; it is a cost that grows with schema
  churn, and this decision accepts that trade rather than pricing it as zero.
* Neutral: migrations must not contain a semicolon inside a string literal, because
  `splitStatements` cuts the file on `;` after stripping `--` comments — a documented limitation of
  hand-rolling the splitter rather than delegating to a driver that understands multi-statement
  scripts natively.

## Pros and cons of the options

### Embedded SQL migrations, applied at start-up

* Good: see Decision outcome above.
* Bad: see Consequences above.

### Atlas

* Good: declarative schema-as-code with real drift detection, which would catch a hand-written
  migration and the live schema disagreeing before it caused a bug — a legitimate strength for a
  schema under active development by several agents in parallel.
* Bad: a CLI to install and run, which under C‑2 means another `docker run` line and image to keep
  current in the `Makefile`, and a second mental model (Atlas's own schema language and migration
  directory format) alongside the plain SQL every other part of the schema is described in. For a
  single-table-per-source schema with no history of drift incidents, the cost is paid up front for a
  benefit not yet needed.

### `golang-migrate` / `goose`

* Good: closer to the chosen option than Atlas — still plain SQL files, still versioned by filename
  — and a lighter dependency than a full schema-as-code tool.
* Bad: still a fifth external dependency the design's §4 deliberately did not budget for, and it
  moves "apply migrations" outside the binary into a CLI invocation that would need its own place in
  `make backend` and in the Fly start-up path (there being no `release_command` to hang it on).
  Embedding the same idea directly in `internal/adapters/libsql` costs less and keeps "the binary
  migrates itself on every start" true without a second tool asserting it.
