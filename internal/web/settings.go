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

// The landing views, which are exactly the views the switch offers.
const (
	landingList         landing = "list"
	landingSites        landing = "sites"
	landingRadar        landing = "radar"
	landingContributors landing = "contributors"
)

// path is where this landing view lives. landingList is "/" itself, so it is the one value that
// never redirects.
func (l landing) path() string {
	switch l {
	case landingSites:
		return "/sites"
	case landingRadar:
		return "/radar"
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
	case landingRadar:
		return landingRadar
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
