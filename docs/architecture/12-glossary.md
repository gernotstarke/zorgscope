# 12. Glossary

See [requirements ch. 7](../requirements/07-glossary.md). Additional architecture terms:

| Term | Meaning |
|------|---------|
| **Adapter** | Package implementing a port against a concrete technology (GitHub API, SQLite, …). |
| **Port** | Go interface owned by the application core that adapters implement or the core consumes. |
| **Registry** | Map from source kind (config key) to adapter constructor; the single place to register a new kind. |
| **Tile partial** | Template fragment rendered for `GET /tiles/{name}` and embedded in the full page. |
| **View model** | Plain struct built by `app.DashboardQuery` for a template; contains no domain logic. |
