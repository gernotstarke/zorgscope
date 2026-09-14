# zorgscope

One page that answers *does anything need me right now?*

**zorgscope** is Gernot Starke's ("zorg") personal status dashboard. It collects the open issues and
pull requests of the arc42 sites' repositories on GitHub, marks what arrived since the last look, and
costs almost nothing to run:

* **GitHub** — open issues and pull requests of the configured repositories, with their age and last
  activity, plus the GitHub Actions build state per repository.
* **Filters** — by repository, kind, creation date and text; every filtered view is a URL.
* **New** — anything first seen after your last visit carries a `NEW` badge until you mark it seen.

One Go binary runs on a Fly.io Machine that is **stopped whenever nothing is happening**. There is no
background scheduler: [cron-job.org](https://cron-job.org) calls `POST /api/refresh` on a schedule,
which fetches every source, writes the result to [Turso](https://turso.tech) — and, incidentally,
keeps the machine warm so that opening the page is fast. All state lives in the database; the
container itself is disposable.

## Quick start

Only Docker and GNU make are needed. Nothing is installed on the host.

```sh
cp deploy/env.example .env   # then fill in the GitHub OAuth pair, REFRESH_SECRET and GITHUB_TOKEN
make backend                 # terminal 1: the backend and its libSQL database
make client                  # terminal 2: open the browser at it
make fakes                   # terminal 3 (optional): fixture upstreams instead of the real ones

make check                   # everything CI runs: vet, lint, tests, docs, fly.toml validation
make clean                   # stop the local backend, drop caches and local data
make help                    # every target
```

## Repository layout

| Path | Purpose |
|------|---------|
| `docs/requirements/` | Requirements in [req42](https://req42.de) form: goals, stakeholders, constraints, functional and quality requirements |
| `docs/decisions/` | Architecture decisions (MADR) |
| `docs/concepts/` | Security and token handling, data storage, configuration, operations |
| `docs/superpowers/` | The design specs and their implementation plans |
| `cmd/zorgscope/` | The binary |
| `cmd/fakesources/` | Fixture-backed GitHub, including the OAuth endpoints |
| `internal/domain/` | The rules — items, new-detection, dashboard assembly. Standard library only |
| `internal/ports/` | `SourceFetcher`, `Store`, `Notifier`, `Clock` |
| `internal/adapters/` | GitHub, Slack, libSQL |
| `internal/config/` | YAML configuration plus environment secrets |
| `internal/refresh/` | One refresh run over all sources |
| `internal/web/` | Router, handlers, templates, static assets, rendered documentation |
| `config/` | The non-secret configuration file |
| `deploy/` | Dockerfile, Compose, `fly.toml` |

Go tests live next to the code they test. The running system serves its own documentation at `/docs`.

## Documentation

* [Requirements](docs/requirements/README.md)
* [Decisions](docs/decisions/README.md)
* [Design: the reset](docs/superpowers/specs/2026-08-17-zorgscope-reset-design.md)
  and [its implementation plan](docs/superpowers/plans/2026-08-17-zorgscope-v1.md)
* [Design: GitHub sign-in and focus](docs/superpowers/specs/2026-09-14-github-signin-and-focus-design.md)
  and [its implementation plan](docs/superpowers/plans/2026-09-14-github-signin-and-focus.md)

## Status

Reset on 2026-08-17 to a much smaller scope than the previous iteration. The requirements, the design
and the implementation plan are written; the infrastructure — module, container, Compose, Fly
configuration and the make targets — is in place and `make check` passes. The application itself is
being built task by task from the plan.

## Licence

MIT — see [LICENSE](LICENSE).
