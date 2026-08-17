# ADR-0006: Passkey‑only single‑user authentication with enrolment token

* Status: accepted
* Date: 2026-08-16
* Deciders: Gernot Starke
* Related: FR-9.x, QS-3.x, C-5, R-4, AR-3

## Context and problem statement

The hosted dashboard shows private repository and provider data. The owner asked for password-level
protection at least, ideally 2FA or passkeys, with minimal friction on a page opened dozens of times a day.

## Considered options

1. Passkeys (WebAuthn, discoverable credentials) only; first enrolment gated by a one‑time token; long‑lived session cookie.
2. Password (bcrypt/argon2) + TOTP.
3. Tailscale‑only access (no login page).
4. OAuth via GitHub / Cloudflare Access.

## Decision outcome

**Chosen target: passkey-backed device authentication.** ADR-0014 introduced a bootstrap bearer token so
the backend is deployable before this target lands. The final design must support a Wails system-browser
authorisation flow with PKCE plus a same-origin browser session; concrete enrolment/account routes,
credential storage and lifetimes remain unimplemented and must be decided with FR-9.2/9.3. Local
development uses `AUTH_MODE=dev` only with a localhost base URL.

### Consequences

* Good: phishing-resistant, no reusable password, and both native and browser clients can share one trust model.
* Bad: device authorisation and recovery require a separate design; a custom-domain change forces
  re-enrolment; HTTPS is mandatory. Bootstrap bearer auth remains until this ADR is implemented.
