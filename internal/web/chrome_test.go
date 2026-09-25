// The page furniture: what the header, the list and the footer say. These live in package web
// with the other page tests because they reach for the same unexported fixtures.
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
	"github.com/gernotstarke/zorgscope/internal/version"
)

// getWithCookies issues a GET carrying several cookies, which the single-cookie helper in
// auth_test.go cannot: the theme tests need a session and a theme preference at once.
func getWithCookies(h http.Handler, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	h.ServeHTTP(rec, req)
	return rec
}

// The header carries the mark and the settings control. The control opens the settings popover
// (FR-1.12) rather than sitting disabled as it did before that panel existed — TestTheCogIsLive in
// settings_test.go covers the panel's own contents; this test keeps its original job of guarding
// the logo and the favicon link, and now also guards that the control is live rather than a link.
func TestTheHeaderCarriesTheLogoAndALiveSettingsControl(t *testing.T) {
	body := getAuthed(t, dashHandler(t, &fakeSource{items: representativeItems()}), "/").Body.String()

	if !strings.Contains(body, `src="/static/logo.png?`) {
		t.Error("the header does not show the logo")
	}
	if !strings.Contains(body, `href="/static/favicon.ico?`) {
		t.Error("no favicon is linked, so every first request of every visit is a 404")
	}

	cog := firstLineContaining(body, "settings-button")
	if cog == "" {
		t.Fatal("the header has no settings control")
	}
	// The control used to be a disabled <button>; it is now the <summary> of a <details>, which
	// opens the panel without a line of script.
	if strings.Contains(cog, "disabled") {
		t.Errorf("the settings control is still disabled: %s", cog)
	}
	// Whatever it is, it must not be a link — the panel opens in place, not by navigation.
	if strings.Contains(cog, "<a ") || strings.Contains(cog, "href=") {
		t.Errorf("the settings control links somewhere, but it should open the panel in place: %s", cog)
	}
}

// FR-1.11: the view switch, the search box, Refresh and Log out live in the top bar of every
// signed-in page, above the rainbow band, and nowhere on the sign-in page.
func TestTopBarCarriesTheChromeOnlyWhenSignedIn(t *testing.T) {
	h := dashHandler(t, &fakeSource{})
	body := getAuthed(t, h, "/").Body.String()
	header := body[strings.Index(body, "<header"):strings.Index(body, "</header>")]
	for _, want := range []string{`class="view-switch"`, `href="/contributors"`, `role="search"`, `action="/refresh"`, `action="/logout"`, `data-search`} {
		if !strings.Contains(header, want) {
			t.Errorf("the top bar lacks %s", want)
		}
	}
	if strings.Index(body, "</header>") > strings.Index(body, `class="rainbow"`) {
		t.Error("the rainbow band is not below the top bar")
	}
	main := body[strings.Index(body, "<main"):]
	for _, gone := range []string{`class="view-switch"`, `action="/refresh"`, `action="/logout"`} {
		if strings.Contains(main, gone) {
			t.Errorf("the page body still carries %s", gone)
		}
	}
	login := get(h, "/login").Body.String() // unauthenticated
	for _, gone := range []string{`class="view-switch"`, `role="search"`, `action="/refresh"`, `action="/logout"`} {
		if strings.Contains(login, gone) {
			t.Errorf("the sign-in page carries %s", gone)
		}
	}
}

// The search box echoes the query only where the query is a search: the list's own filter also
// uses q, and its text is not a search (FR-1.11).
func TestTheSearchBoxEchoesTheQueryOnlyOnTheResultsPage(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?q=header&kind=pr", nil)
	if c := chromeFor(r, snapshot.Snapshot{}, "", testNow); c.View != "list" || c.Query != "" || c.Return != "/?q=header&kind=pr" {
		t.Errorf("chromeFor(list) = %+v", *c)
	}
	r = httptest.NewRequest(http.MethodGet, "/search?q=+bug+", nil)
	if c := chromeFor(r, snapshot.Snapshot{}, "", testNow); c.View != "search" || c.Query != "bug" {
		t.Errorf("chromeFor(search) = %+v", *c)
	}
	r = httptest.NewRequest(http.MethodGet, "/sites", nil)
	if c := chromeFor(r, snapshot.Snapshot{}, "", testNow); c.View != "sites" || c.Return != "/sites" {
		t.Errorf("chromeFor(sites) = %+v", *c)
	}
}

// The footer is on every page, including the ones an anonymous visitor can reach, and it carries
// the build's identity. "Which version am I looking at?" is not a question a deployment should
// need a shell to answer.
func TestEveryPageFooterNamesCologneAndTheVersion(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: representativeItems()})
	cookie := signIn(t, h)

	// /login is requested anonymously: a signed-in visitor is redirected off it, and the page
	// an anonymous visitor sees is the one that most needs to say which build it belongs to.
	pages := []struct {
		path   string
		signed bool
	}{
		{"/", true}, {"/login", false},
	}
	for _, page := range pages {
		t.Run(page.path, func(t *testing.T) {
			rec := get(h, page.path)
			if page.signed {
				rec = getAs(h, page.path, cookie)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", page.path, rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, "Made with") || !strings.Contains(body, "in Cologne") {
				t.Error("the footer does not say where this was made")
			}
			if !strings.Contains(body, version.String()) {
				t.Errorf("the footer does not carry the version %q", version.String())
			}
		})
	}
}

// The footer is one line: where this was made, on the left, and which build it is, on the right.
func TestTheFooterReadsInTheOrderItIsDrawn(t *testing.T) {
	body := getAuthed(t, dashHandler(t, &fakeSource{items: representativeItems()}), "/").Body.String()

	footer := body[strings.Index(body, "<footer"):]
	made := strings.Index(footer, "footer-made")
	ver := strings.Index(footer, `class="version"`)
	if made < 0 || ver < 0 {
		t.Fatalf("the footer is missing one of its two parts: made=%d version=%d", made, ver)
	}
	if made > ver {
		t.Errorf("the footer reads made=%d, version=%d; want that order", made, ver)
	}
	// One line means one row of content, not two paragraphs stacked by the browser's defaults.
	if strings.Contains(footer[:ver], "<p ") {
		t.Error("the footer still uses block paragraphs, which cannot sit on one line")
	}
}

// The footer names what the page is made of and where it runs, between where it was made and
// which build it is, each a link that opens in a new tab like every other outside link.
func TestTheFooterNamesTheStackAndTheHost(t *testing.T) {
	for _, path := range []string{"/", "/login"} {
		rec := get(dashHandler(t, &fakeSource{}), path)
		if path == "/" {
			rec = getAuthed(t, dashHandler(t, &fakeSource{}), path)
		}
		body := rec.Body.String()
		footer := body[strings.Index(body, "<footer"):]
		for _, want := range []string{
			`<a href="https://go.dev" target="_blank" rel="noopener noreferrer">Go</a>`,
			`<a href="https://htmx.org" target="_blank" rel="noopener noreferrer">htmx</a>`,
			`running on <a href="https://fly.io" target="_blank" rel="noopener noreferrer">Fly.io</a>`,
		} {
			if !strings.Contains(footer, want) {
				t.Errorf("%s: the footer lacks %s", path, want)
			}
		}
		made, stack, ver := strings.Index(footer, "footer-made"), strings.Index(footer, "footer-stack"), strings.Index(footer, `class="version"`)
		if made < 0 || stack < made || ver < stack {
			t.Errorf("%s: the footer reads made=%d, stack=%d, version=%d; want that order", path, made, stack, ver)
		}
	}
}

// The appearance switch (FR-1.5). It is a form post, not a script: the CSP carries no
// 'unsafe-inline' (QS-4.4) and the page has to work with JavaScript switched off (FR-1.5 AC1).
func TestTheThemeSwitchCyclesThroughTheThreeAppearances(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: representativeItems()})

	tests := []struct {
		name       string
		cookie     *http.Cookie
		wantAttr   string // what data-theme the document carries
		wantSubmit string // what the control would switch to
	}{
		{name: "no cookie follows the system", wantAttr: "", wantSubmit: "light"},
		{
			name:       "light",
			cookie:     &http.Cookie{Name: themeCookieName, Value: "light"},
			wantAttr:   `data-theme="light"`,
			wantSubmit: "dark",
		},
		{
			name:       "dark",
			cookie:     &http.Cookie{Name: themeCookieName, Value: "dark"},
			wantAttr:   `data-theme="dark"`,
			wantSubmit: "system",
		},
		{
			// A hand-edited cookie must not put arbitrary text into the document's attribute.
			name:       "an unrecognised value falls back to the system",
			cookie:     &http.Cookie{Name: themeCookieName, Value: `x" onload="alert(1)`},
			wantAttr:   "",
			wantSubmit: "light",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := signIn(t, h)
			rec := getAuthed(t, h, "/")
			if tc.cookie != nil {
				rec = getWithCookies(h, "/", c, tc.cookie)
			}
			body := rec.Body.String()

			html := firstLineContaining(body, "<html")
			if tc.wantAttr == "" {
				if strings.Contains(html, "data-theme") {
					t.Errorf("<html> carries a theme attribute while following the system: %s", html)
				}
			} else if !strings.Contains(html, tc.wantAttr) {
				t.Errorf("<html> = %s, want it to carry %s", html, tc.wantAttr)
			}

			if !strings.Contains(body, `name="theme" value="`+tc.wantSubmit+`"`) {
				t.Errorf("the control does not switch to %q:\n%s",
					tc.wantSubmit, firstLineContaining(body, `name="theme"`))
			}
			// QS-4.4: the mechanism is markup. A script toggling a class would need
			// 'unsafe-inline', and would flash the wrong theme before it ran.
			if strings.Contains(firstLineContaining(body, "theme-form"), "onclick") {
				t.Error("the switch is wired with an inline handler")
			}
		})
	}
}

// The switch sets the cookie and comes back to the page it was pressed on.
func TestSwitchingTheThemeReturnsToThePageItWasPressedOn(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: representativeItems()})

	rec := post(h, "/theme", url.Values{"theme": {"dark"}, "return": {"/"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /theme = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want /", got)
	}
	c := cookieNamed(rec, themeCookieName)
	if c == nil || c.Value != "dark" {
		t.Fatalf("theme cookie = %+v, want dark", c)
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("theme cookie = %+v, want the same hardening as every other cookie here", c)
	}
}

// Choosing to follow the system is the absence of a choice, so it clears the cookie rather than
// storing the word.
func TestFollowingTheSystemClearsTheCookie(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: representativeItems()})

	rec := post(h, "/theme", url.Values{"theme": {"system"}, "return": {"/"}})
	c := cookieNamed(rec, themeCookieName)
	if c == nil {
		t.Fatal("no theme cookie in the response, so an existing one is never cleared")
	}
	if c.Value != "" || c.MaxAge >= 0 {
		t.Errorf("theme cookie = %+v, want an expiry that removes it", c)
	}
}

// The return field is submitted by the browser, so a crafted link could otherwise turn this site's
// own redirect into a way of carrying its visitors somewhere else.
func TestTheThemeSwitchIsNotAnOpenRedirect(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: representativeItems()})

	tests := []struct{ name, give string }{
		{"absolute URL", "https://evil.example/"},
		{"protocol-relative URL", "//evil.example/"},
		{"backslash escape", "/\\evil.example"},
		{"header injection", "/ok\r\nSet-Cookie: a=b"},
		{"scheme without a host", "javascript:alert(1)"},
		{"empty", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(h, "/theme", url.Values{"theme": {"dark"}, "return": {tc.give}})
			if got := rec.Header().Get("Location"); got != "/" {
				t.Errorf("Location = %q for return=%q, want /", got, tc.give)
			}
		})
	}
}

// A path this site actually serves is kept, so the switch does not dump the visitor on the
// dashboard from wherever they were.
func TestTheThemeSwitchKeepsAnOrdinaryPath(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: representativeItems()})
	for _, p := range []string{"/", "/items", "/login"} {
		rec := post(h, "/theme", url.Values{"theme": {"light"}, "return": {p}})
		if got := rec.Header().Get("Location"); got != p {
			t.Errorf("Location = %q, want %q", got, p)
		}
	}
}

// The list shows a number, a title and a line of the item's own description, and each repository
// says how many items it holds — the unfiltered figure, which is the one a filter must never be
// able to change (FR-2.1 AC3).
func TestTheListShowsNumbersDescriptionsAndCounts(t *testing.T) {
	items := representativeItems()
	items[0].Number = 4242
	items[0].Summary = "The container needs a liveness probe so a failing start is visible."

	h := dashHandler(t, &fakeSource{items: items})
	body := getAuthed(t, h, "/").Body.String()

	if !strings.Contains(body, "#4242") {
		t.Error("the list does not show the item's number")
	}
	if !strings.Contains(body, "liveness probe") {
		t.Error("the list does not show the item's description")
	}
	if !strings.Contains(body, `class="item-summary"`) {
		t.Error("the description is not set apart from the title")
	}
	// representativeItems holds 150 items across 10 repositories of 15.
	if !strings.Contains(body, "150 open") {
		t.Errorf("the list does not say how many items are open:\n%s",
			firstLineContaining(body, "shown-count"))
	}
	if !strings.Contains(body, `<span class="repo-count">15</span>`) {
		t.Errorf("a repository heading does not state how many items it holds:\n%s",
			firstLineContaining(body, "repo-count"))
	}
	// Narrowed, both counts name the total as well: "5 open" and "5 of 150 open" are different
	// news, and only one of them is true.
	narrowed := getAuthed(t, h, "/?kind=pr").Body.String()
	if !strings.Contains(narrowed, "50 of 150 open") {
		t.Errorf("a filtered list does not state the unfiltered total:\n%s",
			firstLineContaining(narrowed, "shown-count"))
	}
	if !strings.Contains(narrowed, `<span class="repo-count">5 of 15</span>`) {
		t.Errorf("a filtered repository heading does not state its unfiltered total:\n%s",
			firstLineContaining(narrowed, "repo-count"))
	}
}

// A long description is cut on a word boundary. A half word followed by an ellipsis reads as a
// rendering fault rather than as a deliberate abbreviation.
func TestALongDescriptionIsCutOnAWordBoundary(t *testing.T) {
	tests := []struct {
		name, give string
		check      func(t *testing.T, got string)
	}{
		{
			name:  "short enough is untouched",
			give:  "Add a health endpoint.",
			check: func(t *testing.T, got string) { wantEqual(t, got, "Add a health endpoint.") },
		},
		{
			name: "long prose is cut at a space",
			give: "The container needs a liveness probe, and the Compose stack has to wire it in " +
				"so that a failing start is visible without anyone reading the logs.",
			check: func(t *testing.T, got string) {
				if !strings.HasSuffix(got, "…") {
					t.Errorf("%q does not end in an ellipsis", got)
				}
				trimmed := strings.TrimSuffix(got, "…")
				if strings.HasSuffix(trimmed, " ") {
					t.Errorf("%q has a space before the ellipsis", got)
				}
				// The cut landed between words, not inside one.
				if !strings.HasPrefix(
					"The container needs a liveness probe, and the Compose stack has to wire it in",
					trimmed) {
					t.Errorf("%q is not a word-boundary prefix of the description", trimmed)
				}
			},
		},
		{
			// No space to cut at: a single long token. Cutting at the limit beats showing all
			// of it.
			name: "an unbroken run is cut at the limit",
			give: strings.Repeat("z", 200),
			check: func(t *testing.T, got string) {
				if len([]rune(got)) != displaySummaryLen+1 { // the ellipsis
					t.Errorf("length = %d runes, want %d", len([]rune(got)), displaySummaryLen+1)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { tc.check(t, summaryLine(tc.give)) })
	}
}

func wantEqual(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// QS-4.4: the policy names no external host at all.
func TestTheContentSecurityPolicyNamesNoExternalHost(t *testing.T) {
	rec := getAuthed(t, dashHandler(t, &fakeSource{items: representativeItems()}), "/")

	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "img-src 'self' data:;") {
		t.Errorf("img-src is not self and data: alone: %s", csp)
	}
	if strings.Contains(csp, "shields.io") || strings.Contains(csp, "https://") {
		t.Errorf("the policy still names an external host: %s", csp)
	}
}

// The top bar counts what is open across every configured repository, whatever the page below it
// shows, and says nothing until a fetch has returned — "0 issues" before one would be a wrong answer.
func TestTheTopBarCountsOpenIssuesAndPullRequests(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/sites", nil)
	if c := chromeFor(r, snapshot.Snapshot{}, "", testNow); c.Counted {
		t.Errorf("chromeFor before any fetch = %+v, want no count", *c)
	}
	snap := snapshot.Snapshot{FetchedAt: time.Now(), Items: []domain.Item{
		{Kind: domain.KindIssue}, {Kind: domain.KindPR}, {Kind: domain.KindIssue},
	}}
	if c := chromeFor(r, snap, "", testNow); !c.Counted || c.Issues != 2 || c.PRs != 1 {
		t.Errorf("chromeFor = %+v, want 2 issues and 1 PR", *c)
	}
}

// Every signed-in page draws the count, between the switch and the search box.
func TestEveryPageDrawsTheOpenCount(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: []domain.Item{
		{Kind: domain.KindPR, Repo: "o/a", Number: 1, Author: "amy", UpdatedAt: testNow},
		{Kind: domain.KindIssue, Repo: "o/a", Number: 2, Author: "amy", UpdatedAt: testNow},
		{Kind: domain.KindIssue, Repo: "o/a", Number: 3, Author: "amy", UpdatedAt: testNow},
	}})
	for _, path := range []string{"/?view=list", "/sites", "/contributors", "/search?q=x"} {
		body := getAuthed(t, h, path).Body.String()
		i, s, q := strings.Index(body, `id="view-switch"`), strings.Index(body, `class="open-count"`), strings.Index(body, `id="topbar-search"`)
		if s < 0 || i >= s || s >= q {
			t.Errorf("%s: open count missing or out of place (switch %d, count %d, search %d)", path, i, s, q)
		}
		for _, want := range []string{">2 issues<", ">1 PR<"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %s", path, want)
			}
		}
	}
}
