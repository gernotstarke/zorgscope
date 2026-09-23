# 6. Glossary

| Term | Meaning |
|------|---------|
| **Item** | One thing shown on the dashboard: a GitHub issue or pull request of a configured repository. |
| **Snapshot** | The last item list `internal/snapshot` fetched and is holding in memory, together with when it was fetched and the most recent fetch error, if any. It is the whole of zorgscope's state. |
| **Cache TTL** | `github.cache_ttl` in `config/zorgscope.yaml`: how old the snapshot may be before a page view triggers a fetch instead of reusing it. Defaults to 5 minutes. |
| **Site** | One arc42 web property configured in `github.sites`: a name, an `https` address, the one watched repository behind it, a colour from the fixed palette, and an optional short tag. |
| **Tile** | A site's panel on the Sites view (`/sites`): its colour band, at most three pull requests and four issues, its totals, and a link to the filtered list when it holds more than it shows. |
| **Other tile** | The last tile of the Sites view, holding every watched repository no site claims, so the Sites view never hides an item of a watched repository. The shipped configuration since 2026-09-18 claims every repository, so it is empty. |
| **Cold start** | The first request after the Fly Machine has been stopped; it includes starting the machine and the process and, when the snapshot is empty or stale, the fetch that follows — shown as the wait page, not waited for. |
| **Wait page** | The page shown in place of the list or the tiles while a fetch is running: the mark, animated, a status line naming how many repositories are being asked, and a poll that replaces it with the page once the fetch has ended. |
| **Label chip** | A label of an item, drawn after its title on the list. Six names — bug, enhancement, documentation, question, help wanted, in progress — carry a fixed colour; every other label is a neutral chip. |
| **Quiet item** | An item not updated for 90 days: its title is dimmed and its meta line says "quiet". It keeps its place in the order. |
| **Search** | A ranked look through the snapshot (`GET /search?q=`): every word of the query must match, case-insensitively and as a substring, the title, the author login, a label, the repository name or the summary of an item; the hits are ordered by score, then by last update. |
| **Reserved word** | A word of a search query that narrows the kind rather than being looked for: `issue` and `issues` mean issues, `pr`, `prs` and `pull` mean pull requests. The last one typed wins. |
| **Contributor** | The GitHub user who opened an item. The Contributors view (`/contributors`) lists one row per contributor of the open items, with their open pull requests, issues, repositories and last activity; an author GitHub no longer knows is listed last as "unknown". |
| **Fake sources** | A local HTTP server (`cmd/fakesources`) that answers like GitHub — issues, pull requests and the OAuth endpoints a sign-in needs — from fixture files, used for offline development and tests. |
