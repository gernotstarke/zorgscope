# ADR-0012: Repository layout with strict separation of concerns

* Status: accepted
* Date: 2026-08-16
* Deciders: Gernot Starke
* Related: C-9, C-14, QS-7.x, G-4

## Context and problem statement

The owner wants source, documentation, requirements, tests and deployment artefacts strictly separated, yet
Go tooling and idioms expect `go.mod` at the root and unit tests next to the code.

## Considered options

1. Root `go.mod`; `cmd/`, `internal/`, `web/` (source) · `docs/{requirements,architecture,plans,guides}` · `test/{e2e,fakes,fixtures}` · `deploy/` · `config/` · `.github/`. Go unit tests colocated; cross‑cutting tests under `test/`.
2. `src/` containing the Go module; everything else beside it.
3. Multi‑module repository (domain, adapters, http as separate modules).

## Decision outcome

**Chosen option: 1** (owner confirmed). Colocated `_test.go` files are the Go idiom and keep tests next to
the unit under test; `test/` holds what is not tied to one package (e2e, fakes, fixtures).

### Consequences

* Good: standard `go build ./...`, IDE and CI tooling just work; separation is still obvious at the top level.
* Bad: "source" is three top‑level dirs rather than one — documented in the README table.
