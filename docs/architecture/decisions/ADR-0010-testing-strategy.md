# ADR-0010: Testing strategy — layered tests, fixtures, fake sources, Playwright e2e, CI

* Status: accepted
* Date: 2026-08-16
* Deciders: Gernot Starke
* Related: C-10, QS-1.x, QS-7.x, FR-10.4, G-4

## Context and problem statement

Tests are required for backend, domain and frontend, runnable in CI without real credentials, and written
first (TDD) by possibly cheap agents. The core risk is wrong detection logic; the second is UI regressions.

## Considered options

1. Layered pyramid: domain unit + property tests · adapter contract tests with recorded fixtures · store tests on temp SQLite · handler + golden HTML tests · Playwright e2e against a Go fake‑sources server in Docker Compose · `depguard` architecture rules · everything in GitHub Actions.
2. Mostly e2e against live APIs with real tokens.
3. Unit tests only.

## Decision outcome

**Chosen option: 1.** Details in [arc42 §8.9](../08-crosscutting-concepts.md#89-testing-see-adr-0010).
Coverage gate ≥ 90 % for `internal/domain`, ≥ 70 % overall. `cmd/fakesources` exposes a control API to
inject events (new issue, comment, failed run) so e2e can prove QS‑1.1/1.6. Playwright runs Chromium,
Firefox and WebKit; includes viewport and axe checks. An optional, manually triggered "live smoke" workflow
with real tokens may be added later; it is never on the PR path.

### Consequences

* Good: deterministic CI, no secrets needed for tests, fast feedback, e2e also serves as local demo mode
  (`make app` can point at fakes).
* Bad: fixtures must be refreshed when upstream APIs change (mitigated by the smoke job); Playwright image
  is large. Option 2 rejected (flaky, leaks secrets to CI); option 3 rejected (UI/integration risk).
