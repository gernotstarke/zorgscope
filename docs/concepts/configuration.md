# Configuration

What is YAML and what is environment, why the split, how a source becomes disabled, and the one
trap this page exists to name explicitly. See [ADR‑0003](../decisions/0003-fly-scale-to-zero-external-cron.md)
for why there is no in-process scheduler to begin with, and
[security and token handling](security-and-tokens.md) for the two secrets this page only lists —
their derivation, comparison and rotation live there, not here.

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
  repos:
    - arc42/arc42.org-site
    - arc42/quality.arc42.org

plausible:
  sites:
    - arc42.org
    - quality.arc42.org

todoist:
  filter: "overdue | today"

notifications:
  slack:
    enabled: false
```

Everything else — every credential and the three test-only base URL overrides — comes from the
environment: `internal/config.Load(path string, env func(string) string) (Config, error)` reads the
YAML file for the settings above and calls `env` for the rest. `env` is injected rather than
`Load` calling `os.Getenv` itself, so `internal/config/config_test.go` can supply a fixed map and
needs no process environment to test start-up failures.

| Comes from YAML | Comes from environment |
|---|---|
| `timezone`, `refresh.interval`, `refresh.stale_after` | `ZORGSCOPE_TOKEN`, `REFRESH_SECRET` |
| `github.login`, `github.repos` | `GITHUB_TOKEN`, `PLAUSIBLE_API_KEY`, `TODOIST_TOKEN`, `SLACK_WEBHOOK_URL` |
| `plausible.sites`, `todoist.filter` | `TURSO_URL`, `TURSO_AUTH_TOKEN` |
| `notifications.slack.enabled` | `GITHUB_BASE_URL`, `PLAUSIBLE_BASE_URL`, `TODOIST_BASE_URL` |

The last row is not a secret at all: `Load` reads `GITHUB_BASE_URL`, `PLAUSIBLE_BASE_URL` and
`TODOIST_BASE_URL` from the environment purely so `make fakes` and the adapters' tests can point at
a local fixture server instead of the real upstream, without a YAML field that would tempt someone
into committing a real one. Left unset, each adapter uses its real API's base URL.

`deploy/env.example` is the template for the local `.env` file (git-ignored) that `make check-env`
requires before `make backend` will start, and the same names are set as Fly secrets in production:

```env
ZORGSCOPE_TOKEN=
REFRESH_SECRET=
GITHUB_TOKEN=
PLAUSIBLE_API_KEY=
TODOIST_TOKEN=
SLACK_WEBHOOK_URL=
TURSO_URL=
TURSO_AUTH_TOKEN=
```

No value is filled in above, and none belongs in this file or any other file in the repository
(QS‑4.3, C‑9) — every example on this page and every other concept page uses an empty field or an
obvious placeholder such as `<new-value>`, never a real secret.

## Why the split

FR‑8.1 and FR‑8.2 draw the line deliberately, not as an implementation convenience: "the file
contains no secret values" and "every secret comes from the environment." Three consequences follow
directly from keeping it that way:

* **The YAML file can be public.** `config/zorgscope.yaml` is committed and reviewable — which
  repositories are watched, which sites, which Todoist filter — without that commit ever being able
  to leak a credential, because no field in `fileConfig` (`internal/config/config.go`) is capable of
  holding one.
* **Secrets rotate independently of a deployment.** `internal/config.Secrets` is populated once, at
  start-up, purely from `env`; changing `GITHUB_TOKEN` on Fly and restarting the Machine picks it up
  without a new image build or a config file edit.
* **Parsing is strict, so a mistake fails loudly.** `Load` decodes with
  `yaml.NewDecoder(...).KnownFields(true)`: a field the schema does not recognise — a typo like
  `githbu:` instead of `github:` — is a decode error naming the file, not a silently ignored key
  that leaves a source unconfigured with no explanation. `TestLoadRejectsUnknownField` in
  `internal/config/config_test.go` asserts the error names the offending key. Every validation
  error is wrapped with the *field's name*, never a value (`"refresh.interval: %w"`,
  `"ZORGSCOPE_TOKEN must be at least 32 characters"`) — the same QS‑4.3 discipline
  [security and token handling](security-and-tokens.md) describes for the HTTP layer applies here,
  at the configuration layer, from the moment the process starts.

One more strictness worth naming: `Load` calls `time.LoadLocation(fc.Timezone)` to validate
`timezone`, and `internal/config/config.go` blank-imports `_ "time/tzdata"` so that lookup succeeds
inside the distroless runtime image (`deploy/Dockerfile`), which ships no `/usr/share/zoneinfo` —
without it, a correctly spelled `Europe/Berlin` would fail to load only in production, never in a
`docker run` of the Go toolchain image locally.

## How a source becomes disabled

`Config.Enabled(source string) bool` is the single answer to "is GitHub/Plausible/Todoist active":

```go
func (c Config) Enabled(source string) bool {
	switch source {
	case "github":
		return c.Secrets.GitHubToken != "" && len(c.GitHub.Repos) > 0
	case "plausible":
		return c.Secrets.PlausibleKey != "" && len(c.Plausible.Sites) > 0
	case "todoist":
		return c.Secrets.TodoistToken != "" && c.Todoist.Filter != ""
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
`internal/config/config_test.go` checks both directions — GitHub enabled because its token and
repos are both present, Todoist disabled because it has a filter but no token.

## Changing the refresh interval requires editing two places, not one

`refresh.interval` in `config/zorgscope.yaml` looks like the setting that controls how often
zorgscope refreshes. It does not. zorgscope runs on a Fly Machine scaled to zero
([ADR‑0003](../decisions/0003-fly-scale-to-zero-external-cron.md)): nothing survives between
requests to hold a ticker, so nothing in the binary can trigger anything on a schedule. The actual
trigger is external — [cron-job.org](https://cron-job.org) calling `POST /api/refresh` with the
`REFRESH_SECRET` bearer, on cron-job.org's own schedule, configured on cron-job.org's own site, not
in this repository at all.

`refresh.interval` drives one thing only: the freshness display — "data is N minutes old," and
whether a tile is shown as stale once `refresh.stale_after` is exceeded. Nothing reads it to decide
when to fetch anything, because nothing in the running process ever decides that; `config/zorgscope.yaml`
says as much at the point of the trap:

```yaml
refresh:
  # What cron-job.org is configured to. Used for the freshness display; changing it here does not
  # change the schedule — change it in both places (see docs/concepts/configuration.md).
  interval: 15m
```

So changing how often zorgscope actually refreshes means editing **both**:

1. `refresh.interval` in `config/zorgscope.yaml`, redeployed with `make fly-deploy`.
2. The job's schedule at cron-job.org, changed by hand on cron-job.org's site.

Editing only the first changes what the dashboard *says* about its own age without changing how
often a refresh actually happens — cron-job.org keeps calling at the old interval, and the
freshness display now reports a number that has nothing to do with the real cadence. Editing only
the second changes the real cadence without changing what the dashboard reports it as — a tile can
now go stale sooner (or later) than `stale_after` expects, in the wrong direction for FR‑1.4 AC3's
guarantee that a stale tile says so rather than looking current. Neither mistake fails loudly: both
produce a dashboard that runs, answers requests, and misreports its own staleness, which is exactly
why it is worth naming here rather than leaving it to be discovered from a dashboard that quietly
disagrees with itself.
