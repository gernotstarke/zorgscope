# 0016. Site groups: one snapshot, a radar per group

* Status: accepted (implemented in 2.2.0)
* Date: 2026-09-29
* Requirements: FR‑1.8, FR‑1.15, FR‑1.16, QS‑3.5

## Context and problem statement

Gernot asked on 2026‑09‑29:

> I need to add isaqb repos to the radar, two or three more. A button could dynamically add the
> iSAQB repos or switch to the isaqb view and add a single slice condensing all arc42 stuff

The iSAQB repositories (`isaqb-org/curriculum-foundation`, `isaqb-org/glossary`, later req4arc,
improve and adoc) are a second family next to the arc42 sites. They belong on the List and the Sites
tiles like any other watched repository, but ten arc42 sectors plus five iSAQB sectors would make
the radar unreadable, and QS‑3.5 capped the configuration at exactly the ten repositories already
watched (20 GraphQL requests per fetch). The question: how does zorgscope watch a second family of
repositories without crowding the radar or breaking its request budget?

## Considered options

* **A. One snapshot; groups only on the radar.** Every repository is fetched as today and appears on
  the List, the tiles, the needs‑you band and the counts. A site names its `group`; the radar shows
  one group's sites as sectors and condenses every other group into one sector, switched by a link
  per group. QS‑3.5 is raised to 15 repositories.
* **B. A second snapshot, fetched only for the iSAQB radar.** Keeps the arc42 budget where it was.
* **C. Batch several repositories into one aliased GraphQL query.** A handful of requests per fetch
  whatever the number of repositories.

## Decision outcome

Chosen: **A**, in conversation on 2026‑09‑29 (spec `docs/superpowers/specs/2026-09-29-isaqb-group-design.md`
§2, D1–D5): the radar *switches* between groups rather than adding sectors; the switch exists on the
Radar only; the iSAQB repositories are on the List and the tiles too; and the budget is raised.
B was rejected because the List and the tiles need the iSAQB items anyway, so they are fetched on
every page view whatever the radar shows. C is deferred: it would make the budget moot, but it
rewrites the fetcher and its per‑repository paging for a saving nobody needs yet.

The iSAQB repositories want issues and pull requests only (D2). Whether a repository is asked for
Dependabot alerts is therefore an explicit per‑site key, `alerts: false`, rather than something
derived from the group: "only arc42 gets alerts" would be a rule nobody could see in the file.

### Consequences

* Good: the radar stays readable — the arc42 view has 10 + 1 sectors, the iSAQB view 2 + 1 — and
  nothing the list shows is missing from either view (QG‑1); a condensed blip keeps its own site's
  colour.
* Good: adding req4arc, improve or adoc is one `repos` line and one `sites` entry each.
* Bad: QS‑3.5 rises from 20 to 30 requests per fetch. Its old reason — "so that a 15‑minute interval
  stays under 2 % of GitHub's hourly limit" — dates from the external cron of ADR‑0003; since
  ADR‑0011 a fetch happens only on a stale page view, at most every 5 minutes, so the worst case is
  12 × 30 = 360 of 5,000 requests an hour (about 7 %), with 30 in flight at once.
* Neutral: an `alerts: false` repository sends the plain pull request query (ADR‑0015's fallback
  shape) without marking alerts refused, and claims no coverage, so the Security tile says nothing
  about it.
* Neutral: five orange hue keys join the palette; they are not from the arc42 brand registry.
