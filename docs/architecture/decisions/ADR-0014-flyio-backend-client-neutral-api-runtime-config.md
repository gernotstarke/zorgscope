# ADR-0014: Always-on fly.io backend with client-neutral API and mutable runtime configuration

* Status: accepted
* Date: 2026-08-16
* Deciders: Gernot Starke
* Supersedes: ADR-0001
* Related: G-3, QG-1, QG-3, QG-4, FR-1.x, FR-8.x, FR-9.x, C-3, C-15, C-16, C-17

## Context and problem statement

Continuous detection must continue while a Mac sleeps or a client is closed. At the same time, the final
visual client may be a highly polished Wails macOS application, a same-origin browser application, or
both. The owner must also be able to change every ordinary setting and upstream credential from that
visual client without editing repository files or deploying a new image.

ADR-0001 selected a server-rendered browser application and rejected Wails. That decision coupled the
product boundary to one presentation technology and assumed checked-in YAML was the source of runtime
configuration. Those assumptions no longer hold.

## Decision drivers

* Always-on GitHub, Plausible, credential and TLS-certificate monitoring.
* One authoritative store for snapshots, dismissals, source health and configuration.
* Equal support for native and browser clients without duplicating domain logic.
* Complete remote runtime administration without exposing the service trust root.
* Upstream secrets must not be returned to clients and must remain confidential if the volume is copied.

## Considered options

1. Desktop-only Wails application with local polling and Keychain secrets.
2. Existing server-rendered web application with repository YAML and environment secrets.
3. Always-on fly.io backend with a versioned JSON API, volume-backed runtime configuration and future
   Wails and/or same-origin browser clients.

## Decision outcome

**Chosen option: 3.** One Go modular monolith runs continuously on a single fly.io Machine and is the
source of truth. It polls upstream systems, evaluates attention, persists state and exposes authenticated
`/api/v1/*` JSON routes. Presentation is outside the domain/application core.

`GET /api/v1/config` and complete-document `PUT /api/v1/config` expose every runtime-configurable
property. Updates are strictly validated, revisioned, optimistic-concurrency protected and live-activated.
GitHub/Plausible secrets use separate revision-protected PUT/DELETE routes and are write-only: reads reveal
only whether each value is configured and its environment/managed source. They are AES-256-GCM encrypted
before mode-0600 Fly-volume persistence under `ZORGSCOPE_CONFIG_KEY`, which fly.io supplies as a secret.

Deployment-only values define the service trust and runtime envelope: listen address/port, database path,
public base URL, authentication mode/`ZORGSCOPE_API_TOKEN`, `ZORGSCOPE_CONFIG_KEY`, and platform/logging controls.
They remain environment/Fly settings and are not mutable through the runtime API. Non-secret effective
values may be returned as read-only deployment metadata; deployment secret values/presence never are.

Authentication starts with a bootstrap bearer token so the backend slice is usable. Passkey-backed,
revocable device authentication remains the target; it will replace the bootstrap mechanism without
changing the source, dashboard or configuration API semantics.

### Consequences

* Good: polling survives sleeping/closed clients; every client sees the same state and configuration.
* Good: repositories, sites, intervals, thresholds, presentation hints and watched expiries change without
  code or deploy.
* Good: Wails and browser clients can coexist behind one stable contract.
* Good: database/volume disclosure alone does not reveal upstream secret plaintext.
* Bad: the public API and configuration mutation surface require careful authentication, rate limiting,
  validation, audit and compatibility tests.
* Bad: `ZORGSCOPE_CONFIG_KEY` backup and rotation become critical operational procedures.
* Bad: bootstrap bearer authentication is an interim security compromise until passkey device auth lands.
* Neutral: ADR-0003 and ADR-0007 no longer describe the target presentation/configuration architecture and
  must be revised or superseded during their implementation slices.
