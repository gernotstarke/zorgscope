package web

import (
	"net/http"
	"strings"
	"time"
)

// themeCookieName holds the visitor's appearance choice. It is a cookie rather than a stored
// preference because there is nowhere to store it: the machine scales to zero and there is no
// user record — one visitor, one browser session, one cookie.
const themeCookieName = "zorgscope_theme"

// themeTTL is how long a chosen appearance survives. A year: this is a preference, not a session,
// and being asked again every fortnight would be worse than the cookie living a while.
const themeTTL = 365 * 24 * time.Hour

// theme is the appearance the page is rendered in.
type theme string

// The three states, in the order the control cycles through them. themeSystem is deliberately in
// the cycle rather than only being the default: without it a visitor who ever pressed the control
// could never get back to following the operating system, which is what the site did before this
// existed and what most people actually want.
const (
	themeSystem theme = "system"
	themeLight  theme = "light"
	themeDark   theme = "dark"
)

// themeCycle fixes the order the control steps through.
var themeCycle = []theme{themeSystem, themeLight, themeDark}

// parseTheme reads a theme from untrusted text — a cookie value or a form field. Anything it does
// not recognise is themeSystem, so a hand-edited cookie cannot put an arbitrary string into the
// document's data-theme attribute.
func parseTheme(s string) theme {
	for _, t := range themeCycle {
		if string(t) == s {
			return t
		}
	}
	return themeSystem
}

// next is the theme the control switches to.
func (t theme) next() theme {
	for i, c := range themeCycle {
		if c == t {
			return themeCycle[(i+1)%len(themeCycle)]
		}
	}
	return themeLight
}

// label names the theme in the words the control uses.
func (t theme) label() string {
	switch t {
	case themeLight:
		return "light"
	case themeDark:
		return "dark"
	default:
		return "follow system"
	}
}

// themeOf reads the appearance a request asks to be rendered in.
func themeOf(r *http.Request) theme {
	c, err := r.Cookie(themeCookieName)
	if err != nil {
		return themeSystem
	}
	return parseTheme(c.Value)
}

// handleTheme records the visitor's appearance choice and sends them back to the page they were
// on.
//
// It is a form post and a redirect rather than a script toggling a class, for the same reason
// every other control on this site is (FR-1.3 AC3): the Content-Security-Policy carries no
// 'unsafe-inline' (QS-4.4), so there is no inline handler to write, and a visitor with JavaScript
// switched off gets a working switch instead of a dead button. The consequence is that the
// appearance is decided on the server, before a byte of HTML is written — which also means there
// is no flash of the wrong theme on load, because the document never starts in one theme and
// changes to the other.
func (s *Server) handleTheme(w http.ResponseWriter, r *http.Request) {
	want := parseTheme(r.FormValue("theme"))

	// Following the system is the absence of a choice, so it is stored as the absence of a
	// cookie. Writing "system" would work equally well for this application and would leave a
	// cookie behind on a browser that had asked for nothing in particular.
	c := &http.Cookie{
		Name:     themeCookieName,
		Value:    string(want),
		Path:     "/",
		Expires:  s.clock.Now().Add(themeTTL),
		MaxAge:   int(themeTTL / time.Second),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
	if want == themeSystem {
		c.Value = ""
		c.Expires = time.Unix(0, 0)
		c.MaxAge = -1
	}
	http.SetCookie(w, c)

	// safeReturn is the sanitiser between the form field and the Location header: it accepts a
	// path of this site's own and nothing else. gosec's taint analysis cannot see through a
	// function call, so the annotation records where the boundary is rather than waving the
	// finding away — it has to be a trailing comment, which is the only form golangci-lint's
	// gosec honours.
	target := safeReturn(r.FormValue("return"))
	http.Redirect(w, r, target, http.StatusSeeOther) // #nosec G710 -- sanitised by safeReturn
}

// safeReturn turns the form's return field into a path this site will redirect to, or "/".
//
// The field is submitted by the browser and is therefore attacker-influenceable: a link that
// posts the form with a return of "https://elsewhere" would make this site's own redirect carry a
// visitor away, which is what an open redirect is. Only a path is accepted, and "//host" is
// rejected as well — a protocol-relative URL is a full URL wearing a path's clothing, and it is
// the case a naive "must start with /" check lets straight through.
func safeReturn(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return "/"
	}
	// A backslash is treated as a separator by some browsers, so "/\evil.example" can escape the
	// origin. Control characters can split the Location header. Neither belongs in a path this
	// site produced.
	if strings.ContainsAny(p, "\\\r\n") {
		return "/"
	}
	return p
}
