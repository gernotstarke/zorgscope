# 0009. GitHub sign-in gated on push access to the repository

* Status: accepted
* Date: 2026-09-14
* Requirements: FR‑8.2, FR‑8.3, QS‑4.2, QS‑4.3

## Context and problem statement

[ADR‑0007](0007-token-sign-in-derived-cookie.md) put a single shared secret, `ZORGSCOPE_TOKEN`, in
front of the dashboard. It works, and the property it was chosen for — a session cookie whose signing
key is derived from the credential, so that rotating the credential signs every browser out — has
held up. Two things about it have not. It is a 32-character random string that has to be typed or
pasted into whatever device is at hand, which in practice means a phone; and it names nobody, so
"who may see this page" is a fact about a string rather than a fact about a person, and admitting a
second person means giving them the same string.

Meanwhile the answer already exists somewhere else and is already maintained: GitHub knows who may
push to `gernotstarke/zorgscope`, and that set of people is exactly the set that should see the
dashboard. The question this record answers is whether zorgscope should keep deciding admission
itself, or ask GitHub the question it is already answering.

## Considered options

* A GitHub OAuth App requesting no scopes, one request for the visitor's own permission on the
  configured repository, and a session key derived from the App's client secret.
* Keeping the shared token of ADR‑0007.
* A GitHub App installed on the repository, admitting whoever the installation covers.

## Decision outcome

Chosen: **the GitHub OAuth App with no scopes**, because the admission rule then lives where it is
already maintained — the repository's collaborator list — rather than in a secret that has to be
distributed and rotated by hand; because the derivation property ADR‑0007 was chosen for survives
unchanged, with `GITHUB_OAUTH_CLIENT_SECRET` taking the place of `ZORGSCOPE_TOKEN` in the key; and
because the only new moving part is one HTTP request.

`GET /auth/github` sets a short-lived state cookie and redirects to GitHub's authorization endpoint
with the client id, the state and no scopes at all — zorgscope is a public repository, and reading
the authenticated user's own permission on a public repository needs none. `GET /auth/callback`
compares the state in constant time, exchanges the code for an access token, and makes exactly one
request, `GET /repos/{auth_repo}`, whose `permissions` object says what the visitor may do. `push`
or `admin` admits them; `pull` alone, a missing `permissions` object and any non-200 response all
refuse, with a page that says the dashboard is for collaborators of the repository and with no
session set. The visitor's token is used for that one request and then dropped: it is never stored,
never logged and never rendered (QS‑4.3). Refused callbacks, bad states and failed exchanges count
against the same in-memory rate limiter that failed token sign-ins counted against (QS‑4.2), and are
logged without the code, the state or the token. The session cookie keeps the shape ADR‑0007 gave
it, with `key = SHA256("zorgscope-session-v2" + GITHUB_OAUTH_CLIENT_SECRET)`; the mechanism is
described in [security and token handling](../concepts/security-and-tokens.md).

### Consequences

* Good: there is no secret to paste. Signing in on a phone is two taps on a GitHub page the user is
  already logged into, rather than a 32-character string carried across from a password manager.
* Good: more than one person can sign in without sharing anything. Adding a collaborator on GitHub
  adds them to the dashboard; the dashboard itself does not have to know about it.
* Good: revocation is a GitHub setting. Removing push access removes access to zorgscope at the next
  sign-in, and rotating the client secret removes every existing session immediately — two
  mechanisms rather than one, and neither of them a code change.
* Bad: two OAuth Apps have to be registered and kept straight, because a GitHub OAuth App has
  exactly one callback URL and local and production do not share one. A local `.env` holding the
  production App's values sends a local sign-in to the production callback, which is why the
  environment template names which App each value comes from and the configuration concept lists
  both Apps side by side.
* Bad: signing in now depends on GitHub being reachable. An existing session is unaffected — it is
  verified against a derived key and touches nothing upstream — but a visitor without one cannot get
  in while GitHub is down. That is a thirty-day window of tolerance in practice, and the same
  outage already stops the data from being worth looking at.
* Neutral: the session still carries no identity. The cookie is a signature over an expiry and
  nothing else, because the product has no per-user state; a stolen cookie still leaks a thirty-day
  session and nothing else, exactly as under ADR‑0007.

One risk is worth naming, because it is the one thing here that rests on documented behaviour rather
than on a property of the design: the `permissions` object on a repository response fetched with an
unscoped token. GitHub documents it for the authenticated user, and the fake sources server
reproduces it, but if a real sign-in ever came back without it the callback would refuse — it fails
closed — and log it as `no permissions block`. The fix in that case is to request the `read:org` or
`repo` scope, at the cost of asking the visitor for more than their identity. The flow is verified by
hand against the real GitHub before the production OAuth App is put to use, following the steps
that were recorded in `docs/superpowers/plans/HANDOVER.md` (removed in the stateless reset, see git
history); see the 2026-09-14 sign-in and focus design, §8 (also removed in the stateless reset, see
git history).

## Pros and cons of the options

### A GitHub OAuth App requesting no scopes

* Good: see Decision outcome above.
* Bad: see Consequences above.

### Keeping the shared token of ADR‑0007

* Good: it is already built, already tested, and has no upstream dependency at sign-in at all — the
  simplest thing that could possibly work, and it did work for a month.
* Bad: it answers "who may see this page" with a string rather than a person, so it cannot admit a
  second collaborator without being shared, and it cannot un-admit one without being rotated for
  everybody. It also has to be transported to every device by hand, which is the friction that
  prompted this record in the first place.

### A GitHub App installed on the repository

* Good: the modern GitHub integration model — fine-grained permissions, an installation token
  scoped to the repository, and a clean answer for a future in which zorgscope reads private
  repositories it does not own.
* Bad: it solves a problem zorgscope does not have. A GitHub App is built for acting *as an app* on
  a repository; identifying a visitor still goes through the same OAuth user flow, so the App's
  extra machinery — an installation, a private key, JWT signing, installation tokens to refresh —
  is weight without a corresponding gain. The unscoped OAuth App asks the visitor for less and
  leaves the backend with one fewer credential to store and rotate.
