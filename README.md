# zorgscope

One page that answers *does anything need me right now?*

**zorgscope** is Gernot Starke's ("zorg") personal status dashboard. It collects what is scattered
across a few services, marks what arrived since the last look, and costs almost nothing to run:

* **GitHub** — open issues and pull requests of the configured repositories, with their age and last
  activity, plus the GitHub Actions build state per repository.
* **Sites** — Plausible visitors and pageviews over 7 and 30 days, each against the preceding period.
* **Tasks** — the Todoist tasks that are overdue or due today.
* **New** — anything first seen after your last visit carries a `NEW` badge until you mark it seen.

One Go binary runs on a Fly.io Machine that is **stopped whenever nothing is happening**. There is no
background scheduler: [cron-job.org](https://cron-job.org) calls `POST /api/refresh` on a schedule,
which fetches every source, writes the result to [Turso](https://turso.tech) — and, incidentally,
keeps the machine warm so that opening the page is fast. All state lives in the database; the
container itself is disposable.

## Quick start

Only Docker and GNU make are needed. Nothing is installed on the host.

```sh
cp deploy/env.example .env   # then fill in ZORGSCOPE_TOKEN and REFRESH_SECRET
make backend                 # terminal 1: the backend and its libSQL database
make client                  # terminal 2: open the browser at it
make fakes                   # terminal 3 (optional): fixture upstreams instead of the real ones

make test                    # all tests, race detector, against the local database
make lint                    # go vet + golangci-lint, including the architecture rules
make check                   # everything CI runs
make db-shell                # a SQL shell against the local database
make fly-deploy              # validate and deploy to Fly
make help                    # every target
```

## Repository layout

| Path | Purpose |
|------|---------|
| `docs/requirements/` | Requirements in [req42](https://req42.de) form: goals, stakeholders, constraints, functional and quality requirements |
| `docs/decisions/` | Architecture decisions (MADR) |
| `docs/concepts/` | Security and token handling, data storage, configuration, operations |
| `docs/superpowers/` | The design spec and the implementation plan |
| `cmd/zorgscope/` | The binary |
| `cmd/fakesources/` | Fixture-backed GitHub, Plausible and Todoist stand-ins |
| `internal/domain/` | The rules — items, new-detection, dashboard assembly. Standard library only |
| `internal/ports/` | `SourceFetcher`, `Store`, `Notifier`, `Clock` |
| `internal/adapters/` | GitHub, Plausible, Todoist, Slack, libSQL |
| `internal/config/` | YAML configuration plus environment secrets |
| `internal/refresh/` | One refresh run over all sources |
| `internal/web/` | Router, handlers, templates, static assets, rendered documentation |
| `config/` | The non-secret configuration file |
| `deploy/` | Dockerfile, Compose, `fly.toml` |

Go tests live next to the code they test. The running system serves its own documentation at `/docs`.

## Documentation

* [Requirements](docs/requirements/README.md)
* [Decisions](docs/decisions/README.md)
* [Design](docs/superpowers/specs/2026-08-17-zorgscope-reset-design.md)
* [Implementation plan](docs/superpowers/plans/2026-08-17-zorgscope-v1.md)

## Status

Reset on 2026-08-17 to a much smaller scope than the previous iteration. The requirements, the design
and the implementation plan are written; the infrastructure — module, container, Compose, Fly
configuration and the make targets — is in place and `make check` passes. The application itself is
being built task by task from the plan.

## Licence

MIT — see [LICENSE](LICENSE).
