# Configuration

What is YAML and what is environment, where the OAuth values come from, why the split, and how to
develop against a fixture GitHub instead of the real one. See
[ADR‑0010](../decisions/0010-stateless-no-database.md) for why there is nothing here about a
database or a refresh schedule to begin with, and
[security and token handling](security-and-tokens.md) for the secrets this page only lists — their
derivation, comparison and rotation live there, not here.

## What is YAML and what is environment

Non-secret settings live in `config/zorgscope.yaml`, baked into the image and loaded from the path
`CONFIG_PATH` names (`/config/zorgscope.yaml` on Fly, `config/zorgscope.yaml` by default locally):

```yaml
timezone: Europe/Berlin

github:
  auth_repo: gernotstarke/zorgscope   # push access here admits a visitor
  cache_ttl: 5m                       # how old the fetched list may be before a page view refetches
  repos:
    - arc42/arc42.org-site
    - arc42/arc42.de-site
    # … nine in total
  sites:                              # the Sites view: one tile per entry, in this order
    - name: quality.arc42.org
      url: https://quality.arc42.org
      repo: arc42/quality.arc42.org-site
      hue: plum
    # … seven in total
```

`github.auth_repo` is the one field that is not about what is watched: it names the repository whose
push access admits a visitor (FR‑8.3 AC3, [ADR‑0009](../decisions/0009-github-sign-in-push-access.md)).
Sign-in requests no OAuth scopes, so `auth_repo` must be a repository GitHub will describe to a
visitor's scope-less token: a public one, or one they can see without scopes. A private repository
would need the `repo` scope, and an organisation repository behind OAuth App access restrictions may
need `read:org` as well.
`github.cache_ttl` is how old the in-memory snapshot may be before a page view triggers a fetch — shown as the wait page while it runs (FR‑1.9)
instead of reusing it (design §5); it defaults to 5 minutes when the field is left out. Both sit in
the YAML file rather than the environment because neither is a secret, and both are exactly the kind
of thing a reader of this repository should be able to look up.

Everything else — every credential and the two test-only base URL overrides — comes from the
environment: `internal/config.Load(path string, env func(string) string) (Config, error)` reads the
YAML file for the settings above and calls `env` for the rest. `env` is injected rather than `Load`
calling `os.Getenv` itself, so `internal/config/config_test.go` can supply a fixed map and needs no
process environment to test start-up failures.

| Comes from YAML | Comes from environment |
|---|---|
| `timezone`, `github.auth_repo`, `github.cache_ttl`, `github.repos`, `github.sites` | `GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET`, `GITHUB_TOKEN` |
| | `GITHUB_BASE_URL`, `GITHUB_OAUTH_BASE_URL` (optional, fakes only) |

The last row is not a secret at all: `Load` reads those two from the environment purely so
`make fakes` and the tests can point at a local fixture server instead of the real upstream, without
a YAML field that would tempt someone into committing a real one. There are two of them because what
looks like one upstream is served from two hosts: the API and GraphQL endpoint at `api.github.com`
(`GITHUB_BASE_URL`) and the OAuth authorize and token endpoints at `github.com`
(`GITHUB_OAUTH_BASE_URL`). Left unset, each means its real host. Both are validated the same way: set
to anything but `https`, unless the host is this machine (`localhost`, `127.0.0.1`, `::1`,
`host.docker.internal`), `Load` refuses to start and names the variable. `GITHUB_BASE_URL` decides
where this process sends `GITHUB_TOKEN` and every visitor's token; `GITHUB_OAUTH_BASE_URL` decides
where the visitor's browser is sent and where this process posts the client secret. An unvalidated
one would be a redirect to somebody else's host wearing a debugging switch's clothes.

## Sites and their colours

`github.sites` is what the Sites view draws (FR‑1.8): one tile per entry, in the order written. Each
site names exactly one repository that `github.repos` watches; the repositories no site names share a
last tile called Other, so the Sites view never hides an item of a watched repository. The list is optional —
without it the Sites view is the one Other tile.

`hue` is not a colour but a key into a fixed palette: `navy`, `blue`, `plum`, `teal`, `umber`, `rose`,
`slate`. The colours behind the keys live in `internal/web/static/app.css` as `--hue-<key>` custom
properties, taken from the arc42 brand registry (`arc42/meta.arc42.org`, `wiki/concepts/brand.md`),
because the Content-Security-Policy forbids inline styles and a colour can therefore reach the page
only as a class the stylesheet defines. Choosing among the keys is a YAML edit; adding a colour means a
new token and a new key in `config.HueKeys`, and `TestTileColoursKeepTextReadable` holds every key to a
contrast of 4.5:1 in both appearances. `tag` (at most three characters) tells apart two sites that share
a colour, as arc42.de does beside arc42.org.

`Load` refuses a site with an empty or duplicate name, an address that is not an absolute `https` URL,
a repository not in `owner/name` form, not watched or claimed twice, an unknown `hue`, or a longer
`tag`, and names the field — `github.sites[2].hue` — as it does for every other setting.

## `.env` for local development

`deploy/env.example` is the template for the local `.env` file (git-ignored):

```env
# --- required -------------------------------------------------------------
GITHUB_OAUTH_CLIENT_ID=
GITHUB_OAUTH_CLIENT_SECRET=
GITHUB_TOKEN=

# --- optional ---------------------------------------------------------------
# Point the adapter, and the OAuth App's authorize/token endpoints, at `make fakes` instead of the
# real services. Empty means the real GitHub.
# GITHUB_BASE_URL=http://host.docker.internal:9090
# GITHUB_OAUTH_BASE_URL=http://host.docker.internal:9090

LOG_LEVEL=info
```

No value is filled in above, and none belongs in this file or any other file in the repository —
every example on this page and every other concept page uses an empty field or an obvious
placeholder, never a real secret. `make backend` refuses to start on an incomplete `.env` rather
than letting the container start, fail and retry: it creates the file from the template when there
is none, and otherwise checks that the values it needs are *present* — never what they are, and it
echoes none of them — before bringing anything up.

Signing in against a fixture GitHub instead of the real one (`make fakes`, `:9090`) takes the two
commented lines above uncommented, pointing both the API/GraphQL host and the OAuth host at
`http://host.docker.internal:9090` — the backend runs in Compose and reaches the fake, which
`make fakes` runs directly on the host, by that name. Nothing else is needed: `GET
/login/oauth/authorize` on the fake honours whatever `redirect_uri` a request carries, and
zorgscope's own request carries none, so the fake falls back to the same
`http://localhost:8080/auth/callback` the local OAuth App is registered with (see below) — there is
no extra step to register a callback with the fake by hand.

## What main reads directly

`PORT`, `LOG_LEVEL` and `CONFIG_PATH` are read by `cmd/zorgscope/main.go` itself
(`envOr("PORT", "8080")` and its siblings), not by `internal/config.Load` — they decide where the
process listens, how verbosely it logs, and which YAML file it reads, rather than anything about
what the dashboard shows or who may sign in. All three have sensible defaults and are optional
everywhere; `deploy/fly.toml` sets `PORT=8080`, `CONFIG_PATH=/config/zorgscope.yaml` and
`LOG_LEVEL=info` explicitly, and Compose leaves `CONFIG_PATH` at its default because the image's
working directory already holds `config/zorgscope.yaml`.

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
came from.

## Fly secrets

On Fly the three environment values are secrets, set once with `flyctl` and reused across deploys —
`deploy/fly.toml` carries no secret itself:

```sh
flyctl secrets set GITHUB_OAUTH_CLIENT_ID=<client-id> -a zorgscope
flyctl secrets set GITHUB_OAUTH_CLIENT_SECRET=<client-secret> -a zorgscope
flyctl secrets set GITHUB_TOKEN=<token> -a zorgscope
```

`Load` refuses to start without any of the three (FR‑8.1 AC2), so an image deployed ahead of its
secrets does not serve a dashboard with sign-in broken — it fails at start-up and the Machine
restarts into the same failure until the values arrive. Setting a secret on a deployed app restarts
the Machines by itself, so the order that works is `flyctl secrets set` first, `make deploy` second.

## Why the split

FR‑8.1 draws the line deliberately, not as an implementation convenience: "the file contains no
secret values" and "every secret comes from the environment." Two consequences follow directly from
keeping it that way:

* **The YAML file can be public.** `config/zorgscope.yaml` is committed and reviewable — which
  repositories are watched, and which repository's push access admits a visitor — without that
  commit ever being able to leak a credential, because no field in `fileConfig`
  (`internal/config/config.go`) is capable of holding one.
* **Secrets rotate independently of a deployment.** `internal/config.Secrets` is populated once, at
  start-up, purely from `env`; changing `GITHUB_TOKEN` on Fly and restarting the Machine picks it up
  without a new image build or a config file edit.

Parsing is strict, so a mistake fails loudly: `Load` decodes with `yaml.NewDecoder(...).KnownFields(true)`
— a field the schema does not recognise, a typo like `githbu:` instead of `github:`, is a decode
error naming the file, not a silently ignored key that leaves the process half-configured with no
explanation. `TestLoadRejectsUnknownField` in `internal/config/config_test.go` asserts the error
names the offending key. Every validation error is wrapped with the *field's name*, never a value
(`"github.cache_ttl: %w"`, `"GITHUB_OAUTH_CLIENT_SECRET is not set"`) — the same discipline
[security and token handling](security-and-tokens.md) describes for the HTTP layer applies here, at
the configuration layer, from the moment the process starts.

One more strictness worth naming: `Load` calls `time.LoadLocation(fc.Timezone)` to validate
`timezone`, and `internal/config/config.go` blank-imports `_ "time/tzdata"` so that lookup succeeds
inside the distroless runtime image (`deploy/Dockerfile`), which ships no `/usr/share/zoneinfo` —
without it, a correctly spelled `Europe/Berlin` would fail to load only in production, never in a
`docker run` of the Go toolchain image locally.
