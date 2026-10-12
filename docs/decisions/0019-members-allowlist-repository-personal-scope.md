# 0019. Members: an allowlist by a second repository, a personal scope in an encrypted cookie, fetches on the member's own token

* Status: accepted (implemented in 2.5.0)
* Date: 2026-10-12
* Requirements: FR‑1.17, FR‑8.3, FR‑8.4, FR‑8.5, QS‑3.5, QS‑4.3
* Builds on: [ADR‑0009](0009-github-sign-in-push-access.md), [ADR‑0010](0010-stateless-no-database.md), [ADR‑0017](0017-a-token-per-repository-owner.md), [ADR‑0018](0018-fixed-radars-over-families.md)

## Context and problem statement

People from the iSAQB saw zorgscope's radar and asked for the same over their own repositories. The
product was built for one person (ADR‑0009, ADR‑0010, PRODUCT.md): admission is push access on one
repository, the session cookie carries nothing but an expiry, the one snapshot is the configured
nineteen repositories fetched with the owner's tokens. The question: how does a handful of invited
people get a personal list, tiles and radar on this deployment, without a database, without
touching the owner's view, and without their usage degrading it?

## Considered options

* Where it runs: **one deployment** admitting a second audience; or a second Fly app and OAuth App.
* Who is admitted: **write access on a second public repository**, managed on GitHub's
  collaborator page; or organisation membership (needs `read:org`); or an allowlist in the
  configuration file; or a CRUD page over a database.
* Whose token fetches: **the member's own unscoped OAuth token**, kept in the cookie; or the
  owner's `GITHUB_TOKEN` for everyone; or a pasted personal access token per member.
* Where the selection lives: **the cookie**; or a Fly Volume with SQLite.

## Decision outcome

Chosen: the bold options above (spec `docs/superpowers/specs/2026-10-12-members-design.md`,
D1–D12). The admission rule reuses ADR‑0009's mechanism against a second repository, so managing
members is GitHub's collaborator page and nothing to deploy. The session cookie moves from signed
to encrypted (AES‑GCM under a key derived from the client secret, as before) and carries role,
login and — for a member — their token and selection, so the server still remembers nothing
(ADR‑0010). Every page renders from a scope resolved from the session; a member's scope is a
`snapshot.Cache` of their own, fetched with their own token over at most twelve repositories, so
their usage spends their rate limit and never the owner's.

### Consequences

* Good: nothing new to operate — one app, one OAuth App, no scope, no secret, no database.
* Good: the owner's pages run the same code on the same values as before; members cannot make
  them stale or failing.
* Good: adding or removing a member is a GitHub setting; rotating the client secret still ends
  every session at once; a member signing out revokes their token, and they can revoke zorgscope
  under "Authorized OAuth Apps".
* Bad: a stolen member cookie leaks a 7‑day session and an unscoped token that reads public data
  and identifies its owner — more than the expiry-only cookie of ADR‑0012 leaked. The cookie is
  confidential to anyone without the key, and the shorter member session bounds the window.
* Bad: a selection lives in one browser; a new device or an expired session means choosing again.
  Accepted for twelve checkboxes rather than reopening ADR‑0010.
* Bad: public repositories only. Private ones would need the `repo` scope, which grants write
  access to everything the member owns — too much for a triage page.
* Bad: the members repository is public, so the list of people who may use zorgscope is public.
* Neutral: twelve is the cap because the stylesheet has twelve plain hues; sectors are colours.
* Neutral: a member's sites form a family of their own that none of ADR‑0018's fixed radars
  names, so a member gets one radar with no switch; a member's fetch never carries ADR‑0017's
  per-owner tokens.
* Neutral: usage is visible in the log — sign-in, page view, fetch, sign-out with login and role —
  and nowhere else. A `/metrics` endpoint for Fly's Grafana is the follow-up if counting is wanted.

## Pros and cons of the options

### A second deployment

* Good: separate budget, branding and admission; the single-owner product stays exactly as is.
* Bad: a second Fly app, a second OAuth App, a second set of secrets, for a feature that is the
  same new subsystem either way.

### Organisation membership, a config allowlist, a CRUD page

* Good (membership): self-maintaining. Bad: `read:org` asks the member for more than identity.
* Good (config): no new mechanism. Bad: an edit and a deploy per person; the names in git.
* Good (CRUD page): the asked-for experience. Bad: a users table, which is a database, which
  ADR‑0010 removed for good reasons.

### The owner's token for everyone, or a pasted token

* Bad (owner's token): ten people refreshing twelve repositories each can exhaust the owner's
  five thousand requests an hour and make his own page stale.
* Bad (pasted token): the experience ADR‑0009 removed; the reason people would stop using it.

### A Fly Volume for the selection

* Good: a selection that follows a member across devices.
* Bad: a volume, migrations, a backup story and a machine pinned to one host, for twelve checkboxes.
