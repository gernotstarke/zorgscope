# 7. Glossary

| Term | Meaning |
|------|---------|
| **Attention item** | An item that currently needs the user's look: `new`, `unanswered`, `build failed`, a mention or review request — and not dismissed. |
| **Age bucket** | Classification of an item's age (`created_at` for new‑ness, `updated_at` for staleness): `< 24 h`, `< 7 d`, `< 30 d`, `≥ 30 d`. |
| **Dismissal** | The user's explicit "seen" for an item, bound to the item's `updated_at`; expires when the item changes. |
| **Fetch** / **poll** | One execution of a source adapter retrieving current data. |
| **Fake sources** | A Go program (`cmd/fakesources`) that imitates GitHub, Plausible, Todoist and feeds with deterministic data, used by e2e tests and local demo mode. |
| **Fixture** | Recorded upstream response stored under `test/fixtures/`, used by adapter contract tests. |
| **Item** | Normalised unit of information from any source: issue, PR, workflow run, task, article, metric series. |
| **Monitored repository** | A GitHub repository listed in the config whose issues, PRs and workflow runs are fetched. |
| **New** | Item present now but absent from the previous daily snapshot of its source (see FR‑7.2). |
| **Passkey** | WebAuthn/FIDO2 discoverable credential (Touch ID, iCloud Keychain, security key) used for login. |
| **Snapshot** | The set of item ids per source recorded once per day at the configured time. |
| **Source** | A configured origin of items: one GitHub repo, one Plausible site, the Todoist account, one feed, or the synthetic "GitHub mentions". |
| **Source kind** | The type of source; each kind has one adapter, one tile template and one fake. |
| **Stale** | Open issue/PR with no activity for ≥ 30 d (configurable). |
| **Staleness (of data)** | Age of the last successful fetch for a source, shown per tile. |
| **Tile** | A self‑contained rectangle of the dashboard grid rendered from one htmx fragment. |
| **Unanswered** | Open issue/PR older than the grace period without a comment/review by anyone other than its author (bots excluded). |
| **zorg** | Gernot Starke's nickname; **zorgscope** = zorg's scope of things to watch. |
