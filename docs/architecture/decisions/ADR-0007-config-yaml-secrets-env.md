# ADR-0007: YAML configuration in the repository, secrets via environment

* Status: superseded by ADR-0014
* Date: 2026-08-16
* Deciders: Gernot Starke
* Related: FR-8.x, QG-4, QS-3.3, QS-4.1, QS-4.3

## Context and problem statement

Adding a repo/site/feed must take minutes and no code (G‑3); secrets must never end up in git, logs or the
browser (QG‑3). Configuration must be validated so that cheap agents and the owner get precise error messages.

## Considered options

1. `config/zorgscope.yaml` (versioned) + secrets from env vars (`.env` locally, `fly secrets` in prod).
2. Everything in env vars.
3. Admin UI storing config in SQLite.
4. Config in SQLite seeded from YAML.

## Decision outcome

**Chosen option: 1.** Strict schema (unknown keys fail), typed durations, per‑source overrides, `enabled`
flags per kind; env vars only for secrets and environment‑specific overrides (`*_BASE_URL` for fakes,
`AUTH_MODE`, `LOG_LEVEL`). Hot reload on SIGHUP/file change for local use; production picks up config via
image rebuild (CI deploy on push).

### Consequences

* Good: config is reviewable in git history; adding a repo is a one‑line PR; validation errors name key + line.
* Bad: production change = deploy (~5 min) — accepted (QS‑4.1 ≤ 10 min). Option 3 rejected as YAGNI for a
  single user; option 2 unreadable for lists.

ADR-0014 replaces this production model with a bootstrap YAML seed, revisioned runtime YAML on the Fly
volume and an authenticated API. Deployment trust-root secrets stay in the environment; GitHub/Plausible
provider secrets can be stored in a separate encrypted envelope through write-only routes.
