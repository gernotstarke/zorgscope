# 0008. Docker and make as the only local toolchain

* Status: accepted
* Date: 2026-08-17
* Requirements: C‑2, C‑9, QG‑5, QS‑5.3, QS‑5.4

## Context and problem statement

C‑2 states the constraint plainly: "Docker and GNU make are the only tools installed locally. No Go
toolchain, no Node, no database client on the host." A public, MIT-licensed repository (C‑9) built
"largely by LLM agents working from this documentation" (QG‑5) cannot assume any agent's sandbox has
a particular Go version, `sqlite3` binary or `flyctl` install — the more usual arrangement for a Go
project, a locally installed toolchain that `make` merely wraps for convenience, would put exactly
that assumption back in. The question is whether wrapping every command in `docker run` is worth
the friction it visibly adds.

## Considered options

* Every command — `go test`, `go vet`, `golangci-lint`, the libSQL shell, `flyctl` — runs inside a
  container, invoked through `make` targets that are thin wrappers around `docker run`.
* A locally installed Go toolchain (and, as needed, Node, a `sqlite3` client, `flyctl`), with `make`
  as a convenience layer calling the local binaries directly.
* A devcontainer or Nix shell providing one reproducible environment that an editor or CLI attaches
  to, inside which ordinary local commands run.

## Decision outcome

Chosen: **Docker and make wrapping everything**, because it is the only option that makes "only
Docker and make are required" true for every command in the `Makefile`, not just for the ones a
particular contributor happened to remember to containerise.

The pattern is uniform. `GO_RUN` is defined once and used by `test-unit`, `test-domain`, `fmt`,
`tidy` and the generic `go` target:

```makefile
GO_RUN = docker run --rm -t \
           -v "$(CURDIR)":/src -w /src \
           -v $(GOMOD_VOL):/go/pkg/mod -v $(GOCACHE_VOL):/root/.cache/go-build \
           -e CGO_ENABLED=$(CGO_ENABLED) $(GO_IMAGE)
```

`lint` runs `golangci-lint` from `golangci/golangci-lint:v2.12.0` in its own container; `db-shell`
runs `ghcr.io/tursodatabase/libsql-shell` sharing the database container's network namespace, so no
SQL client is ever expected on the host; `fly-*` targets run `flyio/flyctl` the same way, mounting
`$(FLY_CONFIG_DIR)` so a login session persists between invocations without installing `flyctl`
locally; `docs-check` runs `markdownlint-cli2` and `lychee` the same way again. Named volumes
(`$(APP)-gocache`, `$(APP)-gomod`) persist the module and build caches between invocations so the
repeated `docker run` does not mean a full download every time — `make help`'s one-line summary of
every target is the whole interface a contributor or an agent needs.

### Consequences

* Good: `make check` reproduces exactly what CI runs (QS‑5.3), because both are the same containers
  invoked the same way — there is no "works locally, fails in CI" gap caused by a host toolchain
  drifting from the one CI uses.
* Good: QS‑5.4's "an unfamiliar agent picks a task ... and takes under about two hours" is easier to
  satisfy when the agent's environment needs no setup step beyond having Docker available — no
  "install Go 1.26, install golangci-lint v2.12.0, install the libSQL shell" section in the plan.
* Bad: every invocation pays container start-up and, on a cold cache, image pull time that a locally
  installed `go` binary would not. `make go ARGS="test ./internal/domain/... -run TestX"` is
  noticeably slower to first output than `go test` run directly, and that cost is paid on every
  single command, not once at setup. This is a real, felt cost of the decision, not a hypothetical
  one, and is judged worth paying for the reproducibility it buys.
* Neutral: `CGO_ENABLED` is threaded through explicitly (`?= 0` by default, overridden to `1` for the
  store tests that need the libSQL driver's cgo path) because the container, not a host-level
  environment variable, is what decides it; this is a direct consequence of not trusting anything
  about the host environment.

## Pros and cons of the options

### Docker and make wrapping everything

* Good: see Decision outcome above.
* Bad: see Consequences above.

### Locally installed Go toolchain, `make` as convenience

* Good: this is what most Go projects do, and it is faster in the common case — no container
  start-up latency on `go build`, no image to keep current, and tools like `gopls` in an editor work
  against the same toolchain without indirection. It is a real and common choice, not a straw man:
  for a project with a stable contributor base on similar machines, it works well.
* Bad: it reopens exactly what C‑2 exists to close — "a different Go/Node version per contributor
  machine" is no longer prevented by the constraint, it is merely undocumented drift waiting to
  happen, and an agent whose sandbox lacks a Go toolchain (a real and common case for this project's
  intended contributors, per QG‑5) cannot run `make check` at all rather than running it slightly
  slower.

### Devcontainer or Nix shell

* Good: closer to the goal than a bare local toolchain — one definition, one reproducible
  environment, and ordinary commands run at native speed once inside it rather than through a
  `docker run` wrapper on every invocation.
* Bad: it still requires installing something beyond Docker and make on the host — the devcontainer
  CLI, VS Code's devcontainer extension, or Nix itself — which is precisely what C‑2 rules out by
  name. It also does not remove Docker from the picture (a devcontainer is still a container); it
  adds a layer of tooling on top of Docker rather than replacing the need for it, without shedding
  the constraint that layer would need to justify itself against.
