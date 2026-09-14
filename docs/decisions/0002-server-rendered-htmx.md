# 0002. Server-rendered `html/template` plus htmx, no JavaScript build

* Status: accepted
* Date: 2026-08-17
* Requirements: C‑2, C‑6, FR‑1.6, QS‑2.3, QS‑5.3

## Context and problem statement

zorgscope has one page and one user (S‑1). C‑6 already names the client technology — server-rendered
`html/template` plus htmx, no build step — as a constraint, not a proposal; this record exists to
say why that constraint was accepted rather than a client-neutral JSON API with a separately built
frontend, which is what the previous design had and the reset explicitly removed.

## Considered options

* Server-rendered `html/template` with vendored htmx and hand-written CSS.
* A JSON API (`internal/web` exposing the dashboard as JSON) consumed by a single-page application
  built with a framework such as React or Vue.
* A native or Wails desktop client talking to the same JSON API.

## Decision outcome

Chosen: **server-rendered `html/template` plus htmx**, because it needs nothing beyond what C‑2
already provides on the host (Docker and make; no Node, no local Go toolchain) and nothing beyond
what the design already budgets for payload size.

The decision is visible in `internal/web/templates`, in `internal/web/static/htmx.min.js` — the
vendored copy checked into the repository — and in the absence of any `package.json` anywhere in the
repository: there is no JavaScript dependency to install, so there is nothing to lock, audit or
build.

The list of items renders as its own fragment, addressable at `GET /items`, so htmx can replace the
list without a full page reload — this is FR‑1.6 — and so a page left open updates itself while
keeping the filter it was drawn with. This works because the server already assembles a `Dashboard`
struct carrying the repository groups, their counts and the filter that produced them
(`internal/domain/dashboard.go`); htmx polling a fragment is a routing decision layered on data that
already exists, not a reason to add a client-side store. The filter bar is the same argument in the
other direction: it is a plain `GET` form that reloads the page when JavaScript is off, and htmx
attributes turn the same submission into a fragment swap when it is on.

### Consequences

* Good: no Node in CI or on the host — `make check` needs only Docker and make (FR‑9.1 AC3), and
  QS‑5.3's three-minute CI budget carries no `npm install` or bundler step.
* Good: the response size is bounded by what a template renders, which QS‑2.3 turns into a number
  (≤ 150 kB HTML, ≤ 50 kB static assets including htmx) that a test on a rendered fixture can check
  directly, rather than a bundle-size budget that depends on which packages a dependency update
  happened to pull in.
* Bad: every interaction that would be a client-side state update in an SPA is a request the server
  answers instead. For a page opened a few times a day this costs nothing measurable, but it is a
  real constraint the design accepts rather than a free win: htmx swaps HTML fragments, so any UI
  state (which filter is in force, say) that would live in browser memory in an SPA has nowhere to
  live here except in the URL, back on the server, or not at all.
* Neutral: the dashboard's render input is one Go struct that marshals cleanly to JSON even though
  nothing exposes it as JSON in v1 (design §4); a read-only API or a different client stays a small
  addition rather than a rewrite, without that flexibility being exercised in v1.

## Pros and cons of the options

### Server-rendered `html/template` plus htmx

* Good: see Decision outcome above.
* Bad: see Consequences above.

### JSON API plus a single-page application

* Good: a genuinely closer alternative than it might look — the design already keeps the dashboard
  view as one JSON-clean struct for exactly this reason, and QS‑2.3's payload budget would still
  apply to the JSON alone. For a richer future client this is not a wrong choice, only an early one.
* Bad: needs a JavaScript build toolchain, which reopens the Node dependency C‑2 exists to close
  off, and a `package.json` the host cannot build without leaving Docker. It is exactly the
  "client-neutral API for a native client that was never built" the design's §1 lists among what
  the previous iteration over-built for a one-user tool — the API existed, the second client did
  not.

### Native or Wails desktop client

* Bad: explicitly cut in the reset — the
  [functional requirements](../requirements/04-functional-requirements.md)' "Explicitly out of
  scope" section names "a native or Wails client". It adds a second, platform-specific packaging
  and release pipeline for a tool whose
  entire value proposition is "open a URL, glance, close it" — the opposite of something installed.
* Good: none that outweigh the above for a single user who already has a browser open most of the
  day.
