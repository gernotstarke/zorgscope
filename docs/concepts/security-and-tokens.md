# Security and token handling

Two independent kinds of credential, a session cookie derived from one of them, and what never
reaches a log. See [ADR‑0009](../decisions/0009-github-sign-in-push-access.md) for why admission to
the dashboard is GitHub's question to answer rather than this system's, and
[ADR‑0010](../decisions/0010-stateless-no-database.md) for why the session cookie is the whole of
this process's authentication state.

## Two kinds of credential

zorgscope has two kinds of secret, and they are deliberately unrelated: one proves a browser is
signed in, the other reads GitHub on this process's behalf. Neither can do the other's job.

| Credential | Environment variable | Authorises | Never authorises |
|------------|----------------------|------------|-------------------|
| OAuth client secret | `GITHUB_OAUTH_CLIENT_SECRET` | Exchanging the code at `/auth/callback`, and — as the seed of the session key — verifying every later request that carries the session cookie | Reading anything on GitHub |
| GitHub read tokens | `GITHUB_TOKEN`, plus the visitor's own token at sign-in | Reading GitHub: the backend's token fetches issues and pull requests, the visitor's token answers exactly one question about their permission on `github.auth_repo` | Anything at all in zorgscope itself — neither token is ever presented back to this process as a credential |

A leaked client secret lets someone mint themselves a valid session cookie once they also hold it
(it is never sent anywhere except to GitHub's token endpoint and used locally to sign and verify
cookies); it reads nothing on GitHub. A leaked `GITHUB_TOKEN` reads whatever repositories it can see
on GitHub; it opens no session on zorgscope. Keeping them apart means a compromise of one is not
automatically a compromise of the other.

`deploy/env.example` documents the required three, all of which `internal/config.Load` refuses to
start without:

```env
# Sign in with GitHub (FR-8.3): the OAuth App registered for THIS environment.
GITHUB_OAUTH_CLIENT_ID=
GITHUB_OAUTH_CLIENT_SECRET=
GITHUB_TOKEN=
```

## What the sign-in checks

Signing in is three routes, and only the third makes a decision:

```text
GET  /login              public   the sign-in page: one link, "Sign in with GitHub"
GET  /auth/github        public   sets a state cookie, redirects to GitHub's authorize URL
GET  /auth/callback      public   verifies state, exchanges the code, checks push access,
                                  sets the session cookie, redirects to /
```

`GET /auth/github` generates 32 random bytes, stores them base64-encoded in a short-lived
`zorgscope_oauth_state` cookie (HttpOnly, Secure, SameSite=Lax, ten minutes, scoped to
`/auth/callback` alone), and redirects to GitHub with the client id, that state, and **no scopes**.
zorgscope is a public repository, and reading the authenticated user's own permission on a public
repository needs no scope, so the App asks the visitor for nothing beyond their identity. No
`redirect_uri` is sent either: GitHub uses the callback registered on the App, which is why the
backend never has to know its own public URL or trust a `Host` header.

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

`expiry` is Unix seconds, and it is the whole of the payload: the seen mark that used to sit beside
it went with `NEW` on 2026-09-18 ([ADR‑0012](../decisions/0012-no-seen-mark.md)). Verifying
a request recomputes the HMAC from the *current* `GITHUB_OAUTH_CLIENT_SECRET` and compares it
against the cookie's signature with `subtle.ConstantTimeCompare`. This gives three properties for
free, without a database:

* **Stateless verification.** No session table, no lookup — the server needs nothing but the current
  client secret to check a cookie's validity, which fits a machine that may be stopped and started
  between any two requests. It also means an existing session survives a GitHub outage: verifying
  one touches nothing upstream.
* **Rotation invalidates every session (FR‑8.3 AC4).** Because `key` is derived from the client
  secret, regenerating that secret on GitHub changes the key, and every cookie signed under the old
  key fails verification immediately — everywhere, on every device, without a sign-out action or a
  revocation list.
* **Nothing in the payload is worth forging.** The cookie carries no identity, no preference and no
  mark a visitor could move to their advantage — only the moment it stops being accepted. Extending
  that moment is exactly what the signature prevents, and there is nothing else in it to tamper with.

The cookie carries HttpOnly, Secure and SameSite=Lax (FR‑8.3 AC2) and holds a signature over an
expiry and nothing else — no identity, no login name, and above all not the visitor's GitHub token,
which was dropped at the end of the callback. A leaked cookie therefore leaks a thirty-day session,
and nothing else.

## Constant-time comparison and rate limiting

QS‑4.2 requires that refused sign-in callbacks are rate-limited and that neither the authorization
code, the state, nor the visitor's token ever reaches a log. `subtle.ConstantTimeCompare` is used
for both secret comparisons in the sign-in path — the OAuth state parameter against its cookie, and
the session cookie's signature against the recomputed HMAC — so that a failed comparison takes the
same time regardless of how many leading bytes matched. An ordinary `==` on secret bytes does not
have that property, and is exactly the kind of timing side-channel `subtle` exists to close.

Callbacks are rate-limited by an in-memory token bucket keyed by client IP, and the token is spent
at the top of the handler — before the state is compared, and before any request leaves this
process. The ordering is the substance of the limit rather than a detail of it. Charged at the exit
instead, it would decide only what the visitor is shown: an anonymous caller could loop
`/auth/github` and `/auth/callback` for as long as it liked and still make zorgscope post to
GitHub's token endpoint once per attempt, spending an upstream quota it does not own and holding a
machine that scales to zero awake for the length of the loop. A mismatched or missing state, a
cancelled authorisation, a failed code exchange, a visitor without push access and a sign-in that
succeeds therefore all cost the same single token, from the same bucket. One attempt costs one
token on every path, so somebody who gets it right on their last try is still admitted. The bucket
is deliberately in-memory rather than persisted: the machine stops when idle, so a persistent
counter would add a write to every refusal without meaningfully raising the cost of a patient
attack.

That trade-off is comfortable here, because **the rate limiter is not what stands between a stranger
and the dashboard — GitHub is.** There is no value to guess at the callback: a caller who holds no
code issued by GitHub against this App, in a browser holding the matching state cookie, is not
admitted at any rate at all. Throttling keeps the logs quiet and the machine asleep; it is not the
defence.

## What the callback logs

Every callback logs one line, admitted or refused, because "a collaborator signed in" is the whole
of the audit need here and losing it silently would be worse than the line's cost. An admitted
visitor is `sign-in accepted` and the client IP, and nothing further — not the permission level that
admitted them, and not the account that holds it.

A refusal is `sign-in refused`, the client IP, and the reason in the system's own words: `state
mismatch`, `no code` (the visitor cancelled on GitHub's authorisation page, which comes back as
`error=access_denied` rather than as a code), `exchange failed`, `access check failed` (the
repository response was not 200), `no permissions block` (GitHub answered without saying what the
visitor may do), or `no push access` (the permission was `pull` alone). An attempt past the rate
limit is `sign-in rate-limited` and the client IP alone: the budget is spent before the exchange, so
at that point there is no reason yet to name.

It never carries the authorization code, the state value, the visitor's access token, the client
secret, or a login name the visitor did not choose to publish. The session cookie holds no identity
either, so nothing downstream can reconstruct one from it.

## What is never logged (QS‑4.3)

No client secret, no derived session key, no authorization code, no visitor token and no upstream
API token may appear in a log line, an HTTP response body, or an error page. Two pieces of this sit
in code that has nothing to do with the web layer:

* `internal/config/config.go`'s `Load` wraps every validation error with the *name* of the offending
  field (`"github.cache_ttl: %w"`, `"GITHUB_OAUTH_CLIENT_SECRET is not set"`), never the value.
  `TestErrorNeverContainsSecretValues` in `internal/config/config_test.go` asserts this directly: it
  configures a canary secret value and checks that no returned error string contains it.
* `internal/web/server.go`'s `Redact` replaces every configured secret value found in text with a
  marker, reading `config.Secrets`' fields by reflection so a new secret is covered the moment it is
  declared rather than needing a hand-written list kept in step.

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
  `img-src` allows `data:` and nothing external, because every image this page shows — the logo, the
  vendored icons — is served from this origin.
* `X-Content-Type-Options: nosniff`
* `Referrer-Policy: strict-origin-when-cross-origin`
* `Strict-Transport-Security`, on top of the HTTPS Fly already enforces at the edge (`force_https`
  in `deploy/fly.toml`).

## Rotating each secret

`GITHUB_OAUTH_CLIENT_SECRET` and `GITHUB_TOKEN` are Fly secrets in production and `.env` entries
locally — never repository content. `GITHUB_OAUTH_CLIENT_ID` travels beside them for convenience,
though it is not really a secret: it is half of the authorize URL a sign-in is redirected to, and
appears in the browser's address bar on every sign-in.

**Rotating the OAuth client secret.** Generate a new one on GitHub — Settings → Developer settings →
OAuth Apps → the App for this environment → *Generate a new client secret* — and set it as a Fly
secret:

```sh
flyctl secrets set GITHUB_OAUTH_CLIENT_SECRET=<new-value> -a zorgscope
```

This signs everyone out immediately, everywhere, because the session key is derived from that value
— that is the point of the derivation above, not a side effect to work around. Everybody signs in
again at `/login`, which costs them two taps on a GitHub page they are already signed in to. Delete
the old secret on GitHub once the new one is deployed, and replace the old value in any local `.env`.

Taking somebody's access away is the other direction and needs no secret rotation at all: remove
their push access on GitHub, and the next sign-in refuses them. A session they already hold survives
until it expires, because verifying it asks GitHub nothing — so for an urgent revocation, do both.

**Rotating `GITHUB_TOKEN`.** This is an ordinary GitHub personal access token, rotated the way any
PAT is: generate a new one, set it as a Fly secret, delete the old one on GitHub. Nothing in
zorgscope is derived from it, so rotating it changes only what the next fetch reads with — no
session is affected, nobody is signed out, and the change is invisible to a visitor except that the
list keeps working:

```sh
flyctl secrets set GITHUB_TOKEN=<new-value> -a zorgscope
```

`flyctl secrets list` shows secret *names* and their deployment status only, never values, so a
rotation can be verified without ever displaying what was set; `flyctl secrets import` reads
`NAME=VALUE` pairs from stdin rather than from command-line arguments, so a secret value never
appears in shell history or a process listing while it is being set.
