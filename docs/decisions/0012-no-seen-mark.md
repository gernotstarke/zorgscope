# 0012. No seen mark: the page shows what is open, not what is new

* Status: accepted
* Date: 2026-09-18
* Requirements: FR‑1.2 (retired), FR‑1.11, QS‑4.1

## Context and problem statement

`NEW` marked an item created after the visitor's seen mark, and the only thing that ever moved
that mark was the "Mark all seen" button ([ADR‑0010](0010-stateless-no-database.md), design §4).
On 2026-09-18 Gernot asked for the button to go: it was pressed to silence the badge rather than
to record anything, and it took a click on every visit to keep the dashboard honest. A mark
nothing moves is dead — every item stays `NEW` for ever, and the badge stops meaning anything.
So the question is not whether to remove the button but what becomes of the concept behind it.

## Considered options

* A: Remove the button and leave the machinery — the mark in the cookie, `NEW` in the domain, the
  counts on the header and the tiles — in place for a later way of moving it.
* B: Remove `NEW` altogether: the mark leaves the cookie, the badge and its counts leave the
  domain and the pages, and `POST /seen` leaves the router.
* C: Move the mark automatically on every visit, so `NEW` means "since you last looked" —
  last-visit semantics, weighed and rejected in
  [ADR‑0006](0006-first-seen-versus-last-visit.md) for reasons that have not changed.

## Decision outcome

Chosen: **B — remove `NEW` altogether**. Keeping the machinery (A) would leave a badge that lies
on every page in exchange for an option nobody has asked to exercise; C would reintroduce a
definition of "new" this project has already examined and turned down, and would do it silently,
without the user ever marking anything. The page is worth more as a straight answer to "what is
open?" than as a wrong answer to "what is new?". What the removal frees, the same design spends
on finding things instead: search and the Contributors page
(`docs/superpowers/specs/2026-09-18-search-and-contributors-design.md`, FR‑12.1 and FR‑12.2).

The session cookie's payload is now the expiry and nothing else
([concepts](../concepts/security-and-tokens.md)); lists sort by last update; and the top bar
carries the switch, the search box, Refresh and Log out where "Mark all seen" used to sit
(FR‑1.11).

### Consequences

* Good: a smaller cookie — one integer, signed, with nothing in it worth forging — and one route
  fewer to protect, which is one route fewer for QS‑4.1's route-table test to cover.
* Good: no badge that lies. Nothing on the page claims a freshness it cannot establish.
* Bad: G‑1's "recognises at a glance what is new" is gone, and so is the only answer this
  project had to "what arrived while I was away". Bringing it back is a design of its own, not a
  revert: it needs a definition of "new" that moves without a click, which is exactly what 0006
  examined.
* Neutral: every session signs in once more, because the cookie's shape changed and cookies of
  the old shape no longer verify.

## Pros and cons of the options

### A: Remove the button, keep the machinery

* Good: the smallest diff; the domain, the cookie and the templates are untouched, and a future
  way of moving the mark would find everything waiting for it.
* Bad: with nothing to move the mark, it stays at its sign-in zero, so every item is `NEW` for
  ever: a badge on every row, a count on every tile, and a tab title that never goes quiet. Dead
  code that renders is worse than dead code that does not.

### B: Remove `NEW` altogether

* Good: see Decision outcome and Consequences above.
* Bad: the dashboard loses its only per-visit signal, and the domain loses a rule that was
  correct, tested and cheap — it was the answer to a question that stopped being asked, not a
  broken one.

### C: Move the mark on every visit (last-visit semantics)

* Good: the badge would be self-maintaining, with no button and no click.
* Bad: rejected in [ADR‑0006](0006-first-seen-versus-last-visit.md) — a visit that the visitor
  did not read, a second tab, or a poll of the wait page all move the mark, so items go quiet
  without ever having been looked at. It also needs somewhere to write on every page view, which
  the stateless design ([ADR‑0010](0010-stateless-no-database.md)) does not offer.
