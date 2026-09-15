# 6. Glossary

| Term | Meaning |
|------|---------|
| **Item** | One thing shown on the dashboard: a GitHub issue or pull request of a configured repository. |
| **Snapshot** | The last item list `internal/snapshot` fetched and is holding in memory, together with when it was fetched and the most recent fetch error, if any. It is the whole of zorgscope's state. |
| **Cache TTL** | `github.cache_ttl` in `config/zorgscope.yaml`: how old the snapshot may be before a page view triggers a fetch instead of reusing it. Defaults to 5 minutes. |
| **Seen mark** | The Unix-second timestamp carried inside the signed session cookie, recording when the visitor last pressed "Mark all seen". Zero on a fresh sign-in, so nothing is `NEW` until the first mark; forgotten on sign-out or expiry, because it lives only in the cookie. |
| **New** | An item whose creation time is after the visitor's seen mark. |
| **Cold start** | The first request after the Fly Machine has been stopped; it includes starting the machine and the process and, when the snapshot is empty or stale, the one fetch that follows. |
| **Fake sources** | A local HTTP server (`cmd/fakesources`, `make fakes`) that answers like GitHub — issues, pull requests and the OAuth endpoints a sign-in needs — from fixture files, used for offline development and tests. |
