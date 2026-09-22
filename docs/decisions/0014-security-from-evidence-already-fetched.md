# 0014. Security from evidence already fetched: two tiers, no security API

* Status: accepted
* Date: 2026-09-21
* Requirements: FR‑1.13, FR‑1.10, QS‑3.5

## Context and problem statement

Gernot asked for dependabot and other GitHub security issues and PRs to get a clearly visible
visual highlight. Measured against the ten configured repositories on 2026-09-21, four of
them — `arc42.org-site`, `arc42.de-site`, `docs.arc42.org-site`, `quality.arc42.org-site` —
receive regular Dependabot pull requests, most closed within days; at the time of writing none
is open, so the highlight would be dormant the moment it ships and fire the next time Dependabot
runs, which is exactly when it has to be impossible to miss. `arc42.org-site#120`, a
`concurrent-ruby` bump, is the clearest case: its release notes cite three CVE and three GHSA
identifiers, but well past the 300 bytes zorgscope keeps as `Summary` — the evidence exists, and
is simply never read.

Two other bots complicate "highlight anything a bot opens": `copilot-swe-agent` opens pull
requests (Copilot fixes) on these repositories, and so does `github-actions` (a scheduled WCAG
score refresh). Neither is a security concern, so authorship by a bot cannot by itself be the
signal.

`toItem` in `internal/adapters/github/issues.go` already receives each item's title and whole
body before cutting it to that 300-byte summary, so the question is not whether more can be
fetched but what to do with what is already in hand — and whether that is enough, or whether the
highlight should instead ask GitHub for a more authoritative answer.

## Considered options

* A: Highlight every bot-authored item.
* B: One loud tier for every piece of evidence — a cited advisory, a `security` label,
  Dependabot or Renovate authorship, or a `dependencies` label, all painted the same red.
* C: Two tiers drawn from evidence the existing queries already fetch — a cited CVE or GHSA
  identifier, or a `security` label, for Security; Dependabot, Renovate, or a `dependencies`
  label, for Dependency.
* D: Match the word "security" in an item's title or body, in place of an advisory identifier, as
  the signal for the Security tier.
* E: Ask GitHub's Dependabot-alerts, code-scanning or secret-scanning APIs.

## Decision outcome

Chosen: **C — two tiers from evidence already fetched**. A is ruled out by `copilot-swe-agent`
and `github-actions`: both open pull requests on these repositories, and a rule keyed on
authorship alone would paint both red for reasons that have nothing to do with security. B is
ruled out for the reason the seen mark was retired over
([ADR‑0012](0012-no-seen-mark.md)): a mark that is always on stops being read, and on these
repositories most Dependabot pull requests already cite an advisory, so one loud tier would
repaint most of Dependabot's traffic red within a fortnight and mean nothing by the end of it.
Keeping a bump that cites nothing to a quiet Dependency mark is what keeps red worth reading. D
is ruled out because release notes, changelogs and ordinary conversation say "security" in
passing far more often than they name a real vulnerability; a CVE or GHSA identifier is specific,
names a published vulnerability, and, usefully, also catches a person's own issue that cites one —
the "other GitHub security issues" half of what Gernot asked for.

E — asking GitHub's security APIs — turns out not to be a request-budget question, or at least
not entirely. Dependabot alerts are `Repository.vulnerabilityAlerts`, a connection GraphQL lets be
nested straight into the `repository(...)` query this deployment already runs on every page view;
QS‑3.5's own text allows exactly that — a connection nested in a node raises GitHub's point cost
of a query without raising the request count — so the twenty-request budget is not, in fact, why
Dependabot alerts stay unasked. What rules them out is that reading security alerts needs
repository access and a token permission this deployment's token does not carry, and that Gernot
decided, recording it here on 2026‑09‑21, not to reopen the question of asking GitHub for
security data at all — confirming a position he had already taken in conversation on 2026‑09‑17,
though nothing about that earlier conversation was written down anywhere in this repository until
now. Code scanning and secret scanning stay ruled out for the reason the whole family was
originally: both are REST-only, so nesting does not apply to them, and each would be a genuinely
new request in a budget spent exactly to its limit.

The tiers themselves live in the domain, as a pure `Item.Tier()`. An item is **Security** when it
cites an advisory — a CVE or GHSA identifier in its title or body — or carries a `security`
label. Failing that, it is **Dependency** when it is authored by `dependabot` or `renovate`, or
carries a `dependencies` label. Everything else, including `copilot-swe-agent` and
`github-actions`, gets neither mark. What counts as evidence for that rule, though, is the
adapter's job, not the domain's: before `summarise` cuts the body down, `toItem` scans the title
and the whole body for `CVE-\d{4}-\d{4,}` and `GHSA(-[0-9a-z]{4}){3}` and stores what it finds as
`Item.Advisories`. The domain never parses text; it only asks whether `len(Advisories) > 0`. So
the domain owns the tiers, and the adapter owns what counts as evidence for them —
`Dashboard.Security` counts Security items over every open item regardless of the filter, and
`Item.ShowsQuiet` refuses to mark a Security item quiet no matter its age (FR‑1.10 AC4), but
neither of them decides what an advisory identifier looks like.

Splitting the highlight into two tiers rather than one is deliberate, and for the same reason
the seen mark was retired ([ADR‑0012](0012-no-seen-mark.md)): a mark that is always on stops
being read. On these repositories most Dependabot pull requests do cite an advisory, so most
would be red under a one-tier rule too — but a routine version bump that cites nothing would be
red as well, and within a fortnight red would mean nothing. Keeping a bump that cites no
advisory to a quiet Dependency mark is what keeps the red one meaningful, at least for as long as
Dependency itself still fires often enough to be the mark most bumps get — see Consequences.

### Consequences

* Good: no new request and no share of a budget that has none to spare, for Security and
  Dependency both. The domain owns the tiers, drawn by a pure `Item.Tier()`; the adapter owns
  what counts as evidence for them, the regex that decides an advisory identifier.
* Good: the red stays meaningful in principle, because a routine bump that cites nothing is only
  a quiet Dependency mark — the lesson of the retired `NEW` badge (ADR‑0012).
* Bad: on these repositories most Dependabot pull requests do cite an advisory, so red will be
  the common case whenever Dependabot runs, not the rare one, and the quiet Dependency tier — the
  Good above only holds if it keeps firing too — may end up rarely marking anything at all.
* Bad: a vulnerability that Dependabot fixes in a pull request citing no identifier, and that
  nobody labels, is drawn as Dependency, not Security.
* Bad: a routine bump whose quoted release notes happen to cite an unrelated advisory is drawn
  as Security. On these repositories that errs towards visible, which is the safer mistake.
* Bad: a false positive is now twice as prominent: the Sites view opens with a Security tile
  gathering every marked item, so an issue that only mentions a CVE in passing sits at the top of
  that page as well as in the list, and — being a Security item — never goes quiet. That is the
  price of the tile answering "is anything security-related open?" without being read past.
* Bad: a person's issue that mentions a CVE only in passing — "not affected by
  CVE-2021-44228" — turns red on the strength of the mention alone, and because a Security item
  is never marked quiet (FR‑1.10 AC4), that false positive stays just as loud however old it gets.
* Bad: the scan reads GitHub's `bodyText`, the text rendering of the body, which drops Markdown
  link targets and HTML attributes along with the rest of the markup. An identifier cited only as
  the target of a link whose visible text says something else — `[the advisory](…/GHSA-…)` — is
  never seen. An identifier written in the link text itself, in a bare URL, or inside a
  `<details>` block is seen, which covers the shapes Dependabot's own release notes usually take.
* Bad: labels are fetched `first: 5` (`internal/adapters/github/issues.go`), so a `security` or
  `dependencies` label past the fifth on a heavily labelled item is never seen, and whether the
  tier fires then depends on label order rather than on the label being there at all.
* Neutral: Dependabot's pull requests are short-lived and none is open at the time of writing,
  so the highlight is usually dormant — until the next run makes it fire.

### Amendment, 2026-09-22: the Security tile and the tier filter

Marking the items in place left the marks spread over up to ten site tiles, so "is anything
security-related open anywhere?" still had to be answered by reading the whole page. `/sites` now
opens with one Security tile gathering every marked item wherever it is open (FR‑1.13 AC6), and
the list gained a tier axis (FR‑1.13 AC7) that the tile and the list's own security count link to.

Three choices inside that, and why:

* The tier axis is a **floor**, not an equality: `tier=dependency` keeps the Security items too.
  A bump that fixes a vulnerability is still a bump, and the tile needs one link meaning
  "everything I mark".
* The tile is **always drawn**, saying "No security or dependency items open." when nothing is
  marked, which ADR-0014 expects to be the ordinary state. A tile that appeared only when it had
  something to say could not be told apart from a check that had stopped running, and it would
  move the tiles below it on the days it fired. This does not reopen ADR-0012's lesson: what is
  always on is a calm sentence, and the red rule appears only with a Security item.
* The tile's heading band is **neutral**, not red, and the alarm is a rule down its edge. White on
  the dark appearance's `--danger` is about 2.3:1, well under the 4.5:1 FR‑1.8 AC5 requires, so a
  red band could not have carried the heading's text; the rule is a non-text signal and needs only
  3:1, which it has in both appearances.

### Amendment, 2026-09-22: the quiet tier was too quiet

Drawn as first built, a Dependency row was not distinguishable from the rows around it. The chip
took `--muted` at the labels' weight, in the labels' pill, at the labels' size — and sat beside a
`dependencies` label saying the same word in the same grey. Asked to point at what was marked, the
page's author could; its reader could not. "Quieter than red" had been implemented as "identical to
the furniture".

Dependency now has a colour of its own: an amber chip, tinted like a coloured label, and a 3 px
amber rule down the row — the same shape of signal Security gets, in a different colour. The
ordering the two tiers exist for is intact, because loudness is now carried by *which* colour
rather than by having one or none: red means a published vulnerability, amber means maintenance,
and an unmarked row still has no rule at all. The amber is the `--warn` token, which had been
declared in `app.css` and never used; it clears 4.5:1 on its own chip and 3:1 as a rule in both
appearances.

The label that produced the mark is no longer drawn as a label chip beside it. It said nothing the
chip did not say louder, and it was the single biggest reason the chip read as a label.

## Pros and cons of the options

### A: Highlight every bot-authored item

* Good: the simplest possible rule — no parsing, no labels, one authorship check.
* Bad: `copilot-swe-agent` and `github-actions` open pull requests too, and would be painted red
  for work that has nothing to do with security or dependencies.

### B: One loud tier for all evidence

* Good: one rule and one mark — no quiet Dependency tier to define, explain or keep meaningful.
* Bad: on these repositories most Dependabot pull requests already cite an advisory, so nearly
  everything Dependabot opens would be red under this rule, and the red would stop meaning
  anything within a fortnight — the same failure that retired the `NEW` badge (ADR‑0012).

### C: Two tiers from evidence already fetched

* Good: see Decision outcome and Consequences above.
* Bad: a Dependabot fix that cites no identifier and carries no label is under-classified as
  Dependency rather than Security (see Consequences).

### D: Match the word "security" instead of advisory identifiers

* Good: no identifier scheme to match, and no dependence on GitHub's release notes actually
  naming a CVE or GHSA — any mention at all is caught.
* Bad: release notes, changelogs and ordinary conversation use the word "security" in passing
  constantly, so the signal is far less specific than an advisory identifier and would mark
  routine, unrelated text as Security; it also misses a person's issue that cites a CVE without
  ever using the word "security".

### E: Ask GitHub's security APIs

* Good: an authoritative answer for Dependabot alerts — a real alert rather than a guess from
  title and body text — and, for that one feed, technically nestable into the query already made
  without adding a request (see Decision outcome).
* Bad: reading any of the three feeds needs a token permission this deployment's token does not
  carry.
* Bad: code scanning and secret scanning have no nested form; each is REST-only and would be a
  separate, new request in a budget (QS‑3.5) already spent to its limit.
* Bad: reopens a question Gernot decided not to pursue — recorded here as this ADR's own
  decision, made 2026‑09‑21, confirming a position he had already taken in conversation on
  2026‑09‑17.
