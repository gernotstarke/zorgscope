# Cogwheel Settings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The inert cogwheel in the top bar opens a popover holding four per-visitor preferences — landing view, quiet threshold, keeping the browser's last list, and appearance — each stored in a cookie.

**Architecture:** Three new cookies on the existing `handleTheme` pattern (plain form post → cookie → redirect; no JavaScript, no server state, ADR-0010 intact), served by one new route `POST /settings`. `domain.QuietAfter` becomes a parameter of `IsQuiet`. The popover is a `<details>` element; it survives a form post because the handler redirects to `<return>#settings` and browsers open a `<details>` containing the fragment target.

**Tech Stack:** Go standard library only in `internal/domain` (QS-5.1); `html/template`; no new dependencies; Docker-only toolchain via `make`.

**Spec:** `docs/superpowers/specs/2026-09-20-cogwheel-settings-design.md`

## Global Constraints

- **Branch:** `feat/cogwheel-settings`, already created, stacked on `feat/warm-start` at `04585d1`. Do not rebase or merge anything.
- **No new dependencies.** `go.mod` is not touched. No new static asset, no new JavaScript.
- **CSP:** `default-src 'self'; … script-src 'self'; style-src 'self'` with no `'unsafe-inline'` (`internal/web/server.go:88`). No inline `<script>`, no `style=` attribute. The popover must work with JavaScript disabled.
- **Never render a cookie value as text.** Every cookie reader maps an unrecognised value to the default, the way `parseTheme` does (`internal/web/theme.go:37`). A hand-edited cookie must not be able to put arbitrary text into the document.
- **Domain purity:** `internal/domain` imports nothing but the standard library and calls no `time.Now()` — every function needing the clock takes it as a parameter.
- **Budgets that must stay green, unmodified:** `TestRenderedPageStaysInsideItsBudget`, `TestStaticAssetsFitTheirBudgetOnTheWire`, `TestWaitPageStaysInsideItsBudget`, `TestSitesPageStaysInsideItsBudget`, `TestContributorsPageStaysInsideItsBudget`, and the QS-4.1 route-table test in `internal/web/auth_test.go`.
- **Run tests with:**
  `docker run --rm -t -v "$PWD":/src -w /src -v zorgscope-gomod:/go/pkg/mod -v zorgscope-gocache:/root/.cache/go-build golang:1.26 go test ./internal/web/ -timeout 60s`
  Swap `./internal/web/` for `./internal/domain/` where the task says so. The full gate is `make check`.
- **Attribution:** every commit message ends with `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.
- **Prose style:** this codebase comments heavily and explains *why*, in full sentences, British spelling (`colour`, `behaviour`, `recognises`). Match the density of the file you are editing. A comment that restates the code is worse than none; a comment naming the requirement id or the reason a choice was made is what belongs.

---

## File Structure

| File | Responsibility | Task |
|---|---|---|
| `internal/domain/item.go` | `IsQuiet` takes the threshold; `QuietAfter` stays as the default | 1 |
| `internal/domain/item_test.go` | The threshold table | 1 |
| `internal/web/settings.go` (new) | The three cookies: names, parsing, reading, `handleSettings` | 2 |
| `internal/web/settings_test.go` (new) | Cookie round-trip, defaults, redirect, fragment | 2 |
| `internal/web/server.go` | The route; `pageData` gains the settings; `execute` fills them | 2, 4 |
| `internal/web/dashboard.go` | `handleDashboard` redirects on landing view; quiet threshold at the call site | 3 |
| `internal/web/search.go` | Quiet threshold at the call site | 3 |
| `internal/web/templates/layout.html` | The popover; the cog enabled; the theme control moved inside | 4 |
| `internal/web/static/app.css` | Popover styling | 4 |
| `internal/web/static/warm-start.js` | Honour `data-cache="off"` | 4 |
| `docs/requirements/04-functional-requirements.md` | FR-1.12 added, FR-8.1 sentence | 5 |
| `internal/version/version.go` | `0.5.0` → `0.6.0` | 5 |

Tasks 1–3 are behaviour with no visible control; task 4 is the control. Each task ends green and committed.

---

### Task 1: The quiet threshold becomes a parameter

**Files:**

- Modify: `internal/domain/item.go:54-64`
- Test: `internal/domain/item_test.go`

**Interfaces:**

- Produces: `func (i Item) IsQuiet(now time.Time, after time.Duration) bool`. `QuietAfter` stays exported as the default. `after <= 0` means nothing is ever quiet.

- [ ] **Step 1: Write the failing test**

Add to `internal/domain/item_test.go`:

```go
// The threshold is a parameter because it is a preference (FR-1.12 AC4), not a property of an
// item. Zero means nothing is ever quiet, which is what the "never" setting asks for — and it has
// to be the zero value's meaning, because a caller that forgets to pass one must not silently
// mark everything quiet.
func TestIsQuietHonoursTheThreshold(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	item := func(daysAgo int) domain.Item {
		return domain.Item{UpdatedAt: now.AddDate(0, 0, -daysAgo)}
	}
	day := 24 * time.Hour

	for _, tc := range []struct {
		name     string
		daysAgo  int
		after    time.Duration
		wantQuiet bool
	}{
		{"60 days against 30 is quiet", 60, 30 * day, true},
		{"60 days against 90 is not", 60, 90 * day, false},
		{"exactly at the threshold is quiet", 90, 90 * day, true},
		{"a day short of it is not", 89, 90 * day, false},
		{"180 days against 180 is quiet", 180, 180 * day, true},
		{"never: a year old is still not quiet", 365, 0, false},
		{"never: even a decade", 3650, 0, false},
	} {
		if got := item(tc.daysAgo).IsQuiet(now, tc.after); got != tc.wantQuiet {
			t.Errorf("%s: IsQuiet = %v, want %v", tc.name, got, tc.wantQuiet)
		}
	}

	// An unknown update time is never quiet, whatever the threshold: unknown is not idle.
	if (domain.Item{}).IsQuiet(now, 30*day) {
		t.Error("an item with no update time was called quiet")
	}
}
```

Then update every **existing** call of `IsQuiet` in `internal/domain/item_test.go` to pass `domain.QuietAfter` as the second argument, so the current assertions keep their current meaning.

- [ ] **Step 2: Run it to verify it fails**

Run: `docker run --rm -t -v "$PWD":/src -w /src -v zorgscope-gomod:/go/pkg/mod -v zorgscope-gocache:/root/.cache/go-build golang:1.26 go test ./internal/domain/ -run TestIsQuiet -v`

Expected: build failure — "too many arguments in call to IsQuiet".

- [ ] **Step 3: Change the signature**

In `internal/domain/item.go`, replace the `IsQuiet` method and adjust `QuietAfter`'s comment:

```go
// QuietAfter is the default threshold: how long an item may go without an update before the page
// calls it quiet (FR-1.10 AC4). Three months: long enough that a maintainer's own pause does not
// trip it, short enough that a forgotten issue shows up before the year is out. It is the value
// the web layer falls back to when the visitor has expressed no preference (FR-1.12 AC4), so the
// number keeps one home even though the threshold is now chosen per visitor.
const QuietAfter = 90 * 24 * time.Hour

// IsQuiet reports whether nothing has happened to the item for after or longer, measured from its
// last update to now. An item whose update time is unknown is never quiet: unknown is not idle.
//
// An after of zero or less means nothing is ever quiet. That is what the "never" setting asks
// for, and making it the zero value's meaning is deliberate: a caller that forgot to pass a
// threshold marks nothing rather than marking everything.
func (i Item) IsQuiet(now time.Time, after time.Duration) bool {
	return after > 0 && !i.UpdatedAt.IsZero() && now.Sub(i.UpdatedAt) >= after
}
```

- [ ] **Step 4: Fix the two call sites so the package builds**

`internal/web/dashboard.go` — find `Quiet:   it.IsQuiet(now),` and make it `Quiet:   it.IsQuiet(now, domain.QuietAfter),`.
`internal/web/search.go` — find `Quiet:   h.Item.IsQuiet(now),` and make it `Quiet:   h.Item.IsQuiet(now, domain.QuietAfter),`.

Both keep today's behaviour exactly; task 3 replaces `domain.QuietAfter` with the visitor's choice.

- [ ] **Step 5: Run both packages**

Run the domain tests, then `./internal/web/`.

Expected: PASS in both. Domain coverage must stay at or above 90 %.

- [ ] **Step 6: Commit**

```bash
git add internal/domain/item.go internal/domain/item_test.go internal/web/dashboard.go internal/web/search.go
git commit -F - <<'EOF'
refactor(domain): the quiet threshold is a parameter, not a constant (FR-1.10, FR-1.12)

How long an item may go untouched before the page calls it quiet is a judgment call, as
QuietAfter's own comment has always admitted, so it becomes something the caller passes. The
constant stays as the default the web layer falls back to. Zero means nothing is ever quiet, so a
caller that forgets a threshold marks nothing rather than everything.

No behaviour changes: both call sites pass the old constant.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

### Task 2: The cookies and `POST /settings`

**Files:**

- Create: `internal/web/settings.go`, `internal/web/settings_test.go`
- Modify: `internal/web/server.go` (route table ~line 322; `pageData` ~line 706; `execute` ~line 780)

**Interfaces:**

- Produces:

```go
type landing string // "list" | "sites" | "contributors"
const (landingList landing = "list"; landingSites landing = "sites"; landingContributors landing = "contributors")

type settings struct {
	Landing landing
	Quiet   time.Duration // 0 means never
	Cache   bool          // keep the last list in this browser
}

func settingsOf(r *http.Request) settings      // never fails; unknown values become defaults
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request)
```

- `pageData.Settings settings`, filled by `execute` for every page.

- [ ] **Step 1: Write the failing test**

Create `internal/web/settings_test.go`:

```go
package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// The defaults are today's behaviour, so a visitor who has never opened the popover sees exactly
// what they saw before it existed.
func TestSettingsDefaultsAreTodaysBehaviour(t *testing.T) {
	got := settingsOf(httptest.NewRequest(http.MethodGet, "/", nil))
	if got.Landing != landingList {
		t.Errorf("Landing = %q, want %q", got.Landing, landingList)
	}
	if got.Quiet != domain.QuietAfter {
		t.Errorf("Quiet = %v, want %v", got.Quiet, domain.QuietAfter)
	}
	if !got.Cache {
		t.Error("Cache = false, want true: the browser list is on unless it is turned off")
	}
}

// A cookie is a string the browser hands back and anyone can edit. Nothing it can say may reach
// the document as text, and anything unrecognised is the default rather than an error.
func TestSettingsRefuseNonsense(t *testing.T) {
	for _, value := range []string{
		"<script>alert(1)</script>", "sites; DROP", "", "LIST", "../../etc", "99999999999999999999",
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: landingCookieName, Value: value})
		r.AddCookie(&http.Cookie{Name: quietCookieName, Value: value})
		if got := settingsOf(r); got.Landing != landingList || got.Quiet != domain.QuietAfter {
			t.Errorf("cookie %q gave Landing=%q Quiet=%v, want the defaults", value, got.Landing, got.Quiet)
		}
	}
}

func TestSettingsReadEachCookie(t *testing.T) {
	day := 24 * time.Hour
	for _, tc := range []struct {
		name, cookie, value string
		check               func(settings) bool
	}{
		{"landing sites", landingCookieName, "sites", func(s settings) bool { return s.Landing == landingSites }},
		{"landing contributors", landingCookieName, "contributors", func(s settings) bool { return s.Landing == landingContributors }},
		{"quiet 30", quietCookieName, "30", func(s settings) bool { return s.Quiet == 30*day }},
		{"quiet 180", quietCookieName, "180", func(s settings) bool { return s.Quiet == 180*day }},
		{"quiet never", quietCookieName, "never", func(s settings) bool { return s.Quiet == 0 }},
		{"cache off", cacheCookieName, "off", func(s settings) bool { return !s.Cache }},
		{"cache on", cacheCookieName, "on", func(s settings) bool { return s.Cache }},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: tc.cookie, Value: tc.value})
		if !tc.check(settingsOf(r)) {
			t.Errorf("%s: cookie %s=%q was not read", tc.name, tc.cookie, tc.value)
		}
	}
}

// The handler stores one setting and sends the visitor back to the page they were on, with the
// fragment that reopens the popover (FR-1.12 AC1).
func TestSettingsHandlerStoresAndReturns(t *testing.T) {
	h := newTestServer(t).Handler()
	c := signIn(t, h)

	form := url.Values{"name": {"landing"}, "value": {"sites"}, "return": {"/contributors"}}
	rec := postAs(h, "/settings", form, c)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /settings = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/contributors#settings"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	var stored *http.Cookie
	for _, sc := range rec.Result().Cookies() {
		if sc.Name == landingCookieName {
			stored = sc
		}
	}
	if stored == nil {
		t.Fatal("no landing cookie was set")
	}
	if stored.Value != "sites" {
		t.Errorf("landing cookie = %q, want %q", stored.Value, "sites")
	}
	if !stored.HttpOnly || !stored.Secure || stored.SameSite != http.SameSiteLaxMode || stored.Path != "/" {
		t.Errorf("landing cookie flags are wrong: %+v", stored)
	}
}

// An open redirect is the risk any return field carries, and safeReturn is the answer the theme
// form already uses. The fragment is appended after sanitising, never taken from the request.
func TestSettingsHandlerRefusesAForeignReturn(t *testing.T) {
	h := newTestServer(t).Handler()
	c := signIn(t, h)
	for _, bad := range []string{"https://elsewhere.example", "//elsewhere.example", "/\\elsewhere"} {
		form := url.Values{"name": {"landing"}, "value": {"list"}, "return": {bad}}
		rec := postAs(h, "/settings", form, c)
		if got := rec.Header().Get("Location"); got != "/#settings" {
			t.Errorf("return %q redirected to %q, want %q", bad, got, "/#settings")
		}
	}
}

// An unknown name changes nothing rather than failing: the form is the only thing that posts here,
// so an unknown name is a bug or a probe, and neither deserves a 500.
func TestSettingsHandlerIgnoresAnUnknownName(t *testing.T) {
	h := newTestServer(t).Handler()
	c := signIn(t, h)
	rec := postAs(h, "/settings", url.Values{"name": {"nonsense"}, "value": {"x"}, "return": {"/"}}, c)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("POST /settings with an unknown name = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("an unknown setting name set a cookie")
	}
}

// Nothing a cookie says reaches the page as text.
func TestNoCookieValueIsRendered(t *testing.T) {
	h := newTestServer(t).Handler()
	c := signIn(t, h)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	req.AddCookie(&http.Cookie{Name: landingCookieName, Value: "NEEDLE-abc"})
	req.AddCookie(&http.Cookie{Name: quietCookieName, Value: "NEEDLE-def"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "NEEDLE") {
		t.Error("a cookie's value was rendered into the page")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Expected: build failure — `undefined: settingsOf`, `undefined: landingCookieName`, and so on.

- [ ] **Step 3: Write `internal/web/settings.go`**

```go
package web

import (
	"net/http"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// The visitor's own preferences, and the route that records them.
//
// Every one of these is a preference of the person reading rather than a fact about the
// deployment, which is the whole test for what may live here: a deployment fact has nowhere to go
// but config/zorgscope.yaml, because this application keeps no state of its own (ADR-0010), while
// a preference rides the pattern the appearance switch already uses — a form post, a cookie, a
// redirect. No JavaScript, no server state, nothing to migrate.
//
// None of the cookies is signed. The session cookie is signed because it is a claim about who you
// are; these are claims about what you would like to look at, and the worst a hand-edited value
// can do is show its own owner a different view of their own dashboard. What they do need is for
// every reader to be total: an unrecognised value is the default, never an error and never text
// that reaches the page.
const (
	landingCookieName = "zorgscope_landing"
	quietCookieName   = "zorgscope_quiet"
	cacheCookieName   = "zorgscope_cache"

	// settingsTTL matches themeTTL and for the same reason: these are preferences, not sessions,
	// and being asked again every fortnight would be worse than the cookie living a while.
	settingsTTL = 365 * 24 * time.Hour

	// settingsFragment reopens the popover after the redirect. A <details> element is opened by
	// the browser when the fragment it is navigated to lies inside it, so a form post that would
	// otherwise close the panel leaves it open — with no script, which the policy would forbid.
	settingsFragment = "#settings"
)

// landing is which view GET / leads to.
type landing string

// The three landing views, which are exactly the three the view switch offers.
const (
	landingList         landing = "list"
	landingSites        landing = "sites"
	landingContributors landing = "contributors"
)

// path is where this landing view lives. landingList is "/" itself, so it is the one value that
// never redirects.
func (l landing) path() string {
	switch l {
	case landingSites:
		return "/sites"
	case landingContributors:
		return "/contributors"
	default:
		return "/"
	}
}

// parseLanding reads a landing view from untrusted text. Anything unrecognised is landingList,
// which is where the dashboard has always opened.
func parseLanding(s string) landing {
	switch landing(s) {
	case landingSites:
		return landingSites
	case landingContributors:
		return landingContributors
	default:
		return landingList
	}
}

// quietChoices are the thresholds the popover offers, in the order it offers them. "never" is 0,
// which domain.IsQuiet reads as marking nothing.
var quietChoices = []struct {
	Value string
	Label string
	After time.Duration
}{
	{"30", "30 days", 30 * 24 * time.Hour},
	{"90", "90 days", domain.QuietAfter},
	{"180", "180 days", 180 * 24 * time.Hour},
	{"never", "never", 0},
}

// parseQuiet reads a threshold from untrusted text. Only the offered values are accepted — not any
// number that parses — so the cookie cannot ask for a threshold the popover could not then show
// back, and cannot be used to put a large integer anywhere near a duration.
func parseQuiet(s string) time.Duration {
	for _, c := range quietChoices {
		if c.Value == s {
			return c.After
		}
	}
	return domain.QuietAfter
}

// settings is what a request's cookies say the visitor prefers.
type settings struct {
	// Landing is the view GET / leads to (FR-1.12 AC3).
	Landing landing
	// Quiet is how long an item may go untouched before it is marked quiet, or 0 for never
	// (FR-1.12 AC4).
	Quiet time.Duration
	// Cache is whether this browser may keep the last list it was shown (FR-1.12 AC5). When it is
	// false the page says so and warm-start.js clears what it holds.
	Cache bool
}

// settingsOf reads a request's preferences. It cannot fail: every field has a default, and every
// default is what the page did before the popover existed.
func settingsOf(r *http.Request) settings {
	s := settings{Landing: landingList, Quiet: domain.QuietAfter, Cache: true}
	if c, err := r.Cookie(landingCookieName); err == nil {
		s.Landing = parseLanding(c.Value)
	}
	if c, err := r.Cookie(quietCookieName); err == nil {
		s.Quiet = parseQuiet(c.Value)
	}
	// Only the exact word "off" turns it off. Anything else — a typo, a truncated value, a
	// hand-edited cookie — leaves the visitor with the behaviour they had.
	if c, err := r.Cookie(cacheCookieName); err == nil && c.Value == "off" {
		s.Cache = false
	}
	return s
}

// QuietChoice is one row of the quiet control, for the template: which value it posts, what it
// says, and whether it is the one in force.
type QuietChoice struct {
	Value    string
	Label    string
	Selected bool
}

// QuietChoices is the quiet control's rows, for layout.html. It is a method on pageData rather
// than a field so that the "which one is selected" comparison happens in Go, where it is testable,
// and the template only prints.
func (d pageData) QuietChoices() []QuietChoice {
	out := make([]QuietChoice, 0, len(quietChoices))
	for _, c := range quietChoices {
		out = append(out, QuietChoice{Value: c.Value, Label: c.Label, Selected: c.After == d.Settings.Quiet})
	}
	return out
}

// LandingIs reports whether the visitor's landing view is the named one, for the template's
// radio buttons.
func (d pageData) LandingIs(name string) bool { return string(d.Settings.Landing) == name }

// CacheAttr is the document's data-cache value: "off" when this browser may not keep its last
// list, and empty otherwise, so the attribute is absent in the ordinary case. warm-start.js reads
// it before anything else and clears what it holds.
func (d pageData) CacheAttr() string {
	if d.Settings.Cache {
		return ""
	}
	return "off"
}

// handleSettings records one preference and sends the visitor back to the page they were on.
//
// One route for three settings rather than three routes: the route table is a test (QS-4.1), and
// three near-identical entries would be three chances for one of them to be registered with the
// wrong credential. The form names which setting it is changing.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	var c *http.Cookie
	switch r.FormValue("name") {
	case "landing":
		c = s.settingCookie(landingCookieName, string(parseLanding(r.FormValue("value"))))
	case "quiet":
		// The value is written back through the same table it is read through, so a value the
		// popover does not offer stores the default rather than itself.
		want := r.FormValue("value")
		stored := "90"
		for _, choice := range quietChoices {
			if choice.Value == want {
				stored = choice.Value
			}
		}
		c = s.settingCookie(quietCookieName, stored)
	case "cache":
		stored := "on"
		if r.FormValue("value") == "off" {
			stored = "off"
		}
		c = s.settingCookie(cacheCookieName, stored)
	}
	// An unknown name changes nothing. The popover is the only thing that posts here, so an
	// unknown name is a bug or a probe, and neither is worth a 500 or a cookie.
	if c != nil {
		http.SetCookie(w, c)
	}

	// safeReturn is the sanitiser between the form field and the Location header — see its own
	// comment. The fragment is appended afterwards, by this code, so it can never be something the
	// request asked for.
	target := safeReturn(r.FormValue("return")) + settingsFragment
	http.Redirect(w, r, target, http.StatusSeeOther) // #nosec G710 -- sanitised by safeReturn
}

// settingCookie is one preference cookie, with the flags every cookie this site sets carries.
func (s *Server) settingCookie(name, value string) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		Expires:  s.clock.Now().Add(settingsTTL),
		MaxAge:   int(settingsTTL / time.Second),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}
```

- [ ] **Step 4: Register the route and carry the settings to the page**

In `internal/web/server.go`, add to `routes()` immediately after the `POST /logout` line:

```go
		// The visitor's own preferences (FR-1.12). authSessionFragment rather than the public
		// treatment POST /theme gets: the appearance switch is public because the sign-in page is
		// the one page where appearance is all there is, and none of these three settings means
		// anything to a visitor who is not signed in.
		{http.MethodPost, "/settings", authSessionFragment, s.handleSettings, ""},
```

Add to `pageData`, next to `Theme`:

```go
	// Settings is what the visitor's preference cookies say (FR-1.12). execute fills it in for
	// every page, the way it fills in Theme, because the popover is drawn by the shared layout and
	// a page that lost it would draw every control in its default position.
	Settings settings
```

In `execute`, beside `data.Theme = themeOf(r)`:

```go
	data.Settings = settingsOf(r)
```

- [ ] **Step 5: Run the tests**

Run the `./internal/web/` suite.

Expected: PASS, including all six new tests. If `TestNoCookieValueIsRendered` fails, a cookie value is reaching the template — find it and route it through a parser.

- [ ] **Step 6: Run the full gate and commit**

Run `make check`, then:

```bash
git add internal/web/settings.go internal/web/settings_test.go internal/web/server.go
git commit -F - <<'EOF'
feat(web): the visitor's own preferences, in three cookies and one route (FR-1.12)

Landing view, quiet threshold and whether this browser may keep its last list. Each is a
preference of the person reading rather than a fact about the deployment, which is the test for
what may live here at all: a deployment fact has nowhere to go but the YAML file, because this
application keeps no state of its own (ADR-0010).

None of the cookies is signed, because none is a claim about identity; every reader is total
instead, so a hand-edited value is the default rather than an error or text on the page. One route
for three settings, because the route table is a test and three near-identical entries would be
three chances to register one with the wrong credential.

Nothing reads these yet.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

### Task 3: The settings take effect

**Files:**

- Modify: `internal/web/dashboard.go` (`handleDashboard`, and the `Quiet:` call site ~line 488)
- Modify: `internal/web/search.go` (the `Quiet:` call site ~line 82)
- Test: `internal/web/settings_test.go`

**Interfaces:**

- Consumes: `settingsOf(r)` from task 2; `IsQuiet(now, after)` from task 1.

- [ ] **Step 1: Write the failing tests**

Append to `internal/web/settings_test.go`:

```go
// FR-1.12 AC3: the landing view decides where GET / goes.
func TestLandingViewRedirects(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: representativeItems()})
	c := signIn(t, h)

	for _, tc := range []struct{ cookie, wantLocation string }{
		{"sites", "/sites"},
		{"contributors", "/contributors"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(c)
		req.AddCookie(&http.Cookie{Name: landingCookieName, Value: tc.cookie})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Errorf("landing %q: GET / = %d, want %d", tc.cookie, rec.Code, http.StatusSeeOther)
		}
		if got := rec.Header().Get("Location"); got != tc.wantLocation {
			t.Errorf("landing %q: Location = %q, want %q", tc.cookie, got, tc.wantLocation)
		}
	}

	// The default, and an explicit "list", both render rather than redirect — otherwise "/" would
	// bounce to itself for ever.
	for _, value := range []string{"", "list"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(c)
		if value != "" {
			req.AddCookie(&http.Cookie{Name: landingCookieName, Value: value})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("landing %q: GET / = %d, want 200", value, rec.Code)
		}
	}
}

// The redirect must not cost the fetch a round trip. A cold cache reached through a redirecting
// GET / has to be fetching by the time the redirect is written, or a landing view would quietly
// undo what the warm start bought.
func TestTheLandingRedirectStartsTheFetch(t *testing.T) {
	src := &fakeSource{items: representativeItems()}
	s, _ := newColdServerWith(t, func(o *Options) {
		o.Cache = snapshot.New(src, time.Hour, o.Clock)
	})
	h := s.Handler()
	c := signIn(t, h)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	req.AddCookie(&http.Cookie{Name: landingCookieName, Value: "sites"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GET / = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if src.CallCount() == 0 {
		t.Error("the redirect did not start a fetch; a landing view would cost a cold visit a round trip")
	}
}

// FR-1.12 AC4: the threshold decides what is marked quiet, and "never" marks nothing.
func TestTheQuietThresholdChangesTheMarks(t *testing.T) {
	// An item last touched 120 days ago: quiet at 30 and 90, not at 180, never at "never".
	old := ghItem(1, "Long untouched", testNow.AddDate(0, 0, -120))
	h := dashHandler(t, &fakeSource{items: []domain.Item{old}})
	c := signIn(t, h)

	for _, tc := range []struct {
		cookie    string
		wantQuiet bool
	}{
		{"30", true},
		{"90", true},
		{"180", false},
		{"never", false},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(c)
		req.AddCookie(&http.Cookie{Name: quietCookieName, Value: tc.cookie})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if got := strings.Contains(rec.Body.String(), "is-quiet"); got != tc.wantQuiet {
			t.Errorf("quiet=%s: item marked quiet = %v, want %v", tc.cookie, got, tc.wantQuiet)
		}
	}
}
```

Add `"github.com/gernotstarke/zorgscope/internal/snapshot"` to the file's imports.

- [ ] **Step 2: Run them to verify they fail**

Expected: `TestLandingViewRedirects` fails with "GET / = 200, want 303"; `TestTheQuietThresholdChangesTheMarks` fails for `180` and `never`, which are still marked quiet by the hardcoded default.

- [ ] **Step 3: Redirect on the landing view**

In `internal/web/dashboard.go`, at the very top of `handleDashboard`, before `answeredWaiting`:

```go
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	// The visitor's landing view (FR-1.12 AC3). "/" is where the list lives, so only the other
	// two redirect; landingList falling through is what keeps "/" from bouncing to itself.
	//
	// The cache is asked before redirecting, and that is the whole reason this is three lines
	// rather than one. A fetch is started by whichever handler asks the cache, so a redirect that
	// only redirected would push the first ask into the *next* request — costing a cold Machine
	// exactly the round trip ADR-0013 exists to save. Asking here starts it just as early as
	// rendering would, and the answer is deliberately discarded: the page the visitor lands on
	// asks again and will find either the fetch in flight or its result.
	if target := settingsOf(r).Landing.path(); target != "/" {
		_ = s.cache.Get(r.Context())
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}

	snap, waiting := s.answeredWaiting(w, r)
	...
```

- [ ] **Step 4: Use the threshold at both call sites**

`internal/web/dashboard.go` — the item view builder needs the threshold. Find where `newItemView(it, now)` is called inside `itemsView`, and the `Quiet: it.IsQuiet(now, domain.QuietAfter)` line inside `newItemView`. Thread the visitor's value through rather than reading a cookie deep in a view builder: add a `quiet time.Duration` parameter to `newItemView` and to `itemsView`, pass it from `dashboardView`, and give `dashboardView` the value from `render`, which has the request. Concretely, `render` already computes `now`; add beside it:

```go
	quiet := settingsOf(r).Quiet
```

and pass `quiet` down the same path `now` already travels. Do not add a second cookie read inside a view builder: the request is available in `render` and nowhere below it, and a view type that reaches for a cookie is a view type that cannot be tested without one.

`internal/web/search.go` — the same: the handler has `r`, so read `settingsOf(r).Quiet` there and pass it to wherever `h.Item.IsQuiet(now, ...)` is called.

- [ ] **Step 5: Run the tests**

Run the `./internal/web/` suite.

Expected: PASS. Existing tests that assert `is-quiet` must still pass, because the default is unchanged at 90 days.

- [ ] **Step 6: Run the full gate and commit**

```bash
git add internal/web/dashboard.go internal/web/search.go internal/web/settings_test.go
git commit -F - <<'EOF'
feat(web): the landing view and the quiet threshold take effect (FR-1.12)

GET / redirects to the view the visitor chose, and asks the cache on its way past so that a
landing view costs a cold Machine nothing — a redirect that only redirected would push the first
fetch into the next request, which is the round trip ADR-0013 exists to save.

The quiet threshold travels the path now already travels, from the handler that has the request
down to the item view. No view builder reads a cookie: a view type that reaches for one cannot be
tested without one.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

### Task 4: The popover

**Files:**

- Modify: `internal/web/templates/layout.html` (the `topbar-nav` block)
- Modify: `internal/web/static/app.css`
- Modify: `internal/web/static/warm-start.js`
- Test: `internal/web/settings_test.go`

**Interfaces:**

- Consumes: `pageData.Settings`, `pageData.QuietChoices()`, `pageData.LandingIs(name)`, `pageData.CacheAttr()` from task 2.

- [ ] **Step 1: Write the failing tests**

Append to `internal/web/settings_test.go`:

```go
// The cog opens something now, so it is no longer disabled and no longer says there is no
// configuration page.
func TestTheCogIsLive(t *testing.T) {
	h := newTestServer(t).Handler()
	page := getAuthed(t, h, "/").Body.String()

	if strings.Contains(page, "there is no configuration page") {
		t.Error("the cog still says there is no configuration page")
	}
	if !strings.Contains(page, `id="settings"`) {
		t.Error("the page has no settings panel for the cog to open")
	}
	// The panel must open without script: <details> does that, a div plus a handler does not, and
	// the policy grants no 'unsafe-inline' for the handler anyway.
	if !strings.Contains(page, "<details") {
		t.Error("the settings panel is not a <details>, so it cannot open without JavaScript")
	}
	// Every control in it posts to the one route.
	for _, want := range []string{
		`action="/settings"`, `name="landing"`, `name="quiet"`, `name="cache"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the settings panel is missing %s", want)
		}
	}
	// The appearance switch moved inside; it still posts to its own route.
	if !strings.Contains(page, `action="/theme"`) {
		t.Error("the appearance switch is gone from the signed-in page")
	}
}

// The sign-in page has no session and therefore no preferences, but appearance is all there is
// there — so it keeps the bare control and shows no panel.
func TestTheSignInPageHasNoPanel(t *testing.T) {
	page := get(newTestServer(t).Handler(), "/login").Body.String()
	if strings.Contains(page, `id="settings"`) {
		t.Error("the sign-in page shows a settings panel it has no session for")
	}
	if !strings.Contains(page, `action="/theme"`) {
		t.Error("the sign-in page lost its appearance switch")
	}
}

// FR-1.12 AC5: with the browser list turned off the document says so, which is what sends
// warm-start.js to its clearing branch.
func TestTheCacheSettingReachesTheDocument(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	c := signIn(t, h)

	on := getAs(h, "/", c).Body.String()
	if strings.Contains(on, `data-cache="off"`) {
		t.Error("the document says the browser list is off when it is on")
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	req.AddCookie(&http.Cookie{Name: cacheCookieName, Value: "off"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `data-cache="off"`) {
		t.Error("the document does not say the browser list is off")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Expected: FAIL on the missing `id="settings"` and `<details>`.

- [ ] **Step 3: Build the popover**

In `internal/web/templates/layout.html`, replace the whole `<nav class="topbar-nav">…</nav>` block. The theme form moves inside the panel for a signed-in visitor and stays bare for everyone else.

```html
  <nav class="topbar-nav">
    {{/* The appearance switch on the pages that have no popover to hold it — the sign-in page and
         the refusal page, which have no session and therefore no preferences, and where the
         appearance is the only thing there is to set. The signed-in pages draw the same form
         inside the panel below. */}}
    {{if not .Chrome}}{{template "theme-control" .}}{{end}}

    {{if .Chrome}}
    {{/* The settings panel (FR-1.12 AC1). A <details> so it opens, closes and is announced
         without a line of script, which the Content-Security-Policy would forbid in any case: it
         grants no 'unsafe-inline' (QS-4.4).

         The id is what reopens it. Every form in here redirects to <page>#settings, and a browser
         opens a <details> whose subtree contains the fragment it was sent to — so changing a
         setting leaves the panel open, with nothing scripted and nothing remembered on the
         server. */}}
    <details class="settings" id="settings">
      <summary class="icon-button settings-button" title="Settings" aria-label="Settings">
        <svg class="cog" viewBox="0 0 24 24" width="20" height="20" aria-hidden="true" focusable="false"
             fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round">
          <circle cx="12" cy="12" r="6.5"></circle>
          <circle cx="12" cy="12" r="2.5"></circle>
          <path d="M18.5 12h3M16.6 16.6l2.12 2.12M12 18.5v3M7.4 16.6l-2.12 2.12M5.5 12h-3M7.4 7.4 5.28 5.28M12 5.5v-3M16.6 7.4l2.12-2.12"></path>
        </svg>
      </summary>
      <div class="settings-panel">

        <fieldset class="setting">
          <legend>Open at</legend>
          {{/* Three buttons in one form rather than a select and a submit: with JavaScript off a
               select needs a button beside it anyway, and a button says what it does. There are
               exactly three views and there is no fourth coming, so they are written out. */}}
          <form method="post" action="/settings">
            <input type="hidden" name="name" value="landing">
            <input type="hidden" name="return" value="{{$.Path}}">
            <button type="submit" name="value" value="list"{{if $.LandingIs "list"}} aria-current="true"{{end}}>List</button>
            <button type="submit" name="value" value="sites"{{if $.LandingIs "sites"}} aria-current="true"{{end}}>Sites</button>
            <button type="submit" name="value" value="contributors"{{if $.LandingIs "contributors"}} aria-current="true"{{end}}>Contributors</button>
          </form>
        </fieldset>

        <fieldset class="setting">
          <legend>Call an item quiet after</legend>
          <form method="post" action="/settings">
            <input type="hidden" name="name" value="quiet">
            <input type="hidden" name="return" value="{{$.Path}}">
            {{range $.QuietChoices}}
            <button type="submit" name="value" value="{{.Value}}"{{if .Selected}} aria-current="true"{{end}}>{{.Label}}</button>
            {{end}}
          </form>
        </fieldset>

        <fieldset class="setting">
          <legend>This browser</legend>
          {{/* Turning it off is the clear: warm-start.js reads data-cache and goes to the branch
               that empties the store, so a separate "forget now" button would do the same thing
               by a second name (design §4.3). */}}
          <form method="post" action="/settings">
            <input type="hidden" name="name" value="cache">
            <input type="hidden" name="return" value="{{$.Path}}">
            {{if $.Settings.Cache}}
            <button type="submit" name="value" value="off">Stop keeping my last list here</button>
            <p class="setting-note">Your last list is kept in this browser so a cold start has something to show.</p>
            {{else}}
            <button type="submit" name="value" value="on">Keep my last list here</button>
            <p class="setting-note">Nothing is kept in this browser.</p>
            {{end}}
          </form>
        </fieldset>

        <fieldset class="setting">
          <legend>Appearance</legend>
          {{template "theme-control" $}}
        </fieldset>

        {{/* Said rather than offered: ending every session everywhere means rotating the OAuth
             client secret, which is a deployment operation and not a button (FR-8.3 AC4). */}}
        <p class="setting-note">Signing out everywhere means rotating the OAuth client secret.</p>
      </div>
    </details>
    {{end}}
  </nav>
```

Then move the existing theme `<form class="theme-form">…</form>` out of `topbar-nav` and wrap it in a named template at the bottom of `layout.html`, so both call sites draw the same control:

```html
{{/* The appearance switch, drawn either bare in the top bar (signed out) or inside the settings
     panel (signed in). It is one template so the two can never drift apart. */}}
{{define "theme-control"}}
<form class="theme-form" method="post" action="/theme">
  ... the existing form body, unchanged, with . still being pageData ...
</form>
{{end}}
```

Keep the form's body exactly as it is today, including all three SVG branches and the `title`/`aria-label`.

- [ ] **Step 4: Put the cache setting on the document**

In `layout.html`, extend the `<html>` tag, which already carries `data-theme` and `data-cache-epoch`:

```html
<html lang="en"{{with .ThemeAttr}} data-theme="{{.}}"{{end}} data-cache-epoch="{{.CacheEpoch}}"{{with .CacheAttr}} data-cache="{{.}}"{{end}}>
```

In `internal/web/static/warm-start.js`, make the setting the first thing checked, before the signed-out marker:

```js
  // The visitor asked this browser to keep nothing (FR-1.12 AC5). Turning the setting off is the
  // clear, so this branch empties the store rather than merely declining to add to it.
  if (document.documentElement.getAttribute("data-cache") === "off") {
    clear();
  } else if (document.getElementById("signed-out")) {
    clear();
  } else if (document.getElementById("placeholder")) {
    restore();
  } else {
    save();
  }
```

- [ ] **Step 5: Style the popover**

In `internal/web/static/app.css`, beside the other `.icon-button` rules. Use the file's own custom properties — it defines `--muted`, and check the `:root` block at the top for the surface and border names it actually uses rather than inventing any.

```css
/* The settings panel (FR-1.12). A <details> gives the open and closed states for free; all this
   does is take the marker off the summary, so the cog is the control rather than a cog with a
   triangle beside it, and float the panel under it. */
.settings > summary { list-style: none; cursor: pointer; }
.settings > summary::-webkit-details-marker { display: none; }
.settings { position: relative; }
.settings-panel {
  position: absolute;
  right: 0;
  z-index: 20;
  min-width: 16rem;
  padding: 0.75rem;
  border: 1px solid var(--muted);
  border-radius: 0.5rem;
  background: var(--bg);
  text-align: left;
}
.setting { margin: 0 0 0.75rem; padding: 0; border: 0; }
.setting legend { padding: 0; color: var(--muted); font-size: 0.8rem; }
.setting button[aria-current="true"] { font-weight: 600; text-decoration: underline; }
.setting-note { margin: 0.35rem 0 0; color: var(--muted); font-size: 0.78rem; }
```

If `--bg` is not what the file calls its page background, use the name it does. `contrast_test.go` parses this file and asserts contrast ratios — if it fails, take the foreground and background tokens it already accepts rather than loosening the test.

- [ ] **Step 6: Run everything**

Run the `./internal/web/` suite, then `make check`.

Expected: PASS. Watch three things in particular:

- `TestRenderedPageStaysInsideItsBudget` — the panel adds markup to every page. It had ~42 kB of headroom; report the new number in the commit if it moved by more than a kilobyte.
- The QS-4.1 route-table test — `POST /settings` must appear with the right credential.
- `contrast_test.go` — the new colours.

- [ ] **Step 7: Commit**

```bash
git add internal/web/templates/layout.html internal/web/static/app.css \
        internal/web/static/warm-start.js internal/web/settings_test.go
git commit -F - <<'EOF'
feat(web): the cogwheel opens (FR-1.12)

A <details> panel holding the landing view, the quiet threshold, whether this browser keeps its
last list, and the appearance — which moves in from the top bar, since the bar was carrying two
icon buttons for one idea. The sign-in page keeps the bare control: it has no session, so it has
no preferences, and appearance is all there is to set there.

It opens, changes and closes with no JavaScript. A form post would ordinarily close it, so the
handler redirects to <page>#settings and the browser opens the <details> containing the fragment
target — no script, and nothing remembered on the server.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

### Task 5: Requirements and the version

**Files:**

- Modify: `docs/requirements/04-functional-requirements.md`
- Modify: `internal/version/version.go:20`

- [ ] **Step 1: Add FR-1.12**

In `docs/requirements/04-functional-requirements.md`, add a row to the E-1 table immediately after FR-1.11. Copy the non-breaking hyphens from the neighbouring ids — they are `‑`, not `-`.

Priority `S`. Statement: *As the user I set how the dashboard greets me, and it remembers.*

Acceptance criteria, as one cell:

`AC1 A control in the top bar opens a panel holding the landing view, the quiet threshold, the browser-list setting and the appearance; it opens, changes and closes with no JavaScript. AC2 Each setting is stored in a cookie of its own for a year; an absent or unrecognised value is the default — list, 90 days, on — and no value read from a cookie is ever rendered as text. AC3 The landing view decides where GET / goes, and asking GitHub starts no later than it does without the setting. AC4 The quiet threshold decides which items are marked quiet (FR‑1.10 AC4); "never" marks none. AC5 Turning the browser-list setting off removes what that browser has stored (FR‑1.9 AC7).`

- [ ] **Step 2: Extend FR-8.1**

Find the FR‑8.1 row and add to its acceptance criteria, at the end of the cell: `Per-visitor preferences (FR‑1.12) are held in cookies and name nothing the deployment depends on.`

- [ ] **Step 3: Bump the version**

`internal/version/version.go:20` → `const Version = "0.6.0"`.

- [ ] **Step 4: Run the full gate**

Run `make check`. Markdownlint runs over `docs/**/*.md`; the two that bite in this repo are MD040 (fenced blocks need a language) and MD032 (lists need blank lines around them).

- [ ] **Step 5: Commit**

```bash
git add docs/requirements/04-functional-requirements.md internal/version/version.go
git commit -F - <<'EOF'
docs: FR-1.12, FR-8.1 extended, version 0.6.0 (FR-1.12, FR-8.1)

FR-1.12 states what the settings panel owes the visitor: it works without JavaScript, every
setting defaults to today's behaviour, and no cookie value is ever rendered as text. FR-8.1 gains
a sentence, since configuration is now the YAML file and the Fly secrets plus per-visitor
preferences that name nothing the deployment depends on.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

## Verification before calling this done

- [ ] `make check` clean.
- [ ] `docker compose -f deploy/compose.yml up -d`, then in a browser: the cog opens; changing each setting leaves the panel open; the landing view takes you to Sites; the quiet threshold changes which rows are dimmed; turning the browser list off empties `localStorage`.
- [ ] With JavaScript disabled: the panel still opens and every setting still changes.
- [ ] The sign-in page shows the bare sun/moon control and no panel.
