# iSAQB repositories: a second group on the list, the tiles and the radar — design

Date: 2026-09-29. Status: approved in conversation, not yet built. Target version **2.2.0**.
Builds on the per-site tiles (`2026-09-15-per-site-tiles-design.md`), the radar
(`2026-09-25-radar-design.md`) and the Dependabot alerts (`2026-09-28-dependabot-alerts-design.md`).
No database, no ticker, no inline script or style.

## 1. Goal

> I need to add isaqb repos to the radar, two or three more. A button could dynamically add the
> iSAQB repos or switch to the isaqb view and add a single slice condensing all arc42 stuff
> (https://github.com/isaqb-org/curriculum-foundation, https://github.com/isaqb-org/glossary and
> likely some more later, esp req4arc, improve, adoc)

## 2. Decisions taken in conversation (2026-09-29)

| # | Question | Decision |
|---|---|---|
| D1 | What does the iSAQB button do? | **Switch** the radar to an iSAQB view: one sector per iSAQB site, all arc42 condensed into one sector. Not "add sectors to the current radar". |
| D2 | Security for iSAQB? | **None.** iSAQB repositories contribute issues and pull requests only: no Dependabot alerts are asked for. |
| D3 | How far does the switch reach? | **Radar only.** The arc42 \| iSAQB switch exists on the Radar view alone. |
| D4 | Do iSAQB repositories appear elsewhere? | **Yes, merged:** the List, the Sites tiles, the needs-you band, the top bar's counts, search and Contributors all include them, exactly like any other watched repository. |
| D5 | The request budget (QS‑3.5) | **Raised** to at most 15 repositories × 2 queries = 30 requests per fetch (§5). |
| D6 | Symmetry | The arc42 view condenses the iSAQB group into one sector, as the iSAQB view condenses arc42. |
| D7 | Colour | iSAQB sites wear **a variety of orange tones**, on tiles and on the radar (§6). |

## 3. Configuration

Each `github.sites` entry gains an optional `group`:

```yaml
    - name: curriculum-foundation
      url: https://github.com/isaqb-org/curriculum-foundation
      repo: isaqb-org/curriculum-foundation
      hue: orange
      group: iSAQB
      alerts: false
    - name: glossary
      url: https://github.com/isaqb-org/glossary
      repo: isaqb-org/glossary
      hue: apricot
      group: iSAQB
      alerts: false
```

and both repositories are added to `github.repos`.

* `group` is a short display name (letters, digits, `.`, `-`; at most 12 characters). Absent means
  `arc42`, so every existing entry keeps its meaning without an edit.
* The groups, in order of first appearance among the sites, are the radar's switch buttons (§4).
  With only one group the switch is not drawn and the radar is exactly today's.
* Whether a repository is asked for Dependabot alerts is a separate, explicit key on the site entry,
  `alerts`, defaulting to `true`; the shipped iSAQB entries set `alerts: false` (D2). It is not
  derived from `group`, because "only arc42 gets alerts" would be a rule nobody could see in the
  file. A repository is asked for alerts when at least one site claiming it — or no site at all
  (Other) — leaves `alerts` at `true`.
* Validation: `github.repos` holds at most **15** entries (was an unenforced 10, documented only in a
  comment); a bad `group` is rejected with a field-naming error like every other key.
* The shipped `config/zorgscope.yaml` comment about the budget is rewritten to match §5.

Adding req4arc, improve or adoc later is one `repos` line and one `sites` entry each.

## 4. The radar

* `GET /radar` shows the first group (`arc42`); `GET /radar?group=iSAQB` shows the named one. The
  group is matched case-insensitively; an unknown group falls back to the first, it does not 404
  (a stale bookmark still lands on a radar). FR‑1.12's landing view stays `/radar`.
* **Switch.** Top left of the scope, one link per group, in the style of the view switch in the top
  bar, the shown group marked with `aria-current`. Plain links: no JavaScript. Refresh returns to the
  radar with the group it was pressed on.
* **Sectors.** The shown group's sites get one equal sector each, in configuration order; then **one
  sector per other group**, named after the group, holding every blip of that group's repositories;
  then Other, when some repository is claimed by no site. The arc42 view is therefore 10 + 1 sectors,
  the iSAQB view 2 + 1.
* **Condensed sector.** Its rim and dotted edges take the hue of the group's first site. Its blips
  keep **their own site's** hue, so the orange tones stay visible inside the arc42 view's iSAQB
  sector (and the slate/navy/… tones inside the iSAQB view's arc42 sector). Distance, shape, tape,
  pulse, fade and the collision rules are unchanged; the data block names the repository, so nothing
  is lost by condensing.
* The legend's counts count what is drawn, which is always the whole snapshot (QG‑1: nothing the
  list shows is missing from either view).

## 5. The request budget (QS‑3.5)

**Why it existed.** `cost_test.go` states the reason: "≤ 20 GraphQL point-equivalents per fetch, so
that a 15-minute interval stays under 2 % of GitHub's hourly limit". That interval was the external
cron of ADR‑0003; since ADR‑0011 a fetch happens only on a stale page view, cached for 5 minutes. The
cap also doubles as the concurrency bound of `IssueFetcher.Fetch`, which starts every request at
once.

**New wording.** At most 2 requests per repository and at most 15 repositories: **≤ 30 requests per
fetch**. Worst case — somebody reloading all hour — is 12 fetches × 30 = 360 requests, about 7 % of
GitHub's 5,000 an hour; 30 requests in flight stays well under GitHub's secondary limit of about 100
concurrent. `TestGraphQLRequestBudget` asserts the exact count at 15 repositories = 30. The
representative configuration of QS‑2.3/QS‑2.5 stays at 10 repositories; only QS‑3.5's ceiling moves.

A repository with `alerts: false` sends the pull-request query without the nested
`vulnerabilityAlerts` connection (ADR‑0015), and never triggers the one-off REST check an empty
alert list gets (FR‑1.16): it costs 2 requests, never 3.

## 6. Colour: orange tones

Five new hue keys, so up to five iSAQB sites are told apart without tags: `apricot`, `orange`,
`tangerine`, `rust`, `copper` — added to `config.HueKeys` and to `app.css` as `--hue-<key>` /
`--hue-<key>-sig` pairs with a `.hue-<key>` class, following the existing seven.

* The masthead tone carries white tile-title text, so it must keep **≥ 4.5:1 against #ffffff**
  (FR‑1.8 AC5) — which rules out light oranges as masthead colours; the lighter `-sig` tone carries
  the radar blips and the dark-mode stripe, as for the other hues. `contrast_test.go` gains the new
  keys.
* **Risk: orange next to amber.** Dependency items are ringed in amber hazard tape (`--warn`) and
  Security in red. An orange blip is not a Dependency blip: the tape is a striped ring, the fill is
  flat, and colour is never the only signal. The tones are chosen to sit visibly apart from `--warn`
  (#8a5300 / #e0a03c) — checked on the radar in both appearances before shipping.
* The condensed iSAQB sector in the arc42 view takes the first iSAQB site's hue (`orange`).

## 7. Requirements and documents touched

* **FR‑1.8** — AC5: tiles of a non-arc42 group carry that group's configured colours ("from the
  arc42 brand registry" becomes "from the configured palette").
* **FR‑1.15** — AC2 gains the groups: the switch, one sector per site of the shown group, one
  condensed sector per other group, then Other. New AC: `?group=` selects the group, unknown falls
  back to the first.
* **FR‑1.16** — a repository whose sites all say `alerts: false` is not asked for alerts and is
  neither "off" nor "unavailable" on the Security tile: it is not reported on.
* **QS‑3.5** — §5.
* **ADR‑0016** — *Site groups: one snapshot, a radar per group.* Records D1–D5 and the rejected
  alternatives: a separately fetched iSAQB snapshot (rejected by D4: the list needs it anyway) and
  batching several repositories into one GraphQL query with aliases (deferred: it would make the
  budget moot but rewrites the fetcher and its per-repository paging).
* `docs/concepts/configuration.md` — `group`, `alerts`, the 15-repository cap, the new hue keys.
* `internal/version/version.go` — **2.2.0**; README if it names the version.

## 8. Testing

* **config** — `group` defaults to arc42; bad group rejected; `alerts` defaults to true; 16 repos
  rejected; the new hue keys accepted.
* **github adapter** — `TestGraphQLRequestBudget` at 15 repositories = 30 requests; a repository
  with alerts off sends no `vulnerabilityAlerts` field and no REST check (fake server counts).
* **domain / web** — the Security tile does not list an alerts-off repository as off or unavailable;
  List and Sites include iSAQB items and tiles.
* **radar** — arc42 view: 10 site sectors + one iSAQB sector holding both iSAQB repositories' blips
  in their own hues; iSAQB view: 2 + one arc42 sector; `?group=isaqb` (case) works; unknown group
  falls back; one group only → no switch, output unchanged from 2.1.1; switch marks `aria-current`;
  still no style attribute (QS‑4.4).
* **contrast** — every new hue ≥ 4.5:1 with white.
* **by eye** — the real radar in light and dark, both groups, before merging.

## 9. Not in scope

Groups on the List, Sites or Contributors views; Dependabot alerts for iSAQB; batching queries;
per-group owners or auth repositories; req4arc/improve/adoc themselves (config lines when wanted).
