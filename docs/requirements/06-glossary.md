# 6. Glossary

| Term | Meaning |
|------|---------|
| **Item** | One thing shown on the dashboard: a GitHub issue or pull request of a configured repository. |
| **Snapshot** | The last item list `internal/snapshot` fetched and is holding in memory, together with when it was fetched and the most recent fetch error, if any. It is the whole of zorgscope's state. |
| **Cache TTL** | `github.cache_ttl` in `config/zorgscope.yaml`: how old the snapshot may be before a page view triggers a fetch instead of reusing it. Defaults to 5 minutes. |
| **Seen mark** | The Unix-second timestamp carried inside the signed session cookie, recording when the visitor last pressed "Mark all seen". Zero on a fresh sign-in, so nothing is `NEW` until the first mark; forgotten on sign-out or expiry, because it lives only in the cookie. |
| **New** | An item whose creation time is after the visitor's seen mark. |
| **Site** | One arc42 web property configured in `github.sites`: a name, an `https` address, the one watched repository behind it, a colour from the fixed palette, and an optional short tag. |
| **Tile** | A site's panel on the Sites view (`/sites`): its colour band, at most three pull requests and four issues, its totals, and a link to the filtered list when it holds more than it shows. |
| **Other tile** | The last tile of the Sites view, holding every watched repository no site claims, so the Sites view never hides an item of a watched repository. |
| **Cold start** | The first request after the Fly Machine has been stopped; it includes starting the machine and the process and, when the snapshot is empty or stale, the fetch that follows — shown as the wait page, not waited for. |
| **Wait page** | The page shown in place of the list or the tiles while a fetch is running: the mark, animated, a status line naming how many repositories are being asked, and a poll that replaces it with the page once the fetch has ended. |
| **Fake sources** | A local HTTP server (`cmd/fakesources`, `make fakes`) that answers like GitHub — issues, pull requests and the OAuth endpoints a sign-in needs — from fixture files, used for offline development and tests. |
