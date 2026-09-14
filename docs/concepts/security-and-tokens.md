# Security and token handling

Two independent credentials, a session cookie derived from one of them, and what never reaches a
log. See [ADR‑0009](../decisions/0009-github-sign-in-push-access.md) for why admission to the
dashboard is GitHub's question to answer rather than this system's,
[ADR‑0007](../decisions/0007-token-sign-in-derived-cookie.md) for the shared token it superseded and
for where the derived cookie came from, and
[ADR‑0003](../decisions/0003-fly-scale-to-zero-external-cron.md) for why a refresh has to be
triggered by an authenticated HTTP call at all.

## Two independent credentials

zorgscope has exactly two secrets that authenticate a request, and they are deliberately unrelated.
A third row is in the table because it is the row a reader expects to find there, and because it is
the one that must never drift into the first two: the tokens that read GitHub authenticate nothing
here.

| Credential | Environment variable | Authorises | Never authorises |
|------------|----------------------|------------|-------------------|
| OAuth client secret | `GITHUB_OAUTH_CLIENT_SECRET` | Exchanging the code at `/auth/callback`, and — as the seed of the session key — every later request that carries the cookie | `POST /api/refresh` |
| Refresh secret | `REFRESH_SECRET` | `POST /api/refresh` as a bearer token | Reading any page or fragment |
| GitHub read tokens | `GITHUB_TOKEN`, plus the visitor's own token at sign-in | Reading GitHub: the backend's token fetches items and builds, the visitor's answers exactly one question about their permission on the repository | Anything at all in zorgscope. The visitor's token is used for one request and never stored, logged or rendered |

The reason the first two are unrelated is stated directly in the
[reset design](../superpowers/specs/2026-08-17-zorgscope-reset-design.md) (§6): "the two credentials
are independent so the cron service holds only the ability to trigger a refresh." cron-job.org is an
external, third-party service that this system trusts with one narrow capability. If `REFRESH_SECRET`
alone could also read the dashboard, a compromised or misconfigured cron job would expose Gernot's
private repositories — not just trigger a fetch. Symmetrically, a session cookie minted for the
browser must never be accepted by `/api/refresh`: a stolen cookie should not be able to force
refreshes at will (a route to hammering GitHub's rate limit, QS‑3.5) any more than a stolen bearer
secret should be able to open the dashboard.

`deploy/env.example` documents them, with the independence spelled out in the comment on the last:

```env
# The OAuth App registered for this environment (FR-8.3). Local and production have one App each,
# because a GitHub OAuth App has exactly one callback URL; take both values from the same App.
GITHUB_OAUTH_CLIENT_ID=
GITHUB_OAUTH_CLIENT_SECRET=
# Bearer secret cron-job.org sends to POST /api/refresh (FR-5.1). At least 32 characters.
# Deliberately separate from the OAuth pair: the cron service can trigger a refresh and nothing else.
REFRESH_SECRET=
```

`internal/config/config.go`'s `Load` enforces a minimum length of 32 characters on `REFRESH_SECRET`
— a configuration that supplies a shorter value fails at start-up rather than accepting a guessable
secret — and `web.New` refuses to start at all unless both OAuth values are present, exactly as it
refused to start without the sign-in token before it. Half-configured authentication is not a state
this system is willing to serve a page from.

## What the sign-in checks

Signing in is three routes, and only the third makes a decision:

```text
GET  /login              public   the sign-in page: one button, "Sign in with GitHub"
GET  /auth/github        public   sets a state cookie, redirects to GitHub's authorize URL
GET  /auth/callback      public   verifies state, exchanges the code, checks push access,
                                  sets the session cookie, redirects to /
```

`GET /auth/github` generates 32 random bytes, stores them base64-encoded in a short-lived
`zorgscope_oauth_state` cookie (HttpOnly, Secure, SameSite=Lax, ten minutes), and redirects to GitHub
with the client id, that state, and **no scopes**. zorgscope is a public repository, and reading the
authenticated user's own permission on a public repository needs no scope, so the App asks the
visitor for nothing beyond their identity. No `redirect_uri` is sent either: GitHub uses the callback
registered on the App, which is why the backend never has to know its own public URL or trust a
`Host` header.

`GET /auth/callback` compares the `state` parameter with the cookie, clears the cookie, exchanges the
code for an access token, and makes exactly one request with it — `GET {api}/repos/{auth_repo}`,
whose `permissions` object says what this visitor may do on the repository. `push` or `admin` admits
them. `pull` alone, a missing `permissions` object, and any non-200 response all refuse: the visitor
gets a page saying the dashboard is for collaborators of the repository, and no session (FR‑8.3 AC3).
It fails closed, which is the only direction an admission check may fail in.

The button on `/login` is a plain link rather than a form, and that is a security detail rather than
a styling one. `form-action 'self'` in the Content Security Policy below is enforced by the browser
on the redirect that follows a form submission, so a `POST` answering with a redirect to github.com
would be blocked; a link navigation is not subject to `form-action`.

## How the session cookie is derived from the client secret

Signing in does not create a server-side session record. The cookie's value is:

```text
base64(expiry) + "." + base64(HMAC-SHA256(key, expiry))
where key = SHA256("zorgscope-session-v2" + GITHUB_OAUTH_CLIENT_SECRET)
```

Verifying a request recomputes the HMAC from the *current* `GITHUB_OAUTH_CLIENT_SECRET` and compares
it against the cookie's signature. This gives two properties for free, without a database:

* **Stateless verification.** No session table, no lookup — the server needs nothing but the current
  client secret to check a cookie's validity, which fits a machine that may be stopped and started
  between any two requests (ADR‑0003). It also means an existing session survives a GitHub outage:
  verifying one touches nothing upstream.
* **Rotation invalidates every session (FR‑8.3 AC4).** Because `key` is derived from the client
  secret, regenerating that secret on GitHub changes the key, and every cookie signed under the old
  key fails verification immediately — everywhere, on every device, without a sign-out action or a
  revocation list. That is the property
  [ADR‑0007](../decisions/0007-token-sign-in-derived-cookie.md) was chosen for, and the one thing
  about it this design deliberately kept.

The cookie carries HttpOnly, Secure and SameSite=Lax (FR‑8.3 AC2) and holds a signature over an
expiry and nothing else — no identity, no login name, and above all not the visitor's GitHub token,
which was dropped at the end of the callback. A leaked cookie therefore leaks a thirty-day session
and nothing else.

## Constant-time comparison and rate limiting

QS‑4.2 requires that "comparison is constant-time, attempts are rate-limited, and the submitted value
never reaches a log." `subtle.ConstantTimeCompare` is used for all three secret comparisons in the
request path — the OAuth state parameter against its cookie, the session cookie's signature against
the recomputed HMAC, and the bearer on `/api/refresh` against `REFRESH_SECRET` — so that a failed
comparison takes the same time regardless of how many leading bytes matched. An ordinary `==` on
secret bytes does not have that property, and is exactly the kind of timing side-channel `subtle`
exists to close.

Refused callbacks are rate-limited by an in-memory token bucket keyed by client IP: a mismatched or
missing state, a failed code exchange, and a visitor without push access all count against the same
bucket that failed token sign-ins counted against before. It is deliberately in-memory rather than a
Turso-backed table: the machine stops when idle (ADR‑0003), so a persistent counter would add a
database write to every refusal without meaningfully raising the cost of a patient attack.

That trade-off is comfortable here, because **the rate limiter is not what stands between a stranger
and the dashboard — GitHub is.** There is no value to guess at the callback: a caller who holds no
code issued by GitHub against this App, in a browser holding the matching state cookie, is not
admitted at any rate at all. Throttling keeps the logs quiet and the machine asleep; it is not the
defence. The concrete budget and window live in `internal/web/auth.go`, where they can be read and
changed together with the code that enforces them; this page deliberately does not restate them,
because it is published unauthenticated at `/docs`.

## What the callback logs

Every callback logs one line, admitted or refused, because "a collaborator signed in" is the whole of
the audit need here and losing it silently would be worse than the line's cost. The line carries the
client IP, the outcome, and — when the visitor was admitted — the permission level that admitted
them. When they were refused it carries the reason in the system's own words: the state did not
match, the exchange failed, the repository response was not 200, the `permissions` object was
missing, or the permission was `pull` alone.

It never carries the authorization code, the state value, the visitor's access token, the client
secret, or a login name the visitor did not choose to publish. The session cookie holds no identity
either, so nothing downstream can reconstruct one from it.

## What is never logged (QS‑4.3)

No client secret, no derived session key, no authorization code, no visitor token, no webhook URL and
no upstream API token may appear in a log line, an HTTP response body, an error page, or the rendered
configuration page. Two pieces of this sit in code that has nothing to do with the web layer:

* `internal/config/config.go`'s `Load` wraps every validation error with the *name* of the offending
  field (`"refresh.interval: %w"`, `"REFRESH_SECRET must be at least 32 characters"`), never the
  value. `TestErrorNeverContainsSecretValues` in `internal/config/config_test.go` asserts this
  directly: it configures a canary secret value and checks that no returned error string contains it.
* `internal/adapters/libsql/store.go`'s `Open` and `dsn` functions are documented never to return an
  error containing the DSN, "since the auth token lives in it" — errors from that package name the
  host at most.

`TestNoResponseEverContainsASecret` in `internal/web` is the HTTP-level version of the same
guarantee: every secret is configured with a recognisable canary value, every route is exercised
including its error paths, and the output is searched for them. The OAuth client secret and a fake
visitor token are part of that canary set, so a sign-in path that leaked either into a page or a log
fails a test rather than waiting for a review to notice.

## Security headers (QS‑4.4)

One middleware wraps every response with:

* `Content-Security-Policy: default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'self'`
  — no `unsafe-inline`, because htmx and the stylesheet are vendored files, not inline script or
  style. `form-action` and `base-uri` are named explicitly because `default-src` covers neither: an
  injected `<form action="https://elsewhere">` would otherwise be a working exfiltration route, and
  an injected `<base>` would re-point every relative URL on the page, that form's action included.
* `X-Content-Type-Options: nosniff`
* `Referrer-Policy: strict-origin-when-cross-origin`
* `Strict-Transport-Security`, on top of the HTTPS Fly already enforces at the edge (`force_https`
  in `deploy/fly.toml`).

## Rotating each secret

Both secrets are Fly secrets in production and `.env` entries locally — never repository content
(C‑9). The examples below use `<new-value>` as a placeholder, never a real secret.

**Rotating the OAuth client secret.** Generate a new one on GitHub — Settings → Developer settings →
OAuth Apps → the App for this environment → *Generate a new client secret* — and set it as a Fly
secret with `flyctl`:

```sh
flyctl secrets set GITHUB_OAUTH_CLIENT_SECRET=<new-value>
```

This signs everyone out immediately, everywhere, because the session key is derived from that value.
That is the point of the derivation above, not a side effect to work around: it is the one action
that revokes every outstanding session at once. Everybody signs in again at `/login`, which costs
them two taps on a GitHub page they are already signed in to. Delete the old secret on GitHub once
the new one is deployed, and replace the old value in any local `.env`.

Taking somebody's access away is the other direction and needs no secret at all: remove their push
access on GitHub, and the next sign-in refuses them. A session they already hold survives until it
expires, because verifying it asks GitHub nothing — so for an urgent revocation, do both.

**Rotating `REFRESH_SECRET`.** Generate a new value and set it the same way:

```sh
openssl rand -base64 32
flyctl secrets set REFRESH_SECRET=<new-value>
```

Update the `Authorization: Bearer <new-value>` header in the cron-job.org job configuration to
match — until you do, every scheduled refresh fails with 401 (FR‑5.1 AC2) and the dashboard's data
goes stale without failing loudly, since stale-but-present content is exactly what FR‑1.4 AC3
guarantees rather than a blank page.

`flyctl secrets list` shows secret *names* and their deployment status only, never values, so a
rotation can be verified without ever displaying what was set; `flyctl secrets import` reads
`NAME=VALUE` pairs from stdin rather than from command-line arguments, so a secret value never
appears in shell history or a process listing while it is being set. Deploying is CI's job (FR‑9.3);
these two commands are the occasions on which a person talks to Fly directly.
