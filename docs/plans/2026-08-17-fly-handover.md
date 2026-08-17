# Fly backend handover — 2026-08-17

This is the starting point for the next session. The shared Fly backend and its live configuration API
are implemented and locally verified, but no target Fly organisation has been selected and no application,
volume, secrets or deployment have been created yet.

## Repository state

- Branch: `feat/fly-shared-backend-config-api`
- Implementation commit: `9b6fa34 feat(backend): add Fly shared status service`
- Base branch: `m1-walking-skeleton`
- Do not overwrite or fold in the unrelated untracked
  `docs/plans/2026-08-17-session-handover.md` without reviewing it separately.
- The configured Fly application name is `zorgscope`. Fly names are globally unique, so confirm its
  availability and the target organisation before provisioning. If another name is used, update both
  `app` and `ZORGSCOPE_BASE_URL` in [deploy/fly.toml](../../deploy/fly.toml).

The implementation commit contains the Fly-ready backend, provider/watch adapters, runtime configuration
manager, client-neutral JSON API, tests and updated requirements/architecture. The Wails or browser client
has deliberately not been selected or built yet.

## Product and architecture now implemented

The target is one always-on Fly service and one persistent Fly volume. The service is the single source of
truth for future Wails and optional browser clients; clients use `/api/v1/*` and never access SQLite or
volume files directly.

The supported data sources are intentionally limited to:

- GitHub repositories, issues, pull requests, mentions and Actions build state;
- Plausible site metrics and top pages;
- manually configured and automatically discovered credential/API-key expiry;
- URL health, TLS validation and certificate expiry.

There are no news feeds or Todoist integrations. Historical M1/M2 planning documents may still mention
them; those documents describe an older milestone plan, not the current target.

The production service uses:

- a single Machine in `fra`, kept running to support background polling;
- the distroless non-root container from `deploy/Dockerfile`;
- `zorgscope_data` mounted at `/data` for SQLite, runtime YAML and encrypted provider secrets;
- `/readyz` as the Fly health check;
- deployment after successful `main` CI through
  [.github/workflows/deploy.yml](../../.github/workflows/deploy.yml);
- the disabled-by-default [Fly bootstrap configuration](../../config/fly.bootstrap.yaml), allowing the
  service to become healthy before provider credentials are configured.

The volume is deliberately single-writer. Do not scale this version beyond one Machine without first
changing the persistence architecture.

## API and authentication contract

The authoritative client contract is [docs/guides/api.md](../guides/api.md). The routes are:

| Method and route | Purpose |
|------------------|---------|
| `GET /api/v1/dashboard` | Complete cached dashboard with schema version and semantic ETag. |
| `GET /api/v1/status` | Source scheduling, freshness, error and authentication state. |
| `POST /api/v1/refresh` | Queue eligible source refreshes. |
| `POST /api/v1/dismiss` | Dismiss one current attention item. |
| `POST /api/v1/dismiss-all` | Dismiss all current attention items. |
| `GET /api/v1/config` | Read the complete secret-safe configuration document. |
| `PUT /api/v1/config` | Replace the complete editable configuration using `If-Match`. |
| `PUT /api/v1/config/secrets/{name}` | Set `github_token` or `plausible_api_key`. |
| `DELETE /api/v1/config/secrets/{name}` | Clear one provider secret after disabling its source. |

Bootstrap authentication is a single bearer credential from `ZORGSCOPE_API_TOKEN`. It is compared using
a fixed-size hash and constant-time comparison. Invalid attempts are globally limited to 10 per minute;
valid requests are not blocked by that limiter. Passkey-backed device/browser authentication is a later
addition behind the same API resources.

The server-rendered page is only a local-development interim view. In production token mode `/` returns
401, so do not treat it as the production browser client or as proof that passkey/session routes exist.

## Live configuration behavior

`GET /api/v1/config` returns:

```text
schema_version: 1
revision: opaque optimistic-concurrency value
config: complete editable configuration
deployment: read-only effective deployment settings
secrets: status/source only; values are never returned
```

The complete `config` object is replaceable with `PUT`; PATCH is intentionally unsupported. Clients must
send the current revision in `If-Match`, retain the returned document after every mutation, and reload on
409. Requests reject unknown fields, multiple JSON values and bodies larger than 1 MiB.

Editable properties include timezone; client presentation order, caps and poll hints; snapshot time and
retention; every GitHub/Plausible source property and API base URL; and credential/URL/TLS watch settings.
Deployment-only values such as authentication mode, log level, data path, public base URL and listen port
are read-only. Deployment secrets, the config encryption key, API token and future session keys are never
exposed through this route.

GitHub and Plausible credentials have separate write-only routes. Managed values are stored in an
AES-256-GCM envelope at `/data/zorgscope.secrets`, with mode `0600`. `ZORGSCOPE_CONFIG_KEY` must be the
base64 encoding of exactly 32 bytes. A DELETE stores a tombstone so an old environment fallback cannot
silently become active again.

Configuration activation is transactional from the API caller's perspective. Validation, file
persistence and scheduler replacement are serialized. If runtime activation fails, the exact prior file
or absence, encrypted secret bytes, in-memory configuration, revision and running scheduler are restored.

## Source implementation notes

- GitHub reports token expiry when GitHub supplies it, represents a non-expiring/header-less token, and
  preserves the previous completed Actions conclusion while a newer run is in progress.
- Plausible uses Stats API v2. A site refresh currently makes six requests: explicit current/previous
  aggregates, daily series and seven-day top pages. Date ranges are derived in the configured server
  timezone.
- Credential watch merges manually configured credentials with expiry discovered from upstream APIs.
- URL/TLS watch records issuer, hostname verification, certificate expiry and last-check state; it retains
  known certificate metadata across transient failures during the running process. An endpoint becomes
  DOWN after two consecutive failed checks, and unchanged failures keep a stable update timestamp so a
  dismissal remains effective.
- URL-watch consecutive-failure and last-success state is still process-local. A restart or complete
  runtime rebuild resets it. Persisting that operational state is a possible follow-up, not a deployment
  blocker.

## Local verification already completed

The final implementation state passed:

```text
make test          # race-enabled suite; 78.2% overall coverage
make lint          # 0 findings
make docs-check    # 0 Markdown errors; 0 link errors
make image         # production image built successfully
make test-domain   # 98.5% domain coverage
make e2e           # 4/4 Chromium tests
```

A local authenticated production-image smoke test also passed using temporary credentials and a tmpfs
`/data`; the temporary container was removed afterward. Package coverage at handover included server
79.6%, watch 89.8%, config 83.1%, app 85.1%, GitHub 85.5%, Plausible 82.3% and domain 98.5%.

`make fly-whoami` now succeeds as `gernot.starke@innoq.com`. The account has the `personal` organisation;
its current app list does not contain `zorgscope`. Strict remote validation has therefore not run against
that app, and no actual deployment has taken place.

## Current remote state

Authentication is resolved. The Dockerized flyctl image has no `HOME`, so the original Make wrapper could
not find the valid host session even though `~/.fly/config.yml` existed. The wrapper now explicitly sets
`FLY_CONFIG_DIR` to the mounted host config, and the account identity was verified without changing Fly
state.

The following Fly resources still do not exist: the `zorgscope` application, its volume, runtime secrets,
Machines and releases. Provisioning still needs user-controlled secret values that must not be invented or
stored in the repository:

1. the bootstrap API token and configuration encryption key, with secure backups;
2. an app-scoped deploy token for the GitHub Actions `FLY_API_TOKEN` secret.

## Exact next-session deployment sequence

Use [the Fly deployment guide](../guides/fly-deployment.md) as the command reference. In order:

1. Confirm this branch and commit, then run `make fly-whoami`. Use `make fly-login` only if the existing
   session no longer works.
2. Confirm the app name in the `personal` organisation. If the name is not `zorgscope`, update
   [deploy/fly.toml](../../deploy/fly.toml) before deployment.
3. Create it with `make fly ARGS="apps create <app> --org personal"`.
4. Run `make fly-validate FLY_APP=<app>`.
5. Create one 1 GB volume with
   `make fly ARGS="volumes create zorgscope_data --app <app> --region fra --size 1"`.
6. Generate two independent secrets:
   - an API bearer token of at least 32 random characters;
   - a configuration key with `openssl rand -base64 32`.
7. Back up the configuration key in a password manager before setting it. Losing it makes managed
   provider credentials unreadable even if the volume survives.
8. Import `ZORGSCOPE_API_TOKEN` and `ZORGSCOPE_CONFIG_KEY` with
   `make fly-secrets-import FLY_APP=<app>` from a secure stdin source, then verify their names with
   `make fly-secrets FLY_APP=<app>`.
9. Run `make fly-deploy FLY_APP=<app>`; it performs strict validation first.
10. Run `make fly-status FLY_APP=<app>`, `make fly-checks FLY_APP=<app>` and
    `make fly-volumes FLY_APP=<app>`. Check `/healthz` for liveness, `/readyz` for config/DB readiness,
    authenticated `/api/v1/status` and `/api/v1/config`, and use `make fly-logs FLY_APP=<app>` for
    diagnosis. Explicitly confirm that UID 65532 can create SQLite/config files under the mounted `/data`;
    Fly mounts can hide ownership baked into the image, so this remains a first-deployment risk.
11. Read the current config revision. Set `github_token` through its secret PUT route, read the new
    revision, then set `plausible_api_key` if required.
12. Send one complete config PUT to add repositories/sites/watches and enable only sources whose
    credentials are configured. Verify `/api/v1/dashboard` after the first refreshes complete.
13. Create an app- or organisation-scoped Fly deploy token and save it as the GitHub Actions repository
    secret `FLY_API_TOKEN`. It is a CI credential, not an application runtime secret. Automatic deployment
    starts only after this work is on `main`, because the workflow listens for successful `main` CI runs;
    the feature branch still requires a manual deployment or workflow dispatch.

Do not enable GitHub or Plausible before its credential is present. Do not clear a provider credential
until its source has been disabled. Backups must include the complete Fly volume and the separately held
configuration key.

## Next product decisions

Remote deployment is the immediate operational step. After it works, decide whether the first polished
client is Wails-only or whether the same API should support both Wails and a browser frontend. The backend
does not force that decision.

For a Wails macOS client, keep the bearer token in Keychain rather than a config file, treat the API as
the only data/configuration boundary, and design the dashboard/configuration experience as native macOS UI.
The requested highly pleasing visual treatment, client interaction design and passkey/device-enrolment
flow remain separate work; none is implied by the current local HTML page.

## Useful entry points

- [Fly deployment guide](../guides/fly-deployment.md)
- [Client-neutral API guide](../guides/api.md)
- [Hybrid-client ADR](../architecture/decisions/ADR-0014-flyio-backend-client-neutral-api-runtime-config.md)
- [Fly runtime manifest](../../deploy/fly.toml)
- [Safe bootstrap config](../../config/fly.bootstrap.yaml)
- [Deploy workflow](../../.github/workflows/deploy.yml)
