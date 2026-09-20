# 0013. Stale while revalidating: the page shows what it has, and says it is refreshing

* Status: accepted
* Date: 2026-09-20
* Requirements: FR‑1.9, FR‑8.3, QS‑2.3, QS‑2.6

## Context and problem statement

On 2026-09-20 Gernot asked whether browser storage could shorten the dashboard's startup — an auth
cookie held client-side, valid for about thirty days, so that signing in to GitHub is not needed
every time.

The auth half was already built. `sessionTTL` has been 30 days since the product had sessions, and
the browser holds no credential at all: the cookie carries an expiry and an HMAC over it, nothing
more. The re-sign-in that prompted the question was the one-off recorded in
[ADR‑0012](0012-no-seen-mark.md), when the payload's shape changed.

What measurement did find is that the startup cost is the GitHub fetch, and that the wait page
(FR‑1.9) is shown in two quite different situations. One is a Fly Machine woken from zero with an
empty snapshot, which genuinely has nothing to show. The other — far more frequent — is a warm
Machine whose snapshot has merely crossed `github.cache_ttl`, which is holding a perfectly good
list in memory and shows the wait page anyway, because AC1 said "and not a list". So the question
is not whether to store something in the browser but what a page should do when it has data that
is merely old.

## Considered options

* A: Keep the wait page as it is and add browser storage only, so the visitor's own last list
  appears behind it.
* B: The server serves a stale list, marked as refreshing, and browser storage covers only the
  cold Machine that has nothing to serve.
* C: Persist the snapshot server-side — a Fly Volume with SQLite — so a cold Machine boots with
  data and no browser storage is needed at all.

## Decision outcome

Chosen: **B — both layers, each where its case lives**. A would leave the five-minute wait in place
for every visitor, and would only ever help a browser that had been there before — the common
complaint would be untouched. C is the option [ADR‑0010](0010-stateless-no-database.md) examined
and rejected under its own name: persistence is what broke the first production deploy, and every
piece that failed then existed because persistence existed at all. Nothing about that has changed.

So `answeredWaiting` now chooses the wait page by *having nothing to show* rather than by *a fetch
being in flight*. A snapshot with items renders its list with a status line and a poll that asks
`GET /items` every 500 ms; the poll sits inside the fragment it replaces, so the first fragment
rendered after the fetch lands carries none and takes the running one away with it. A snapshot
without items still shows the wait page, and `warm-start.js` restores the visitor's last list into
it from `localStorage`, guarded by `data-cache-epoch` — a one-way tag derived from the OAuth client
secret under its own context string, so rotating that secret makes every stored list in every
browser unreadable at once (FR‑8.3 AC4).

The browser copy is a placeholder, never a cache of record. It is replaced whole by the page that
arrives, and nothing is merged: the server already falls back per repository when a fetch partly
fails, and it is the only party that knows which repositories failed. Merging in the browser would
need `SortItems`, the grouping and the filter semantics all over again in JavaScript, which is a
second renderer and exactly what [ADR‑0002](0002-server-rendered-htmx.md) exists to prevent.

### Consequences

* Good: the common wait — a warm Machine past its TTL — disappears for every visitor, with
  JavaScript or without it, and nothing is stored anywhere to achieve it.
* Good: ADR‑0002, ‑0003, ‑0010 and ‑0011 all stand. No database, no volume, no ticker, no second
  renderer. The fetch is still started by a request and only by a request.
* Bad: pressing Refresh now shows the old list being replaced rather than a wait page, so for a
  moment the visitor sees the list they just asked to replace. Carving Refresh back out would mean
  distinguishing "invalidated" from "aged out" in `Snapshot`, a field nothing else wants.
* Bad: a list in `localStorage` is readable by anything with script access to the origin. The
  epoch bounds it in time but does not prevent it; `script-src 'self'` with no `'unsafe-inline'`
  is what keeps that set empty.
* Neutral: on iOS the placeholder is gone after seven days of not opening the browser, because
  Safari evicts script-writable storage on that schedule. The visitor then sees the wait page, as
  they did before it existed.

## Pros and cons of the options

### A: Browser storage only

* Good: the smallest diff, and FR‑1.9's policy is untouched.
* Bad: the frequent wait — a warm Machine past its TTL — stays for everyone, which is the larger
  half of the problem.
* Bad: helps only a browser that has been here before, and not at all with JavaScript off.

### B: Both layers

* Good: each case is answered where it actually lives, and the bigger one is answered on the
  server, where it needs no storage and no script.
* Good: shrinks browser storage to the one case the server cannot cover, which is also the case
  where the stored copy is least likely to be misleading — there is nothing else to show.
* Bad: it is two mechanisms rather than one, and FR‑1.9 has to be reworded.

### C: Persist the snapshot server-side

* Good: one mechanism, helping every visitor and every device including a phone that has never
  been here.
* Bad: reopens ADR‑0010. A volume is a thing to provision, back up, migrate and lose, in a product
  whose whole state is one list that GitHub will re-serve on request.
* Bad: does nothing that B does not already do for the frequent case, at considerably more cost.
