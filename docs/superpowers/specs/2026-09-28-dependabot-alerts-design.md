# Dependabot alerts on the list, the tiles and the radar — design

Date: 2026-09-28. Status: implemented in 2.1.0 (§12 lists where the build departs from the draft). Decisions taken in conversation on
2026-09-28 are in §3. The fetching decision is
[ADR‑0015](../../decisions/0015-dependabot-alerts-nested-in-the-pull-request-query.md), which
supersedes ADR‑0014's option E for Dependabot alerts only. It builds on the security highlight
(`2026-09-21-security-highlight-design.md`), the needs‑you band (FR‑1.14) and the radar
(`2026-09-25-radar-design.md`). No database, no ticker, no inline script or style, and no request
to GitHub beyond the 20 already made while the token is right.

## 1. Goal

> we currently don't report Dependabot security alerts, check if and how we could include these
> (in list, tiles and radar), visually distinct from the other categories, and included in
> "needs-you" attention

## 2. What the data said

Measured against the ten configured repositories on 2026-09-28 with a token that can read alerts:

| Repository | Alerts enabled | Open alerts | Fix pull request |
|---|---|---|---|
| `quality.arc42.org-site` #62, `docs.arc42.org-site` #45, `faq.arc42.org-site` #1, `examples.arc42.org-site` #1 | yes | **HIGH** — `rubyzip` 2.3.2 (quality) or 2.4.1 (the other three) in `Gemfile.lock`, "rubyzip path traversal vulnerability", GHSA‑47m2‑wp7j‑p9vc / CVE‑2026‑85396, patched in 3.4.0; raised 2026‑09‑26 | none |
| `arc42.org-site` #64, `arc42.de-site` #34 | yes | LOW — `json` 2.20.0 in `Gemfile.lock`, a use‑after‑free crash in `JSON::ResumableParser#partial_value` on truncated duplicate‑key streams, GHSA‑9hj4‑r449‑hfvc / CVE‑2026‑71847, patched in 2.21.2; raised 2026‑08‑08/09 | none |
| `arc42-generator` | yes | none | — |
| `arc42-template`, `trainings.arc42.org-site`, `zorgscope` | **no** | unknown | — |

Three things follow:

* **The list misses real vulnerabilities today.** No alert has a pull request. `dependabotUpdate`
  is empty and carries no error, which suggests Dependabot *security updates* are switched off on
  these repositories while *alerts* are on. Turning them on is a repository setting, outside
  zorgscope, but it would give most alerts a fix pull request (§6).
* **Severity varies, and LOW alerts go unfixed.** The two LOW `json` alerts have been open for seven
  weeks. These are static Jekyll sites, so the JSON parser most likely runs only at build time over
  the site's own files, and a crash there is not an exposure. That is probably why nobody fixed
  them. Counted as "needs you", they would sit in the band and the top bar indefinitely, which is
  the always‑on mark ADR‑0012 retired. Hence §3's severity line.
* **"Off" is not "clean".** Three repositories cannot say whether they are vulnerable. A tile that
  counts them as clean says "All clear" without knowing.

## 3. Decisions (Gernot, 2026-09-28)

| Question | Decision |
|---|---|
| Ask GitHub for alerts at all | Yes, reversing ADR‑0014 option E for Dependabot alerts only. New ADR‑0015. |
| Where the alerts come from | Nested into the first page of the pull request query; no new request while the token is right (ADR‑0015). |
| Which alerts need the owner | **HIGH and CRITICAL** only. LOW and MEDIUM appear on the list, the tiles and the radar, but never in the band or the top‑bar count. |
| Token | A classic personal access token with the `security_events` scope only (ADR‑0015). |
| Distinct from the other marks | A kind of its own, a colour of its own and a pattern of its own (§7). |
| Delivery | Branch `feat/dependabot-alerts` off `main`; version **2.1.0** — a new feature bumps the minor digit. |

## 4. The model

### 4.1 An alert is an item of its own kind

`domain.KindAlert = "alert"` joins `KindIssue` and `KindPR`. An alert is an `Item`, not a separate
type, so grouping by repository, the filter, search, the snapshot's partial merge, the tiles and
the radar all take it without a second code path. What an alert has that an issue does not goes
into one pointer field, so issues and pull requests stay the size they are:

```go
// Alert is set for KindAlert only: what GitHub's Dependabot alert says (FR-1.16).
Alert *AlertFacts

type AlertFacts struct {
    Severity   Severity // Low, Medium, High, Critical — GitHub's four
    Package    string   // "rubyzip"
    Ecosystem  string   // "RUBYGEMS"
    Manifest   string   // "Gemfile.lock"
    Vulnerable string   // "= 2.3.2", the requirement in the manifest
    PatchedIn  string   // "3.4.0"; "" when no patched version exists
    FixPR      int      // the open Dependabot pull request fixing it; 0 when none
}
```

How the ordinary fields are filled:

| Field | From |
|---|---|
| `Number` | the alert's `number` |
| `Title` | `securityAdvisory.summary` |
| `Summary` | built by the adapter: *"rubyzip 2.3.2 → 3.4.0 · Gemfile.lock"* (or *"… no patched version"*) |
| `URL` | `https://github.com/{owner}/{name}/security/dependabot/{number}` — GraphQL offers no URL field for an alert, and this is GitHub's stable address for one |
| `Author` | `dependabot` |
| `Advisories` | `securityAdvisory.identifiers`, GHSA first, then CVE, normalised as `advisoryIDs` does, at most three |
| `CreatedAt`, `UpdatedAt` | both the alert's `createdAt`. An alert has no update time, and "raised when" is the honest age: it drives the sort, the radar's distance and the needs window |
| `Labels`, `ReviewRequested`, `State` | empty; `State` is `OPEN` |

`Severity` is an ordered `int` type in the domain, whose `String()` is fixed text (`low`, `medium`,
`high`, `critical`), as `Tier` does. An unknown value from upstream maps to `High`: an alert
GitHub raised at a severity this build does not know should err loud.

### 4.2 Tier and need

* **Tier.** `TierAlert` is added above `TierSecurity`: `None < Dependency < Security < Alert`.
  `Item.Tier()` returns `TierAlert` for every `KindAlert` item, whatever its severity. It returns
  it before looking at `Advisories`, which an alert always has and which would otherwise make it
  Security. The filter's floor reading holds: `tier=security` keeps alerts, since a published
  vulnerability GitHub has matched to a manifest is at least as serious as a pull request citing
  one. `tier=alert` shows alerts alone.
* **Need.** `NeedAlert` is added as the loudest reason, before `NeedSecurity`. `NeedFor` gives it to
  an alert of severity HIGH or CRITICAL, and `NeedNone` to a LOW or MEDIUM alert (§3). An alert
  needing the owner is **never stale**, like a Security item: an old unfixed HIGH is exactly what
  must stay in sight.
* **Quiet.** No alert is ever marked quiet (FR‑1.10 AC4), whatever its severity. Quiet means "the
  conversation has stopped", and an alert has no conversation; it is open until it is fixed or
  dismissed. `ShowsQuiet` gains the case beside Security's.
* **One problem, one row in the band.** When an alert has an open fix pull request (`FixPR` set, and
  that pull request is in the snapshot), the band lists the alert, carrying *"fix ready: #n"*, and
  not the pull request. The list below still shows both (QG‑1: nothing leaves the list). This is
  done in `BuildNeedsYou`, over the whole item set, since the pairing crosses items.

### 4.3 Coverage

Per repository, the fetch reports one of:

* `CoverageOn` — alerts are enabled and were read. Zero alerts means none are open.
* `CoverageOff` — `hasVulnerabilityAlertsEnabled` is false. Unknown, not clean.
* `CoverageUnavailable` — GitHub refused (token scope or repository access), or the field did not
  arrive. Unknown, not clean.

`ports.Source.Fetch` changes from `([]domain.Item, error)` to `(domain.Fetched, error)`, with
`Fetched{Items []Item; Coverage map[string]Coverage}`. `SourceFunc`, `ports.fake`, the snapshot
and every test source change shape once. `snapshot.Snapshot` gains `Coverage`, and `mergePartial`
applies the rule it applies to items: a repository the fresh fetch has no coverage for keeps its
previous coverage.

## 5. The adapter

* **Two pull request query types.** `pullRequestsFirstPageQuery` carries the nested alert fields;
  `pullRequestsQuery` stays as it is and serves page 2 onward. They are two structs because
  `shurcooL/graphql` builds the query from the struct, and a field cannot be switched off per call.
  The shared page‑handling code takes the connection, not the query.
* **The nested fields:**

  ```graphql
  hasVulnerabilityAlertsEnabled
  vulnerabilityAlerts(states: OPEN, first: 100) {
    totalCount
    nodes {
      number createdAt vulnerableManifestPath vulnerableRequirements
      securityAdvisory { summary identifiers { type value } }
      securityVulnerability { severity package { name ecosystem } firstPatchedVersion { identifier } }
      dependabotUpdate { pullRequest { number state } }
    }
  }
  ```

  At most 100 alerts per repository are read, and no `totalCount` is asked for: no arc42
  repository has had more than one open at a time, so neither paginating nor saying "and N more"
  earns its code.
* **Refusal and fallback** (ADR‑0015):
  1. First page answers with data and no error: read everything, coverage `On` or `Off`.
  2. First page answers with pull requests but an error, and `vulnerabilityAlerts` is `null`: keep
     the pull requests, coverage `Unavailable`, set the fetcher's `alertsRefused` flag. No retry is
     needed.
  3. First page answers with no data and an error: ask for the same first page with
     `pullRequestsQuery` (no alerts). If that succeeds, coverage `Unavailable` and set
     `alertsRefused`; if it also fails, it is the ordinary repository failure it is today.
  4. While `alertsRefused` is set, every repository's first page uses `pullRequestsQuery` and
     reports `Unavailable`. The flag lives on the `IssueFetcher` and is cleared by a process
     restart, which on this machine is every wake from zero.

  Neither step reads the error's text. `shurcooL/graphql` hands back only messages, and matching
  GitHub's wording would break silently the day GitHub rewrites it. What arrived is the signal.
* **Mapping** is a new `toAlertItem(owner, name, node)`, next to `toPRItem`. The identifiers go
  through the same normalisation as `advisoryIDs`.

## 6. What zorgscope does not do

* It does not fix, dismiss or open alerts, and it never writes to GitHub.
* It does not switch on Dependabot alerts or Dependabot security updates for the three repositories
  that have them off. That is a repository setting for their admin. **Recommended, outside this
  change:** turn on alerts for `arc42-template`, `trainings.arc42.org-site` and `zorgscope`, and
  security updates wherever alerts are on, so that most alerts arrive with a fix pull request to
  merge.
* No code scanning and no secret scanning (ADR‑0014, ADR‑0015).

## 7. The visual

An alert must be told apart from a Security item (red, diagonal hazard tape) and a Dependency item
(amber, diagonal hazard tape) at a glance, and not by colour alone. It gets a **new colour**, a
**new pattern** and a **new shape**:

* **Colour:** `--alert`, a magenta‑violet, a new token in `app.css` with a light and a dark value.
  It must clear 4.5:1 as chip text and 3:1 as a non‑text rule in both appearances;
  `contrast_test.go` gains both pairs. As with the Security tile band, if the dark appearance's
  value cannot carry white text, the chip uses dark text on the colour rather than a darker colour.
* **Pattern:** a **crosshatch**, both diagonals, not the one diagonal of hazard tape. It is a
  `<pattern>` in CSS for the list and an SVG `<pattern>` for the radar.
* **Chip:** an inline‑SVG shield carrying a "!" and the words *Alert · high*. HIGH and CRITICAL
  alerts get a solid chip, MEDIUM and LOW an outlined one. The chip's `title` reads *"Dependabot:
  rubyzip 2.3.2 → 3.4.0, GHSA‑47m2‑wp7j‑p9vc"*. It is one `{{define "item-alert"}}` in
  `fragments/tier.html`, next to `item-tier`, so the list, `GET /items`, search, the tiles and the
  band draw the same markup.

Where it appears:

* **List.** An alert sits in its repository's group, sorted like everything else, with a meta line
  of *"Alert · raised 2 days ago · Gemfile.lock"*. HIGH and CRITICAL rows carry 12 px of crosshatch
  down the left edge; MEDIUM and LOW a plain 3 px `--alert` rule. The header gains *"· 6 alerts (4
  high)"* beside the security count, over every item whatever the filter, and omitted at zero. It
  links to `?tier=alert`. The filter form's kind control gains *Alerts* (`kind=alert`), and its tier
  control gains *Alerts*.
* **Needs‑you band.** HIGH and CRITICAL alerts lead the band, before Security. The whole band is
  framed in `--alert` while one is in it; if a Security item is in it too, red wins the frame and the
  alert keeps its chip. A row reads *Alert · high · rubyzip · quality.arc42.org‑site*, plus
  *"fix ready: #n"* when there is one. The top bar's "N need you" counts them.
* **Security tile** (`/sites`). It keeps its name and first place. Alerts are listed first, at most
  four, CRITICAL then HIGH then MEDIUM then LOW, each naming its repository. After them come the
  three pull requests and four issues it holds today. The count line becomes *"4 high · 2 low
  alerts · 0 security · 0 dependency"*, dropping any term that is zero. Two new lines, drawn only
  when they apply:
  * *"Alerts off: arc42‑template, trainings, zorgscope"* — coverage `Off`.
  * *"Alerts unavailable: the token cannot read them"* — coverage `Unavailable` on any
    repository.

  *"All clear"* is said only when nothing is marked, no alert is open **and** every repository's
  coverage is `On`. Otherwise the tile says what it does know. The heading band becomes crosshatch
  while a HIGH or CRITICAL alert is open, and red hazard tape takes precedence while a Security item
  is.
* **Site tiles.** No alert rows; the tile's seven rows stay issues and pull requests. A tile whose
  repositories have open alerts gains one line, *"▲ 1 high alert"*, in `--alert`, linking to the
  list filtered to that repository and `kind=alert`.
* **Radar.** An alert is a **triangle** (issue circle, pull request diamond), filled in its site's
  colour. HIGH and CRITICAL alerts are larger, ringed in crosshatch `--alert`, pulse like Security
  items and carry a *"▲ #n"* tag. MEDIUM and LOW alerts get a thin `--alert` ring and no pulse. No
  alert fades. Draw order, calm to loud: everything else, Dependency, low alerts, Security,
  high alerts, so a HIGH alert is never under anything. The data block shows *Alert*, the severity,
  the package line, the manifest and *raised*. The legend gains a triangle entry, *"Dependabot alert
  — GitHub says a dependency is vulnerable"*, and the counts gain *alerts*.
* **Search.** An alert is found by its title (the advisory summary) and summary (package, versions,
  manifest). The keywords `alert` and `alerts` set `Kind` to `KindAlert` (FR‑12.1).
* **Contributors.** Alerts are excluded: they are not somebody's work.

Text carries the meaning everywhere. The glyphs, the crosshatch and the rules are decorative
(`aria-hidden`); the chip's words and the radar blip's accessible name say *Dependabot alert, high*.

## 8. Requirements

**FR‑1.16** (new, E‑1, S): *As the user I see every open Dependabot alert, and the serious ones as
needing me.*

* AC1 Every open Dependabot alert of a configured repository is an item of kind Alert, in its
  repository's group, drawn with the Alert chip, which names its severity, and linking to the
  alert on GitHub.
* AC2 HIGH and CRITICAL alerts need the owner (FR‑1.14) ahead of every other reason, are never
  stale, and carry "fix ready: #n" when an open pull request fixes them; that pull request is then
  not listed again in the band. LOW and MEDIUM alerts do not need the owner.
* AC3 No alert is marked quiet.
* AC4 Alerts are drawn in a colour and a pattern used by nothing else — solid chip and crosshatch
  for HIGH and CRITICAL, outlined chip and a plain rule for LOW and MEDIUM — on the list, the band,
  the search results and the tiles; on the radar an alert is a triangle. Colour is never the only
  signal.
* AC5 The Security tile lists alerts first and states their counts by severity. It names every
  repository whose alerts are off or unreadable, and says "All clear" only when nothing is marked,
  nothing is alerted and every repository's alerts were read.
* AC6 Alerts are read as fields of the pull request query already made. With a token that may read
  them, no request is added (QS‑3.5). With one that may not, the pull requests still arrive, the
  alerts are reported unavailable, and at most one extra request per repository is made per
  process.
* AC7 `kind=alert` and `tier=alert` narrow the list to alerts; `tier=security` includes them.

**Amended:** FR‑1.13 AC6 (the Security tile, §7), FR‑1.14 AC1/AC2 (Alert as the loudest reason),
FR‑1.15 AC3 (the triangle), FR‑1.8 AC1 (the site tile's alert line), FR‑12.1 (the `alert`
keyword), QS‑3.5 (alerts named among the nested connections), and
`docs/concepts/security-and-tokens.md` (what `GITHUB_TOKEN` now reads, and its scope). `README.md`
line 34 changes from "any personal access token; public repositories need no scope" to the
`security_events` scope.

## 9. Testing

* **Adapter** (fake GraphQL server, through the real decode path). New fixture repositories are
  added so that no existing count moves:
  * `org/alerts` — alerts on, one HIGH alert with a fix pull request, one LOW without.
  * `org/alerts-off` — `hasVulnerabilityAlertsEnabled: false`.
  * `org/alerts-forbidden` — pull requests present, `vulnerabilityAlerts: null`, an error.
  * `org/alerts-scope` — the first page with alerts answers with no data and an error; without
    alerts it answers normally.

  Asserted: the mapping of every field of §4.1, the URL, the identifier normalisation, the summary
  line, and an unknown severity mapping to High. Coverage comes out `On`/`Off`/`Unavailable` for each
  fixture. The forbidden and scope fixtures still yield their pull requests. After a scope refusal,
  the next fetch sends no alert fields. Alerts are read from page 1 only of `org/paged`.
* **Budget.** `TestGraphQLRequestBudget` stays at exactly 20 for the representative configuration
  with alerts on. A new test asserts that the scope‑refused configuration makes at most 30 requests
  on its first fetch and 20 on its second.
* **Domain.** Tables over `Tier()` (every alert is `TierAlert`, even with advisories), `NeedFor`
  (HIGH/CRITICAL → `NeedAlert`, LOW/MEDIUM → `NeedNone`), `Stale` (a `NeedAlert` never is),
  `ShowsQuiet` (an alert never is), the filter's floor (`tier=security` keeps alerts), the band's
  pairing (an alert with an open fix pull request suppresses that pull request in the band, not in
  the list) and `Dashboard` counts over every item regardless of the filter.
* **Snapshot.** Coverage survives a partial fetch for the repository that failed, and is replaced
  for those that fetched.
* **Web.** The chip, row class and title on `/`, `GET /items`, `/search` and the band. The header
  clause and its omission at zero. The Security tile's order, count line, *off* and *unavailable*
  lines, and "All clear" only under full coverage. The site tile's alert line. The radar's triangle,
  rings, tag, legend and accessible name. `contrast_test.go` for `--alert` in both appearances.
  No `style` attribute anywhere (QS‑4.4), and the radar within QS‑2.3's budget with alerts added
  to the representative fixture.
* Every existing test stays green, changed only for the `Source` signature.

## 10. Rollout

1. Create the classic token (`security_events` only) under an account that is an admin of all ten
   repositories. Check it with
   `gh api graphql -f query='{repository(owner:"arc42",name:"arc42.org-site"){vulnerabilityAlerts(first:1){totalCount}}}'`.
2. Set it as `GITHUB_TOKEN` on Fly and in local `.env`.
3. Deploy. If the Security tile shows *"Alerts unavailable"*, the token or its owner's access is
   wrong, and nothing else is affected.

## 11. Open points

* **Unverified until implementation:** that a missing *scope* fails the whole query with no data
  (§5 step 3). This is GitHub's documented behaviour for `INSUFFICIENT_SCOPES`, but it was not
  reproduced here, because the only token at hand carries `repo`. The fallback handles both shapes
  either way; this point decides only which fixture mirrors production.
* **`--alert`'s exact values** — chosen in implementation against the contrast tests, and shown to
  Gernot before merge, as the amber was.

## 12. As built (2.1.0)

Where the implementation departs from the draft above, and why:

* **Refusal is one rule, not two.** Any error on the first page that asks for alerts is followed by
  the same page without them; the draft's separate "field refused, keep the data" path (§5 step 2)
  is gone. The retry costs one request in the field‑refusal case too, but only once per process,
  and it means neither refusal shape is recognised by anything GitHub might reword. The process
  stops asking only when that retry succeeds, so a transport failure does not switch alerts off.
* **No `totalCount`** (§5): see there.
* **Unknown coverage is not named on the tile.** A repository nobody reported on is one whose fetch
  failed outright, which the page's error notice already says; the tile names only repositories
  GitHub reported *off* or *unavailable to the token*.
* **The top bar counts the band.** It used to count item by item; an alert absorbing its fix pull
  request would then have been one row in the band and two in the top bar. It now counts the
  band's rows that are not stale, so the two cannot disagree.
* **Colour:** `--alert` is `#9c1f8f` light / `#e38ae0` dark (6.99:1 and 6.90:1 against the surface;
  chip ink white / `#1a0620`, 6.99:1 and 8.23:1); the tile band is `#9c1f8f` hatched with
  `#5e1256` under white. `contrast_test.go` asserts the rule, the solid chip and the outlined chip
  in both appearances.
* **An empty list is checked before it is believed** (2.1.1, ADR‑0015 amendment). A fine‑grained
  token without the Dependabot alerts permission gets an empty list from GraphQL and no error; the
  REST alerts endpoint refuses it with 403. Each repository with alerts on and none open is checked
  there once per process; only a 200 makes it clean. Fixtures `org/alerts-clean` and
  `org/alerts-silent` cover both answers.
