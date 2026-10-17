package web

import (
	"net/http"
	"strings"
	"testing"
)

// FR-1.18 AC1: /about is the logo, the tagline and the credit as the page's own text, so it is
// complete without JavaScript, and the radar is a static file of its own, linked here only.
func TestTheAboutPageIsCompleteAtRest(t *testing.T) {
	rec := get(newTestServer(t).Handler(), "/about")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /about anonymously = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`aria-label="zorgscope"`,
		"keep your issues and PRs under control",
		"in Cologne by Gernot, courtesy",
		`href="https://arc42.org"`,
		`src="/static/about.js?`,
		`id="about-play" hidden`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the about page lacks %s", want)
		}
	}
	main := body[strings.Index(body, "<main"):strings.Index(body, "</main>")]
	letters := strings.Count(main, `<span aria-hidden="true">`)
	if letters != len("zorgscope") {
		t.Errorf("the logo has %d letters, want %d", letters, len("zorgscope"))
	}
	// QS-4.4: the animation sets its values through the CSSOM; the markup carries none.
	for _, forbidden := range []string{`style="`, "<style", " onclick="} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the about page contains %q, which the CSP forbids (QS-4.4)", forbidden)
		}
	}
}

// FR-1.18 AC3: an anonymous visitor gets the bare top bar of the sign-in page; a signed-in one gets
// their own, login included, and the page is then not stored by the browser.
func TestTheAboutPageCarriesTheTopBarOnlyWhenSignedIn(t *testing.T) {
	h := dashHandler(t, &fakeSource{})

	anon := get(h, "/about")
	if strings.Contains(anon.Body.String(), `action="/logout"`) {
		t.Error("the anonymous about page offers Log out")
	}

	signed := getAs(h, "/about", signIn(t, h))
	body := signed.Body.String()
	header := body[strings.Index(body, "<header"):strings.Index(body, "</header>")]
	for _, want := range []string{`action="/logout"`, `class="signed-in-as"`, `id="settings"`} {
		if !strings.Contains(header, want) {
			t.Errorf("the signed-in about page's top bar lacks %s", want)
		}
	}
	if got := signed.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store on a page that names the login", got)
	}
	// Nothing is fetched for this page, so the open counts are left out rather than claiming
	// nothing is open.
	if strings.Contains(header, `class="open-count"`) {
		t.Error("the about page draws open counts it never fetched")
	}
}

// FR-1.18 AC2: the footer's "Made with love in Cologne" leads to /about on every page, the sign-in
// page included, and the settings panel offers it too.
func TestTheAboutPageIsReachedFromTheFooterAndTheSettings(t *testing.T) {
	h := dashHandler(t, &fakeSource{})
	for _, page := range []struct {
		path   string
		signed bool
	}{{"/", true}, {"/login", false}, {"/about", false}} {
		rec := get(h, page.path)
		if page.signed {
			rec = getAs(h, page.path, signIn(t, h))
		}
		footer := rec.Body.String()[strings.Index(rec.Body.String(), "<footer"):]
		if !strings.Contains(footer, `<a class="footer-made" href="/about">Made with`) {
			t.Errorf("the footer of %s does not link to /about", page.path)
		}
	}
	panel := getAuthed(t, h, "/").Body.String()
	panel = panel[strings.Index(panel, `id="settings"`):strings.Index(panel, "</details>")]
	if !strings.Contains(panel, `href="/about"`) {
		t.Error("the settings panel does not offer the about page")
	}
}
