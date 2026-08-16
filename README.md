# zorgscope

**zorgscope** is Gernot Starke's ("zorg") personal dashboard: a single web page — meant to be the
new‑tab page of the browser — that condenses everything worth a glance into a grid of tiles:

* **GitHub attention** – new and unanswered issues / PRs across the arc42 and personal site repositories, plus mentions and review requests, so no contributor waits for an answer.
* **Repository health** – open counts and GitHub‑Actions build status per monitored repo.
* **Site statistics** – Plausible visitor numbers and trends for the arc42 and personal sites.
* **Todoist** – overdue tasks and everything due within the next seven days.
* **News** – configurable RSS/Atom feeds (AI/LLM, agentic engineering, …).

Single Go binary, server‑rendered UI (html/template + htmx + hand‑written CSS), SQLite for cache and
daily snapshots, passkey login, hosted on one small fly.io machine. Locally everything runs under
Docker: `make app`.

## Quick start

```sh
make app        # build + run in Docker, then open http://localhost:8080
make test       # unit + integration tests (in Docker)
make lint       # go vet + golangci-lint (in Docker)
make e2e        # Playwright end-to-end tests against fake sources (Docker Compose)
make deploy     # deploy to fly.io (flyctl in Docker)
make help       # all targets
```

Only Docker and GNU make are required locally (and a browser).

## Repository layout

| Path | Purpose |
|------|---------|
| `docs/requirements/` | Requirements (req42 style): goals, stakeholders, scope, functional & quality requirements, constraints |
| `docs/architecture/` | Architecture documentation (arc42) incl. `decisions/` (ADRs, MADR format) |
| `docs/plans/` | Implementation plans (task lists executable by agents) |
| `docs/guides/` | How‑tos: browser new‑tab setup, secrets, deployment |
| `cmd/` | Go entry points (`zorgscope`, `fakesources`) |
| `internal/` | Go source: `domain`, `ports`, `adapters`, `app`, `http` |
| `web/` | Templates and static assets (CSS, vendored htmx) |
| `config/` | Non‑secret runtime configuration (`zorgscope.yaml`) |
| `deploy/` | Dockerfiles, Compose files, `fly.toml` |
| `test/` | Cross‑cutting tests: e2e (Playwright), fake upstream servers, recorded fixtures |
| `.github/workflows/` | CI: lint → test → build → e2e → deploy |

Go unit tests live next to the code they test (`*_test.go`), as is idiomatic in Go.

## Documentation entry points

* [Requirements](docs/requirements/README.md)
* [Architecture (arc42)](docs/architecture/README.md)
* [Architecture decisions](docs/architecture/decisions/README.md)
* [Guides](docs/guides/)

## Status

Requirements and architecture are written; implementation follows the plan in `docs/plans/`.

## Licence

MIT — see [LICENSE](LICENSE).
