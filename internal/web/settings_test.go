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
