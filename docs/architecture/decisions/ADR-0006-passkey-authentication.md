# ADR-0006: Passkey‑only single‑user authentication with enrolment token

* Status: accepted
* Date: 2026-08-16
* Deciders: Gernot Starke
* Related: FR-9.x, QS-3.x, C-5, R-4, AR-3

## Context and problem statement

The hosted dashboard shows private data (Todoist, private repo). The owner asked for password‑level
protection at least, ideally 2FA or passkeys, with minimal friction on a page opened dozens of times a day.

## Considered options

1. Passkeys (WebAuthn, discoverable credentials) only; first enrolment gated by a one‑time token; long‑lived session cookie.
2. Password (bcrypt/argon2) + TOTP.
3. Tailscale‑only access (no login page).
4. OAuth via GitHub / Cloudflare Access.

## Decision outcome

**Chosen option: 1** using `github.com/go-webauthn/webauthn`. Enrolment: `GET /enroll?token=<ENROLL_TOKEN>`
(constant‑time compare, rate limited, 404 on mismatch) or from an authenticated session (add device).
Credentials in SQLite; sessions 90 d sliding, revocable on `/account`. RP ID = host of `server.base_url`.
Local development uses `AUTH_MODE=dev` (only allowed for `http://localhost*`). Recovery: rotate `ENROLL_TOKEN`
plus `make fly ARGS="ssh console -C '/zorgscope reset-credentials'"` (documented).

### Consequences

* Good: phishing‑resistant, no password to store/leak, effectively MFA (device + biometric), one touch to log
  in, syncs across Apple devices via iCloud Keychain, works in Arc/Vivaldi/Firefox/Safari.
* Bad: a little JS is unavoidable (`navigator.credentials`); custom‑domain change forces re‑enrolment;
  requires HTTPS (fly provides). Options 2–4 rejected: weaker or heavier, or add an external dependency.
