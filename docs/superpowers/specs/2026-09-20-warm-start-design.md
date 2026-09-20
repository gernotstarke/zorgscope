# Warm start: stale while revalidating, and a placeholder for the cold Machine — design

Date: 2026-09-20. Status: approved by Gernot in conversation (both layers — the server-side policy
change and the browser placeholder — his choice after the alternatives were weighed). Builds on
the fast first view (`2026-09-16-fast-first-view-design.md`) and inverts one half of the policy it
established. Changes no other decision: no database, no ticker, no second renderer, no inline
script or style, the 20-request budget of QS‑3.5 untouched.

## 1. Goal

Gernot asked whether browser storage could make the dashboard start faster — an auth cookie held
client-side so that signing in to GitHub is not needed every time, valid for about thirty days.

The investigation that opened this design found the auth half already built and the premise of the
question misplaced. What follows is what the measurement actually said, and what is worth building
because of it.

### 1.1 The auth half needs nothing

`sessionTTL` in `internal/web/auth.go` is already `30 * 24 * time.Hour`. A returning visitor inside
thirty days spends **no** round trip on GitHub for authentication. The re-sign-in Gernot noticed was
the one-off recorded as the last consequence of [ADR‑0012](../../decisions/0012-no-seen-mark.md):
the cookie's payload lost its seen mark on 2026-09-18, so cookies of the old shape stopped
verifying. It was paid once.

Moving a credential into browser storage would make this strictly worse. Today the browser holds no
credential at all — only an expiry and an HMAC over it, so neither the visitor's GitHub token nor
the client secret is ever in the cookie jar (`sessionCodec`'s own comment). A token in
`localStorage` would be a real credential, readable by script, that the server could never revoke.
**Nothing about authentication changes in this design.**

### 1.2 Where the time actually goes

| Component | Cost on a cold visit | State |
|---|---|---|
| Fly Machine wake, stopped → running | ~0.3–1.5 s | Inherent to C‑3, [ADR‑0003](../../decisions/0003-fly-scale-to-zero-external-cron.md) |
| Authentication | 0 | Already a 30-day cookie |
| Static assets | 0 bytes on a repeat visit | Content-hashed URLs, `public, max-age=31536000, immutable` |
| **The GitHub fetch** | **seconds** | **The whole remaining cost** |

`internal/snapshot/snapshot.go`, first paragraph: *"a Fly Machine that wakes from zero starts empty
and fetches on its first page view."* That is the startup time, and FR‑1.9 currently answers it
with the wait page.

But the wait page is shown in a second case too, and that one is far more frequent: a **warm**
Machine whose snapshot has merely crossed `github.cache_ttl` (five minutes). There the process holds
a perfectly good list in memory and shows the wait page anyway, because FR‑1.9 AC1 says
*"and not a list"*.

### 1.3 What this design does

Show stale data, marked as refreshing, instead of waiting — applied at the two layers where the
two cases live.

* **Layer 1, server-side.** A snapshot that is stale but not empty renders its list, labelled, with
  a poll that swaps the fresh list in when the fetch lands. This covers the common case, works with
  JavaScript off, and needs no browser storage.
* **Layer 2, browser-side.** The genuinely cold Machine has nothing to serve and still shows the
  wait page. Behind that wait page, the visitor's own last view is restored from `localStorage` as a
  placeholder, guarded by an epoch that a secret rotation invalidates.

The layers are disjoint: layer 2 activates only where layer 1 has nothing.

## 2. Decisions (Gernot, 2026-09-20)

| Question | Decision |
|---|---|
| Credentials in browser storage | No. The session cookie stays exactly as it is; `sessionTTL` is already 30 days and the browser holds no credential. |
| How far the stale-while-revalidating goes | Both layers. The server-side policy change alone would leave the cold visit as slow as it is today; the browser placeholder alone would leave the five-minute wait for everyone. |
| Whether the browser merges old and new items | No. The stored copy is a placeholder paint, replaced whole. See §5.3. |
| How much is stored | The whole `#items` fragment, ungoverned. See §5.2 — the sizing is not a constraint. |
| How long a stored copy lives | Seven days, not thirty. See §5.5 — Safari caps it there regardless. |
| Delivery | Branch `feat/warm-start` off `main` at `7ba8a61`; version 0.5.0. |

## 3. Sizing: why there is no eviction policy

QS‑2.3 already fixes the number this design would otherwise have to guess. Its stimulus is
*"the representative configuration (10 repositories, ~150 open items between them)"* and its
response is *"≤ 150 kB uncompressed HTML"* — **1 kB of rendered HTML per item**, asserted on every
run by `TestRenderedPageStaysInsideItsBudget`.

Counted against the ten configured repositories on 2026-09-20, the real corpus is **46** open issues
and pull requests — roughly 46 kB. Against the most pessimistic `localStorage` budget available
(Safari's 5 MB origin quota, and UTF-16 storage doubling every byte, so ~2.5 M usable characters):

| | Share of the worst-case quota |
|---|---|
| Today's list, ~46 kB | ~2 % |
| QS‑2.3's full design ceiling, 150 kB | ~6 % |
| Items that would fit at 1 kB each | ~2,500 — 17× the design ceiling |

So the fragment is stored whole. **No pagination, no pruning, no eviction, no item cap.** Building
any of those would be machinery against a bound three orders of magnitude away.

The pleasing part is that QS‑2.3's existing budget test *is* the storage guarantee. If the rendered
page ever outgrows what a browser will hold, that test fails long before `localStorage` does, and it
fails in CI rather than on someone's phone. No new invariant is introduced.

## 4. Layer 1 — the server serves what it has

### 4.1 The change

`answeredWaiting` (`internal/web/dashboard.go`) is the first thing every page handler calls, and is
the single place the wait page is chosen. One predicate moves:

```go
// A fetch in flight is not by itself a reason to withhold the page. What is, is having nothing
// to put on it: an empty snapshot can only draw an empty list, and an empty list is a wrong
// answer rather than a stale one. With items in hand the page is drawn from them and says so
// (FR-1.9 AC1), and the poll inside the list swaps the fresh one in when the fetch lands.
if !snap.Fetching || len(snap.Items) > 0 {
	return snap, false
}
```

Everything downstream already copes. `render` takes a snapshot and its `Fetching` flag; the
"fetched at" line, the error notice and the per-repository fallback already exist and already say
the right thing about a list that is not current.

### 4.2 Arrival on its own (FR‑1.9 AC2)

The page must still finish by itself. It reuses `GET /items`, which `handleItems` already serves
from the current snapshot whatever the cache is doing.

`itemsView` gains the two fields the fragment needs — it has neither today:

```go
// Fetching says a fetch is in flight, so the list draws its own refresh poll and says that it
// is not current. Repos is how many repositories that fetch is asking, for the status line;
// the wait page names the same number.
Fetching bool
Repos    int
```

Both are set where `dashboardView` is built, from the snapshot `render` already holds and from
`s.cfg.GitHub.Repos`. `writeFragment` passes `view.Items` unchanged, so `GET /items` and the page
draw the same poll from the same fields.

```html
{{/* While a fetch runs, the list says so and asks for itself again. The polling element is
     inside the fragment it replaces, so the first fragment rendered after the fetch has landed
     carries no poll and takes this one away with it: the poll stops because the thing that
     polls is gone, not because anything counted. */}}
{{if .Fetching}}
<p class="refreshing" role="status" aria-live="polite"
   hx-get="/items" hx-trigger="every 500ms" hx-target="#items" hx-swap="outerHTML">
  Asking GitHub about {{.Repos}} repositories…
</p>
{{end}}
```

The self-terminating poll is the point: there is no stop condition to get wrong, no second route,
and no state outside the fragment.

`hx-target="#items"` with `hx-swap="outerHTML"` replaces the section the poll lives in. The existing
`HX-Trigger` discrimination in `answeredWaiting` is unaffected — this poll asks for `/items`, which
that function already declines to answer and leaves to `handleItems` (FR‑1.9 AC5).

### 4.3 Refresh is covered by the same rule

`POST /refresh` calls `Invalidate` and redirects, and the redirected `GET` then finds a stale
snapshot — indistinguishable, in `Snapshot`, from one the TTL aged out. So the policy applies to
Refresh too: the visitor stays on their list, which says it is being refreshed and updates in
place, instead of having the page taken away and given back.

This is a deliberate user-visible change and the one place the new policy is arguable, because a
visitor who presses Refresh has explicitly asked for fresh data and is instead shown the old list
first. It is taken as the better behaviour on two grounds: the content they were reading is not
yanked away, and the label plus the moving "fetched at" time answers "did it work?" more directly
than a wait page that hides the answer. Carving Refresh back out would mean distinguishing
"invalidated" from "aged out" in `Snapshot`, which is a field and a concept this design otherwise
does not need.

### 4.4 With JavaScript off

No poll fires; the visitor sees the stale list, labelled, and the next navigation shows a current
one. That is a better failure than today's, where a `<noscript>` meta refresh reloads the whole page
every two seconds until the fetch ends.

### 4.5 What this does not disturb

[ADR‑0011](../../decisions/0011-request-triggered-fetch-never-a-ticker.md) holds. The *fetch* is
still started by a request and only by a request; this poll asks what the snapshot already is and
never causes one. QS‑2.6 holds and gets easier: the page still answers without waiting for GitHub,
and now answers with more.

## 5. Layer 2 — the browser's own last view

### 5.1 Where it applies

Only where layer 1 has nothing: `snap.Fetching && len(snap.Items) == 0`, which after §4.1 is exactly
when `waiting.html` still renders. The dashboard saves; the wait page restores. Nowhere else
participates.

### 5.2 What is stored

One key, one value:

```json
zorgscope.cache.v1 = {"epoch": "a3f19c04", "savedAt": 1758326400000,
                      "fetchedAt": "2026-09-20T09:12:00Z", "html": "<section class=…>"}
```

`html` is the `#items` fragment exactly as the server rendered it. Only the dashboard is stored —
Sites, Contributors and the search results are not. A cold visit lands on `/`, and a second stored
page would double the machinery to cover a case that barely happens.

### 5.3 There is no merge

The stored copy is never merged with fetched data, for three reasons worth recording because the
opposite is the intuitive choice:

1. **The server already merges.** `Snapshot.Items` is documented as *"the items of the last fetch
   that produced any, plus — when that fetch failed for some repositories — the previous items of
   each repository it returned nothing for."* Per-repository fallback exists, and it belongs on the
   server, which knows which repositories failed. The browser never will.
2. **The stored copy is a paint, not a source.** It is replaced whole by the authoritative fragment,
   through the swap FR‑1.9 AC2 already performs. Replace, never merge.
3. **A merge would fork the domain into JavaScript.** It would need its own `SortItems`, its own
   grouping, its own filter semantics and its own per-repository failure knowledge — a second
   definition of "the current list" beside `internal/domain`, in a language none of it is written
   in. [ADR‑0002](../../decisions/0002-server-rendered-htmx.md) exists to prevent exactly that.

What the visitor is owed during the placeholder is honesty about its age, and that is a label rather
than logic: the banner names the `fetchedAt` the fragment was rendered with.

### 5.4 The epoch

`Cache-Control: no-store` on every session route exists so that rotating
`GITHUB_OAUTH_CLIENT_SECRET` is a real revocation — `requireSession`'s comment says so, and FR‑8.3
AC4 depends on it. A copy in `localStorage` would survive that rotation for ever and the server
could never reach it. The epoch restores the property:

```go
// cacheEpochContext domain-separates this value from the session signing key, which is derived
// from the same secret. The epoch is published in the page's markup; the signing key must not be
// derivable from it, and a separate context string is what guarantees that one hash says nothing
// about the other.
const cacheEpochContext = "zorgscope-cache-epoch-v1"

func newCacheEpoch(secret string) string {
	sum := sha256.Sum256([]byte(cacheEpochContext + secret))
	return hex.EncodeToString(sum[:4])
}
```

It reaches the script as a `data-` attribute on `<html>`, the way `data-theme` already does — the
CSP carries no `'unsafe-inline'` (`server.go`), so an inline `<script>` holding it is not available
and not wanted. The restore refuses any stored value whose epoch differs from the document's.
Rotating the secret therefore makes every browser's copy unreadable, everywhere, at once.

Log out clears the key outright, so the visitor who asks to leave does not leave their reading
behind.

### 5.5 Seven days, not thirty

Safari's Intelligent Tracking Prevention evicts all script-writable storage — `localStorage`,
IndexedDB, Cache Storage — after **seven days of browser non-use**. `sessionTTL`'s own comment calls
this *"a dashboard whose whole purpose is being glanced at"* on a phone; a glance less often than
weekly finds the cache gone on iOS.

This breaks nothing, because the placeholder is advisory and its absence is today's behaviour
exactly. But it settles two things: the stored copy carries a **seven-day** self-imposed expiry
rather than a thirty-day one, since claiming longer would be claiming something iOS will not honour;
and nothing in the product may ever depend on the cache being there.

### 5.6 The script

A file under `/static`, alongside `search.js`, versioned by content hash like every other asset, and
about forty lines: save after the dashboard renders, restore on the wait page, clear on log out.
It holds no application logic — it moves a string into and out of `localStorage` and sets
`innerHTML` from a value the server itself rendered.

`try`/`catch` around every access. `localStorage` throws rather than returning null in a Safari
private window and under blocked site data, and a placeholder that is merely absent must never
become a wait page that fails to draw.

### 5.7 The budget

QS‑2.3 measures **the wire**. The restored fragment arrives from `localStorage` and crosses no
network, so it does not count against the wait page's 20 kB of HTML. Only the script counts, against
the wait page's 100 kB static allowance, which htmx and the stylesheet leave ample room inside.

## 6. What is not disturbed

* [ADR‑0010](../../decisions/0010-stateless-no-database.md) — **stateless, no database.** Untouched.
  Nothing is persisted server-side; no volume, no SQLite, no row survives the Machine stopping. The
  option that ADR considered and rejected does not reappear here.
* [ADR‑0002](../../decisions/0002-server-rendered-htmx.md) — **server-rendered htmx.** Preserved.
  The browser stores server-rendered HTML and renders nothing itself.
* [ADR‑0011](../../decisions/0011-request-triggered-fetch-never-a-ticker.md) — **never a ticker.**
  Preserved, as §4.4 sets out.
* [ADR‑0003](../../decisions/0003-fly-scale-to-zero-external-cron.md) and C‑3 — **scale to zero.**
  Preserved. `min_machines_running` stays 0; `deploy/fly.toml` is not touched.
* **Authentication.** `auth.go` and `signin.go` are unchanged but for the epoch helper, which mints
  no session and verifies none.

## 7. Requirements

FR‑1.9 AC1 currently reads *"show the wait page … and not a list, filters or tiles"*. It becomes a
statement about having nothing rather than about fetching:

* **AC1** While a fetch is running **and the snapshot holds no items**, `GET /`, `GET /sites`,
  `GET /contributors` and `GET /search` show the wait page. While a fetch is running and the
  snapshot holds items, they show their ordinary page, drawn from that snapshot, with a status line
  naming how many repositories are being asked.
* **AC2** gains the second route to arrival: the wait page polls as it does today; the stale list
  polls `GET /items` every 500 ms from an element inside the fragment, which the arriving fragment
  removes.
* **AC6** (new) A stale list states the time its items were fetched at and that a fetch is running;
  colour is never the only signal, and the status line is announced politely.
* **AC7** (new) On the wait page, a stored copy of the visitor's last list may be shown as a
  labelled placeholder. It is replaced by the arriving page, it is refused when its epoch does not
  match the document's or it is older than seven days, and its absence leaves the wait page exactly
  as it is without it.

FR‑1.3 and FR‑1.8 AC4 keep their words but change in effect: Refresh still throws the snapshot away
and still comes back to the page it was pressed on, and that page is now the list marked refreshing
rather than the wait page (§4.3).

QS‑2.3's wait-page measure gains the restore script within the existing 100 kB static allowance;
its 150 kB HTML ceiling is unchanged and unaffected. QS‑2.6 is unchanged. FR‑8.3 AC4 gains the
epoch as the mechanism by which a rotation reaches browser storage.

One new ADR — **0013, stale while revalidating** — records the inverted policy, its supersession of
FR‑1.9 AC1's original wording, and why the browser copy is a placeholder rather than a cache of
record.

## 8. Testing

**Go, `internal/web`:**

**Two existing tests assert the policy this design inverts, and are rewritten rather than
deleted** — each keeps its subject and reverses its expectation:

* `TestAnExpiredTTLShowsTheWaitPageNotTheOldList` (`waiting_test.go:256`) becomes
  *TestAnExpiredTTLShowsTheOldListMarkedRefreshing*: past the TTL, `GET /` shows "Held already"
  **and** the refreshing status, and no wait page.
* `TestRefreshShowsTheWaitPageEvenThoughAListIsAlreadyHeld` (`waiting_test.go:219`) becomes
  *TestRefreshKeepsTheListAndMarksItRefreshing*, per §4.3.

Both end as they do today: once the refetch lands, the fresh list is there.

`TestWaitPageWhileFetching` and `TestSitesViewWaitsToo` keep their expectations and gain an empty
snapshot, since a cold cache is now what the wait page is for. `TestPollAnswers204WhileFetching`,
`TestPollReceivesThePageAfterTheFetch`, `TestFilterRequestDuringFetchIsServedFromTheSnapshot` and
`TestAFailedFetchEndsTheWaiting` are unaffected.

**New:**

* The items fragment carries the refreshing poll **iff** `Fetching`, and a fragment rendered with
  `Fetching` false carries none — the property the self-termination rests on.
* The stale page states its `fetchedAt` and does not claim to be current.
* The epoch changes when the secret changes, and equals neither the session signing key nor
  anything derived from it with the session context.
* The wait page carries `data-epoch` and links the restore script; no page carries an inline script
  (the existing CSP assertion in `dashboard_test.go` already covers this and must stay green).

**Go, unchanged and must stay green:** `TestRenderedPageStaysInsideItsBudget`,
`TestStaticAssetsFitTheirBudgetOnTheWire`, `TestWaitPageStaysInsideItsBudget`,
`TestPageNeverWaitsForGitHub`, and the QS‑4.1 route table.

**Browser, by hand:** a cold Machine with a warm `localStorage` shows the placeholder and then the
real list; a rotated client secret leaves the placeholder unshown; log out empties the key; a
private window degrades to the plain wait page.
