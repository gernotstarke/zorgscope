# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Gernot Starke, maintainer of the arc42 family of sites, and a handful of invited members of the
iSAQB, each with a personal view of public repositories they chose; "you" means the signed-in
person. Gernot opens zorgscope many times a day, for seconds at a time, from whatever device and
connection is at hand, to answer one question: *does anything need me right now?* Sign-in is gated
on push access to `gernotstarke/zorgscope` (or, for a member, to `gernotstarke/zorgscope-members`),
and the interface's "you" and "me" always mean the signed-in GitHub login.

## Product Purpose

zorgscope collects the open issues and pull requests of the arc42 sites' GitHub repositories on one
page, finds any of them and the people behind them in seconds, and costs almost nothing to run
(docs/requirements/01-goals.md, G‑1). Success is a glance: open, see what needs attention, close.

## Positioning

A single-owner triage page for one specific family of documentation sites — arc42, req42 and their
relatives — and, for invited members, the same over their own public repositories; not a general GitHub dashboard. It knows which repository belongs to which site, draws
each in that site's registry colour, and marks security and dependency items from evidence
(FR‑1.13).

## Operating Context

- Glanced at between other work; no session is long.
- The list is fetched from GitHub on demand and cached in memory for minutes; a fetch in flight
  shows the wait page or the last list (FR‑1.9, ADR‑0013).
- Stateless by decision (ADR‑0010): no database, nothing remembered server-side between restarts
  except what the visitor's own cookies carry. "New since last visit" was retired with that reset
  (FR‑1.2) and stays retired.

## Capabilities and Constraints

- Views: List (grouped by repository, filterable), Sites (one tile per site, plus the Security
  tile), Radar (every item as a blip on a radar scope), Contributors, Search (Cmd‑K).
- A member sees List, Sites, Radar, Contributors and Search over up to 12 chosen public
  repositories; no Security tile, no alerts, no group switch (FR‑1.17).
- Every open item of every watched repository is shown; a failing repository never hides the
  others (QG‑1). Anything that emphasises some items must not hide the rest.
- Go server-rendered HTML with htmx; hand-written CSS, no build step (ADR‑0002). Strict CSP with no
  `'unsafe-inline'`; fully usable without JavaScript.
- Pages stay light (QG‑2, QS‑2.3 page budget); hosting stays in the free tier (QG‑3).
- Item data on hand: kind, repository, number, title, summary, author, labels, advisories, created
  and updated times. Review requests are not fetched yet.
- Every software change bumps the semantic version in `internal/version/version.go`.

## Brand Commitments

- The arc42 rainbow band on every page, and each site's colour from the arc42 brand registry.
- The zorgscope mark (docs/logo) and its orbiting wait-page animation.
- The hazard tape as the security signal.
- "Made with ♥ in Cologne" in the footer.

## Evidence on Hand

Real data is the live GitHub state of the configured repositories (config/zorgscope.yaml). Tests use
fixtures (internal/web `representativeItems`, cmd/fakesources). There are no users besides Gernot,
no metrics and no testimonials; nothing of the kind may be invented.

## Product Principles

1. Answer "does anything need me?" first; everything else is second.
2. Complete, never selective: emphasis is allowed, omission is not.
3. Honest state: say when the list is stale, fetching or failed, and never claim more than is known.
4. Frugal and fast: no database, no background jobs, a light page.
5. Works without script, under a strict CSP, in both appearances.

## Accessibility & Inclusion

Text contrast of at least 4.5:1 in both appearances is enforced by tests (FR‑1.8 AC5, FR‑1.5 AC2);
colour is never the only signal.
