# ADR-0009: Docker‑only local toolchain driven by make

* Status: accepted
* Date: 2026-08-16
* Deciders: Gernot Starke
* Related: C-2, FR-10.3, QS-7.x

## Context and problem statement

Only Docker and make may be required on a developer's (or agent's) machine. Build, test, lint, e2e, docs
checks and deployment must all be reproducible.

## Considered options

1. `Makefile` whose every target runs official images (`golang`, `golangci-lint`, Playwright, `flyctl`, markdownlint, lychee) with caches in named volumes; Docker Compose for run and e2e.
2. Dev container / devbox with everything preinstalled.
3. Native tools required locally.

## Decision outcome

**Chosen option: 1.** `make app` = `docker compose up --build`; `make test/lint/e2e/deploy/docs-check`.
The production Dockerfile is the single source of the binary; CI uses the same targets. Go module and build
caches live in named Docker volumes so repeated runs are fast.

### Consequences

* Good: identical behaviour on any machine and in CI; no version drift; agents in sandboxes only need Docker.
* Bad: first run downloads images (~1–2 GB incl. Playwright); IDE features (gopls) benefit from a local Go
  install but never require one. Option 2 is a fine addition later, not a replacement.
