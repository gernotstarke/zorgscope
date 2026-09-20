# The cogwheel: per-visitor settings — design

Date: 2026-09-20. Status: approved by Gernot in conversation (landing view, quiet threshold and the
warm-start switch; a popover rather than a page — his choices; he declined a summaries toggle).
Stacks on `feat/warm-start` (`2026-09-20-warm-start-design.md`), whose `localStorage` placeholder
the third setting governs. Changes no other decision: no database, no ticker, no second renderer,
no inline script or style.

## 1. Goal

`layout.html` has carried an inert cogwheel since FR‑1.11, with a comment explaining itself:

> the settings control, deliberately inert: there is no configuration page in v1 — configuration is
> a YAML file and Fly secrets (FR‑8.1) — and the button says so rather than leading to a 404

This design gives it something to open. It does not make the cogwheel a general configuration
surface: the YAML file stays exactly as authoritative as it is.

## 2. The test that decided the contents

**Does a setting belong to the deployment, or to the person looking at it?**

The question is forced rather than stylistic. [ADR‑0010](../../decisions/0010-stateless-no-database.md)
removed persistence, so a deployment-level setting has nowhere to live but
`config/zorgscope.yaml` and changing it means a redeploy. A per-person setting, on the other hand,
is nearly free: it rides the pattern `zorgscope_theme` already uses — a plain form post, a cookie, a
redirect, no server state, no JavaScript, no CSP question.

So the cogwheel holds per-person preferences and nothing else. That is what makes it cheap.

### What went in

| Setting | Cookie | Why it is the visitor's |
|---|---|---|
| Landing view | `zorgscope_landing` | Which of three equal views you want to arrive at is taste. |
| Quiet threshold | `zorgscope_quiet` | `QuietAfter`'s own comment admits it is a judgment call. |
| Keep my last list in this browser | `zorgscope_cache` | It governs storage in *that* browser and nowhere else. |

### What stayed in YAML, and why

`github.repos` and `github.sites` are the tempting ones and are refused twice over. QS‑3.5 budgets
twenty GraphQL queries per page view and the fetcher spends two per repository, so the list cannot
grow past ten however it is edited; and editing it from the page needs exactly the persistence
ADR‑0010 deleted on purpose. A redeploy for a list that changes twice a year is the right trade.
`cache_ttl`, `timezone` and `auth_repo` are facts about the deployment rather than preferences of
the reader.

### What was refused outright

* **An auto-refresh interval.** [ADR‑0011](../../decisions/0011-request-triggered-fetch-never-a-ticker.md)
  is titled "never a ticker". A dropdown offering 1, 5 or 15 minutes is a ticker with a nicer hat.
* **Label colours.** FR‑1.10 AC3 fixes six names to a palette deliberately; making it configurable
  trades a decided thing for a fiddly one.
* **"Sign out everywhere."** That is rotating `GITHUB_OAUTH_CLIENT_SECRET`, a Fly secret operation.
  The popover may *say* so; it must not pretend to do it.

## 3. Storage and the route

Three cookies, each on the `handleTheme` pattern: `Path=/`, a year, `HttpOnly`, `Secure`,
`SameSite=Lax`.

None is signed, and that is deliberate. A session cookie is signed because it is a claim about
who you are; these are claims about what you would like to look at, and the worst a hand-edited
value can do is show its owner a different view of their own dashboard. What they do need is
`parseTheme`'s defensive shape: every reader maps an unrecognised value to the default, so a
hand-edited cookie can never put arbitrary text into the document.

One route, **`POST /settings`**, taking `name`, `value` and `return`, rather than three routes —
QS‑4.1's route table grows by one line instead of three, and the table is a test. It is
`authSessionFragment`, unlike `POST /theme`: the appearance switch is public because the sign-in
page is the one page where appearance is all there is, and none of these three settings has any
meaning to a visitor who is not signed in.

`return` passes through `safeReturn` exactly as the theme form's does, and the handler appends
`#settings` to what comes back (§5).

## 4. The three settings

### 4.1 Landing view

`zorgscope_landing` is `list`, `sites` or `contributors`; absent means `list`. `GET /` redirects
with 303 when it is not `list`.

One wrinkle is worth stating because getting it wrong would quietly undo work already done. A
redirect handler that only redirects delays the GitHub fetch by a round trip: the fetch is started
by whichever handler calls `s.cache.Get`, and on a cold Machine that is the difference
`feat/warm-start` exists to shave. So the redirect calls `s.cache.Get` on its way past. One line,
and the fetch starts exactly as early as it does today.

The view switch keeps pointing at `/`, `/sites` and `/contributors` as it does now. "List" is still
`/`, which still renders the list for a visitor whose landing view is `list`, and for anyone else
`/` is simply a door that leads somewhere else — so the switch's own `aria-current` logic is
untouched.

### 4.2 Quiet threshold

`zorgscope_quiet` is `30`, `90`, `180` or `never`; absent means `90`, which is today's behaviour.

`domain.QuietAfter` stops being a constant and becomes a parameter:

```go
// IsQuiet reports whether nothing has happened to the item for after or longer, measured from its
// last update to now. An item whose update time is unknown is never quiet: unknown is not idle.
// An after of zero means nothing is ever quiet, which is what the "never" setting asks for.
func (i Item) IsQuiet(now time.Time, after time.Duration) bool {
	return after > 0 && !i.UpdatedAt.IsZero() && now.Sub(i.UpdatedAt) >= after
}
```

The domain stays pure — it already takes `now` as a parameter for exactly this reason (QS‑5.1) — and
`QuietAfter` remains as the default the web layer falls back to, so the number keeps one home.
There are two call sites, both already in the web layer with `now` in hand: `search.go` and
`dashboard.go`. Nothing threads through `DashboardInput`.

### 4.3 Keep my last list in this browser

`zorgscope_cache` is `on` or `off`; absent means `on`.

**Turning it off is the clear.** `warm-start.js` already branches to `clear()`, so a page rendered
with the setting off routes to that branch and the stored list is gone on the next page view. A
separate "forget now" button would do the same thing by a second name, and would raise the question
of what "off" means if the list is still there.

The page carries the state as `data-cache="off"` on `<html>`, beside `data-cache-epoch`. The script
checks it first, before the sign-out marker and the placeholder mount, and clears.

With JavaScript off the setting is inert and harmless: nothing was ever stored.

## 5. The popover

`<details class="settings">` with the cog as its `<summary>`, holding one small form per setting.
No JavaScript, no CSP question, and `<details>` brings its own keyboard and screen-reader behaviour
— the same argument the inert button's comment already makes for being a `<button>` rather than a
styled `<span>`.

A form post reloads the page, so the popover would close on every change. The fix needs no script:
the handler redirects to `<return>#settings`, and a browser opens a `<details>` that contains the
fragment target. `safeReturn` never sees the fragment, because the handler appends it after
sanitising.

**The appearance switch moves inside the popover.** The navbar is otherwise carrying two icon
buttons for one idea, and appearance is a per-visitor preference like the other three. `POST /theme`
stays exactly as it is — public, its own route, its own cookie — because the sign-in page still
needs it and has no popover to put it in. So the sign-in page keeps the bare sun/moon control and
signed-in pages show it inside the cog; `layout.html` already branches on `.Chrome` for precisely
this kind of difference.

The cogwheel stops being `disabled`, and its title stops saying there is no configuration page.

## 6. What this does not disturb

* [ADR‑0010](../../decisions/0010-stateless-no-database.md) — nothing persists server-side; three
  cookies are three strings the browser hands back.
* [ADR‑0011](../../decisions/0011-request-triggered-fetch-never-a-ticker.md) — no interval setting
  exists to create a ticker, and §4.1 keeps the fetch request-triggered and just as early.
* [ADR‑0013](../../decisions/0013-stale-while-revalidating.md) — the cache setting governs the
  placeholder, never the server-side stale list, which needs no permission to show what it has.
* **The CSP** — no inline script, no inline style, no new asset.

## 7. Requirements

**FR‑1.12** (new, E‑1): *As the user I set how the dashboard greets me, and it remembers.* AC1 A
control in the top bar opens a panel holding the landing view, the quiet threshold, the browser-list
setting and the appearance, and needs no JavaScript to open, change or close. AC2 Each setting is
stored in a cookie of its own for a year; an absent or unrecognised value is the default — `list`,
90 days, on — and no value from a cookie is ever rendered as text. AC3 The landing view decides
where `GET /` goes, and asking GitHub starts no later than it does without the setting. AC4 The
quiet threshold decides which items are marked quiet (FR‑1.10 AC4), and `never` marks none. AC5
Turning the browser list off removes what is stored (FR‑1.9 AC7).

**FR‑8.1** gains a sentence: configuration is the YAML file and the Fly secrets, plus per-visitor
preferences held in cookies, which name nothing the deployment depends on.

No ADR. This adds no decision the design above does not simply follow from ADR‑0010 and ADR‑0011.

## 8. Testing

* `POST /settings` sets each cookie, rejects an unknown `name` or `value` by falling back to the
  default, redirects through `safeReturn`, and appends `#settings`.
* An unrecognised cookie value renders the default, and never reaches the document as text.
* `GET /` redirects for `sites` and `contributors`, does not for `list` or an absent cookie, and
  the redirect starts a fetch on a cold cache.
* `IsQuiet` with 30, 90, 180 and 0 (domain, table-driven).
* The quiet cookie changes which items carry the quiet mark on `/` and `/search`.
* `data-cache="off"` appears iff the cookie says so.
* The cog is not `disabled`; the popover holds four controls; the sign-in page has the bare theme
  control and no popover.
* Every existing budget and route-table test stays green unmodified.
