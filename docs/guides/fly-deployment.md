# Deploy the zorgscope backend to fly.io

This guide creates the single always-on backend, its persistent state/config volume and deployment secrets.
Replace `<app>` and `<org>` with your Fly values; the configured app name must also match
`deploy/fly.toml` or be supplied with `--app`.

## 1. Create the app and volume

```sh
fly auth login
fly apps create <app> --org <org>
fly volumes create zorgscope_data --app <app> --region fra --size 1
```

The volume name and mount path must match `deploy/fly.toml` (SQLite, runtime YAML and encrypted provider
secrets live below `/data`). Run one Machine only: SQLite on one Fly volume is deliberately single-writer.

## 2. Set required deployment secrets

Generate two independent high-entropy values locally:

* `ZORGSCOPE_API_TOKEN` authenticates bootstrap `/api/v1/*` requests. Treat it like a password and store a
  copy in the macOS Keychain/password manager used by the client.
* `ZORGSCOPE_CONFIG_KEY` is the master key for authenticated encryption of API-managed upstream secrets.
  Back it up securely: losing it makes stored GitHub/Plausible secrets undecryptable. Never reuse the API
  token as this key. Its value must be base64 encoding of exactly 32 bytes (`openssl rand -base64 32`).

Set them without committing values:

```sh
fly secrets set --app <app> \
  ZORGSCOPE_API_TOKEN='<generated-api-token>' \
  ZORGSCOPE_CONFIG_KEY='<generated-config-key>'
```

The Fly seed at `config/fly.bootstrap.yaml` deliberately starts GitHub and Plausible disabled, so the
service can become ready with only those two deployment secrets. `GITHUB_TOKEN` and `PLAUSIBLE_API_KEY`
remain optional environment fallbacks, but new installations should manage them through the API.

## 3. Deploy and verify

```sh
fly deploy --app <app> --config deploy/fly.toml
fly status --app <app>
curl --fail "https://<app>.fly.dev/healthz"
curl --fail \
  -H 'Authorization: Bearer <generated-api-token>' \
  "https://<app>.fly.dev/api/v1/config"
```

The first config response supplies revision `r`. Set each provider credential without reading it back:

```sh
curl --fail-with-body -X PUT \
  -H 'Authorization: Bearer <generated-api-token>' \
  -H 'If-Match: "<r>"' -H 'Content-Type: application/json' \
  --data '{"value":"<read-only-github-token>"}' \
  "https://<app>.fly.dev/api/v1/config/secrets/github_token"
```

Fetch config again because every state-changing mutation returns a new revision. Edit the returned `config` object, set
`github.enabled` (and later `plausible.enabled`) to true, then send that complete object to
`PUT /api/v1/config` with its current revision in `If-Match`. Partial PATCH is not supported. The
[client API guide](api.md) documents the full contract and the Plausible secret name.

Accepted runtime YAML, encrypted provider-secret overrides, SQLite snapshots and dismissals persist on
the Fly volume and survive deploys/Machine replacement. Back up the complete volume and the master key.

## 4. CI deployment credential

Create a Fly deploy token scoped to the app or organisation and store it as the GitHub Actions repository
secret `FLY_API_TOKEN`. The deploy workflow consumes this credential; it is not an application runtime
secret and must not be added through `/api/v1/config`.

After the workflow is enabled, pushes to its deployment branch run the same `fly deploy` against
`deploy/fly.toml`. Keep `ZORGSCOPE_API_TOKEN` and `ZORGSCOPE_CONFIG_KEY` only in Fly secrets (and their
secure backups), never in GitHub Actions variables or repository files.
