package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/snapshot"
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

// Finding 1 (blocking, whole-branch review): a request that carries a query string must not be
// redirected away by the landing preference. sites.go's "N more" link builds exactly such a
// request — "/?repo=org/repository-number-0" — and a visitor whose landing view is Sites must
// still be able to follow it to the filtered list (FR-1.8 AC3) rather than being bounced back to
// /sites with the filter thrown away.
func TestLandingRedirectLeavesAQueryAlone(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: representativeItems()})
	c := signIn(t, h)

	req := httptest.NewRequest(http.MethodGet, "/?repo=org%2Frepository-number-0", nil)
	req.AddCookie(c)
	req.AddCookie(&http.Cookie{Name: landingCookieName, Value: "sites"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("landing sites, GET /?repo=... = %d, want 200 (no redirect)", rec.Code)
	}
}

// Finding 2 (blocking, whole-branch review): the view switch's List entry links to "/?view=list",
// so this is the request that link now sends. With a non-default landing view it must still reach
// the list (FR-1.1) and its filters (FR-2.1) rather than being redirected on to the landing page —
// which would make List unreachable from the switch entirely.
//
// It also settles the question the review raised about parseFilter: "view" is a key parseFilter
// (filter.go) does not read, so it must narrow nothing. The body is checked for both halves of
// that — no "Nothing matches this filter" (which would mean a filter was wrongly judged to be in
// force) and the ordinary unfiltered count line, exactly as a bare "/" would render it.
func TestLandingRedirectLeavesTheViewSwitchAlone(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: representativeItems()})
	c := signIn(t, h)

	req := httptest.NewRequest(http.MethodGet, "/?view=list", nil)
	req.AddCookie(c)
	req.AddCookie(&http.Cookie{Name: landingCookieName, Value: "sites"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("landing sites, GET /?view=list = %d, want 200 (no redirect)", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "Nothing matches this filter") {
		t.Error("?view=list was treated as narrowing the list, but view is a key parseFilter does not read")
	}
	if !strings.Contains(body, "150 open") {
		t.Errorf("?view=list did not render the ordinary unfiltered list; body: %s", firstLineContaining(body, "open"))
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

	// The baseline is taken after signing in, because whatever sign-in did to the cache is not
	// what this test is about: the claim is that the redirect itself asks, so what matters is
	// that the count goes up across the redirect rather than what it happens to start at.
	before := src.CallCount()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	req.AddCookie(&http.Cookie{Name: landingCookieName, Value: "sites"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GET / = %d, want %d", rec.Code, http.StatusSeeOther)
	}

	// The fetch is detached on purpose: Get starts a goroutine and returns at once, so that no
	// page view ever waits for GitHub (QS-2.6, ADR-0011). Reading the count straight after
	// ServeHTTP therefore races that goroutine and samples before it has run. Polling to a
	// deadline is what the rest of this suite does, and it still fails for the regression this
	// test is for — a redirect that never asks the cache never increments at all.
	deadline := time.Now().Add(2 * time.Second)
	for src.CallCount() == before {
		if time.Now().After(deadline) {
			t.Fatal("the redirect did not start a fetch; a landing view would cost a cold visit a round trip")
		}
		time.Sleep(time.Millisecond)
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

// Finding 5 (minor, whole-branch review): the design asks for the threshold to be proven on / and
// on /search alike, and until now only / had a test — search.go's own call site was correct by
// inspection, but nothing would have caught a refactor that quietly dropped the cookie's threshold
// back to the default there. This mirrors TestTheQuietThresholdChangesTheMarks exactly, but drives
// /search with a query that matches the item on its title, so a real search hit is what carries
// the mark.
func TestTheQuietThresholdChangesTheMarksOnSearch(t *testing.T) {
	// An item last touched 120 days ago, matched by "untouched" in its own title: quiet at 30 and
	// 90, not at 180, never at "never" — the same shape as the dashboard's own test.
	old := ghItem(1, "Long untouched issue", testNow.AddDate(0, 0, -120))
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
		req := httptest.NewRequest(http.MethodGet, "/search?q=untouched", nil)
		req.AddCookie(c)
		req.AddCookie(&http.Cookie{Name: quietCookieName, Value: tc.cookie})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		// Not the literal title: a matched word is wrapped in <mark>, which splits "Long
		// untouched issue" into runs and breaks a plain substring check on the whole title —
		// exactly as TestSearchPageRendersHitsWithMarks checks "#7" rather than a title for the
		// same reason. "#1" is ghItem's own number for this fixture.
		if !strings.Contains(rec.Body.String(), "#1") {
			t.Fatalf("quiet=%s: the search for %q did not match the fixture item; body: %s",
				tc.cookie, "untouched", firstLineContaining(rec.Body.String(), "result"))
		}
		if got := strings.Contains(rec.Body.String(), "is-quiet"); got != tc.wantQuiet {
			t.Errorf("quiet=%s: search hit marked quiet = %v, want %v", tc.cookie, got, tc.wantQuiet)
		}
	}
}

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
	// Every control in it posts to the one route, discriminated by its hidden "name" field's
	// value — the form field is literally named "name" (handleSettings reads r.FormValue("name")),
	// so what appears in the markup is value="landing", not name="landing".
	for _, want := range []string{
		`action="/settings"`, `value="landing"`, `value="quiet"`, `value="cache"`,
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
