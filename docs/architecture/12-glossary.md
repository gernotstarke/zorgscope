# 12. Glossary

See [requirements ch. 7](../requirements/07-glossary.md). Additional architecture terms:

| Term | Meaning |
|------|---------|
| **Adapter** | Package implementing a port against a concrete technology (GitHub API, SQLite, …). |
| **Port** | Go interface owned by the application core that adapters implement or the core consumes. |
| **Registry** | Map from source kind (config key) to adapter constructor; the single place to register a new kind. |
| **API DTO** | Versioned JSON representation built by the application/delivery boundary; contains presentation-ready data but no duplicated domain rules. |
| **Secret vault** | Port/adapter that seals and opens runtime provider secrets under the deployment master key; API reads expose presence only. |
