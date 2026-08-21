// The page furniture: what the header, the warning box, the details page and the footer say, and
// what they must never say. These live in package web with the other page tests because they
// reach for the same unexported fixtures.
package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
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

// failingStates returns healthy states with one source broken, so a test can name the one thing
// it is about.
func failingStates(at time.Time, source, message string) map[string]domain.SourceState {
	m := healthyStates(at)
	m[source] = domain.SourceState{
		Source:        source,
		LastSuccessAt: at.Add(-2 * time.Hour),
		LastError:     message,
		LastErrorAt:   at.Add(-time.Minute),
	}
	return m
}

// The header carries the mark and the settings control. The control is inert for the whole of v1 —
// configuration is a YAML file and Fly secrets (C-9) — and saying so is the point: a cog that led
// to a 404 would be worse than no cog at all.
func TestTheHeaderCarriesTheLogoAndAnInertSettingsControl(t *testing.T) {
	body := getAuthed(t, dashHandler(t, representativeStore()), "/").Body.String()

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
	if !strings.Contains(cog, "disabled") {
		t.Errorf("the settings control is not disabled: %s", cog)
	}
	// Whatever it is, it must not be a link — there is nowhere for it to go.
	if strings.Contains(cog, "<a ") || strings.Contains(cog, "href=") {
		t.Errorf("the settings control links somewhere, but there is no configuration page: %s", cog)
	}
}

// The header's second date: when the dashboard was last used. It is the last-visit watermark,
// which is the same instant every NEW badge is measured against, so the header states the age of
// the comparison the badges are making.
func TestTheHeaderSaysWhenTheDashboardWasLastUsed(t *testing.T) {
	t.Run("after a visit", func(t *testing.T) {
		store := representativeStore()
		store.lastVisit = testNow.Add(-3 * time.Hour)

		line := firstLineContaining(getAuthed(t, dashHandler(t, store), "/").Body.String(), "Last used")
		if line == "" {
			t.Fatal("the header does not say when the dashboard was last used")
		}
		if !strings.Contains(line, "3 hours ago") {
			t.Errorf("last-used line = %q, want it to name the age of the last visit", line)
		}
	})

	t.Run("before any visit", func(t *testing.T) {
		store := representativeStore()
		store.lastVisit = time.Time{}

		body := getAuthed(t, dashHandler(t, store), "/").Body.String()
		if !strings.Contains(body, "Nothing marked seen yet") {
			t.Error("a database with no visit recorded must say so rather than show a date")
		}
		if strings.Contains(body, "1970") {
			t.Error("the zero time reached the page as a date")
		}
	})
}

// FR-1.4: the box appears when an external interface has gone wrong, says how many, and hands over
// one link to the whole story.
func TestTheDashboardWarnsWhenAnExternalServiceHasGoneWrong(t *testing.T) {
	store := representativeStore()
	store.states = failingStates(testNow, "plausible", "502 Bad Gateway")

	body := getAuthed(t, dashHandler(t, store), "/").Body.String()

	if !strings.Contains(body, `class="alert alert-error"`) {
		t.Fatal("no warning box on a dashboard with a failing source")
	}
	if !strings.Contains(body, "1 external service needs attention") {
		t.Error("the box does not say how many services are affected, in the singular")
	}
	if !strings.Contains(body, `href="/problems"`) {
		t.Error("the box offers no way to reach the details")
	}
	// The box is a signpost. The upstream text belongs on the details page, where there is room
	// for it and where it is the subject rather than an interruption — the failing tile shows it
	// too, which is why this looks at the box's own sentence rather than at the whole page.
	if sentence := firstLineContaining(body, "alert-text"); strings.Contains(sentence, "502") {
		t.Errorf("the warning box quotes the upstream error instead of linking to it: %s", sentence)
	}
}

// Two broken services are counted, and counted in the plural.
func TestTheWarningBoxCountsInThePlural(t *testing.T) {
	store := representativeStore()
	store.states = failingStates(testNow, "plausible", "502")
	store.states["todoist"] = domain.SourceState{
		Source: "todoist", LastSuccessAt: testNow.Add(-5 * time.Hour),
	}

	body := getAuthed(t, dashHandler(t, store), "/").Body.String()
	if !strings.Contains(body, "2 external services need attention") {
		t.Error("two affected services are not counted in the plural")
	}
}

// A healthy dashboard is quiet, and so is one whose sources are merely switched off. A box that
// appeared for a deliberate configuration would be dismissed unread within a week, which is
// exactly the failure this box exists to avoid.
func TestNoWarningBoxWhenNothingIsWrong(t *testing.T) {
	t.Run("everything healthy", func(t *testing.T) {
		body := getAuthed(t, dashHandler(t, representativeStore()), "/").Body.String()
		if strings.Contains(body, `class="alert`) {
			t.Error("a healthy dashboard shows a warning box")
		}
	})

	t.Run("sources switched off", func(t *testing.T) {
		// No credentials at all: every source is dark, and none of them is broken.
		h := newTestServerWith(t, func(o *Options) {
			o.Store = representativeStore()
		}).Handler()

		body := getAuthed(t, h, "/").Body.String()
		if strings.Contains(body, `class="alert`) {
			t.Error("an unconfigured source is reported as something that went wrong")
		}
	})
}

// The details page: every interface, in the state it is in, with the upstream text.
func TestTheProblemsPageReportsEveryInterface(t *testing.T) {
	store := representativeStore()
	store.states = failingStates(testNow, "github", "502 Bad Gateway from api.github.com")
	store.lastRun = domain.RefreshRun{
		StartedAt:  testNow.Add(-2 * time.Minute),
		FinishedAt: testNow.Add(-time.Minute),
		Detail:     "github: fetch failed",
	}

	rec := getAuthed(t, dashHandler(t, store), "/problems")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /problems = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	for _, want := range []string{"GitHub", "Builds", "Sites", "Tasks", "Notifications (Slack)"} {
		if !strings.Contains(body, want) {
			t.Errorf("the details page does not list %q", want)
		}
	}
	// The state is a word as well as a colour (FR-1.5 AC2).
	for _, want := range []string{"Error", "OK", "Not configured"} {
		if !strings.Contains(body, ">"+want+"<") {
			t.Errorf("the details page never says %q in words", want)
		}
	}
	if !strings.Contains(body, "502 Bad Gateway from api.github.com") {
		t.Error("the details page does not show the upstream error, which is the whole point of it")
	}
	if !strings.Contains(body, "github: fetch failed") {
		t.Error("the details page does not reproduce what the last run recorded")
	}
}

// QS-4.3, at the one place on the site that renders upstream error text as its subject rather
// than as a footnote. An upstream library quotes what it was given, and what it was given was the
// credential.
func TestTheProblemsPageNeverShowsASecret(t *testing.T) {
	const canary = "canary-github-token-9f3a1c"

	store := representativeStore()
	store.states = failingStates(testNow, "github",
		"Get \"https://api.github.com/x\": 401 with Authorization: Bearer "+canary)

	h := newTestServerWith(t, func(o *Options) {
		credentialAllSources(o)
		o.Config.Secrets.GitHubToken = canary
		o.Store = store
	}).Handler()

	body := getAuthed(t, h, "/problems").Body.String()
	if strings.Contains(body, canary) {
		t.Fatal("the credential reached the details page")
	}
	if !strings.Contains(body, "401") {
		t.Error("scrubbing removed the error along with the secret; the message is still needed")
	}
}

// FR-6.1 AC3 leaves a rejected announcement invisible everywhere else on the site — the run stays
// OK, no tile changes, no source goes stale — so the details page is where it has to surface.
func TestARejectedAnnouncementReachesTheDetailsPage(t *testing.T) {
	store := representativeStore()
	store.lastRun = domain.RefreshRun{
		StartedAt:  testNow.Add(-2 * time.Minute),
		FinishedAt: testNow.Add(-time.Minute),
		OK:         true,
		Detail:     "github: 12" + domain.DetailSeparator + domain.NotifyDetailPrefix + "webhook returned 404 no_service",
	}

	h := newTestServerWith(t, func(o *Options) {
		credentialAllSources(o)
		o.Config.Notifications.Slack.Enabled = true
		o.Config.Secrets.SlackWebhook = "https://hooks.slack.example/T/B/xxxx"
		o.Store = store
	}).Handler()

	dash := getAuthed(t, h, "/").Body.String()
	if !strings.Contains(dash, "1 external service needs attention") {
		t.Error("a rejected announcement raises no warning on the dashboard; it is invisible everywhere else")
	}

	body := getAuthed(t, h, "/problems").Body.String()
	if !strings.Contains(body, "webhook returned 404 no_service") {
		t.Error("the details page does not report why the announcement failed")
	}
	if !strings.Contains(body, ">Warning<") {
		t.Error("the announcement failure is not reported as a warning")
	}
}

// Slack switched off is listed as unconfigured, never as broken.
func TestNotificationsSwitchedOffAreReportedAsUnconfigured(t *testing.T) {
	body := getAuthed(t, dashHandler(t, representativeStore()), "/problems").Body.String()

	notif := firstLineContaining(body, "Notifications (Slack)")
	if notif == "" {
		t.Fatal("the notifier is not listed on the details page")
	}
	if strings.Contains(body, `class="problem problem-error"`) {
		t.Error("a notifier that is switched off is reported as an error")
	}
}

// The footer is on every page, including the ones an anonymous visitor can reach, and it carries
// the build's identity. "Which version am I looking at?" is not a question a deployment should
// need a shell to answer.
func TestEveryPageFooterNamesCologneAndTheVersion(t *testing.T) {
	h := dashHandler(t, representativeStore())
	cookie := signIn(t, h)

	// /login is requested anonymously: a signed-in visitor is redirected off it, and the page
	// an anonymous visitor sees is the one that most needs to say which build it belongs to.
	pages := []struct {
		path   string
		signed bool
	}{
		{"/", true}, {"/problems", true}, {"/docs", true}, {"/login", false},
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
			if !strings.Contains(body, `class="footer-docs"`) {
				t.Error("the footer does not link the documentation")
			}
		})
	}
}

// The footer is one line: where this was made, which build it is, where the writing is. The order
// in the markup is the order it is read aloud and the order the keyboard reaches it, so the
// version has to be written between the other two rather than positioned there by the stylesheet
// — a footer that reads correctly only for people who can see it is not one line, it is two
// layouts.
func TestTheFooterReadsInTheOrderItIsDrawn(t *testing.T) {
	body := getAuthed(t, dashHandler(t, representativeStore()), "/").Body.String()

	footer := body[strings.Index(body, "<footer"):]
	made := strings.Index(footer, "footer-made")
	ver := strings.Index(footer, `class="version"`)
	docs := strings.Index(footer, "footer-docs")
	if made < 0 || ver < 0 || docs < 0 {
		t.Fatalf("the footer is missing one of its three parts: made=%d version=%d docs=%d", made, ver, docs)
	}
	if made > ver || ver > docs {
		t.Errorf("the footer reads made=%d, version=%d, docs=%d; want that order", made, ver, docs)
	}
	// One line means one row of content, not two paragraphs stacked by the browser's defaults.
	if strings.Contains(footer[:docs], "<p ") {
		t.Error("the footer still uses block paragraphs, which cannot sit on one line")
	}
}

// FR-1.4 under polling. This dashboard is meant to be left open, so "the next page load" may be
// tomorrow: a source that starts failing while the tab sits there has to raise the box then, and
// a tile that swapped without it would show a failing source under a page claiming all is well.
func TestAPolledTileBringsTheWarningBoxBackWithIt(t *testing.T) {
	store := representativeStore()
	store.states = failingStates(testNow, "plausible", "502 Bad Gateway")
	h := dashHandler(t, store)

	fragment := getAuthed(t, h, "/tile/github").Body.String()
	if !strings.Contains(fragment, `id="dash-alert" hx-swap-oob="true"`) {
		t.Fatalf("the polled fragment does not carry the warning box:\n%s", fragment)
	}
	if !strings.Contains(fragment, "1 external service needs attention") {
		t.Error("the swapped box does not say what is wrong")
	}

	// The page draws the same element plainly — an out-of-band marker on a full page load would
	// ask htmx to swap into a document it has just replaced.
	page := getAuthed(t, h, "/").Body.String()
	if strings.Contains(firstLineContaining(page, `id="dash-alert"`), "hx-swap-oob") {
		t.Error("the page marks its own warning box as an out-of-band swap")
	}
}

// The slot is rendered even when nothing is wrong. An out-of-band swap addresses an element by id,
// so a box that vanished entirely while everything was healthy could never be brought back by a
// later poll — the first failure of the day would be silent until someone reloaded.
func TestTheWarningBoxKeepsItsPlaceholderWhileHealthy(t *testing.T) {
	h := dashHandler(t, representativeStore())

	page := getAuthed(t, h, "/").Body.String()
	if !strings.Contains(page, `id="dash-alert"`) {
		t.Fatal("a healthy page carries no slot for the warning box, so a poll can never find it")
	}
	if strings.Contains(page, `class="alert`) {
		t.Error("a healthy page draws the box itself, not just its slot")
	}

	fragment := getAuthed(t, h, "/tile/github").Body.String()
	if !strings.Contains(fragment, `id="dash-alert" hx-swap-oob="true"`) {
		t.Error("a poll from a healthy page carries no slot, so the box could never be cleared")
	}
}

// The appearance switch (FR-1.5). It is a form post, not a script: the CSP carries no
// 'unsafe-inline' (QS-4.4) and the page has to work with JavaScript switched off (FR-1.3 AC3).
func TestTheThemeSwitchCyclesThroughTheThreeAppearances(t *testing.T) {
	h := dashHandler(t, representativeStore())

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
	h := dashHandler(t, representativeStore())

	rec := post(h, "/theme", url.Values{"theme": {"dark"}, "return": {"/problems"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /theme = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/problems" {
		t.Errorf("Location = %q, want /problems", got)
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
	h := dashHandler(t, representativeStore())

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
	h := dashHandler(t, representativeStore())

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
	h := dashHandler(t, representativeStore())
	for _, path := range []string{"/", "/problems", "/docs", "/docs/requirements/01-goals", "/login"} {
		rec := post(h, "/theme", url.Values{"theme": {"light"}, "return": {path}})
		if got := rec.Header().Get("Location"); got != path {
			t.Errorf("Location = %q, want %q", got, path)
		}
	}
}

// The GitHub tile shows a number, a title and a line of the item's own description, and says how
// many items it is not showing.
func TestTheGitHubTileShowsNumbersDescriptionsAndTheRemainder(t *testing.T) {
	store := representativeStore()
	store.items[0].Number = 4242
	store.items[0].Summary = "The container needs a liveness probe so a failing start is visible."

	body := getAuthed(t, dashHandler(t, store), "/").Body.String()

	if !strings.Contains(body, "#4242") {
		t.Error("the tile does not show the item's number")
	}
	if !strings.Contains(body, "liveness probe") {
		t.Error("the tile does not show the item's description")
	}
	if !strings.Contains(body, `class="item-summary"`) {
		t.Error("the description is not set apart from the title")
	}
	// representativeStore holds 150 GitHub items, so the tile shows five and names the rest.
	if !strings.Contains(body, "and 145 more open") {
		t.Errorf("the tile does not say how many it left out:\n%s",
			firstLineContaining(body, "item-more"))
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

// FR-2.3 on the front page: one indicator, in a colour and in words, with a link to the rest.
func TestTheFrontPageShowsBuildStatusAsOneIndicator(t *testing.T) {
	tests := []struct {
		name        string
		builds      []domain.Build
		wantHealth  string
		wantSummary string
	}{
		{
			name: "all green",
			builds: []domain.Build{
				{Repo: "org/a", Conclusion: "success", FetchedAt: testNow},
				{Repo: "org/b", Conclusion: "success", FetchedAt: testNow},
			},
			wantHealth:  "ok",
			wantSummary: "all 2 repositories are green",
		},
		{
			name: "one cancelled",
			builds: []domain.Build{
				{Repo: "org/a", Conclusion: "success", FetchedAt: testNow},
				{Repo: "org/b", Conclusion: "cancelled", FetchedAt: testNow},
			},
			wantHealth:  "warning",
			wantSummary: "1 repository of 2 need a look",
		},
		{
			name: "two broken",
			builds: []domain.Build{
				{Repo: "org/a", Conclusion: "failure", FetchedAt: testNow},
				{Repo: "org/b", Conclusion: "timed_out", FetchedAt: testNow},
			},
			wantHealth:  "broken",
			wantSummary: "2 repositories of 2 broken",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repos := make([]string, 0, len(tc.builds))
			for _, b := range tc.builds {
				repos = append(repos, b.Repo)
			}
			h := newTestServerWith(t, func(o *Options) {
				credentialAllSources(o)
				o.Config.GitHub.Repos = repos
				o.Store = &dashStore{builds: tc.builds, states: healthyStates(testNow)}
			}).Handler()

			body := getAuthed(t, h, "/").Body.String()
			bar := firstLineContaining(body, "buildbar")
			if bar == "" {
				t.Fatal("no build indicator on the front page")
			}
			if !strings.Contains(bar, "buildbar-"+tc.wantHealth) {
				t.Errorf("indicator = %s, want state %q", bar, tc.wantHealth)
			}
			if !strings.Contains(bar, tc.wantSummary) {
				t.Errorf("indicator does not say %q: %s", tc.wantSummary, bar)
			}
			// FR-1.5 AC2: the state is in words as well as in colour.
			if !strings.Contains(bar, "Build status:") {
				t.Error("the indicator names the state only by colour")
			}
			if !strings.Contains(bar, `href="/builds"`) {
				t.Error("the indicator offers no way to reach the details")
			}
			// The front page is for issues and pull requests; the repository list belongs on the
			// details page.
			if strings.Contains(body, `class="builds-table"`) {
				t.Error("the repository table is on the front page")
			}
		})
	}
}

// The details page is the list the front page no longer carries.
func TestTheBuildDetailsPageListsEveryWatchedRepository(t *testing.T) {
	h := newTestServerWith(t, func(o *Options) {
		credentialAllSources(o)
		o.Config.GitHub.Repos = []string{"org/broken", "org/green", "org/silent"}
		o.Store = &dashStore{
			builds: []domain.Build{
				{Repo: "org/broken", Workflow: "CI", Conclusion: "failure", Status: "completed",
					RunURL: "https://github.com/org/broken/actions/runs/9",
					FinishedAt: testNow.Add(-time.Hour), FetchedAt: testNow},
				{Repo: "org/green", Workflow: "CI", Conclusion: "success", Status: "completed",
					FinishedAt: testNow.Add(-2 * time.Hour), FetchedAt: testNow},
			},
			states: healthyStates(testNow),
		}
	}).Handler()

	rec := getAuthed(t, h, "/builds")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /builds = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	for _, repo := range []string{"org/broken", "org/green", "org/silent"} {
		if !strings.Contains(body, repo) {
			t.Errorf("the details page does not list %q", repo)
		}
	}
	if !strings.Contains(body, "https://github.com/org/broken/actions/runs/9") {
		t.Error("a broken build does not link to its run, which is where it is diagnosed")
	}
	// Worst first: the broken repository is above the green one.
	if strings.Index(body, "org/broken") > strings.Index(body, "org/green") {
		t.Error("the broken repository is not listed first")
	}
}

// QS-4.3 at the two places build state is rendered.
func TestTheBuildPagesNeverShowASecret(t *testing.T) {
	const canary = "ghp-canary-build-7c2a91"

	h := newTestServerWith(t, func(o *Options) {
		credentialAllSources(o)
		o.Config.Secrets.GitHubToken = canary
		o.Config.GitHub.Repos = []string{"org/a"}
		o.Store = &dashStore{states: map[string]domain.SourceState{
			"github-builds": {
				Source:      "github-builds",
				LastError:   "GET /actions/runs failed with Authorization: Bearer " + canary,
				LastErrorAt: testNow,
			},
		}}
	}).Handler()

	for _, page := range []string{"/", "/builds"} {
		if strings.Contains(getAuthed(t, h, page).Body.String(), canary) {
			t.Errorf("the credential reached %s", page)
		}
	}
}

// FR-2.3 under polling. A build that breaks while the tab sits open has to turn the indicator red
// then — on a dashboard meant to be left open, the next page load may be tomorrow.
func TestAPolledTileBringsTheBuildIndicatorBackWithIt(t *testing.T) {
	h := newTestServerWith(t, func(o *Options) {
		credentialAllSources(o)
		o.Config.GitHub.Repos = []string{"org/a"}
		o.Store = &dashStore{
			builds: []domain.Build{{Repo: "org/a", Conclusion: "failure", FetchedAt: testNow}},
			states: healthyStates(testNow),
		}
	}).Handler()

	fragment := getAuthed(t, h, "/tile/github").Body.String()
	if !strings.Contains(fragment, `id="build-status" hx-swap-oob="true"`) {
		t.Fatalf("the polled fragment does not carry the build indicator:\n%s", fragment)
	}
	if !strings.Contains(fragment, "buildbar-broken") {
		t.Error("the swapped indicator does not carry the current state")
	}

	page := getAuthed(t, h, "/").Body.String()
	if strings.Contains(firstLineContaining(page, `id="build-status"`), "hx-swap-oob") {
		t.Error("the page marks its own indicator as an out-of-band swap")
	}
	// GET /tile/builds is gone with the tile; the page took its place.
	if got := getAuthed(t, h, "/tile/builds").Code; got != http.StatusNotFound {
		t.Errorf("GET /tile/builds = %d, want 404 — builds are no longer a tile", got)
	}
}

// FR-2.3 AC5: the details page carries a shields.io badge per repository — a second, independent
// reading of the same workflow, fetched by the browser at the moment the page is read.
func TestTheBuildsPageCarriesAShieldsBadgePerRepository(t *testing.T) {
	store := representativeStore()
	store.builds = []domain.Build{{
		Repo: "arc42/faq.arc42.org-site", Workflow: "Refresh training dates",
		WorkflowPath: ".github/workflows/refresh-trainings.yml",
		Status:       "completed", Conclusion: "success",
	}}
	body := getAuthed(t, dashHandler(t, store), "/builds").Body.String()

	const want = "https://img.shields.io/github/actions/workflow/status/" +
		"arc42/faq.arc42.org-site/refresh-trainings.yml"
	if !strings.Contains(body, `src="`+want+`"`) {
		t.Errorf("the badge URL is not on the page; wanted %s", want)
	}

	badge := firstLineContaining(body, "img.shields.io")
	if badge == "" {
		t.Fatal("no badge image")
	}
	// The badge is somebody else's request, made from the visitor's browser. Without this it
	// would carry this dashboard's URL to a third party in the Referer header.
	if !strings.Contains(body, `referrerpolicy="no-referrer"`) {
		t.Error("the badge sends this dashboard's URL to shields.io as a referrer")
	}
	// A badge is an image, and an image whose meaning is a build state needs a text alternative
	// naming the repository it belongs to (FR-1.5 AC2).
	if !strings.Contains(badge, `alt="Build badge for arc42/faq.arc42.org-site`) {
		t.Errorf("the badge has no alt text naming its repository: %s", badge)
	}
}

// The badge is only offered where there is something to address. GitHub's built-in Pages
// deployment reports a path that is not a file in the repository, and every badge service answers
// such a request with "repo or workflow not found" — which on this page reads as a broken build.
func TestABuildWithNoWorkflowFileOffersNoBadge(t *testing.T) {
	store := representativeStore()
	store.builds = []domain.Build{{
		Repo: "arc42/arc42.org-site", Workflow: "pages build and deployment",
		WorkflowPath: "dynamic/pages/pages-build-deployment",
		Status:       "completed", Conclusion: "success",
	}}
	body := getAuthed(t, dashHandler(t, store), "/builds").Body.String()

	if strings.Contains(body, "img.shields.io") {
		t.Error("a badge was built for a run with no workflow file behind it")
	}
	if !strings.Contains(body, "no badge") {
		t.Error("the page does not say why the badge is missing")
	}
}

// The one external host this site loads anything from has to be named in the policy, or every
// badge is blocked and the column is a row of broken images (QS-4.4).
func TestTheContentSecurityPolicyAllowsTheBadgeHost(t *testing.T) {
	rec := getAuthed(t, dashHandler(t, representativeStore()), "/builds")

	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "img-src 'self' data: https://img.shields.io") {
		t.Errorf("img-src does not allow the badge host: %s", csp)
	}
	// It is one host on one directive. A wildcard would let anything injected into any page load
	// an image from anywhere, which is a working exfiltration channel for whatever the URL says.
	if strings.Contains(csp, "img-src") && strings.Contains(csp, "*") {
		t.Errorf("the policy has a wildcard in it: %s", csp)
	}
}

// The header's mark carries the state of the last refresh run, rendered by the server, so a page
// opened while a run is in flight shows the run with no script involved (FR-1.3 AC3).
func TestTheHeaderMarkShowsWhatTheLastRefreshRunDid(t *testing.T) {
	cases := []struct {
		name string
		run  domain.RefreshRun
		want string
	}{
		{"a run in flight", domain.RefreshRun{ID: 1, StartedAt: testNow.Add(-time.Minute)}, "refreshing"},
		{"a run that failed", domain.RefreshRun{ID: 1, StartedAt: testNow.Add(-time.Hour),
			FinishedAt: testNow.Add(-time.Hour), OK: false}, "stale"},
		{"a run that worked", domain.RefreshRun{ID: 1, StartedAt: testNow.Add(-time.Hour),
			FinishedAt: testNow.Add(-time.Hour), OK: true}, "idle"},
		{"no run at all", domain.RefreshRun{}, "idle"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := representativeStore()
			store.lastRun = c.run
			body := getAuthed(t, dashHandler(t, store), "/").Body.String()

			mark := firstLineContaining(body, `class="orbit"`)
			if mark == "" {
				t.Fatal("the header has no mark")
			}
			if !strings.Contains(mark, `data-state="`+c.want+`"`) {
				t.Errorf("the mark is not %q: %s", c.want, mark)
			}
		})
	}
}

// A page that knows nothing about refresh runs must not have its mark claim anything. The sign-in
// page is reached without a session and carries no dashboard at all.
func TestThePageWithoutADashboardHasAnIdleMark(t *testing.T) {
	body := get(dashHandler(t, representativeStore()), "/login").Body.String()

	if mark := firstLineContaining(body, `class="orbit"`); !strings.Contains(mark, `data-state="idle"`) {
		t.Errorf("the sign-in page's mark is not idle: %s", mark)
	}
}

// The enhancement that drives the mark while "Refresh now" is running is a file, wired by a data
// attribute rather than an inline handler the CSP would forbid (QS-4.4) — and the form beneath it
// is still the plain form post it was, so the control works with script switched off
// (FR-1.3 AC3).
func TestTheRefreshControlIsAPlainFormWithAnOrbitHook(t *testing.T) {
	body := getAuthed(t, dashHandler(t, representativeStore()), "/").Body.String()

	if !strings.Contains(body, `<script src="/static/orbit.js?`) {
		t.Error("the orbit enhancement is not linked")
	}
	form := firstLineContaining(body, `action="/refresh"`)
	if form == "" {
		t.Fatal("there is no refresh control")
	}
	if !strings.Contains(form, "data-orbit-trigger") {
		t.Errorf("the refresh control carries no hook for the mark: %s", form)
	}
	if !strings.Contains(form, `method="post"`) {
		t.Errorf("the refresh control is not a form post, so it needs script to work: %s", form)
	}
}
