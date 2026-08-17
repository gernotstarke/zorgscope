# 7. Glossary

| Term | Meaning |
|------|---------|
| **Attention item** | An item that currently needs the user's look: `new`, `unanswered`, `build failed`, a mention or review request — and not dismissed. |
| **Age bucket** | Classification of an item's age (`created_at` for new‑ness, `updated_at` for staleness): `< 24 h`, `< 7 d`, `< 30 d`, `≥ 30 d`. |
| **Credential (watched)** | A token or API key registered through runtime configuration (or detected automatically) with expiry metadata and a warning threshold. Secret material, when needed by a source, is write-only. |
| **Dismissal** | The user's explicit "seen" for an item, bound to the item's `updated_at`; expires when the item changes. |
| **Fetch** / **poll** | One execution of a source adapter retrieving current data. |
| **Fake sources** | A Go program (`cmd/fakesources`) that imitates GitHub and Plausible with deterministic data, used by integration/e2e tests and local demo mode. |
| **Fixture** | Recorded upstream response stored under `test/fixtures/`, used by adapter contract tests. |
| **Item** | Normalised unit of information: issue, PR, workflow run, mention, metric series, watched credential or TLS certificate. |
| **Monitored repository** | A GitHub repository listed in the config whose issues, PRs and workflow runs are fetched. |
| **New** | Item present now but absent from the previous daily snapshot of its source (see FR‑7.2). |
| **Passkey** | WebAuthn/FIDO2 discoverable credential (Touch ID, iCloud Keychain, security key) used for login. |
| **Deployment configuration** | Values required to start or secure the service itself, such as database path, listen port, public URL, bootstrap auth token and master encryption key. These are environment/Fly secrets and not remotely mutable. |
| **Runtime configuration** | Source, polling, attention, snapshot, watch and presentation-hint properties persisted by the backend and fully manageable via `/api/v1/config`. |
| **Write-only secret** | A runtime secret a client may set, replace or delete but never retrieve; reads expose only whether it is configured. Stored ciphertext is protected by the deployment master key. |
| **Snapshot** | The set of item ids per source recorded once per day at the configured time. |
| **Source** | A configured origin: one GitHub repo, one Plausible site, one TLS endpoint, or synthetic GitHub mentions/credential-expiry data. |
| **Source kind** | The type of source; each kind has one adapter, configuration schema, response mapping and fake where applicable. |
| **Stale** | Open issue/PR with no activity for ≥ 30 d (configurable). |
| **Staleness (of data)** | Age of the last successful fetch for a source, shown per tile. |
| **Dashboard snapshot** | The complete presentation-ready JSON representation returned from cached server data by `GET /api/v1/dashboard`. |
| **TLS endpoint** | A configured host/URL whose served certificate chain and expiry are checked; invalid or expiring certificates become attention items. |
| **Unanswered** | Open issue/PR older than the grace period without a comment/review by anyone other than its author (bots excluded). |
| **zorg** | Gernot Starke's nickname; **zorgscope** = zorg's scope of things to watch. |
