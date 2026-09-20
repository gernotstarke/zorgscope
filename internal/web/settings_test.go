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
