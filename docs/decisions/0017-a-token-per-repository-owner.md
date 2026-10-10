# 0017. A token per repository owner

* Status: accepted (implemented in 2.3.0)
* Date: 2026-10-07
* Requirements: FR‑1.8, FR‑1.15, FR‑1.16, FR‑8.1, QS‑3.5, QG‑4

## Context and problem statement

Gernot asked on 2026‑10‑07:

> we need to add the systems42-org organizations' repos to it. Let's summarize these in a single
> tile and radar-segment (remove zorgscope and replace by systems42).

`systems42-org` holds three repositories besides `.github`: `systems42.org-site` (public), and
`systems42` and `meta` (both **private**). Every repository watched so far was public, so
`GITHUB_TOKEN` needed no access to code at all — only `security_events`, or *Dependabot alerts: read*,
for the alerts (ADR‑0015). A private repository cannot be read that way:

* A classic token reads private repositories only through the `repo` scope, which also grants
  **write** access to every private repository its owner can reach.
* A fine‑grained token can be limited to read-only *Issues*, *Pull requests*, *Metadata* and
  *Dependabot alerts* on chosen repositories, but it has exactly **one resource owner**: a token owned
  by `systems42-org` cannot read the arc42 repositories' alerts, and the token reading those cannot
  reach `systems42-org`'s private repositories.

The question: how does one process read repositories of owners that no single least‑privilege token
covers?

## Considered options

* **A. A token per owner, beside `GITHUB_TOKEN`.** `GITHUB_TOKEN_<OWNER>` — the owner upper‑cased,
  every other character an underscore — is read for each owner of a watched repository. That owner's
  repositories are fetched with it; every other repository still with `GITHUB_TOKEN`.
* **B. One classic token with the `repo` scope.** No code change.
* **C. A GitHub App installed on each organisation.** Fine‑grained, one credential, installation
  tokens per owner.
* **D. Watch only the public `systems42.org-site`.** No token change.

## Decision outcome

Chosen: **A**, Gernot's choice on 2026‑10‑07. B was rejected because a leaked `GITHUB_TOKEN` would
then be write access to every private repository Gernot can reach, where today it reads at most
alerts. C is the same model with more machinery — a private key, JWT signing, installation tokens to
refresh — which ADR‑0009 already declined for sign‑in. D would leave the two private repositories,
which carry the work, invisible.

The fetcher holds one *lane* per distinct token: its GraphQL client, the client the REST alert probe
asks with, and its own memory of whether GitHub refused it the alert fields (ADR‑0015). One token's
refusal therefore no longer stops another token's repositories from asking. An owner whose variable
is unset is read with `GITHUB_TOKEN`, so an unset `GITHUB_TOKEN_SYSTEMS42_ORG` makes the two private
repositories fail with GitHub's "could not resolve" message while everything else still arrives
(QS‑1.4). Only owners of watched repositories are looked up.

The same change lets a site name several repositories (`repos:` instead of `repo:`), so that the
organisation is one tile and one radar sector, `systems42`, in a green of its own; and it raises
QS‑3.5 from 15 to 17 repositories (34 requests per fetch) — the three `systems42-org` repositories
replace `gernotstarke/zorgscope`, which is no longer watched but remains `github.auth_repo`.

### Consequences

* Good: each token is as narrow as its owner allows. The `systems42-org` token is read‑only and can
  see nothing outside the three repositories it is given.
* Good: `config.Secrets.OwnerTokens` is a map of strings; `Redact` scrubs every value of a map field,
  and `TestRedactCoversEveryFieldOfSecrets` fails for any secret field of another shape (QS‑4.3).
* Bad: **private issue and pull request titles are now on the dashboard.** Everyone admitted —
  anyone with push access to `gernotstarke/zorgscope` — sees them, and with the browser cache on
  (ADR‑0013) the rendered list sits in that browser's `localStorage` for up to seven days. Adding a
  collaborator to `gernotstarke/zorgscope` now grants a read of `systems42-org`'s private titles.
* Bad: one more secret to rotate. `systems42-org`, like `isaqb-org`, may restrict fine‑grained token
  lifetimes; either token expiring stops its own repositories only.
* Bad: QS‑3.5 rises to 34 requests per fetch, 12 × 34 = 408 of GitHub's 5,000 an hour (about 8 %).
  Each token also has its own hourly limit, so the per‑token share is lower still.
* Neutral: the configuration again holds exactly the ceiling: an eighteenth repository needs a budget
  decision first.
