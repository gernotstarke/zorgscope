# Fast first view — design

Date: 2026-09-16. Status: approved by Gernot in conversation (the alternative chosen, when the wait
screen appears, and the ADR are his decisions). Builds on the stateless reset
(`2026-09-15-stateless-reset-design.md`) and the per-site tiles (`2026-09-15-per-site-tiles-design.md`)
and changes none of their decisions: no database, no ticker, three secrets, one Fly Machine that
scales to zero.

## 1. Goal

The first page after the Fly Machine has been stopped takes about eleven seconds today, almost all
of it waiting for GitHub. The goal is a first view of about two and a half seconds, and a page that
never sits blank: while GitHub is being asked, the visitor sees the zorgscope mark, large and
animated, and the page they asked for replaces it on its own.

Measured on 2026-09-16 against the deployed app and real GitHub, from Cologne:

| Step of a cold visit | Measured |
|---|---|
| Fly Machine wake, stopped to reachable (Fly's own log) | 1.2 to 1.6 s |
| First byte of `/healthz` from a stopped machine | 1.8 s |
| The fetch as written: 18 GraphQL requests one after the other | 9.0 s (0.35 to 0.6 s each) |
| The same 18 requests side by side | 0.7 s |
| One combined query for all nine repositories | 1.3 to 1.5 s |

The nine watched repositories hold 40 open items in total; the largest has 18 open issues, so no
connection ever needs a second page. Fly stops the machine after five to eight idle minutes and the
cache TTL is five minutes, so in practice every visit that is more than five minutes after the
previous one pays the whole fetch, warm machine or not. The session cookie lives thirty days, so
sign-in is not on the path.

## 2. Decisions (Gernot, 2026-09-16)

| Question | Decision |
|---|---|
| How to make the fetch fast | Fan the requests out: every repository and connection side by side, in the GitHub adapter. Not a combined query (slower, and it would rewrite both the client and the fake), not a persisted last-known state (a Fly Volume or Turso would reopen state for a gain of a few hundred milliseconds), not an always-on machine. |
| What the visitor sees meanwhile | A wait page: the mark, large, with the scanning-orbit animation from `docs/logo/scanning-orbit-animation.md`, and a status line. |
| When the wait page appears | On every fetch: cold start, an expired TTL, and the Refresh button. Not only on a cold start, and no stale-while-revalidate: a stale list is not shown while a fresh one is on its way. |
| How the fetch runs | In a goroutine the request started, detached from that request, bounded by the existing 60 s budget. Never a ticker; nothing runs unless a request asked for it. Recorded as ADR‑0011. |
| Motion | CSS only. The motion study's settle-to-twelve-o'clock on success needs the Web Animations API and is left out. |
| Delivery | Branch `feat/fast-first-view` off `main` at `bfdd8fe`. Test-first, `make check` green after every task. |

## 3. The fan-out (`internal/adapters/github`)

`IssueFetcher.Fetch` keeps its signature and its contract (FR‑1.4: every per-repository error
collected, every fetched item returned) and changes only how the work is scheduled:

- One goroutine per repository and connection — issues and pull requests of one repository are two
  tasks — all started at once. There is no separate concurrency limit: QS‑3.5 caps the configuration
  at ten repositories, so at most twenty requests are ever in flight, well inside GitHub's guidance
  against concurrent requests in the hundreds.
- Inside a task, pagination stays as it is: sequential, the forward-progress check and `maxPages`
  unchanged (QS‑2.5).
- Each task writes its items and its error into its own slot of two slices sized to the task count.
  Items are then concatenated in slot order and errors joined in slot order, so the result is
  byte-for-byte what the sequential loop produced: repositories in configuration order, issues before
  pull requests. Nothing downstream can tell the difference except by the clock.
- The context handed to every task is the caller's. A cancelled fetch cancels every task; the tasks
  are waited for before `Fetch` returns, so no goroutine outlives the call.
- A repository name that is not `owner/name` is still an error in its slot and no task.

The fake GitHub server (`internal/fakesources`) serves read-only fixtures through `net/http`, which
already runs handlers concurrently, so it needs no change. `TestGraphQLRequestBudget` still counts
exactly twenty requests for ten repositories.

## 4. The cache never blocks a page (`internal/snapshot`)

`Snapshot` gains one field:

```go
type Snapshot struct {
	Items     []domain.Item
	FetchedAt time.Time
	Err       error
	ErrAt     time.Time
	Fetching  bool // a fetch is in flight; what is above is what was known before it started
}
```

`Cache.Get(ctx) Snapshot` keeps its signature and returns at once, always:

- A fetch is due exactly when it is today: never fetched, invalidated, the list older than the TTL,
  or the last failure older than the TTL.
- When a fetch is due and none is in flight, `Get` marks one in flight, stamps the moment as the
  fetch's `FetchedAt`-to-be, starts a goroutine, and returns the current snapshot with `Fetching`
  true. The goroutine runs `src.Fetch` under `context.WithoutCancel(ctx)` and `fetchBudget`, as the
  fetch does today, so a visitor closing the tab does not cancel it and a hung upstream cannot hold
  it for more than sixty seconds.
- When a fetch is in flight, `Get` returns the current snapshot with `Fetching` true, whatever its
  age. One fetch at a time; concurrent callers share it (single flight, as the mutex gave for free).
- When the goroutine finishes it takes the mutex and applies today's rules unchanged: a clean fetch
  replaces the list and stamps `FetchedAt` with the moment the fetch began; a total failure keeps the
  previous list and records `Err`/`ErrAt`; a partial fetch fills in the repositories that failed and
  leaves `FetchedAt` alone (a partial first fetch stamps now). Then it clears the in-flight mark and
  the `stale` flag.
- `Invalidate` sets `stale` as today. Pressed while a fetch is in flight, it changes nothing: the
  running fetch began at most sixty seconds ago and is the freshest list there can be; its completion
  clears the flag.

This is what ADR‑0011 records: the process still does nothing between requests (C‑3), the goroutine
lives at most `fetchBudget`, and Fly's proxy keeps the machine awake for the polling requests that
wait on it anyway. There is no timer and no loop.

`cmd/zorgscope`'s `loggingSource` is unchanged: it wraps the source, not the cache, and still logs
every failed fetch once.

## 5. The wait page (`internal/web`)

### 5.1 When it is shown

`handleDashboard` and `handleSites` call `s.cache.Get` as they do now and then branch on
`snap.Fetching`:

| Request while a fetch is in flight | Answer |
|---|---|
| An ordinary `GET /` or `GET /sites` (no `HX-Request` header) | The wait page, 200. |
| The wait page's own poll (`HX-Request: true` and `HX-Trigger: waiting`) | `204 No Content`, so htmx swaps nothing and the animation keeps running. |
| Any other htmx request — the filter form's `GET /` with `hx-select="#items"`, `GET /items` | The requested page or fragment from the current snapshot, however old. A filter change during a fetch narrows the list on screen rather than blanking it; the new list arrives with the next visit or poll. |

When `Fetching` is false the handlers render exactly what they render today, error notice included.
The poll therefore ends with the page: a failed fetch clears `Fetching`, the next poll receives the
normal page with the notice and the previous or empty list, and because a failure is not retried
within the TTL, no poll loop can chase a broken upstream.

### 5.2 What it holds

A new template `waiting.html`, executed inside the shared `layout` so the top bar and footer stay:

```html
{{define "head"}}<noscript><meta http-equiv="refresh" content="2"></noscript>{{end}}
{{define "content"}}
<section class="waiting">
  <div class="orbit-stage" data-state="refreshing" aria-busy="true">
    <img class="orbit-mark" src="{{.Asset "logo-large.jpg"}}" width="512" height="512" alt="">
    <svg class="orbit-ring" viewBox="0 0 100 100" aria-hidden="true">
      <circle class="orbit-track" cx="50" cy="50" r="46.5" pathLength="100"></circle>
      <circle class="orbit-beam"  cx="50" cy="50" r="46.5" pathLength="100"></circle>
    </svg>
    <span class="polling-pulse" aria-hidden="true"></span>
  </div>
  <p class="waiting-status" role="status" aria-live="polite">Asking GitHub about {{.Waiting.Repos}} repositories…</p>
  <div id="waiting" hx-get="{{.Waiting.Path}}" hx-trigger="every 500ms"
       hx-target="main" hx-swap="outerHTML" hx-select="main"></div>
</section>
{{end}}
```

- `layout.html` gains `{{block "head" .}}{{end}}` at the end of `<head>`, empty for every page but
  this one. The `noscript` element is permitted inside `head` and may hold `meta`, so the refresh
  applies only when JavaScript is off: without script the wait page reloads itself every two
  seconds until the fetch has ended and the server answers with the page (FR‑1.5's promise kept).
- With script, htmx polls the page's own path and query every 500 ms. The poll carries
  `HX-Trigger: waiting`, the id of the polling element, which is how the server tells it from the
  filter form. On `204` nothing happens; on `200` the response's `main` replaces the wait page's
  `main` — the polling element goes with it, so polling stops — and htmx applies the response's
  `<title>`, NEW count included, as it does by default.
- `Waiting.Path` is `r.URL.RequestURI()`, the same value the header's `Return` field already
  carries, so the Sites view polls `/sites` and a filtered list polls its own query.
- `Waiting.Repos` is `len(cfg.GitHub.Repos)`.
- `pageData` gains `Waiting *waitingView` beside `Dashboard` and `Sites`; `Title` is empty and
  `NewCount` zero, since neither is known yet.
- The wait page carries the same security headers and Content-Security-Policy as every page. Nothing
  is inline: the animation is CSS keyframes in `app.css`, the polling is htmx attributes, and no
  JavaScript is added (QS‑4.4).

### 5.3 How it moves

The scanning-orbit study, reduced to what CSS alone can do, in `app.css`:

- The stage is a square of `min(70vmin, 420px)`, centred in `main`. The mark fills it, clipped to a
  circle (`clip-path: circle(42.5%)`) so its square dark background disappears; the ring sits over
  it. 42.5% is the ring's outer edge in the artwork.
- The track is a faint circle in the brand green; the beam is a rounded lime arc
  (`stroke-dasharray: 17 83`, `stroke-width: 3.8`) rotating once per 1.05 s with a linear
  `@keyframes` on `transform` about the centre (`transform-box: view-box`,
  `transform-origin: 50px 50px`).
- The heartbeat is a translucent reddish ring at the centre, scaling from 0.86 to 1.86 and fading
  out over the same 1.05 s, on `transform` and `opacity` only.
- `@media (prefers-reduced-motion: reduce)`: no rotation and no scaling — a thicker stationary arc
  (`stroke-dasharray: 34 66`) and a steady centre ring at fixed opacity — and the status text stands.
- Colours reach the page as CSS custom properties defined once for both appearances, next to the
  tile palette; the status text keeps a contrast of at least 4.5:1 against the page in light and dark
  (FR‑1.5, checked by the existing contrast test harness).
- The `data-state="refreshing"` attribute is kept from the study so the same stylesheet can carry an
  idle state later without renaming anything; today only `refreshing` exists.

### 5.4 The large mark

`internal/web/static/logo-large.jpg`: the artwork `docs/logo/zorgscope-logo.jpeg` (971 px, 127 kB)
scaled to 512 px at JPEG quality 80, about 37 kB, made once with macOS's own `sips` and committed
like `logo.png` was — there is no `make logo` target, and the stale comment in `layout.html` that
says there is gets corrected in passing:

```sh
sips -Z 512 -s format jpeg -s formatOptions 80 docs/logo/zorgscope-logo.jpeg --out internal/web/static/logo-large.jpg
```

It is served through the existing hashed static route, cached for a year like every other asset.
512 px covers the stage at 2x on a phone and at 1.2x on a 420 px desktop stage; the JPEG is already
compressed, so gzip does not apply.

### 5.5 Budget

QS‑2.3's static budget of 50 kB on the wire is written for the dashboard page and is untouched by
this design: the dashboard page links no new asset. The wait page links the stylesheet, htmx and the
large mark, about 57 kB on the wire together, of which the stylesheet and htmx are the same hashed
files the dashboard uses and are already in the browser's cache on every visit but the first. Its own
measure is added to QS‑2.3: the wait page is at most 20 kB of HTML and at most 100 kB of static
assets on the wire.

## 6. Requirements and documentation

- `docs/requirements/04-functional-requirements.md`, E‑1: **FR‑1.9** (M) "As the user I see that
  GitHub is being asked, and the page arrives on its own."
  AC1 While a fetch is running, `GET /` and `GET /sites` show the wait page — the mark, animated,
  with a status line naming how many repositories are being asked — and not a list, filters or
  tiles. AC2 The page the visitor asked for, with its query, replaces the wait page without any
  action: with JavaScript by polling every 500 ms and swapping the page in once the fetch has
  ended, without it by reloading every two seconds. AC3 A fetch that fails ends the waiting the same
  way, with the page's error notice and the previous or empty list. AC4 With reduced motion requested
  the mark neither rotates nor scales, and the status line is shown regardless; colour is never the
  only signal. AC5 An htmx request other than the poll — the filter form — is answered from the
  current list during a fetch, never with the wait page.
- `docs/requirements/05-quality-requirements.md`: **QS‑2.6** — Context: a list that is empty or
  stale; Stimulus: a page view while the source has not answered; Response: the page answers without
  waiting for the source; Measure: with a source that blocks until a test releases it, `GET /`
  answers within 200 ms, asserted by `TestPageNeverWaitsForGitHub`. **QS‑2.7** — Context: the
  representative configuration (10 repositories); Stimulus: a fetch against a source that answers
  each request after 200 ms; Response: the requests run side by side; Measure: the fetch completes
  within 1 s, asserted by `TestFetchRunsRepositoriesSideBySide`. QS‑2.3 gains the wait page's
  byte measure (§5.5). QS‑3.5 unchanged.
- `docs/decisions/0011-request-triggered-fetch-never-a-ticker.md`: Context — a page that waits for
  GitHub is blank for as long as GitHub takes, and C‑3 forbids a process that works between
  requests. Options — block the page (today); a goroutine the request starts, detached and bounded,
  with a wait page (chosen); serve the stale list and refetch behind it; a background ticker.
  Consequences — good: the page answers in milliseconds and the machine still stops when idle; bad:
  a stale list is withheld for the length of a fetch, by decision; neutral: the goroutine can outlive
  the request that started it by at most the fetch budget. `docs/decisions/README.md` gains the row.
- `docs/requirements/06-glossary.md`: **Wait page** — the page shown in place of the list or the
  tiles while a fetch is running: the mark, animated, a status line, and a poll that replaces it with
  the page once the fetch has ended. **Cold start** amended: the fetch that follows is shown, not
  waited for.
- `README.md`: one sentence under the page description.
- Code comments citing the sequential loop, the blocking fetch, or "a few seconds" —
  `internal/snapshot`'s package comment and `fetchBudget`, `cmd/zorgscope/main.go`'s package
  comment, `config/zorgscope.yaml`'s QS‑3.5 note, `deploy/fly.toml`'s header, ADR‑0010's
  consequence about the first view — are brought in line.

## 7. Testing

Test-first, each task's tests written before its code, run under the race detector as `make check`
does.

- **Adapter** (`internal/adapters/github`): `TestFetchRunsRepositoriesSideBySide` (QS‑2.7) against a
  handler that sleeps 200 ms per request; `TestFetchKeepsConfigurationOrder` asserts the item
  sequence of a three-repository fixture equals the sequential result; the existing partial-failure,
  cursor and page-cap tests pass unchanged; `TestGraphQLRequestBudget` still counts 20;
  `TestFetchStopsWhenCancelled` asserts a cancelled context returns promptly with every goroutine
  finished.
- **Snapshot** (`internal/snapshot`): with a source that blocks on a channel —
  `TestGetReturnsAtOnceWhileFetching` (`Fetching` true, previous items returned, returns within
  the test's patience), `TestManyCallersShareOneFetch` (ten concurrent `Get`s, one source call),
  `TestFetchedListLandsAfterRelease`, `TestInvalidateDuringFetchDoesNotStartAnother`, and the
  existing failure, partial and seen-mark tests adapted to release the source and wait for the
  result.
- **Web** (`internal/web`): `TestPageNeverWaitsForGitHub` (QS‑2.6); `TestWaitPageWhileFetching`
  (200, the mark, the status line with the repository count, the poller with the page's own path and
  query, the noscript refresh, the CSP header); `TestPollAnswers204WhileFetching`;
  `TestPollReceivesThePageAfterTheFetch` (with a `<title>`); `TestFilterRequestDuringFetchIsServed`;
  `TestSitesViewWaitsToo`; `TestWaitPageStaysInsideItsBudget` (QS‑2.3); the contrast test extended
  to the status text in both appearances.
- **Real GitHub**: `make backend` with a fresh token (the one in the local `.env` is expired —
  GitHub answers 401 to it), a cold `GET /` timed by hand, and `fly logs` after `make deploy`.

## 8. Delivery

Branch `feat/fast-first-view` off `main` at `bfdd8fe`. Commits cite FR‑1.9, QS‑2.6, QS‑2.7 and, for
the adapter, FR‑1.4 and QS‑3.5. Suggested order: adapter fan-out; snapshot cache; the large mark and
the stylesheet; the wait page, the poll and the handlers; documents; the real-GitHub check and
deploy. Expected result at the end: a cold visit shows the wait page after about 1.8 s and the list
about a second later; a warm visit after the TTL shows the wait page for under a second.

## 9. Out of scope, deliberately

- The settle-to-twelve-o'clock deceleration and the idle orbit from the motion study: both need the
  Web Animations API or a page that stays. The stylesheet keeps the state attribute so they can come
  later without renaming.
- Stale-while-revalidate: rejected in §2. Fly's `suspend` stop mode: a separate one-line experiment
  that also needs the cache's TTL comparison moved off the monotonic clock; not part of this design.
- A combined GraphQL query, a persisted last-known state, an always-on machine, a warming ping, a
  change to `cache_ttl`.
