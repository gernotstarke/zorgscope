# 0018. Fixed radars over families; aim42 in three colours

* Status: accepted (implemented in 2.4.0)
* Date: 2026-10-11
* Requirements: FR‑1.8, FR‑1.15, FR‑1.16, QS‑3.5
* Amends: [ADR‑0016](0016-site-groups-one-snapshot-a-radar-per-group.md)

## Context and problem statement

Gernot asked on 2026‑10‑10 to add `aim42/aim42` and `aim42/aim42.org-site`, one tile each, and to
decide first where they go on the radar. Two facts shaped the answer:

* `aim42/aim42` has 85 open issues, 83 of them last touched between 2014 and 2019. On the radar they
  all stand on the rim, so whichever sector holds them holds a row of about 80 blips along its edge.
* Under ADR‑0016 a site's `group` was a radar: every group got a link, the first group's radar
  showed its own sites only, and any other group's radar condensed the rest. Gernot did not want an
  aim42 radar. He wanted a radar called **all** with one sector each for arc42, iSAQB, systems42,
  aim42 and a **private** sector to hold repositories added later, drawn empty from the start —
  while systems42 stays a sector of the arc42 radar, and the iSAQB radar condenses every other
  family.

No rule over `group` alone produces that: systems42 must be a sector of its own on all, yet sit on
the arc42 radar beside the arc42 sites, and aim42 must be a sector on two radars without a radar of
its own.

## Considered options

* **A. A `radars:` list in the YAML** naming, per radar, the families drawn site by site and the
  families condensed into one sector each.
* **B. Radars fixed in the code**, over families: a site's `group` becomes its family, and a short
  table in `internal/web/radar.go` says which radars exist and what each draws.
* **C. Keep group = radar** and put aim42 into an existing group (arc42: 12 sectors with a buried
  aim42 wedge; or iSAQB, which aim42 is not part of).

## Decision outcome

Chosen: **B**, in conversation on 2026‑10‑10. Gernot: the repositories are a fixed set for now,
configuring the radars comes later. The table:

| radar | site by site | condensed, one sector per family |
|---|---|---|
| arc42 | arc42, systems42 | — (the other families' items are left off) |
| iSAQB | iSAQB | every other family that has a site, in order of first appearance |
| all | — | arc42, iSAQB, systems42, aim42, private |

A radar is offered when a site belongs to a family it draws site by site, or — for all — when two of
its families have a site; the switch is drawn when more than one is offered. A condensed sector
wears its family's first site's colour, slate for a family without a site (private). On all, a
family the table does not name stands in Other, so nothing the list shows is missing there (QG‑1).

aim42 is asked for Dependabot alerts (Gernot's choice): `aim42.org-site` had 20 open alerts on
2026‑10‑10. Both repositories are public; the classic `GITHUB_TOKEN` in the local `.env` reads their
alerts, the fine-grained one there is refused (403) even on arc42.

**aim42's colour.** Gernot asked for aim42's red, blue and green "as a shortened rainbow, all three
clearly visible". The colours are aim42.org's phase colours (`_sass/base/_variables.scss`: analyze
`#d4405f`, evaluate `#2f9fd0`, improve `#3fae63`). The heading band uses each darkened just far
enough for white text at 4.5:1 (`#d33a5a`, `#257ca2`, `#2f834b`); the dark appearance draws the
phase colours themselves. The hue key `aim42` is striped everywhere a site's colour appears: three
blocks on the tile's band, a stripe repeating every 4.5 rem down a list group's or search hit's
edge (so a long group shows all three wherever it is scrolled to), a three-part swatch, the radar
rim cut into three arcs, and blips filled with three stripes (`#radar-aim42`). Where one colour must
do — the tile's wash, a sector's dotted edges — aim42 is its evaluate blue. The tokens are
`--aim42-red`, `--aim42-blue` and `--aim42-green` (each with a `-sig`), named by colour rather than
by phase because the linter's British spelling list rejects "analyze".

### Consequences

* Good: the arc42 radar is unchanged (ten sectors). The old aim42 backlog gets a sector of 72° on
  all and 45° on iSAQB, not 30° on arc42.
* Good: the private sector is already on all, so adding a repository there later moves nothing.
* Bad: which radars exist, and the family names they list, are code, not configuration — the very
  thing ADR‑0016 avoided by making `alerts: false` explicit ("a rule nobody could see in the
  file"). A family renamed in the YAML silently drops out of its radar. Recorded as interim until the
  radars are configured.
* Bad: QS‑3.5 rises to 19 repositories and 38 requests per fetch, 12 × 38 = 456 of GitHub's 5,000 an
  hour (about 9 %), the ceiling met again.
* Bad: about 95 more open items — 229 in all on 2026‑10‑11, past QS‑2.3's representative fixture of
  150 items and its 150 kB. Amended in 2.4.1: the fixture holds 250 items and the budget is 300 kB
  (the list renders it in about 240 kB, the radar in about 220 kB).
* Neutral: `aim42` is the one hue key with special rules in app.css and radar.go (`stripedHue`); the
  contrast of its three stripes is held by `TestTheAim42StripesKeepTheirContrast`.
