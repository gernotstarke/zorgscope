# 3. Context and scope

Business context, external interfaces (EXT-1 … EXT-8) and the context diagram are maintained in
[requirements ch. 3](../requirements/03-scope-and-context.md).

## Technical context

| Neighbour | Channel | zorgscope side |
|-----------|---------|----------------|
| Wails macOS client (future) | HTTPS JSON `/api/v1/*`; bearer bootstrap, later passkey device token | `internal/server` API delivery |
| Same-origin browser client (optional) | HTTPS JSON `/api/v1/*` plus self-hosted assets | `internal/server` API/static delivery |
| GitHub | HTTPS to `api.github.com/graphql` and `/notifications` | `internal/adapters/github` |
| Plausible | HTTPS to `plausible.io/api/v2/query` | `internal/adapters/plausible` |
| TLS endpoints | TLS handshake and optional HTTPS request for certificate metadata | `internal/adapters/watch` |
| SQLite file | local file on volume `/data/zorgscope.db` | `internal/adapters/sqlite` |
| Runtime configuration manager | revisioned non-secret YAML plus AES-GCM encrypted provider-secret overrides on the Fly volume | `internal/config` |
| Environment/Fly secrets | deployment-only listen/storage/auth/master-key/platform controls | startup wiring |
| fly.io | health checks on `/healthz`, `/readyz` | `internal/server` |
