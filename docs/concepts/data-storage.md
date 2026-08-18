# Data storage

The schema, the first-seen invariant, per-source transactions, the refresh lease, and how to look
at the database yourself. See [ADR‑0004](../decisions/0004-turso-libsql.md) for why Turso and
libSQL, and [ADR‑0006](../decisions/0006-first-seen-versus-last-visit.md) for why "new" is defined
the way it is — this page describes the mechanism those two records decided on.

## Schema

`internal/adapters/libsql/migrations/0001_initial.sql` is the schema as it exists today, applied by
`(*Store).Migrate` (`internal/adapters/libsql/migrate.go`) on every start-up:

```sql
CREATE TABLE IF NOT EXISTS items (
  source          TEXT,
  external_id     TEXT,
  kind            TEXT,
  repo            TEXT,
  number          INTEGER,
  title           TEXT,
  url             TEXT,
  author          TEXT,
  state           TEXT,
  created_at      TEXT,
  updated_at      TEXT,
  due_at          TEXT,
  priority        INTEGER,
  first_seen_at   TEXT NOT NULL,
  last_fetched_at TEXT NOT NULL,
  payload         TEXT,
  PRIMARY KEY (source, external_id)
);

CREATE TABLE IF NOT EXISTS builds (
  repo TEXT PRIMARY KEY, workflow TEXT, conclusion TEXT, status TEXT,
  run_url TEXT, finished_at TEXT, fetched_at TEXT
);

CREATE TABLE IF NOT EXISTS metrics (
  site TEXT, window_days INTEGER, visitors INTEGER, pageviews INTEGER,
  prev_visitors INTEGER, prev_pageviews INTEGER, fetched_at TEXT,
  PRIMARY KEY (site, window_days)
);

CREATE TABLE IF NOT EXISTS refresh_run (
  id INTEGER PRIMARY KEY AUTOINCREMENT, started_at TEXT, finished_at TEXT,
  "trigger" TEXT, ok INTEGER, detail TEXT
);

CREATE TABLE IF NOT EXISTS source_state (
  source TEXT PRIMARY KEY, last_success_at TEXT, last_error TEXT,
  last_error_at TEXT, item_count INTEGER
);

CREATE TABLE IF NOT EXISTS app_state (
  key TEXT PRIMARY KEY, value TEXT
);

CREATE TABLE IF NOT EXISTS notified (
  key TEXT PRIMARY KEY, sent_at TEXT
);
```

`app_state` is a generic key-value table with two known keys: `last_visit_at` (the timestamp "mark
all seen" writes) and `refresh_lease` (below). `notified` exists for v2's Slack notifications
(FR‑6.1) and is empty until Task 17 lands. Every timestamp is an RFC 3339 UTC string, with the empty
string standing for the zero `time.Time` — `sqlTime` and `parseTime` in `store.go` are the two
functions responsible for that round trip.

## The first-seen invariant

The whole "what's new" mechanism (ADR‑0006) rests on one asymmetry in `upsertItemSQL`,
`internal/adapters/libsql/store.go`:

```sql
INSERT INTO items (
  source, external_id, kind, repo, number, title, url, author, state,
  created_at, updated_at, due_at, priority, first_seen_at, last_fetched_at, payload)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL)
ON CONFLICT(source, external_id) DO UPDATE SET
  kind = excluded.kind, repo = excluded.repo, number = excluded.number,
  title = excluded.title, url = excluded.url, author = excluded.author,
  state = excluded.state, created_at = excluded.created_at,
  updated_at = excluded.updated_at, due_at = excluded.due_at,
  priority = excluded.priority, last_fetched_at = excluded.last_fetched_at
```

`first_seen_at` is in the `INSERT` column list, so every row gets one the first time it is written.
It is deliberately absent from the `ON CONFLICT DO UPDATE SET` clause — a later refresh of the same
`(source, external_id)` updates every other column, including `last_fetched_at`, which *is* in both
the insert and the update. `first_seen_at` is the one column a second, third or hundredth refresh
can never move. That is the entire mechanism behind the `NEW` badge (FR‑5.3 AC2, QS‑1.2): an item is
new exactly while `first_seen_at > last_visit_at`, and `first_seen_at` only ever gets one value, set
once, at the moment the item was first stored.

`TestReplaceItemsPreservesFirstSeen` in `internal/adapters/libsql/store_test.go` is the test that
guards this: it stores an item at one timestamp, replaces it with a different title an hour later,
and asserts the stored `first_seen_at` still equals the *first* call's time while the title reflects
the second.

`upsertArgs` in the same file ignores whatever `FirstSeenAt` value the `domain.Item` it is given
happens to carry — "a fetcher does not know it, and the store is the only place that decides it."
Correctness depends on there being exactly one writer of that column; the `depguard` rule from
[ADR‑0001](../decisions/0001-go-modular-monolith.md) makes it structurally true that only
`internal/adapters/libsql` can reach the database at all.

## Per-source transactions

`ReplaceItems(ctx, source, items, now)` makes the stored rows of one source exactly match `items`:
it upserts every item, then deletes the rows of that source no longer present, all inside one
transaction (FR‑5.5 AC1). A failure partway through rolls back the whole transaction, so a source
that errors leaves its previously stored rows untouched rather than half-replaced.

The deletion side is worth reading precisely, because it is not `DELETE ... WHERE external_id NOT IN
(...)` the way a first draft might write it. `deleteAbsent` reads the set of ids already stored for
that source inside the same transaction, computes which of those are no longer present in the
incoming `items`, and deletes exactly that complement, in chunks bounded by `maxParams` (400)
placeholders per statement:

```go
gone := make([]string, 0, len(known))
for id := range known {
	if !present[id] {
		gone = append(gone, id)
	}
}
for _, chunk := range chunks(gone, maxParams-1) {
	// DELETE FROM items WHERE source = ? AND external_id IN (?,?,…)
}
```

The comment in `store.go` explains why: a `NOT IN` list cannot safely be chunked, because each chunk
would delete the rows every *other* chunk was supposed to keep — chunking only works on the set of
ids to remove, not the set to keep. An item that disappears from a source and reappears later goes
through a plain delete-then-reinsert, which is exactly why FR‑5.3 AC3 ("an item that disappears
upstream and returns later is treated as new again") needs no special-case code:
`TestReplaceItemsDeletesAbsentAndReSeesReturning` covers it directly.

Because each source commits independently, a machine stopped mid-refresh (QS‑1.6, ADR‑0003's whole
reason for existing) loses only the sources not yet reached in that run; the ones already committed
keep their data, and `refresh_run.detail` records which sources the run did and did not reach
(FR‑5.5 AC2).

## The refresh lease

Only one refresh may run at a time (FR‑5.2 AC2, QS‑1.7), and the guarantee has to survive a machine
restart — there is no long-lived process to hold a mutex in. The lease lives in `app_state` under
key `refresh_lease`, as a single string value `holder|expiry`. RFC 3339 UTC timestamps compare
lexicographically, which is what lets the whole acquire-or-refuse decision be one SQL statement
rather than a read followed by a write (which would let two concurrent callers both pass the read):

```sql
INSERT INTO app_state (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value
WHERE substr(app_state.value, instr(app_state.value, '|') + 1) <= ?
   OR substr(app_state.value, 1, instr(app_state.value, '|') - 1) = ?
```

The write is admitted only when the stored lease has already expired, or the caller already holds
it (an idempotent re-acquire, not a new lock). One affected row means the caller now holds the
lease; zero means someone else does. `ReleaseRefreshLease` deletes the row only if the caller named
in it is still the holder, so a lease that expired and was taken over by someone else is never
accidentally freed by the machine that used to hold it.

## Inspecting the database with `make db-shell`

```sh
make db-shell
```

This starts the local `libsql-server` container if it is not already running (`db-up`), then opens
`ghcr.io/tursodatabase/libsql-shell` against it, sharing the database container's network namespace
so no port needs publishing and no client needs installing on the host (ADR‑0008) — the shell speaks
plain SQLite SQL. From the prompt:

```sql
.tables
SELECT * FROM app_state;
SELECT source, count(*) FROM items GROUP BY source;
SELECT id, started_at, "trigger", ok, detail FROM refresh_run ORDER BY id DESC LIMIT 5;
```

This connects to the local development database only, the same one `make backend` runs against —
not production Turso, which has its own credentials and is not reachable through this target.
`make db-reset` drops the local database and its volume entirely, for a clean slate.
