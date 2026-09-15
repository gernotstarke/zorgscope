# 0006. First-seen versus last-visit as the definition of "new"

* Status: accepted
* Date: 2026-08-17
* Requirements: FR‑1.2, FR‑1.3, FR‑5.3, QS‑1.2, QS‑1.3, QS‑3.2

## Context and problem statement

QG‑1 states the correctness goal directly: "an item is marked new exactly while the user has not
seen it. A dashboard that cries wolf, or silently hides something, is worse than no dashboard."
The previous design answered this with "daily snapshots" and "per-item dismissals" (design §1,
among what was cut). Both are gone from the requirements' "Explicitly out of scope" list. The
question this record answers is what replaced them, and why the replacement is a smaller mechanism
than either.

## Considered options

* `first_seen_at` per item, `last_visit_at` for the user: `NEW` is `first_seen_at > last_visit_at`,
  computed at read time.
* Daily snapshots: store the full item set once a day, diff consecutive snapshots to find what
  changed.
* Per-item dismissal: a `dismissed` table (or column) the user writes to per item, independent of
  any global "last visit" concept.

## Decision outcome

Chosen: **first-seen versus last-visit**, because it is a single stored fact per item
(`first_seen_at`) and a single stored fact per user (`last_visit_at`), and both a `_test.go` file
and a request handler can express the whole rule as one comparison.

The mechanism is one asymmetry in one SQL statement,
`upsertItemSQL` in `internal/adapters/libsql/store.go`:

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

`first_seen_at` is in the `INSERT` column list — every row gets one — and is deliberately absent
from the `ON CONFLICT DO UPDATE SET` clause, while `last_fetched_at`, written from the same `now`
argument, is present in both. A row that already exists keeps the `first_seen_at` it was born with,
no matter how many later refreshes touch every other column; a row that does not yet exist gets
`first_seen_at = now`. The file's own package comment names this "the single asymmetry ... that
makes the NEW badge correct (FR-5.3 AC2, QS-1.2)", and `store.go` repeats the warning at the
statement itself: "Adding `first_seen_at` to the SET clause would silently turn the NEW badge into
'changed since the last refresh'."

`TestReplaceItemsPreservesFirstSeen` (`internal/adapters/libsql/store_test.go`) is the test that
would fail first if that line were ever added back: it replaces the same item twice, an hour apart,
with a changed title, and asserts the stored `FirstSeenAt` still equals the *first* call's
timestamp while the title reflects the second. `internal/domain/item.go`'s `IsNew` is the read side,
and it is exactly the comparison the option promises — "only a first sighting strictly after the
visit counts":

```go
func (i Item) IsNew(lastVisit time.Time) bool {
	return !i.FirstSeenAt.IsZero() && i.FirstSeenAt.After(lastVisit)
}
```

FR‑5.3 AC3 — "an item that disappears upstream and returns later is treated as new again" — falls
out of the same store method rather than needing separate logic: `ReplaceItems` deletes rows whose
source no longer reports them (`deleteAbsent`, reading the ids already stored inside the
transaction and removing the complement of what is present, in chunks bounded by `maxParams` — a
`NOT IN` list cannot safely be chunked, because each chunk would delete rows the other chunks are
supposed to keep). A row that comes back later is, from the database's point of view, a fresh
insert with a fresh `first_seen_at`, which is `TestReplaceItemsDeletesAbsentAndReSeesReturning`'s
assertion.

### Consequences

* Good: "mark all seen" (FR‑1.3) is one write — `SetLastVisit(now)` — with no per-item bookkeeping
  to update alongside it; QS‑1.3 ("no item is new; an item first seen after the click is new
  again") is a direct consequence of the comparison, not a case that needs separate handling.
* Good: storage is one column added to the row a source already produces; there is no second table
  scaling with history, which is what keeps QS‑3.2's ≤ 50 MB budget realistic under refreshes every
  fifteen minutes.
* Bad: "new" is necessarily a per-user, whole-dashboard concept — there is exactly one
  `last_visit_at`. An item cannot be individually dismissed while leaving others new, and there is
  no way to say "I've seen this one but not that one" without changing the model. For a single-user
  tool (S‑1) this is a real property, not an oversight, but it is a real limitation that a
  multi-user or per-item-tracking future would have to design around, not one this decision
  eliminates for free.
* Neutral: `first_seen_at` is decided by the store, not by the fetcher — `upsertArgs` ignores
  whatever `FirstSeenAt` a `domain.Item` arrives with, because "a fetcher does not know it, and the
  store is the only place that decides it" (store.go). Correctness depends on there being exactly
  one writer of this column, which the depguard rule from ADR‑0001 makes structurally true rather
  than merely documented.

## Pros and cons of the options

### First-seen versus last-visit

* Good: see Decision outcome above.
* Bad: see Consequences above.

### Daily snapshots with diffing

* Bad: this is what the previous design did, and it fails QS‑1.1 directly — "visible at the latest
  at *t + i* + 60 s" where *i* is the refresh interval, which for the chosen 15-minute interval is
  well under a day. A daily diff can only ever be as fresh as a day, independent of how often the
  refresh itself runs.
* Bad: storing a full snapshot per day, rather than one timestamp per item, is exactly the kind of
  history-scaling storage QS‑3.2's ≤ 50 MB budget is written against; the chosen option's one
  column costs nothing per refresh that does not change the item set, where a snapshot regime pays
  for every day regardless.

### Per-item dismissal

* Bad: cut explicitly in the reset (requirements' "Explicitly out of scope": "per-item
  dismissals"). It also complicates the reappearance rule this decision gets for free: a dismissed
  item that disappears and returns needs its dismissal record reconciled against the delete-and-reinsert
  `ReplaceItems` already performs, or it silently un-dismisses (arguably correct per FR‑5.3 AC3) or
  silently stays dismissed (arguably wrong, since it is a new sighting) depending on which table is
  consulted first. The single-comparison design has no such ambiguity to resolve.
* Good: finer-grained control — a user could keep one long-running issue marked seen while new ones
  still show as new — is a real usability property the chosen option does not offer. It was not
  asked for (S‑1 is the only stakeholder) and its cost, above, was judged not worth carrying for a
  feature nobody requested.
