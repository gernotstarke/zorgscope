# zorgscope

One page that answers *does anything need me right now?*

**zorgscope** is Gernot Starke's ("zorg") personal status dashboard. It collects the open issues and
pull requests of the arc42 sites' repositories on GitHub, behind a GitHub sign-in, and marks what is
new since the last look.

(screenshot placeholder)

One Go binary runs on a Fly.io Machine that is **stopped whenever nothing is happening**. There is
no database and no background scheduler: the backend fetches straight from GitHub on demand, every
repository side by side, keeps the last fetched list in memory for a few minutes, and a "Refresh"
button fetches immediately when that is not fresh enough. While a fetch runs the page shows the
zorgscope mark, animated, and replaces it with the list on its own. The container itself is
disposable — it remembers nothing between restarts except what a signed-in visitor's own browser
carries in its session cookie (design [ADR‑0010](docs/decisions/0010-stateless-no-database.md),
[ADR‑0011](docs/decisions/0011-request-triggered-fetch-never-a-ticker.md)).

## Quick start

Only Docker and GNU make are needed. Nothing is installed on the host.

```sh
cp deploy/env.example .env   # then fill in the GitHub OAuth pair and GITHUB_TOKEN
make backend                 # terminal 1: the backend, fetching straight from GitHub
make client                  # terminal 2: open the browser at it
make fakes                   # terminal 3 (optional): a fixture GitHub instead of the real one
```

`.env` needs three values:

```env
GITHUB_OAUTH_CLIENT_ID=      # the local OAuth App, callback http://localhost:8080/auth/callback
GITHUB_OAUTH_CLIENT_SECRET=
GITHUB_TOKEN=                # any personal access token; public repositories need no scope
```

See [configuration](docs/concepts/configuration.md) for where these come from and
[security and token handling](docs/concepts/security-and-tokens.md) for what each can do and how to
rotate it.

## Make targets

| Target | Purpose |
|--------|---------|
| `make help` | List every target |
| `make backend` | Run the backend locally against the real GitHub (terminal 1) |
| `make client` | Open the browser at the local backend (terminal 2) |
| `make fakes` | Serve fixture GitHub responses, OAuth endpoints included, on `:9090` (terminal 3) |
| `make check` | Everything CI runs, plus `markdownlint` and `fly.toml` validation |
| `make deploy` | Build remotely on Fly and deploy — `fly deploy --remote-only --config deploy/fly.toml` |
| `make clean` | Stop the local backend; remove build output and caches |

Deploying is `make deploy`, run from the laptop with a `fly` login; there is no deploy workflow in
CI — CI only tests (design §9, [ADR‑0010](docs/decisions/0010-stateless-no-database.md)).

## Repository layout

| Path | Purpose |
|------|---------|
| `docs/requirements/` | Requirements in [req42](https://req42.de) form: goals, stakeholders, constraints, functional and quality requirements |
| `docs/decisions/` | Architecture decisions (MADR) |
| `docs/concepts/` | Security and token handling, configuration |
| `docs/superpowers/` | The design spec of the stateless reset and its implementation plan |
| `cmd/zorgscope/` | The binary |
| `cmd/fakesources/` | Fixture-backed GitHub, including the OAuth endpoints |
| `internal/domain/` | The rules — items, new-detection, dashboard assembly. Standard library only |
| `internal/ports/` | `Source`, `AccessChecker`, `Clock` |
| `internal/adapters/github/` | The GitHub GraphQL fetcher and the access checker |
| `internal/snapshot/` | The in-memory cache of the last fetched item list |
| `internal/config/` | YAML configuration plus environment secrets |
| `internal/web/` | Router, handlers, templates, static assets |
| `config/` | The non-secret configuration file |
| `deploy/` | Dockerfile, Compose, `fly.toml` |

Go tests live next to the code they test.

## Documentation

* [Requirements](docs/requirements/README.md)
* [Decisions](docs/decisions/README.md), including
  [ADR‑0010: stateless, no database, no refresh pipeline](docs/decisions/0010-stateless-no-database.md)
* [The stateless reset design](docs/superpowers/specs/2026-09-15-stateless-reset-design.md)

## Status

Reset on 2026-09-15 to a stateless process: no database, no refresh pipeline, three secrets, one
deploy command, one CI job. The requirements, the decisions and the design are current with the
code; `make check` passes.

## Licence

MIT — see [LICENSE](LICENSE).
