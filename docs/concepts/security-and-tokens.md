# Security and token handling

Two independent credentials, a derived session cookie, and what never reaches a log. See
[ADR‑0007](../decisions/0007-token-sign-in-derived-cookie.md) for why the design landed here rather
than on passkeys, and [ADR‑0003](../decisions/0003-fly-scale-to-zero-external-cron.md) for why a
refresh has to be triggered by an authenticated HTTP call at all.

**A note on evidence.** `internal/web/auth.go` is Task 12 of the
[implementation plan](../superpowers/plans/2026-08-17-zorgscope-v1.md) and does not exist at the
time of writing. Everything below describes what the design (§6 and §7) and Task 12 commit to
building, not behaviour observed in running code. Re-check this page against `internal/web/auth.go`
and `internal/web/auth_test.go` once Task 12 lands.

## Two independent credentials

zorgscope has exactly two secrets that authenticate a request, and they are deliberately unrelated:

| Credential | Environment variable | Authorises | Never authorises |
|------------|----------------------|------------|-------------------|
| Sign-in token | `ZORGSCOPE_TOKEN` | Signing in at `/login`, which issues a session cookie | `POST /api/refresh` |
| Refresh secret | `REFRESH_SECRET` | `POST /api/refresh` as a bearer token | Reading any page or tile |

The reason is stated directly in the design (§6): "the two credentials are independent so the cron
service holds only the ability to trigger a refresh." cron-job.org is an external, third-party
service that this system trusts with one narrow capability. If `REFRESH_SECRET` alone could also
read the dashboard, a compromised or misconfigured cron job would expose Gernot's private
repositories, task list and site statistics — not just trigger a fetch. Symmetrically, a session
cookie minted for the browser must never be accepted by `/api/refresh`: a stolen cookie should not
be able to force refreshes at will (a route to hammering GitHub's rate limit, QS‑3.5) any more than
a stolen bearer secret should be able to open the dashboard.

`deploy/env.example` documents both, with the independence spelled out in the second one's comment:

```env
# Sign-in token for the browser session (FR-8.3). At least 32 characters.
ZORGSCOPE_TOKEN=
# Bearer secret cron-job.org sends to POST /api/refresh (FR-5.1). At least 32 characters.
# Deliberately separate from ZORGSCOPE_TOKEN: the cron service can trigger a refresh and nothing else.
REFRESH_SECRET=
```

`internal/config/config.go`'s `Load` enforces a minimum length of 32 characters on both — a
configuration that supplies a shorter value fails at start-up rather than accepting a guessable
secret.

## How the session cookie is derived from the token

Per the design (§6) and Task 12's plan, signing in does not create a server-side session record.
Instead, the cookie's value is planned to be:

```text
base64(expiry) + "." + base64(HMAC-SHA256(key, expiry))
where key = SHA256("zorgscope-session-v1" + ZORGSCOPE_TOKEN)
```

Verifying a request recomputes the HMAC from the *current* `ZORGSCOPE_TOKEN` and compares it against
the cookie's signature. This gives two properties for free, without a database:

* **Stateless verification.** No session table, no lookup — the server needs nothing but the
  current token to check a cookie's validity, which fits a machine that may be stopped and started
  between any two requests (ADR‑0003).
* **Rotation invalidates every session (FR‑8.3 AC3).** Because `key` is derived from
  `ZORGSCOPE_TOKEN`, changing the token changes the key, and every cookie signed under the old key
  fails verification immediately — everywhere, on every device, without a sign-out action or a
  revocation list.

The cookie is planned to carry HttpOnly, Secure and SameSite=Lax attributes (FR‑8.3 AC2), and the
token itself is never placed in the cookie — only a signature over an expiry timestamp is. A leaked
cookie therefore leaks a time-limited session, not the sign-in credential itself.

## Constant-time comparison and rate limiting

QS‑4.2 requires that "comparison is constant-time, attempts are rate-limited, and the submitted
value never reaches a log." The plan for Task 12 names `subtle.ConstantTimeCompare` explicitly for
both the bearer check on `/api/refresh` and the cookie signature check, so that a failed comparison
takes the same time regardless of how many leading bytes matched — an ordinary `==` on secret bytes
does not have this property and is exactly the kind of timing side-channel `subtle` exists to close.

Failed sign-ins are planned to be rate-limited by an in-memory token bucket keyed by client IP, 10
failures per 15 minutes. It is deliberately in-memory rather than a Turso-backed table: the machine
stops when idle (ADR‑0003), so a persistent counter would add a database write to every failed
attempt for no real security benefit against an attacker who can simply wait out a machine restart
to reset an in-memory bucket anyway.

## What is never logged (QS‑4.3)

No submitted token value, no derived session key, no webhook URL, no upstream API token may appear
in a log line, an HTTP response body, an error page, or the rendered configuration page. Two pieces
of this are already true in code today, ahead of Task 12:

* `internal/config/config.go`'s `Load` wraps every validation error with the *name* of the
  offending field (`"refresh.interval: %w"`, `"ZORGSCOPE_TOKEN must be at least 32 characters"`),
  never the value. `TestErrorNeverContainsSecretValues` in `internal/config/config_test.go` asserts
  this directly: it configures a canary secret value and checks that no returned error string
  contains it.
* `internal/adapters/libsql/store.go`'s `Open` and `dsn` functions are documented never to return an
  error containing the DSN, "since the auth token lives in it" — errors from that package name the
  host at most.

`check-env` in the `Makefile` follows the same discipline operationally: it tests only whether a
value is *present* in `.env` ("never what it is") and echoes no values, so even a local development
failure message cannot leak a secret to a terminal transcript.

Task 12's plan adds the HTTP-level version of this guarantee, `TestNoResponseEverContainsASecret`: a
canary test that configures every secret with a recognisable value, exercises every route including
error paths, and searches the output for them.

## Security headers (QS‑4.4)

Per the design and Task 12's plan, one middleware wraps every response with:

* `Content-Security-Policy: default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'`
  — no `unsafe-inline`, because htmx and the stylesheet are vendored files, not inline script or
  style.
* `X-Content-Type-Options: nosniff`
* `Referrer-Policy: strict-origin-when-cross-origin`
* `Strict-Transport-Security`, on top of the HTTPS Fly already enforces at the edge (`force_https`
  in `deploy/fly.toml`).

## Rotating each secret

Both secrets are Fly secrets in production and `.env` entries locally — never repository content
(C‑9). Rotate with `openssl rand -base64 32` for a new value; the examples below use
`<new-value>` as a placeholder, never a real secret.

**Rotating `ZORGSCOPE_TOKEN`:**

```sh
openssl rand -base64 32
make fly ARGS="secrets set ZORGSCOPE_TOKEN=<new-value>"
```

This signs every existing browser session out immediately — that is the point of the derivation
above, not a side effect to work around. Sign in again with the new value at `/login`.

**Rotating `REFRESH_SECRET`:**

```sh
openssl rand -base64 32
make fly ARGS="secrets set REFRESH_SECRET=<new-value>"
```

Update the `Authorization: Bearer <new-value>` header in the cron-job.org job configuration to
match — until you do, every scheduled refresh fails with 401 (FR‑5.1 AC2) and the dashboard's data
goes stale without failing loudly, since a stale-but-present tile is exactly what FR‑1.4 AC3
guarantees rather than a blank page.

`make fly-secrets` lists secret *names* and their deployment status only, never values — it exists
so rotation can be verified without ever displaying what was set. `make fly-secrets-import` reads
`NAME=VALUE` pairs from stdin rather than command-line arguments, so a secret value never appears in
shell history or a process listing while it is being set.
