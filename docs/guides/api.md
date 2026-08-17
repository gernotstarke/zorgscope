# Client-neutral JSON API

The Fly backend is the source of truth for both a future Wails macOS client and an optional browser
client. All product operations use `/api/v1/*`; clients must not read the SQLite database or volume files.

## Authentication and representation

In the bootstrap release, send `Authorization: Bearer <ZORGSCOPE_API_TOKEN>` on every API request.
Token comparison is constant-time and API responses use `Cache-Control: no-store`. `AUTH_MODE=dev`
bypasses authentication only when the configured public URL is localhost. Passkey-backed device/browser
sessions are a later authentication mechanism behind the same resources.

Requests with bodies use `Content-Type: application/json`. Bodies are limited to 1 MiB and reject unknown
fields or multiple JSON values. Timestamps are RFC 3339 unless a field explicitly says Unix seconds.

## Resources

| Method and path | Result |
|-----------------|--------|
| `GET /api/v1/dashboard` | Complete cached dashboard: header/source health, attention, repositories and GitHub Actions build state, Plausible sites, and credential/URL/TLS watch state. Supports `If-None-Match` with the returned `ETag`. |
| `GET /api/v1/status` | Per-source last success/error, next run, item count, in-flight and auth-failed state. |
| `POST /api/v1/refresh` | Queues every source not inside its configured minimum gap; returns 202 and source count. |
| `POST /api/v1/dismiss` | Dismisses one current attention item; body is `{"id":"source\|external","updated_at":<unix-seconds>}`. |
| `POST /api/v1/dismiss-all` | Dismisses the current attention set. |
| `GET /api/v1/config` | Secret-safe complete configuration document and `ETag`. |
| `PUT /api/v1/config` | Replaces the complete editable `config` object, using its current revision in `If-Match`. |
| `PUT /api/v1/config/secrets/{name}` | Sets `github_token` or `plausible_api_key`; body is `{"value":"..."}` and `If-Match` is required. |
| `DELETE /api/v1/config/secrets/{name}` | Explicitly clears one provider secret; `If-Match` is required. The source must be disabled first. |

Successful dismiss routes return 204; refresh returns JSON; reads and configuration mutations return JSON.
Dashboard reads never contact upstream services.

## Configuration document

`GET /api/v1/config` returns four top-level parts:

```json
{
  "schema_version": 1,
  "revision": "opaque-value",
  "config": { "server": {}, "ui": {}, "snapshot": {}, "github": {}, "plausible": {}, "watch": {} },
  "deployment": { "auth_mode": "token", "log_level": "info", "data_path": "/data/zorgscope.db", "base_url": "https://zorgscope.fly.dev", "port": 8080 },
  "secrets": {
    "github_token": { "configured": false, "source": "none" },
    "plausible_api_key": { "configured": false, "source": "none" }
  }
}
```

The abbreviated `config` above is illustrative; a PUT must send the complete object returned by GET, not
the whole document and not a partial patch. Duration fields are strings such as `"15m0s"`. The editable
sections cover timezone; presentation order/caps/poll hints; snapshot time/retention; all GitHub and
Plausible source settings; and all credential/URL/TLS watch settings. `deployment` is read-only.

Example update flow:

1. GET config and retain `revision`/`ETag`.
2. Change the local copy of `config`.
3. PUT that complete object with `If-Match: "<revision>"`.
4. Replace the local document with the response; every state change creates a new revision (an
   idempotent PUT may retain the same content-derived revision).

Provider-secret values are never returned. Their status source is `none`, `environment`, or `managed`.
An explicit DELETE writes a tombstone, so an older Fly environment fallback cannot silently reappear.

## Errors and concurrency

Errors use a short JSON object with stable `error` and safe `message` fields. Important statuses are:

| Status | Meaning |
|--------|---------|
| 400 | Malformed JSON, unknown field or wrong request shape. |
| 401 | Missing/invalid bearer authentication. |
| 404 | Unknown API-manageable secret name. |
| 409 | Revision conflict; reload config before retrying. |
| 422 | Semantically invalid configuration; response includes `field`. |
| 428 | A mutating config/secret request omitted `If-Match`. |
| 429 | Too many invalid authentication attempts; valid credentials remain accepted. |
| 500 | The request could not be completed; secret/config values are not reflected in the message. |

Clients must handle 409 by presenting/reloading the latest document rather than silently overwriting it.
