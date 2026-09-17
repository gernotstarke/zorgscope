# 0011. The fetch runs in a goroutine a request started, never a ticker; pages never wait for it

* Status: accepted
* Date: 2026-09-16
* Requirements: FR‑1.9, QS‑2.6, QS‑2.7, C‑3

## Context and problem statement

Measured on 2026-09-16, a cold visit took about eleven seconds, nine of them eighteen GraphQL
requests made one after the other while the page handler waited for all of them. The page was
blank for as long as GitHub took. C‑3 forbids a process that works between requests, so the
usual answer — a background loop that keeps the list warm — is not available. The question is
where the fetch runs and what the visitor sees while it does.

## Considered options

* Block the page until the fetch has returned, as before, but fetch the repositories side by side.
* Run the fetch in a goroutine the request starts, detached from that request and bounded by the
  existing budget, and show a wait page that polls until the page is ready.
* Serve the stale list at once and refetch behind it (stale-while-revalidate).
* A background ticker that refetches on a schedule.

## Decision outcome

Chosen: **a request-started, budget-bounded goroutine with a wait page**, together with the
side-by-side fetch. The page answers in milliseconds whatever GitHub does, the machine still does
nothing between requests, and the visitor is told what is happening rather than shown a blank tab.

`internal/snapshot.Cache.Get` never waits: when a fetch is due and none is in flight it starts one
under `context.WithoutCancel` and `fetchBudget`, marks it in flight, and returns what it has with
`Fetching` set. The two page handlers answer a request during a fetch with the wait page, its poll
with `204`, and every other htmx request from the current snapshot (design
`docs/superpowers/specs/2026-09-16-fast-first-view-design.md`, §4 and §5).

### Consequences

* Good: a page view answers within 200 ms with the source blocked (QS‑2.6); a cold visit is about
  the machine's own wake plus one round trip to GitHub.
* Good: nothing runs unless a request asked for it, and the goroutine lives at most sixty seconds;
  Fly's proxy keeps the machine awake for the polling requests that wait on it anyway, so C‑3's
  scale-to-zero is untouched.
* Bad: a stale list is withheld for the length of a fetch, by decision — the wait page appears on
  every fetch, an expired TTL and the Refresh button included, rather than only on a cold start.
* Neutral: the goroutine can outlive the request that started it by at most the fetch budget; a
  visitor closing the tab does not cancel a fetch everyone else is waiting for.

## Pros and cons of the options

### Block the page, fetch side by side

* Good: the smallest change; the side-by-side fetch alone takes nine seconds down to under one.
* Bad: the page is still blank for as long as the slowest request takes, and a slow GitHub still
  turns into a slow page.

### A request-started goroutine with a wait page

* Good: see Decision outcome above.
* Bad: see Consequences above.

### Stale-while-revalidate

* Good: a returning visitor sees a list at once, however old.
* Bad: what is on screen is silently replaced a moment later; the header would have to say the
  list is being replaced; and it does nothing at all for the cold start, which has no stale list.
  Rejected in conversation on 2026-09-16.

### A background ticker

* Good: the list is always warm.
* Bad: contradicts C‑3 directly and would keep the machine running; rejected for the same reason
  as in [ADR‑0003](0003-fly-scale-to-zero-external-cron.md).
