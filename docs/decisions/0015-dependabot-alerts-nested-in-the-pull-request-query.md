# 0015. Dependabot alerts, nested in the pull request query

* Status: accepted (implemented in 2.1.0)
* Date: 2026-09-28
* Supersedes: [ADR‑0014](0014-security-from-evidence-already-fetched.md), option E, for Dependabot
  alerts only — code scanning and secret scanning stay ruled out
* Requirements: FR‑1.16 (new), FR‑1.13, FR‑1.14, FR‑1.15, FR‑1.8, QS‑3.5, QS‑1.4, QS‑4.3

## Context and problem statement

ADR‑0014 marks items as Security or Dependency from evidence the issue and pull request queries
already fetch, and declined to ask GitHub for security data. That rests on an assumption the data
no longer supports: that a vulnerability shows up on the list as a Dependabot pull request.
Measured on 2026‑09‑28 against the ten configured repositories:

| Repository | Alerts enabled | Open alerts |
|---|---|---|
| `quality.arc42.org-site`, `docs.arc42.org-site`, `faq.arc42.org-site`, `examples.arc42.org-site` | yes | one each, **HIGH** — `rubyzip` 2.3.2, path traversal, GHSA‑47m2‑wp7j‑p9vc, patched in 3.4.0 |
| `arc42.org-site`, `arc42.de-site` | yes | one each, LOW — `json` 2.20.0, GHSA‑9hj4‑r449‑hfvc, patched in 2.21.2, open since 2026‑08‑08/09 |
| `arc42-generator` | yes | none |
| `arc42-template`, `trainings.arc42.org-site`, `zorgscope` | **no** | — |

None of the six alerts has a Dependabot pull request, and Dependabot reports no error for any of
them, so no fix pull request is coming. zorgscope's Security tile says "All clear" while four
HIGH vulnerabilities are open. ADR‑0014 does not list this among its consequences. Gernot asked on
2026‑09‑28 for Dependabot alerts on the list, the tiles and the radar, drawn distinct from the
existing marks, and counted as needing him.

The question: how does zorgscope read Dependabot alerts, and what happens when it may not?

## Considered options

* A: Keep ADR‑0014. No alerts.
* B: One GraphQL query per repository for alerts, beside the two already made (30 requests).
* C: The REST endpoint `GET /orgs/{org}/dependabot/alerts`, one extra request for the arc42
  organisation and one for `gernotstarke/zorgscope`.
* D: Nest `vulnerabilityAlerts` and `hasVulnerabilityAlertsEnabled` into the first page of the pull
  request query already made, falling back to that page without them when GitHub refuses.

## Decision outcome

Chosen: **D — nested in the pull request query**, as Gernot decided on 2026‑09‑28, reversing
ADR‑0014's option E for Dependabot alerts only.

* **Where.** `repository(owner, name)` gains `hasVulnerabilityAlertsEnabled` and
  `vulnerabilityAlerts(states: OPEN, first: 100)` on the **first** page of the pull request query
  only; later pages use the query as it is today, so a repository with more than 100 open pull
  requests does not fetch its alerts twice. A connection nested in a node raises GitHub's point cost
  without raising the request count (QS‑3.5's own reading), so the representative configuration
  still makes exactly 20 requests.
* **When GitHub refuses.** Asking for alerts must never cost a repository its pull requests
  (QS‑1.4). GitHub refuses in two shapes: a missing token scope fails the *whole* query with no
  data; missing repository access fails only the field and returns the rest. Either way — and
  without telling the two apart — the adapter asks for the same first page once more without
  alerts. If that is answered, it records the repository's alert coverage as *unavailable*, and
  the process remembers that the token was refused, so later fetches stop asking until the process
  restarts; if it fails too, it is the ordinary failure of that repository. A
  refused token therefore costs at most one extra request per repository, once per process
  lifetime, and on this scale-to-zero machine that means once per wake.
* **Coverage is a fact of its own.** For each repository the fetch reports *on*, *off*
  (`hasVulnerabilityAlertsEnabled` false) or *unavailable*. "No open alerts" is only claimed for a
  repository whose coverage is *on*. This is what lets the Security tile say "All clear" and have it
  be true.
* **Token.** A classic personal access token with the `security_events` scope and nothing else
  (Gernot's choice over a fine‑grained token). The issue and pull request queries on these public
  repositories need no scope, and `security_events` is what the alerts need. `public_repo` would
  also be accepted by GitHub but grants write access to code, so it is not used. The token's owner
  must be able to see the alerts: an admin of each repository, or a member of a role or security
  manager team that has been granted alert access.
* **Code scanning and secret scanning stay out**, for ADR‑0014's reason: both are REST‑only, so
  there is nothing to nest into, and each would be a new request in a budget spent to its limit.

The design is `docs/superpowers/specs/2026-09-28-dependabot-alerts-design.md`. It covers the new
item kind, the tier and need it gets, and how it is drawn.

### Consequences

* Good: zorgscope reports what GitHub itself says is vulnerable, not only what a pull request
  happens to cite. The four HIGH `rubyzip` alerts appear on the day they are raised.
* Good: QS‑3.5 still holds at 20 requests when the token is right. A wrong token degrades to
  "alerts unavailable" and one extra request per repository per process, and never loses a pull
  request.
* Good: "All clear" becomes a statement about coverage, not just about the absence of a pull
  request. Three repositories with alerts turned off are named, not silently counted as clean.
* Bad: `GITHUB_TOKEN` must carry a scope for the first time. A token that reads security data is
  more sensitive than one that reads public issues: leaked, it tells its holder which arc42 sites
  have unpatched vulnerabilities. It must be rotated on Fly before this ships, and
  `docs/concepts/security-and-tokens.md` must say what it can now read.
* Bad: the token must belong to someone who can see the alerts. If that person loses admin rights
  on a repository, its coverage silently becomes *unavailable*. It is drawn, but nothing forces
  anyone to notice.
* Bad: `shurcooL/graphql` passes on only an error's message, not its type, so "refused" cannot be
  told from any other error on the first page. The retry without alerts is what tells them apart:
  it succeeds only when the alerts were the problem. A transient failure that hits exactly the
  first request and not the retry would switch alerts off until the next restart — on this
  scale-to-zero machine, the next wake.
* Bad: the point cost of the pull request query rises by up to 100 alert nodes per repository.
  That is well inside GitHub's 5,000 points an hour at the page views this deployment sees, but it
  is no longer free.
* Neutral: `ports.Source.Fetch` now returns coverage beside the items, so the snapshot, its partial
  merge and every fake source changed shape once.
* Neutral: dismissing or fixing an alert stays on GitHub. zorgscope reads, it never writes
  (unchanged).

## Pros and cons of the options

### A: Keep ADR‑0014

* Good: no scope on the token, no change.
* Bad: four HIGH alerts are open today and invisible. The Security tile says "All clear" about
  them.

### B: One query per repository for alerts

* Good: an alerts failure is isolated in its own request by construction.
* Bad: 30 requests per page view against QS‑3.5's 20, and nothing gained over D except that
  isolation, which D gets by falling back.

### C: The organisation REST endpoint

* Good: two requests for everything, not ten nested connections.
* Bad: two new requests over a budget with no headroom. It needs organisation‑level permission,
  and a second client beside the GraphQL one (REST pagination, REST error shapes). It still leaves
  `hasVulnerabilityAlertsEnabled` to be asked per repository.

### D: Nested in the pull request query, with a fallback

* Good: see Decision outcome and Consequences.
* Bad: the fallback logic in the adapter is the most delicate code this change adds, and it is
  only exercised when something is wrong — so it is what the fake server's fixtures must cover
  hardest.
