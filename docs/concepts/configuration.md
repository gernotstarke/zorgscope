# Configuration

What is YAML and what is environment, where the OAuth values come from, why the split, how a source
becomes disabled, and the one trap this page exists to name explicitly. See
[ADR‑0003](../decisions/0003-fly-scale-to-zero-external-cron.md)
for why there is no in-process scheduler to begin with, and
[security and token handling](security-and-tokens.md) for the secrets this page only lists — their
derivation, comparison and rotation live there, not here.

## What is YAML and what is environment

Non-secret settings live in `config/zorgscope.yaml`, baked into the image and loaded from the path
`CONFIG_PATH` names:

```yaml
timezone: Europe/Berlin

refresh:
  interval: 15m
  stale_after: 45m

github:
  login: gernotstarke
  auth_repo: gernotstarke/zorgscope
  repos:
    - arc42/arc42.org-site
    - arc42/quality.arc42.org

notifications:
  slack:
    enabled: false
```

`github.auth_repo` is the one field that is not about what is watched: it names the repository whose
push access admits a visitor (FR‑8.3 AC3, [ADR‑0009](../decisions/0009-github-sign-in-push-access.md)).
It sits in the YAML file rather than the environment because it is not a secret and it is exactly
the kind of thing a reader of this repository should be able to look up.

Everything else — every credential and the three test-only base URL overrides — comes from the
environment: `internal/config.Load(path string, env func(string) string) (Config, error)` reads the
YAML file for the settings above and calls `env` for the rest. `env` is injected rather than
`Load` calling `os.Getenv` itself, so `internal/config/config_test.go` can supply a fixed map and
needs no process environment to test start-up failures.

| Comes from YAML | Comes from environment |
|---|---|
| `timezone`, `refresh.interval`, `refresh.stale_after` | `GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET`, `REFRESH_SECRET` |
| `github.login`, `github.repos`, `github.auth_repo` | `GITHUB_TOKEN`, `SLACK_WEBHOOK_URL`, `TURSO_URL`, `TURSO_AUTH_TOKEN` |
| `notifications.slack.enabled` | `GITHUB_BASE_URL`, `GITHUB_OAUTH_BASE_URL`, `GITHUB_BADGE_BASE_URL` |

The last row is not a secret at all: `Load` reads those three from the environment purely so
`make fakes` and the tests can point at a local fixture server instead of the real upstream, without
a YAML field that would tempt someone into committing a real one. There are three of them because
what looks like one upstream is served from three hosts: the API at `api.github.com`
(`GITHUB_BASE_URL`), the OAuth authorize and token endpoints at `github.com`
(`GITHUB_OAUTH_BASE_URL`), and the build badges at `img.shields.io` (`GITHUB_BADGE_BASE_URL`, which
this design left alone). Left unset, each means its real host. `GITHUB_OAUTH_BASE_URL` is the one of
the three that is validated: set to anything but `https`, or to a host that is not this machine
(`localhost`, `127.0.0.1`, `::1`, `host.docker.internal`), `Load` refuses to start and names the
variable. It decides where the visitor's browser is sent and where this process posts the client
secret, so an unvalidated one would be a redirect to somebody else's host wearing a debugging
switch's clothes.

Signing in against the fake locally takes four `.env` values and one call the fake remembers:

```env
GITHUB_OAUTH_CLIENT_ID=local-fake
GITHUB_OAUTH_CLIENT_SECRET=local-fake-secret
GITHUB_OAUTH_BASE_URL=http://host.docker.internal:9090
GITHUB_BASE_URL=http://host.docker.internal:9090
```

```sh
curl -X POST 'http://localhost:9090/_control/oauth-callback?url=http://localhost:8080/auth/callback'
```

The `curl` is needed once per `make fakes`, and it is not optional: zorgscope never sends a
`redirect_uri` of its own (see below), so the fake — like the real GitHub — has to be told the
callback belonging to the App before it will send a browser back to one. Without it the sign-in
stops at the fake with nowhere to go. `make fakes` runs on the host, and the backend reaches it from
inside Compose as `host.docker.internal`, which is why the two base URLs above do not say
`localhost` while the callback registered with the `curl` does: that one is the browser's view.

`deploy/env.example` is the template for the local `.env` file (git-ignored), and the same names are
set as Fly secrets in production:

```env
GITHUB_OAUTH_CLIENT_ID=
GITHUB_OAUTH_CLIENT_SECRET=
REFRESH_SECRET=
GITHUB_TOKEN=
SLACK_WEBHOOK_URL=
TURSO_URL=
TURSO_AUTH_TOKEN=
```

No value is filled in above, and none belongs in this file or any other file in the repository
(QS‑4.3, C‑9) — every example on this page and every other concept page uses an empty field or an
obvious placeholder such as `<new-value>`, never a real secret.

On Fly the same names are secrets, and `GITHUB_OAUTH_CLIENT_ID` and `GITHUB_OAUTH_CLIENT_SECRET`
have to be set **before** the deploy that needs them lands. `Load` refuses to start without either
(FR‑8.3), so an image deployed ahead of its secrets does not serve a dashboard with sign-in broken —
it fails at start-up and the Machine restarts into the same failure until the values arrive. Setting
a secret on a deployed app restarts the Machines by itself, so the order that works is
`flyctl secrets set` first, deploy second.

`make backend` refuses to start on an incomplete `.env` rather than letting the container start,
fail and retry: it creates the file from the template when there is none, and otherwise checks that
the values it needs are *present* — never what they are, and it echoes none of them (QS‑4.3) —
before bringing anything up.

## Registering the two OAuth Apps

`GITHUB_OAUTH_CLIENT_ID` and `GITHUB_OAUTH_CLIENT_SECRET` are not generated with `openssl`; they come
from a GitHub OAuth App, and there are two of them, registered once under the account that owns
`gernotstarke/zorgscope` (Settings → Developer settings → OAuth Apps → New):

| | Local | Production |
|---|---|---|
| Homepage URL | `http://localhost:8080` | `https://zorgscope.fly.dev` |
| Authorization callback URL | `http://localhost:8080/auth/callback` | `https://zorgscope.fly.dev/auth/callback` |

Two Apps rather than one because a GitHub OAuth App has exactly one callback URL, and local
development and production do not share one. That single registered callback is also why zorgscope
never sends a `redirect_uri` of its own and therefore never has to know its own public URL or trust
a `Host` header — GitHub already knows where to come back to.

Copy each App's client id and a generated client secret into the environment it belongs to: `.env`
locally, Fly secrets in production. The pairing is the trap worth naming here, because nothing
enforces it: a local `.env` holding the production App's values would send a local sign-in to the
production callback, and the mistake looks like a redirect to the wrong host rather than like a
configuration error. Take both values from the same App, and label them in `.env` with the App they
came from. Nothing about cron-job.org changes; `REFRESH_SECRET` is untouched by any of this.

## Why the split

FR‑8.1 and FR‑8.2 draw the line deliberately, not as an implementation convenience: "the file
contains no secret values" and "every secret comes from the environment." Three consequences follow
directly from keeping it that way:

* **The YAML file can be public.** `config/zorgscope.yaml` is committed and reviewable — which
  repositories are watched, and which repository's push access admits a visitor — without that
  commit ever being able to leak a credential, because no field in `fileConfig`
  (`internal/config/config.go`) is capable of holding one.
* **Secrets rotate independently of a deployment.** `internal/config.Secrets` is populated once, at
  start-up, purely from `env`; changing `GITHUB_TOKEN` on Fly and restarting the Machine picks it up
  without a new image build or a config file edit.
* **Parsing is strict, so a mistake fails loudly.** `Load` decodes with
  `yaml.NewDecoder(...).KnownFields(true)`: a field the schema does not recognise — a typo like
  `githbu:` instead of `github:` — is a decode error naming the file, not a silently ignored key
  that leaves a source unconfigured with no explanation. `TestLoadRejectsUnknownField` in
  `internal/config/config_test.go` asserts the error names the offending key. Every validation
  error is wrapped with the *field's name*, never a value (`"refresh.interval: %w"`,
  `"REFRESH_SECRET must be at least 32 characters"`) — the same QS‑4.3 discipline
  [security and token handling](security-and-tokens.md) describes for the HTTP layer applies here,
  at the configuration layer, from the moment the process starts.

One more strictness worth naming: `Load` calls `time.LoadLocation(fc.Timezone)` to validate
`timezone`, and `internal/config/config.go` blank-imports `_ "time/tzdata"` so that lookup succeeds
inside the distroless runtime image (`deploy/Dockerfile`), which ships no `/usr/share/zoneinfo` —
without it, a correctly spelled `Europe/Berlin` would fail to load only in production, never in a
`docker run` of the Go toolchain image locally.

## How a source becomes disabled

`Config.Enabled(source string) bool` is the single answer to "is GitHub active". There is one source
now, and the function keeps its shape rather than collapsing into a field, because QS‑5.5's promise
is that a second source costs one adapter, one configuration fragment, one fixture set and one
registry line — and this `switch` is that registry line:

```go
func (c Config) Enabled(source string) bool {
	switch source {
	case "github":
		return c.Secrets.GitHubToken != "" && len(c.GitHub.Repos) > 0
	default:
		return false
	}
}
```

A source needs both halves of the split to be present: the credential (environment) and something
to watch (YAML). A `GITHUB_TOKEN` with an empty `github.repos` list is disabled just as surely as a
populated `repos` list with no token — there would be nothing for the token to fetch, or nothing to
fetch it with. This is FR‑8.2 AC2's "a source whose secret is absent is disabled, and says so on the
dashboard, instead of failing every refresh": a disabled source is a configuration state the refresh
loop and the dashboard are expected to render, not an error condition. `TestLoadValid` in
`internal/config/config_test.go` checks both directions — GitHub enabled because its token and its
repository list are both present, and disabled when either half is missing.

## Changing the refresh interval requires editing two places, not one

`refresh.interval` in `config/zorgscope.yaml` looks like the setting that controls how often
zorgscope refreshes. It does not. zorgscope runs on a Fly Machine scaled to zero
([ADR‑0003](../decisions/0003-fly-scale-to-zero-external-cron.md)): nothing survives between
requests to hold a ticker, so nothing in the binary can trigger anything on a schedule. The actual
trigger is external — [cron-job.org](https://cron-job.org) calling `POST /api/refresh` with the
`REFRESH_SECRET` bearer, on cron-job.org's own schedule, configured on cron-job.org's own site, not
in this repository at all.

`refresh.interval` drives one thing only: the freshness display — "data is N minutes old," and
whether the page is shown as stale once `refresh.stale_after` is exceeded. Nothing reads it to decide
when to fetch anything, because nothing in the running process ever decides that; `config/zorgscope.yaml`
says as much at the point of the trap:

```yaml
refresh:
  # What cron-job.org is configured to. Used for the freshness display; changing it here does not
  # change the schedule — change it in both places (see docs/concepts/configuration.md).
  interval: 15m
```

So changing how often zorgscope actually refreshes means editing **both**:

1. `refresh.interval` in `config/zorgscope.yaml`, which reaches production when CI deploys the
   push to `main` that carried it (FR‑9.3).
2. The job's schedule at cron-job.org, changed by hand on cron-job.org's site.

Editing only the first changes what the dashboard *says* about its own age without changing how
often a refresh actually happens — cron-job.org keeps calling at the old interval, and the
freshness display now reports a number that has nothing to do with the real cadence. Editing only
the second changes the real cadence without changing what the dashboard reports it as — the list can
now go stale sooner (or later) than `stale_after` expects, in the wrong direction for FR‑1.4 AC3's
guarantee that stale content says so rather than looking current. Neither mistake fails loudly: both
produce a dashboard that runs, answers requests, and misreports its own staleness, which is exactly
why it is worth naming here rather than leaving it to be discovered from a dashboard that quietly
disagrees with itself.
