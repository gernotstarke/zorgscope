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
* B: Two tiers drawn from evidence the existing queries already fetch — a cited CVE or GHSA
  identifier, or a `security` label, for Security; Dependabot, Renovate, or a `dependencies`
  label, for Dependency.
* C: Ask GitHub's Dependabot-alerts, code-scanning or secret-scanning APIs.

## Decision outcome

Chosen: **B — two tiers from evidence already fetched**. A is ruled out by `copilot-swe-agent`
and `github-actions`: both open pull requests on these repositories, and a rule keyed on
authorship alone would paint both red for reasons that have nothing to do with security. C was
already closed: on 2026‑09‑17 Gernot rejected surfacing GitHub's security alerts, and nothing
about the case for that has changed — QS‑3.5 budgets twenty GraphQL queries per page view and
the configuration spends exactly twenty, so alerts, code-scanning and secret-scanning would each
be a new request the budget has no room for, and each needs a `security_events` token scope the
deployment does not carry.

So the rule lives entirely in the domain, as a pure `Item.Tier()`. An item is **Security** when
it cites an advisory — a CVE or GHSA identifier in its title or body — or carries a `security`
label. Failing that, it is **Dependency** when it is authored by `dependabot` or `renovate`, or
carries a `dependencies` label. Everything else, including `copilot-swe-agent` and
`github-actions`, gets neither mark. The adapter's part is limited to reporting a fact only it
can see: before `summarise` cuts the body down, `toItem` scans the title and the whole body for
`CVE-\d{4}-\d{4,}` and `GHSA(-[0-9a-z]{4}){3}` and stores what it finds as `Item.Advisories`. It
makes no judgement about tiers — that stays in the domain, where `Dashboard.Security` counts
Security items over every open item regardless of the filter, and `Item.ShowsQuiet` refuses to
mark a Security item quiet no matter its age (FR‑1.10 AC4).

Splitting the highlight into two tiers rather than one is deliberate, and for the same reason
the seen mark was retired ([ADR‑0012](0012-no-seen-mark.md)): a mark that is always on stops
being read. On these repositories most Dependabot pull requests do cite an advisory, so most
would be red under a one-tier rule too — but a routine version bump that cites nothing would be
red as well, and within a fortnight red would mean nothing. Keeping a bump that cites no
advisory to a quiet Dependency mark is what keeps the red one meaningful.

### Consequences

* Good: no new request, no new token scope, no share of a budget that has none to spare — the
  whole rule is one pure function in the domain.
* Good: the red stays meaningful, because a routine bump is only a quiet Dependency mark — the
  lesson of the retired `NEW` badge (ADR‑0012).
* Bad: a vulnerability that Dependabot fixes in a pull request citing no identifier, and that
  nobody labels, is drawn as Dependency, not Security.
* Bad: a routine bump whose quoted release notes happen to cite an unrelated advisory is drawn
  as Security. On these repositories that errs towards visible, which is the safer mistake.
* Neutral: Dependabot's pull requests are short-lived and none is open at the time of writing,
  so the highlight is usually dormant — until the next run makes it fire.

## Pros and cons of the options

### A: Highlight every bot-authored item

* Good: the simplest possible rule — no parsing, no labels, one authorship check.
* Bad: `copilot-swe-agent` and `github-actions` open pull requests too, and would be painted red
  for work that has nothing to do with security or dependencies.

### B: Two tiers from evidence already fetched

* Good: see Decision outcome and Consequences above.
* Bad: a Dependabot fix that cites no identifier and carries no label is under-classified as
  Dependency rather than Security (see Consequences).

### C: Ask GitHub's security APIs

* Good: an authoritative answer — a real Dependabot alert or a real code-scanning finding,
  rather than a guess from title and body text.
* Bad: each of Dependabot-alerts, code-scanning and secret-scanning is a separate API and a new
  request, in a budget QS‑3.5 already spends exactly to its limit.
* Bad: each needs a `security_events` token scope the deployment does not have.
* Bad: reopens a question Gernot already closed on 2026‑09‑17, for reasons unchanged since.
