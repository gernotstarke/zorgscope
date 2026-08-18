# 0007. Token sign-in with a session cookie derived from the token

* Status: accepted
* Date: 2026-08-17
* Requirements: FR‑8.2, FR‑8.3, QS‑4.1, QS‑4.2, QS‑4.3

## Context and problem statement

The dashboard has exactly one legitimate user (S‑1), who already holds every credential the system
uses — GitHub token, Turso auth token, and now a sign-in credential of their own. The previous
design used passkey authentication, cut in the reset along with "credential and TLS expiry
watching" (design §1) and named explicitly in the requirements' "Explicitly out of scope" list. The
question is what a browser session should rest on, given that whatever it is must satisfy FR‑8.3
AC1's "redirected to a sign-in page, not answered with a bare 401", and must never be usable to
authorise `POST /api/refresh` (that is `REFRESH_SECRET`'s job, kept independent — see
[security-and-tokens.md](../concepts/security-and-tokens.md)).

**A note on evidence.** `internal/web/auth.go` does not exist at the time of writing — it is Task
12 of the [implementation plan](../superpowers/plans/2026-08-17-zorgscope-v1.md), not yet executed.
This record documents the decision as specified by the design (§6) and by Task 12's interface and
implementation notes; every mechanism named below is a commitment the plan makes, not a behaviour
observed in running code. Re-check this record's claims against `internal/web/auth.go` and
`internal/web/auth_test.go` once Task 12 lands.

## Considered options

* A shared-secret token (`ZORGSCOPE_TOKEN`) exchanged once at `/login` for a signed, HttpOnly
  session cookie whose signing key is derived from the token itself.
* Passkeys (WebAuthn) — the previous design's choice.
* HTTP Basic Authentication over TLS, with no separate cookie.

## Decision outcome

Chosen: **token sign-in with a derived session cookie**, because it gives FR‑8.3 AC3 — "the
cookie's signing key derives from the token, so changing the token invalidates every session" — as
a structural property of the derivation, not as a feature that has to be separately implemented and
kept correct.

Per the design (§6) and Task 12's implementation notes, posting `ZORGSCOPE_TOKEN` to `/login` is to
set the cookie value to `base64(expiry) + "." + base64(HMAC-SHA256(key, expiry))`, where
`key = SHA256("zorgscope-session-v1" + ZORGSCOPE_TOKEN)`. Verifying a request means recomputing the
HMAC from the current `ZORGSCOPE_TOKEN` and comparing with `subtle.ConstantTimeCompare` (QS‑4.2);
the token itself is never in the cookie, so a leaked cookie does not leak the sign-in credential
(FR‑8.3 AC2: "the token itself is never stored in the browser"). Failed sign-ins are rate-limited —
an in-memory token bucket keyed by client IP, 10 failures per 15 minutes, deliberately not persisted
to Turso "since the machine stops when idle, so a persistent counter would add a database write to
every failed attempt for no security gain against an attacker who can simply wait" (Task 12). `POST
/api/refresh` is planned to be wrapped by a separate `requireBearer` middleware comparing against
`REFRESH_SECRET`, and is planned never to accept the session cookie as an alternative credential —
the two credentials stay independent by construction, which is the substance of
[security-and-tokens.md](../concepts/security-and-tokens.md).

### Consequences

* Good: rotating one environment variable (`ZORGSCOPE_TOKEN`) invalidates every outstanding session
  everywhere, with no session table to clear and no revocation list to consult — the derivation
  itself is the revocation mechanism.
* Good: no credential storage beyond the two environment variables already required — no user table,
  no passkey credential store, no relying-party configuration to keep correct across a domain
  change.
* Bad: it is a single shared secret for a single named user. It does not scale to more than one
  person having their own identity — there is no username, only "the" token — which is an accepted
  limitation given S‑1 is the only stakeholder with requirements of their own, not a property a
  second user could work around later without a design change.
* Neutral: the sign-in credential and the session credential are deliberately different artefacts
  (a bearer token exchanged once; a derived, time-limited cookie used per request) so that the
  higher-value secret (`ZORGSCOPE_TOKEN`) is transmitted only at `/login`, not on every request the
  way HTTP Basic Auth would send it.

## Pros and cons of the options

### Token sign-in with a derived session cookie

* Good: see Decision outcome above.
* Bad: see Consequences above.

### Passkeys (WebAuthn)

* Good: no shared secret to leak in the first place — a passkey's private key never leaves the
  authenticator — and it is the strongest option against phishing of the three.
* Bad: explicitly cut in the reset alongside "credential and TLS expiry watching" (design §1); it
  needs a WebAuthn library (a fifth external dependency the design's §4 does not budget for), a
  relying-party origin to configure and keep correct across any domain change, and a credential
  enrolment flow — real engineering weight for a threat model (one operator, who already controls
  every other credential in the system) that does not obviously need it. It was judged
  disproportionate for S‑1 alone, not judged insecure.

### HTTP Basic Authentication over TLS

* Good: the simplest possible option — no cookie, no derivation, no login endpoint; the browser
  handles credential storage and re-submission itself.
* Bad: sends the credential on every request rather than once at sign-in, which is a larger exposure
  window across any intermediary that logs headers than a short-lived derived cookie sent instead;
  and it cannot satisfy FR‑8.3 AC1's "redirected to a sign-in page, not answered with a bare 401" —
  Basic Auth's browser-native challenge is a modal dialog, not a page the design controls. It is
  close in mechanism simplicity to the chosen option but loses on both the exposure surface and the
  explicit UX requirement.
