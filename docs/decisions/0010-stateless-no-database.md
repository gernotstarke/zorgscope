# 0010. Stateless: no database, no refresh pipeline

* Status: accepted
* Date: 2026-09-15
* Requirements: FR‑1.1, FR‑1.2, FR‑1.3, FR‑1.4, C‑3

## Context and problem statement

The first production deploy failed on every stateful piece at once: Turso, the embedded
migrations, the refresh pipeline, an external cron job with its own secret, and a CI job that had
to boot a libSQL server just to run the tests. All of it existed to serve one idea from August —
`NEW` is computed from a `first_seen_at` row per item, so the server has to remember every item it
ever saw. The page it serves needs none of that: it is the open issues and pull requests of eight
repositories, refetched from GitHub on demand. The question is what the smallest process is that
can still show the same page and tell the visitor what is new since they last looked.

## Considered options

* Keep the database, fix the deploy (Turso reachability, migrations, the cron secret) one problem
  at a time.
* Go stateless: no database, no refresh pipeline. `NEW` is "created after my previous *mark seen*",
  carried in the signed session cookie; the list is held in memory for `github.cache_ttl` and
  refetched when it is stale; refresh is a button that invalidates that cache.
* Move persistence to a Fly Volume with local SQLite, dropping only Turso.

## Decision outcome

Chosen: **stateless — no database**, because every piece that failed on the first deploy was a
consequence of persistence existing at all, and the page does not need persistence: everything it
shows is a request away from GitHub, and "what's new" only ever needs one number per visitor, which
fits inside the cookie that already identifies their session.

`internal/snapshot.Cache` holds the last fetched item list in memory and refetches it once it is
older than `github.cache_ttl` (design §5); a fetch runs under the cache's mutex, so concurrent
requests wait for the one fetch rather than start their own. The session cookie's signed payload
becomes two integers, `expiry` and `seen` (design §4): `seen` is the moment of the visitor's last
*mark seen*, `0` on a fresh sign-in, so nothing is `NEW` until the first mark. `POST /seen` re-mints
the cookie with the fetched-at time of the list the visitor was actually shown; `POST /refresh`
invalidates the cache and lets the next `GET /` fetch. Because the mark lives inside the
HMAC-signed cookie, nobody can tamper with it without invalidating the signature, and because it
lives in the cookie rather than a table, the server keeps no state of its own to lose, back up or
migrate.

### Consequences

* Good: three secrets, one deploy command (`make deploy`), one CI job — no Turso account, no
  migration files, no cron-job.org secret, no libSQL server in CI or in Compose.
* Good: nothing to run to get from empty repository to running system beyond `flyctl secrets set`
  and `fly deploy`; there is no database to provision before the first deploy can succeed.
* Bad: no history. An item that disappears and returns is indistinguishable from one that was
  always there; there is no build status, no notifications, and no per-item dismissal — a `NEW`
  mark is global to the visitor's session, not a fact about the item.
* Bad: the first view after the Fly Machine scales to zero pays one fetch (design §5, ~16 GraphQL
  calls for eight repositories) rather than reading a warm cache; this is the price a refresh button
  charges in place of a cron-warmed database, and it is accepted as such.
* Bad: signing out, or the session cookie expiring, forgets the seen mark — there is nowhere else
  for it to live. For a single-user tool this is a real property, not an oversight (see
  [security and token handling](../concepts/security-and-tokens.md)).
* Neutral: if state is ever wanted again — history, notifications, per-item dismissal — a Fly Volume
  with SQLite is the cheapest way back in, not Turso and not a cron trigger; nothing about this
  decision forecloses that, it only stops paying for it until it is asked for again.

This record supersedes [0004](0004-turso-libsql.md), [0005](0005-embedded-sql-migrations.md) and
[0006](0006-first-seen-versus-last-visit.md) outright, and the cron-triggered-refresh half of
[0003](0003-fly-scale-to-zero-external-cron.md) — scale to zero itself stays, decided there and
unchanged by this record.

## Pros and cons of the options

### Keep the database, fix the deploy piecemeal

* Good: no rewrite of the requirements, the concepts or the sign-in flow's storage assumptions;
  the schema and its tests (ADR‑0004, ADR‑0006) already existed and worked in development.
* Bad: every piece that failed did so because it depended on something outside this process being
  reachable and correctly configured at the moment of a cold start — Turso's network, a cron
  service's schedule, a migration having already run. Fixing each individually leaves the same
  shape that produced the first failure in place to produce the next one; it treats the symptom
  once per piece rather than the reason all the pieces exist.

### Stateless: no database, no refresh pipeline

* Good: see Decision outcome above.
* Bad: see Consequences above.

### Fly Volume with local SQLite, dropping only Turso

* Good: keeps history, first-seen semantics and the refresh pipeline's shape, while removing the
  one component (Turso) that is a genuinely external dependency; a volume is Fly's own
  infrastructure rather than a third party's.
* Bad: a volume ties a Machine instance to one host, which sits awkwardly against "any instance can
  start cold" scale-to-zero relies on ([ADR‑0004](0004-turso-libsql.md) rejected this for the same
  reason when the previous design chose Turso over it); it also keeps the migration mechanism, the
  refresh pipeline and the cron trigger in place, none of which the page actually needs once `NEW`
  no longer requires a stored history. It solves the reachability problem without asking whether the
  state behind it was necessary in the first place.
