# Security highlight: Dependabot and security items stand out — design

Date: 2026-09-21. Status: approved by Gernot in conversation (two tiers by evidence; a chip, a
danger rule and a count, in place, keeping the last-updated sort — his choices). Builds on the
cogwheel settings (`2026-09-20-cogwheel-settings-design.md`), whose `IsQuiet(now, after)` the
quiet override extends. No database, no ticker, no second renderer, no inline script or style, and
no new request to GitHub.

## 1. Goal

> dependabot and other github security issues/PRs shall get a clearly visible visual highlight

## 2. What the data said

Measured against the ten configured repositories on 2026-09-21.

**Dependabot runs often, and its pull requests are short-lived.** Four repositories —
`arc42.org-site`, `arc42.de-site`, `docs.arc42.org-site`, `quality.arc42.org-site` — receive
regular Dependabot pull requests, most closed within days. At the time of writing **none is open**:
all 45 open items are authored by people. The highlight is dormant today and fires the next time
Dependabot does, which is exactly the moment it has to be impossible to miss.

**A bot is not a security item.** `copilot-swe-agent` opens pull requests (Copilot fixes) and
`github-actions` opens them too (a scheduled WCAG score refresh). A rule of "highlight every bot"
would paint both red.

**The evidence is in the body, past where zorgscope stops reading.** Dependabot's pull requests
cite CVE and GHSA identifiers — `arc42.org-site#120`, a `concurrent-ruby` bump, cites three of each
— but inside the release notes, well past the first 300 bytes. zorgscope keeps only a 300-byte
`Summary` (`maxSummaryLen`), whose first bytes are *"Bumps [concurrent-ruby](…) from 1.2.2 to
1.3.7."* and nothing more.

**Reading it costs nothing.** `toItem` in `internal/adapters/github/issues.go` receives the body
whole and cuts it only when it builds the `Summary`. Scanning it there, before the cut, adds no
request. That matters: QS‑3.5 budgets twenty GraphQL queries per page view and the configuration
spends exactly twenty.

## 3. Decisions (Gernot, 2026-09-21)

| Question | Decision |
|---|---|
| What counts | Two tiers by evidence: **Security** and **Dependency** (§4). |
| How loud | A chip, a danger-coloured rule on the row, a count in the header — in place, the sort by last update unchanged (ADR‑0012). |
| Security alerts API | Not used (§6). This highlights items already on the list; it fetches nothing. |
| Delivery | Branch `feat/security-highlight` off `main` at `1cec874`; version 0.7.0. |

## 4. The two tiers

The rule lives in the domain, as a pure `Item.Tier()`:

* **Security** — the item cites an advisory (a CVE or GHSA identifier in its title or body), *or*
  it carries a label spelled `security` in any case.
* **Dependency** — not Security, and either authored by `dependabot` or `renovate` (compared
  without regard to case), or labelled `dependencies` in any case.
* **None** — everything else. `copilot-swe-agent` and `github-actions` land here.

**Why two tiers rather than one.** If every routine version bump shone red, the red would stop
meaning anything within a fortnight — the failure that retired the `NEW` badge
([ADR‑0012](../../decisions/0012-no-seen-mark.md)): a mark that is always on is read as noise. A
Dependabot bump that cites no advisory is maintenance, and gets a quiet mark; one that cites a CVE
is a fix for a known vulnerability, and gets the loud one. On these repositories most Dependabot
pull requests do cite one, so most will be red — which is correct.

**Why the advisory identifiers and not the word "security".** Release notes say "security" in
passing all the time. A CVE or GHSA identifier is specific: it names a published vulnerability,
and a bump whose release notes cite one is, in effect, the fix for it. It also catches a
*person's* security issue that references a CVE, which is the "other github security issues" half
of the request.

**A security item is never quiet.** An old, unfixed vulnerability is the item a reader most needs
to see, and FR‑1.10 AC4's dimming would hide exactly that. `Item.ShowsQuiet(now, after)` is
`IsQuiet` except that a Security item never is. Putting the override in the domain means the list
and the search results call one function rather than each re-implementing "security beats quiet".

## 5. Where each part lives

**Adapter — reports a fact only it can see.** In `toItem`, before `summarise` cuts the body, the
title and the whole body are scanned for advisory identifiers and the result stored as
`Item.Advisories`: deduplicated, in first-seen order, at most three. The adapter makes no judgement
about tiers; it extracts text it alone has. `toPRItem` already routes through `toItem`, so there is
one place to change, not two.

The patterns:

* CVE: `CVE-\d{4}-\d{4,}`, case-insensitive, stored upper-case.
* GHSA: `GHSA(-[0-9a-z]{4}){3}`, case-insensitive, stored as `GHSA-` plus lower-case groups.

**Domain — owns the rule.** `Item.Advisories`, `type Tier`, `Item.Tier()`, `Item.ShowsQuiet`, and
`Dashboard.Security`, counted beside `Total` over every item regardless of the filter.

**Web — draws it, in both places.** Search builds its rows in `searchView`, separately from the
list's `newItemView`; both gain the tier and both call `ShowsQuiet`. The chip is one
`{{define "item-tier"}}` in a new `templates/fragments/tier.html`, which `fragmentGlob` parses into
every page and into the fragment set, so the list, the `GET /items` fragment and the search results
all draw the same markup and cannot drift.

## 6. Why not GitHub's security alerts

On 2026‑09‑17 Gernot rejected surfacing GitHub's security alerts, and this design keeps that
decision rather than reopening it. The Dependabot alerts, code-scanning and secret-scanning feeds
are separate APIs: each would be a new request in a budget with none to spare (QS‑3.5), and each
needs a `security_events` scope the token does not have. Everything this design highlights is
already on the list, fetched by the queries that run today.

## 7. The visual

* **Security** — an inline-SVG shield and the word *Security*, in `--danger`, and a 3 px
  `--danger` rule down the row's left edge. The chip's `title` names the evidence — *"cites
  CVE-2026-54904"* — so a red row always answers "why?". For a Security item that has a
  `security` label but no identifier, the title says *"labelled security"*.
* **Dependency** — the word *Dependency* in `--muted`, no rule. Visible, not loud.
* The list header reads **"46 open · 2 security"**; the clause is absent when the count is zero.
* Text carries the meaning everywhere. The shield is decorative (`aria-hidden`), the rule is
  decorative, and the word is what a screen reader announces.

**The count covers everything open, not the filtered view.** FR‑2.1 AC3 already applies that
principle to counts — *"the filter says what is being looked at, never what is out there"* — and it
applies here with more force: a visitor filtered to one repository still needs to know that another
has an open vulnerability.

## 8. Out of scope

* A security count on the Sites tiles.
* Excluding bots from the Contributors page, whose job is listing people.
* A `security` search keyword beside `issue` and `pr` (FR‑12.1).
* Any GitHub security API (§6).

## 9. Requirements

**FR‑1.13** (new, E‑1): *As the user I cannot miss a security item or a dependency update.* AC1 An
item citing a CVE or GHSA identifier in its title or body, or labelled `security` in any case, is
marked Security; otherwise an item authored by Dependabot or Renovate, or labelled `dependencies` in
any case, is marked Dependency; nothing else is marked. AC2 A Security item carries a chip reading
"Security" and a rule down its left edge, a Dependency item a chip reading "Dependency", on the list
and on the search results alike; the chip's title names the evidence; colour is never the only
signal. AC3 A Security item is never marked quiet (FR‑1.10 AC4). AC4 The list states how many
Security items are open, over every open item whatever the filter, and says nothing when there are
none. AC5 No request beyond those already made is sent to GitHub (QS‑3.5).

**ADR‑0014** records why the tiers are drawn from evidence already fetched rather than from
GitHub's security APIs, and why a bot is not by itself a security item.

## 10. Testing

* **Adapter.** Advisory extraction finds a CVE and a GHSA placed past byte 300; deduplicates;
  keeps first-seen order; caps at three; normalises case; finds nothing in an ordinary body. A new
  fixture repository `org/deps` carries a Dependabot security PR, a routine Dependabot bump and a
  `copilot-swe-agent` PR, and a fetch through the real GraphQL decode path populates `Advisories`
  for the first only. It is a new repository rather than new nodes in `org/repo`, so that no
  existing count moves.
* **Domain.** A table over `Tier()`: advisory → Security; `Security` label → Security; Dependabot
  with no advisory → Dependency; `renovate` → Dependency; `Dependencies` label → Dependency;
  Dependabot *with* an advisory → Security; `copilot-swe-agent` → None; `github-actions` → None; a
  person's issue citing a CVE → Security. `ShowsQuiet` never marks an old Security item quiet and
  otherwise agrees with `IsQuiet`. `Dashboard.Security` counts over every item, not the filtered set.
* **Web.** The chip, the row class and the evidence title appear on `/`, on `GET /items` and on
  `/search`; an old Security item carries no quiet mark on `/` or on `/search`; the header counts
  Security items and omits the clause at zero; the count ignores a filter.
* Every existing budget, contrast and route test stays green unmodified.
