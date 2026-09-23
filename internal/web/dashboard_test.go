// These tests live in package web rather than web_test for the same reason auth_test.go does:
// they reach for the route table's helpers and for the unexported view types that turn a
// domain.Dashboard into markup.
package web

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/snapshot"
)

// FR-1.1 AC1: the list is grouped by repository, and says how many items are open.
func TestDashboardRendersTheListGroupedByRepository(t *testing.T) {
	src := &fakeSource{items: []domain.Item{
		ghItem(1, "An issue from last week", testNow.Add(-7*24*time.Hour)),
		ghItem(2, "An issue that arrived since", testNow.Add(-time.Hour)),
	}}
	body := getAuthed(t, dashHandler(t, src), "/").Body.String()

	if !strings.Contains(body, `id="items"`) {
		t.Error("the dashboard has no list section (FR-1.1 AC1)")
	}
	if !strings.Contains(body, `<a href="https://github.com/org/repo"`) {
		t.Error("the list does not group by repository, or the heading does not link it (FR-1.1 AC1)")
	}
	if !strings.Contains(body, "2 open") {
		t.Error("the list does not state how many items are open (FR-1.1)")
	}
}

// The list is the site rather than a page within it, so its tab title is the site name alone —
// nothing is counted into it (FR-1.2 retired 2026-09-18).
func TestTabTitleIsTheSiteName(t *testing.T) {
	src := &fakeSource{items: []domain.Item{ghItem(1, "Old news", testNow.Add(-72*time.Hour))}}
	h := dashHandler(t, src)
	body := getAuthed(t, h, "/").Body.String()
	if !strings.Contains(body, "<title>zorgscope</title>") {
		t.Errorf("the tab title is not the site name alone:\n%s", firstLineContaining(body, "<title>"))
	}
}

// FR-1.4 AC1/AC4: after a fetch in which one repository failed, the page still lists that
// repository's previous items, and the Fetched line still names the last fetch whose items were
// all current rather than the failed one.
func TestAPartialFetchKeepsTheFailingRepositoryAndTheFetchedAtOfTheGoodFetch(t *testing.T) {
	clock := &ports.FixedClock{T: testNow}
	other := ghItem(2, "From the repository that later fails", testNow.Add(-time.Hour))
	other.Repo = "org/repository-number-0"
	src := &fakeSource{items: []domain.Item{ghItem(1, "Before", testNow.Add(-time.Hour)), other}}
	o := testOptions()
	o.Config.GitHub.Repos = append([]string{"org/repo"}, representativeRepos()...)
	o.Clock = clock
	o.Cache = snapshot.New(src, time.Hour, clock)
	s, err := New(o)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	warmCache(t, o.Cache) // one warm-up fetch, so the good fetch below reads it rather than starting its own
	h := s.Handler()
	c := signIn(t, h)
	getAs(h, "/", c) // the good fetch, at testNow

	clock.Advance(2 * time.Minute)
	src.setItems([]domain.Item{ghItem(1, "After", testNow.Add(-time.Hour))})
	src.setErr(errors.New("github: org/repository-number-0: unexpected status 502"))
	postAs(h, "/refresh", nil, c)
	warmCache(t, o.Cache) // deterministically wait for the fetch /refresh made due to land
	body := getSettled(t, h, "/", c).Body.String()

	if !strings.Contains(body, "From the repository that later fails") {
		t.Error("a partial fetch dropped the failing repository's items (FR-1.4 AC1)")
	}
	if !strings.Contains(body, "After") {
		t.Fatal("the fixture is wrong: the repository that did fetch is not current")
	}
	if want := "Fetched " + clockLabel(testNow, s.loc); !strings.Contains(body, want) {
		t.Errorf("the Fetched line names a moment later than the good fetch:\nwant: %s\ngot:  %s",
			want, firstLineContaining(body, "fetched-line"))
	}
}

// design §5: POST /refresh invalidates the cache, so the next GET fetches again.
func TestRefreshInvalidatesTheCacheSoTheNextGetFetches(t *testing.T) {
	src := &fakeSource{items: []domain.Item{ghItem(1, "One", testNow.Add(-time.Hour))}}
	h, _ := dashHandlerWith(t, src)
	c := signIn(t, h)

	getAs(h, "/", c) // the warm-up already paid the one fetch; this view reads it, not a new one
	if n := src.CallCount(); n != 1 {
		t.Fatalf("the fixture is wrong: %d fetches before refresh, want 1", n)
	}

	rec := postAs(h, "/refresh", nil, c)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("POST /refresh = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Errorf("POST /refresh redirects to %q, want %q", got, "/")
	}

	// getSettled reads through the handler, wait page included, rather than reaching past it into
	// the cache directly — deterministic now that the wait page exists to poll past.
	getSettled(t, h, "/", c)
	if n := src.CallCount(); n != 2 {
		t.Errorf("fetches after refresh = %d, want 2: /refresh must make the next GET fetch again", n)
	}
}

// Two page views within the TTL fetch nothing of their own: the warm-up already paid the one fetch
// CallCount counts here, and rendering only reads the cache — it does not decide on its own to go
// to GitHub (FR-1.1 AC2's stateless equivalent).
func TestTwoPageViewsWithinTheTTLShareOneFetch(t *testing.T) {
	src := &fakeSource{items: []domain.Item{ghItem(1, "Stored already", testNow.Add(-time.Hour))}}
	h := dashHandler(t, src)

	getAuthed(t, h, "/")
	getAuthed(t, h, "/items")

	if n := src.CallCount(); n != 1 {
		t.Errorf("two page views fetched %d time(s), want 1 (the cache, not the handler, decides when to fetch)", n)
	}
}

// design §5: a failing fetch keeps the previous items and reports the error; a first fetch that
// fails yields an empty list with the notice.
func TestAFailingSourceShowsTheNoticeAndKeepsThePreviousList(t *testing.T) {
	src := &fakeSource{items: []domain.Item{ghItem(1, "Still visible", testNow.Add(-72*time.Hour))}}
	h, cache := dashHandlerWith(t, src)
	c := signIn(t, h)

	getAs(h, "/", c) // the warm-up already populated the cache with a good fetch; this reads it

	src.setErr(errors.New("github: unexpected status 502"))
	postAs(h, "/refresh", nil, c)

	warmCache(t, cache) // deterministically wait for the fetch /refresh made due to land
	body := getSettled(t, h, "/", c).Body.String()
	if !strings.Contains(body, "github: unexpected status 502") {
		t.Error("the notice does not show the error text (FR-1.4)")
	}
	if !strings.Contains(body, "GitHub unreachable since") {
		t.Error("the notice does not say the source is unreachable")
	}
	if !strings.Contains(body, "Still visible") {
		t.Error("a failing source blanked the previous list")
	}
}

// QS-4.3: the error notice renders upstream error text, and upstream error text can quote a
// credential.
func TestErrorNoticeNeverContainsASecret(t *testing.T) {
	const secret = "ghp-canary-value-0123456789abcdef"
	src := &fakeSource{err: errors.New("github: 401 for token " + secret)}
	s := newTestServerWith(t, func(o *Options) {
		o.Config.Secrets.GitHubToken = secret
		o.Cache = snapshot.New(src, time.Hour, o.Clock)
	})
	body := getAuthed(t, s.Handler(), "/").Body.String()

	if strings.Contains(body, secret) {
		t.Error("the notice rendered a secret out of an upstream error (QS-4.3)")
	}
	if !strings.Contains(body, "[redacted]") {
		t.Error("the error text was dropped rather than scrubbed; the visitor loses the diagnosis")
	}
}

// FR-2.1 AC2: GET /items narrows by the same filter as the page, and answers with the fragment alone.
func TestItemsFragmentNarrowsByRepoAndIsFragmentOnly(t *testing.T) {
	inRepo := ghItem(1, "In org/repo", testNow.Add(-time.Hour))
	inOther := domain.Item{
		Kind: domain.KindIssue,
		Repo: "org/other", Number: 2, Title: "In org/other",
		URL:       "https://github.com/org/other/issues/2",
		CreatedAt: testNow.Add(-time.Hour), UpdatedAt: testNow.Add(-time.Hour),
	}
	src := &fakeSource{items: []domain.Item{inRepo, inOther}}
	h := newTestServerWith(t, func(o *Options) {
		o.Config.GitHub.Repos = []string{"org/repo", "org/other"}
		o.Cache = snapshot.New(src, time.Hour, o.Clock)
	}).Handler()

	rec := getAuthed(t, h, "/items?repo=org/repo")
	body := rec.Body.String()

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /items?repo=org/repo = %d, want 200", rec.Code)
	}
	if strings.Contains(body, "<html") {
		t.Error("the list fragment must be a fragment, not a page (FR-2.1 AC2)")
	}
	if !strings.Contains(body, "In org/repo") {
		t.Error("the fragment does not show the matching item")
	}
	if strings.Contains(body, "In org/other") {
		t.Error("the fragment did not narrow to the requested repository")
	}
}

// FR-2.1 AC2.
func TestTheItemsFragmentRendersWithoutTheLayout(t *testing.T) {
	src := &fakeSource{items: []domain.Item{ghItem(1, "An issue", testNow.Add(-time.Hour))}}
	rec := getAuthed(t, dashHandler(t, src), "/items")
	body := rec.Body.String()

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /items = %d, want 200", rec.Code)
	}
	if strings.Contains(body, "<html") {
		t.Error("the list fragment must be a fragment, not a page (FR-2.1 AC2)")
	}
	if !strings.Contains(body, `id="items"`) {
		t.Error("the fragment does not replace the list it came from (FR-2.1 AC2)")
	}
	if !strings.Contains(body, "An issue") {
		t.Error("the fragment carries no content")
	}
}

// FR-2.1: the filter narrows the list from the query string alone, so the narrowed page is a URL
// that can be reloaded, bookmarked and shared — everything htmx does on top of it is enhancement.
func TestTheFrontPageFiltersByQueryString(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: representativeItems()})
	c := signIn(t, h)

	all := getAs(h, "/", c).Body.String()
	prs := getAs(h, "/?kind=pr", c).Body.String()
	if strings.Count(prs, `class="item`) >= strings.Count(all, `class="item`) {
		t.Fatal("kind=pr did not narrow the list")
	}
	if !strings.Contains(prs, `value="pr" checked`) {
		t.Fatal("the filter form does not echo the applied kind")
	}
	// A filter that matches nothing says which of the two empty lists this is: a filter with
	// nothing behind it, or a dashboard with nothing on it.
	none := getAs(h, "/?q=zzz-no-such-text", c).Body.String()
	if !strings.Contains(none, "Nothing matches this filter.") {
		t.Fatal("an empty filtered result should say so")
	}
	if strings.Contains(getAs(h, "/", c).Body.String(), "Nothing matches this filter.") {
		t.Error("an unfiltered empty list would blame a filter that is not there")
	}
}

// A repository that configuration no longer names keeps the items it already had — the domain
// lists such a group after the configured ones — so a filter naming one selects something real
// and the control that applied it has to be able to show it.
func TestTheFilterEchoesARepositoryConfigurationNoLongerNames(t *testing.T) {
	retired := ghItem(1, "Opened before the repository was dropped", testNow.Add(-time.Hour))
	retired.Repo = "gone/repo"
	src := &fakeSource{items: []domain.Item{retired, ghItem(2, "Still watched", testNow.Add(-time.Hour))}}
	// org/repo is configured; gone/repo is not.
	h := newTestServerWith(t, func(o *Options) {
		o.Config.GitHub.Repos = []string{"org/repo"}
		o.Cache = snapshot.New(src, time.Hour, o.Clock)
	}).Handler()

	body := getAuthed(t, h, "/?repo=gone/repo").Body.String()
	if !strings.Contains(body, `<option value="gone/repo" selected>gone/repo</option>`) {
		t.Errorf("the filter cannot echo a repository configuration no longer names:\n%s",
			firstLineContaining(body, "<option"))
	}
	if !strings.Contains(body, "Opened before the repository was dropped") {
		t.Error("the filtered page does not show the retired repository's items")
	}
	if strings.Contains(body, "Still watched") {
		t.Error("the filter drew the option but narrowed nothing")
	}
	if !strings.Contains(body, `<option value="org/repo">org/repo</option>`) {
		t.Error("appending the retired repository dropped a configured one")
	}
	plain := getAuthed(t, h, "/").Body.String()
	if strings.Contains(plain, `value="gone/repo"`) {
		t.Error("a repository nobody is watching is offered on the unfiltered page")
	}
}

// FR-2.1 AC2: the filter is a plain GET form with htmx on top, never a form that only works with
// script.
func TestTheFilterFormIsAPlainGetFormWithHtmxOnTop(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: representativeItems()})
	body := getAs(h, "/", signIn(t, h)).Body.String()

	form := openingTag(t, body, `<form class="filter"`)
	for _, want := range []string{
		`method="get"`, `action="/"`,
		`hx-get="/"`, `hx-select="#items"`, `hx-target="#items"`,
		`hx-swap="outerHTML"`, `hx-push-url="true"`,
	} {
		if !strings.Contains(form, want) {
			t.Errorf("the filter form lacks %s:\n%s", want, form)
		}
	}
	for _, want := range []string{`name="repo"`, `name="kind"`, `name="since"`, `name="tier"`} {
		if !strings.Contains(body, want) {
			t.Errorf("form lacks %s", want)
		}
	}
	if !strings.Contains(body, "<noscript><button type=\"submit\">Apply</button></noscript>") {
		t.Error("the form cannot be applied with JavaScript switched off (FR-2.1 AC2)")
	}
}

// The filter has no text box of its own: the top bar's search is the one place to type (FR-12.1),
// and a form that also listened for keystrokes would fire on the top bar's box — which it once did,
// racing the search's own hx-push-url. It reacts to its own controls changing and nothing else. A q
// already in the URL still narrows the list and rides along as a hidden field (FR-2.1 AC1).
func TestTheFilterHasNoTextBoxAndKeepsAQueryFromTheURL(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: representativeItems()})
	c := signIn(t, h)
	body := getAs(h, "/", c).Body.String()
	form := openingTag(t, body, `<form class="filter"`)
	if !strings.Contains(form, `hx-trigger="change"`) {
		t.Errorf("the filter reacts to more than its own controls changing:\n%s", form)
	}
	filter := body[strings.Index(body, `<form class="filter"`):]
	filter = filter[:strings.Index(filter, "</form>")]
	if strings.Contains(filter, `name="q"`) {
		t.Error("the unfiltered filter form carries a text field")
	}
	if strings.Contains(filter, "<details class=\"filter-panel\" open") {
		t.Error("the filter is open with nothing in force")
	}

	body = getAs(h, "/?q=header&kind=pr", c).Body.String()
	if n := strings.Count(body, ">Clear filter<"); n != 1 {
		t.Errorf("a filtered list offers Clear filter %d times, want once", n)
	}
	for _, want := range []string{`<input type="hidden" name="q" value="header">`, `<details class="filter-panel" open>`, `id="filter-count"> · 2 active<`, `>Clear filter<`} {
		if !strings.Contains(body, want) {
			t.Errorf("a filtered list lacks %s", want)
		}
	}
}

// FR-2.1 AC3: a repository's count line is taken before the filter is applied, so narrowing the
// list never makes the page understate what is out there.
func TestTheRepositoryTotalIgnoresTheFilter(t *testing.T) {
	src := &fakeSource{items: []domain.Item{
		ghItem(1, "Alpha", testNow.Add(-time.Hour)),
		ghItem(2, "Beta", testNow.Add(-2*time.Hour)),
		ghItem(3, "Gamma", testNow.Add(-3*time.Hour)),
	}}
	h := dashHandler(t, src)
	c := signIn(t, h)

	all := getAs(h, "/", c).Body.String()
	some := getAs(h, "/?q=Alpha", c).Body.String()
	if want := `<span class="repo-count">3</span>`; !strings.Contains(all, want) {
		t.Errorf("the unfiltered count line is not the repository's total:\n%s",
			firstLineContaining(all, "repo-count"))
	}
	if want := `<span class="repo-count">1 of 3</span>`; !strings.Contains(some, want) {
		t.Errorf("the filtered count line does not name the unfiltered total:\n%s",
			firstLineContaining(some, "repo-count"))
	}
}

// FR-1.2 retired 2026-09-18: nothing on the page is marked new, and the route that moved the
// seen mark is gone.
func TestNothingIsMarkedNewAndSeenIsNoRoute(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: representativeItems()})
	body := getAuthed(t, h, "/").Body.String()
	for _, gone := range []string{"badge-new", "is-new", "NEW", "Mark all seen", "seen_at"} {
		if strings.Contains(body, gone) {
			t.Errorf("the page still carries %q", gone)
		}
	}
	if rec := post(h, "/seen", nil); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /seen = %d, want 404 or 405", rec.Code)
	}
}

// A plain GET must never re-mint the session cookie: rendering the page changes nothing about the
// session it was rendered for.
func TestRenderingNeverReMintsTheSessionCookie(t *testing.T) {
	src := &fakeSource{items: []domain.Item{ghItem(1, "Fresh", testNow.Add(-time.Hour))}}
	h := dashHandler(t, src)
	c := signIn(t, h)

	for _, p := range []string{"/", "/items"} {
		rec := getAs(h, p, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", p, rec.Code)
		}
		if cookieNamed(rec, sessionCookieName) != nil {
			t.Errorf("GET %s re-minted the session cookie; rendering changes no session state", p)
		}
	}
}

// design §5: FetchedAt is shown as a clock time in the configured timezone.
func TestTheHeaderStatesWhenTheListWasFetched(t *testing.T) {
	src := &fakeSource{items: []domain.Item{ghItem(1, "One", testNow.Add(-time.Hour))}}
	body := getAuthed(t, dashHandler(t, src), "/").Body.String()
	want := "Fetched " + testNow.In(time.UTC).Format("15:04")
	if !strings.Contains(body, want) {
		t.Errorf("the header does not state when the list was fetched (design §5):\nwant: %s\ngot:  %s",
			want, firstLineContaining(body, "fetched-line"))
	}
}

// A first fetch that fails yields an empty list with the notice, and FetchedAt says "never".
func TestTheHeaderSaysNeverWhenTheFirstFetchFails(t *testing.T) {
	src := &fakeSource{err: errors.New("github: unreachable")}
	body := getAuthed(t, dashHandler(t, src), "/").Body.String()
	if !strings.Contains(body, "Fetched never") {
		t.Errorf("the header does not say the list was never fetched:\n%s", firstLineContaining(body, "fetched-line"))
	}
}

// Nothing derived from upstream data may reach the page unescaped.
func TestUpstreamTextIsEscaped(t *testing.T) {
	src := &fakeSource{items: []domain.Item{ghItem(1, `<script>alert("xss")</script>`, testNow.Add(-time.Hour))}}
	body := getAuthed(t, dashHandler(t, src), "/").Body.String()

	if strings.Contains(body, `<script>alert`) {
		t.Error("an item title reached the page as live markup")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("the item title is missing entirely; it should be present but escaped")
	}
}

// inlineEventHandler matches any HTML attribute of the on* family — onclick, onmouseover, onfocus,
// onsubmit and the rest of them.
var inlineEventHandler = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)

// QS-4.4: the Content-Security-Policy carries no 'unsafe-inline', so nothing this application
// renders may contain an inline style, an inline script or an inline event handler.
func TestNoRenderedHTMLNeedsUnsafeInline(t *testing.T) {
	src := &fakeSource{items: []domain.Item{ghItem(1, "An issue", testNow.Add(-time.Hour))}}
	h := dashHandler(t, src)
	c := signIn(t, h)

	pages := map[string]string{
		"GET /":               getAs(h, "/", c).Body.String(),
		"GET /?kind=pr":       getAs(h, "/?kind=pr", c).Body.String(),
		"GET /login":          get(h, "/login").Body.String(),
		"GET /items":          getAs(h, "/items", c).Body.String(),
		"POST /refresh (401)": post(h, "/refresh", nil).Body.String(),
	}
	// The sign-in page's error state renders visitor-facing text, so it is swept too. A callback
	// with no state cookie is the refusal anyone who did not start here is answered with.
	pages["GET /auth/callback (refused)"] = get(h, "/auth/callback?code=x&state=y").Body.String()

	// The wait page (FR-1.9) is swept too: it is the one page a cold start shows. Nothing below
	// reads release again — the body above is already captured — so whether it closes before or
	// after the sweep that follows makes no difference, and a defer is the simpler cleanup.
	wh, release := coldServer(t)
	defer close(release)
	pages["GET / (waiting)"] = getAs(wh, "/", signIn(t, wh)).Body.String()

	for name, body := range pages {
		t.Run(name, func(t *testing.T) {
			if body == "" {
				t.Fatal("no body to inspect")
			}
			for _, forbidden := range []string{`style="`, "style='", "<style", "javascript:"} {
				if strings.Contains(body, forbidden) {
					t.Errorf("contains %q, which the CSP forbids (QS-4.4)", forbidden)
				}
			}
			if m := inlineEventHandler.FindString(body); m != "" {
				t.Errorf("contains the inline event handler %q, which the CSP forbids (QS-4.4)", strings.TrimSpace(m))
			}
			// Any script at all has to be a file this application serves from /static — never
			// an inline one, which is what 'unsafe-inline' would be needed for.
			external := strings.Count(body, `<script src="/static/`)
			if n := strings.Count(body, "<script"); n != external {
				t.Errorf("has %d script tags of which %d are files under /static; the rest "+
					"would need 'unsafe-inline' (QS-4.4)", n, external)
			}
			if name == "GET /" {
				if !strings.Contains(body, `<script src="/static/htmx.min.js?`) {
					t.Error("the dashboard does not link htmx (QS-4.4)")
				}
			}
			if name == "GET /items" && external != 0 {
				t.Error("the list fragment carries a script tag (QS-4.4)")
			}
		})
	}
}

// QS-2.3.
func TestRenderedPageStaysInsideItsBudget(t *testing.T) {
	body := getAuthed(t, dashHandler(t, &fakeSource{items: representativeItems()}), "/").Body.String()
	if n := len(body); n > 150*1024 {
		t.Errorf("dashboard is %d bytes, budget is 150 kB (QS-2.3)", n)
	} else {
		t.Logf("dashboard is %d bytes of the 150 kB budget (QS-2.3)", n)
	}
}

// BenchmarkDashboard measures the server-side render cost with a warm cache, i.e. assembly and
// rendering alone rather than any upstream fetch. It has no asserted threshold — `go test -bench`
// reports a number for a human to read, it does not fail a build on its own.
func BenchmarkDashboard(b *testing.B) {
	src := &fakeSource{items: representativeItems()}
	o := testOptions()
	o.Config.GitHub.Repos = append([]string{"org/repo"}, representativeRepos()...)
	clock := &ports.FixedClock{T: testNow}
	o.Clock = clock
	o.Cache = snapshot.New(src, time.Hour, clock)
	s, err := New(o)
	if err != nil {
		b.Fatalf("New() error = %v", err)
	}
	warmCache(b, o.Cache)
	h := s.Handler()

	c := mintSession()

	b.ReportAllocs()
	for b.Loop() {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(c)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			b.Fatalf("GET / = %d, want 200", w.Code)
		}
	}
}

func TestHumanise(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "less than a minute"},
		{time.Minute, "1 minute"},
		{90 * time.Second, "1 minute"},
		{30 * time.Minute, "30 minutes"},
		{time.Hour, "1 hour"},
		{5 * time.Hour, "5 hours"},
		{25 * time.Hour, "1 day"},
		{7 * 24 * time.Hour, "7 days"},
		{45 * 24 * time.Hour, "1 month"},
		{400 * 24 * time.Hour, "1 year"},
		{-2 * time.Hour, "2 hours"},
	}
	for _, c := range cases {
		if got := humanise(c.d); got != c.want {
			t.Errorf("humanise(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// FR-1.1 AC3: a stored row whose created_at is zero must read as unknown, not render an empty
// <time datetime="">, which is invalid HTML and shows the word "opened" followed by nothing.
func TestAnItemWithoutACreationTimeSaysSo(t *testing.T) {
	item := ghItem(1, "An issue whose creation time did not survive", testNow.Add(-time.Hour))
	item.CreatedAt = time.Time{}
	src := &fakeSource{items: []domain.Item{item}}
	body := getAuthed(t, dashHandler(t, src), "/items").Body.String()

	if strings.Contains(body, `datetime=""`) {
		t.Error("a zero timestamp rendered as an empty <time datetime=\"\">")
	}
	if !strings.Contains(body, "opened at an unknown time") {
		t.Error("an item with no creation time does not say so (FR-1.1 AC3)")
	}
	if !strings.Contains(body, "updated <time") {
		t.Error("the update time went missing along with the creation time")
	}
}

// GET / used to be registered as the mux's catch-all, so every path no other route claimed —
// /admin, a typo, a fragment path with a segment too many — answered 200 with the whole dashboard.
func TestAnUnknownPathIsNotTheDashboard(t *testing.T) {
	h := dashHandler(t, &fakeSource{})
	c := signIn(t, h)

	for _, p := range []string{"/admin", "/no-such-page", "/items/extra"} {
		rec := getAs(h, p, c)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404: an unknown path must not render the dashboard", p, rec.Code)
		}
		if strings.Contains(rec.Body.String(), `id="items"`) {
			t.Errorf("GET %s answered with the dashboard", p)
		}
	}
	if rec := getAs(h, "/", c); rec.Code != http.StatusOK {
		t.Errorf("GET / = %d, want 200: the dashboard itself still has to answer", rec.Code)
	}
}

// QS-2.3, the static half of the budget, measured where the requirement's goal clause points: on
// the wire. Uncompressed the vendored htmx alone is 50 kB, so the budget is unreachable by
// construction unless it means transferred bytes.
func TestStaticAssetsFitTheirBudgetOnTheWire(t *testing.T) {
	const budget = 50 * 1024

	h := dashHandler(t, &fakeSource{items: representativeItems()})
	page := getAuthed(t, h, "/").Body.String()

	assets := linkedStaticAssets(page)
	if len(assets) < 2 {
		t.Fatalf("found %d static assets on the page (%v); the stylesheet and htmx are both "+
			"linked, so the extraction is broken", len(assets), assets)
	}

	total := 0
	for _, asset := range assets {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, asset, nil)
		req.Header.Set("Accept-Encoding", "gzip, deflate, br")
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", asset, rec.Code)
		}
		transferred := rec.Body.Len()
		total += transferred
		t.Logf("%s: %d bytes on the wire", asset, transferred)

		stored, err := fs.ReadFile(embedded, "static"+strings.TrimPrefix(asset, "/static"))
		if err != nil {
			t.Fatalf("reading the embedded %s: %v", asset, err)
		}

		if !compressibleStatic[strings.ToLower(path.Ext(asset))] {
			if got := rec.Header().Get("Content-Encoding"); got != "" {
				t.Errorf("GET %s: Content-Encoding = %q, want none — the file is already compressed",
					asset, got)
			}
			if !bytes.Equal(rec.Body.Bytes(), stored) {
				t.Errorf("GET %s: the body is not the stored asset", asset)
			}
			continue
		}

		if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
			t.Errorf("GET %s: Content-Encoding = %q, want gzip (QS-2.3)", asset, got)
		}
		if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
			t.Errorf("GET %s: Vary = %q, want it to name Accept-Encoding", asset, got)
		}
		zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
		if err != nil {
			t.Fatalf("GET %s: the body is not gzip: %v", asset, err)
		}
		decoded, err := io.ReadAll(zr)
		if err != nil {
			t.Fatalf("GET %s: decoding the body: %v", asset, err)
		}
		if !bytes.Equal(decoded, stored) {
			t.Errorf("GET %s: the decoded body is not the stored asset", asset)
		}
		if transferred >= len(stored) {
			t.Errorf("GET %s: %d bytes on the wire against %d stored — compression saved nothing",
				asset, transferred, len(stored))
		}
	}

	if total > budget {
		t.Errorf("the static assets are %d bytes on the wire, budget is %d (QS-2.3)", total, budget)
	} else {
		t.Logf("the static assets are %d bytes of the %d-byte wire budget (QS-2.3)", total, budget)
	}
}

// A client that does not accept gzip still gets the file. QS-2.3 is about a slow connection, not
// about refusing to serve a text browser or a curl without the header.
func TestStaticIsServedUncompressedWhenTheClientCannotAcceptGzip(t *testing.T) {
	h := newTestServer(t).Handler()
	stored, err := fs.ReadFile(embedded, "static/app.css")
	if err != nil {
		t.Fatalf("reading the embedded stylesheet: %v", err)
	}

	for _, accept := range []string{"", "identity", "br", "deflate"} {
		t.Run("Accept-Encoding: "+accept, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/static/app.css", nil)
			if accept != "" {
				req.Header.Set("Accept-Encoding", accept)
			}
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Content-Encoding"); got != "" {
				t.Errorf("Content-Encoding = %q, want none", got)
			}
			if !bytes.Equal(rec.Body.Bytes(), stored) {
				t.Error("the body is not the stored stylesheet")
			}
			if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
				t.Errorf("Vary = %q, want it to name Accept-Encoding: a shared cache must not "+
					"hand a gzipped body to a client that cannot read it", got)
			}
		})
	}

	// "gzip" has to be matched as an encoding, not as a substring: a q-value or spacing must not
	// change the answer, and an encoding merely containing the word must not be mistaken for it.
	for accept, wantGzip := range map[string]bool{
		"gzip;q=1.0, identity;q=0.5": true,
		" GZIP ":                     true,
		"br, gzip":                   true,
		"x-gzip-not-really":          false,
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/static/app.css", nil)
		req.Header.Set("Accept-Encoding", accept)
		h.ServeHTTP(rec, req)
		if got := rec.Header().Get("Content-Encoding") == "gzip"; got != wantGzip {
			t.Errorf("Accept-Encoding %q: gzipped = %v, want %v", accept, got, wantGzip)
		}
	}
}

// FR-1.8 AC4: "Refresh" goes back to the page it was pressed on — and only ever to a page of this
// site, since the return field is the browser's to send.
func TestRefreshReturnsToThePageItWasPressedOn(t *testing.T) {
	h := dashHandler(t, &fakeSource{})
	c := signIn(t, h)
	for _, tc := range []struct{ ret, want string }{
		{"", "/"},
		{"/sites", "/sites"},
		{"/?repo=org%2Frepo&kind=pr", "/?repo=org%2Frepo&kind=pr"},
		{"https://evil.example/", "/"},
		{"//evil.example/", "/"},
	} {
		t.Run("return="+tc.ret, func(t *testing.T) {
			form := url.Values{}
			if tc.ret != "" {
				form.Set("return", tc.ret)
			}
			rec := postAs(h, "/refresh", form, c)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("POST /refresh = %d, want %d", rec.Code, http.StatusSeeOther)
			}
			if got := rec.Header().Get("Location"); got != tc.want {
				t.Errorf("POST /refresh with return %q redirects to %q, want %q", tc.ret, got, tc.want)
			}
		})
	}
}

// FR-1.8 AC4: the top bar's Refresh form carries the page it sits on, query included, so a
// filtered list comes back filtered — true of a request rendered whole, without JavaScript. Under
// an htmx-driven filter change, hx-push-url updates the address bar but the top bar sits outside
// the swapped #items fragment, so its form keeps carrying the return value from the last full page
// load, not the filter now showing.
func TestTopBarFormsCarryTheCurrentPageAsTheirReturn(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: []domain.Item{ghItem(1, "Anything", testNow)}})
	body := getAuthed(t, h, "/?kind=pr").Body.String()
	const want = `<input type="hidden" name="return" value="/?kind=pr">`
	if line := firstLineContaining(body, `action="/refresh"`); !strings.Contains(line, want) {
		t.Errorf("the Refresh form does not carry its page as return:\n%s", line)
	}
}

// ---------------------------------------------------------------- helpers

// dashServer builds a fully configured server whose snapshot cache is backed by src, over the
// repositories these tests expect, in the order they rely on for grouping.
func dashServer(t *testing.T, src ports.Source) *Server {
	t.Helper()
	s, _ := dashServerWithCache(t, src)
	return s
}

// dashServerWithCache is dashServer plus the cache it built, for a test that must deterministically
// wait for a later fetch — after Invalidate, say — to land: it calls warmCache on the very cache
// the returned server reads from, rather than guessing from HTTP responses alone.
func dashServerWithCache(t *testing.T, src ports.Source) (*Server, *snapshot.Cache) {
	t.Helper()
	s, c := newColdServerWith(t, func(o *Options) {
		// The one repository ghItem produces, plus the ten representativeItems holds: the
		// grouping follows configuration order, so a fixture repository nobody is watching
		// would be listed after the configured ones rather than where the tests expect it.
		o.Config.GitHub.Repos = append([]string{"org/repo"}, representativeRepos()...)
		o.Cache = snapshot.New(src, time.Hour, o.Clock)
	})
	warmCache(t, c)
	return s, c
}

func dashHandler(t *testing.T, src ports.Source) http.Handler {
	t.Helper()
	return dashServer(t, src).Handler()
}

// dashHandlerWith is dashHandler plus the cache it built — see dashServerWithCache.
func dashHandlerWith(t *testing.T, src ports.Source) (http.Handler, *snapshot.Cache) {
	t.Helper()
	s, c := dashServerWithCache(t, src)
	return s.Handler(), c
}

// mintSession is the cookie a browser holds right after signing in. It is made directly from the
// client secret every test server is built with, rather than by walking the OAuth flow, which is
// signin_test.go's own subject.
func mintSession() *http.Cookie {
	return &http.Cookie{
		Name:  sessionCookieName,
		Value: newSessionCodec(testClientSecret).mint(session{Expiry: testNow.Add(sessionTTL)}),
	}
}

// signIn returns that cookie and proves h actually accepts it, so a server built with a different
// client secret fails here rather than in whatever the test went on to assert.
func signIn(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	c := mintSession()
	if rec := getAs(h, "/login", c); rec.Code != http.StatusSeeOther {
		t.Fatalf("the minted session cookie does not sign in: GET /login = %d, want 303", rec.Code)
	}
	return c
}

func getAuthed(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	return getAs(h, path, signIn(t, h))
}

func postAs(h http.Handler, path string, form url.Values, c *http.Cookie) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	h.ServeHTTP(rec, req)
	return rec
}

func firstLineContaining(body, needle string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return "(not found)"
}

func ghItem(number int, title string, at time.Time) domain.Item {
	id := strconv.Itoa(number)
	return domain.Item{
		Kind:      domain.KindIssue,
		Repo:      "org/repo",
		Number:    number,
		Title:     title,
		URL:       "https://github.com/org/repo/issues/" + id,
		Author:    "someone",
		State:     "OPEN",
		CreatedAt: at,
		UpdatedAt: at,
	}
}

// representativeItems is QS-2.3's representative configuration: 10 repositories holding 150 open
// items between them, of both kinds — which is the expensive case for the grouping and for every
// filter that has to be counted around.
func representativeItems() []domain.Item {
	var items []domain.Item
	for r := range 10 {
		repo := "org/repository-number-" + strconv.Itoa(r)
		for i := range 15 {
			n := r*100 + i
			// Every third item is a pull request, so a kind filter has something to narrow to
			// and something to leave out in every repository.
			kind := domain.KindIssue
			if i%3 == 0 {
				kind = domain.KindPR
			}
			item := domain.Item{
				Kind:      kind,
				Repo:      repo,
				Number:    n,
				Title:     "A reasonably long issue title that describes some problem, number " + strconv.Itoa(n),
				URL:       "https://github.com/" + repo + "/issues/" + strconv.Itoa(n),
				Author:    "a-contributor",
				State:     "OPEN",
				CreatedAt: testNow.Add(-time.Duration(n) * time.Hour),
				UpdatedAt: testNow.Add(-time.Duration(i) * time.Hour),
			}
			if i%4 == 0 {
				item.Labels = []string{"bug", "help wanted"}
			}
			items = append(items, item)
		}
	}
	return items
}

// representativeRepos is what a server over representativeItems has to be configured to watch, so
// that the groups come out in configuration order and the filter's repository list offers the
// repositories the fixture actually holds.
func representativeRepos() []string {
	out := make([]string, 0, 10)
	for r := range 10 {
		out = append(out, "org/repository-number-"+strconv.Itoa(r))
	}
	return out
}

// linkedStaticAssets returns every /static/ URL the rendered page references, deduplicated and in
// a stable order.
func linkedStaticAssets(page string) []string {
	re := regexp.MustCompile(`/static/[A-Za-z0-9._/-]+`)
	seen := make(map[string]bool)
	var out []string
	for _, match := range re.FindAllString(page, -1) {
		if seen[match] {
			continue
		}
		seen[match] = true
		out = append(out, match)
	}
	sort.Strings(out)
	return out
}

// openingTag returns the opening tag that starts with prefix, so an assertion about one element's
// attributes cannot be satisfied by another element somewhere else on the page.
func openingTag(t *testing.T, body, prefix string) string {
	t.Helper()
	i := strings.Index(body, prefix)
	if i < 0 {
		t.Fatalf("no element starting %s on the page", prefix)
	}
	rest := body[i:]
	j := strings.Index(rest, ">")
	if j < 0 {
		t.Fatalf("the element starting %s is never closed", prefix)
	}
	return rest[:j+1]
}

// FR-1.10 AC3: labels become chips after the title, in GitHub's order; six names get their own
// class, matched without regard to case and with spaces as hyphens; anything else is "other"; an
// item without labels draws no chip at all.
func TestLabelsRenderAsChipsWithTheFixedPalette(t *testing.T) {
	labelled := ghItem(1, "Labelled", testNow.Add(-time.Hour))
	labelled.Labels = []string{"bug", "Help Wanted", "in progress", "needs-triage", "Documentation"}
	plain := ghItem(2, "Plain", testNow.Add(-time.Hour))
	body := getAuthed(t, dashHandler(t, &fakeSource{items: []domain.Item{labelled, plain}}), "/").Body.String()

	want := `<span class="visually-hidden">Labels:</span>` +
		`<span class="label label-bug">bug</span>` +
		`<span class="label label-help-wanted">Help Wanted</span>` +
		`<span class="label label-in-progress">in progress</span>` +
		`<span class="label label-other">needs-triage</span>` +
		`<span class="label label-documentation">Documentation</span>`
	if !strings.Contains(strings.Join(strings.Fields(body), ""), strings.Join(strings.Fields(want), "")) {
		t.Errorf("the labelled row does not carry the cue before its chips, in order; row:\n%s", firstLineContaining(body, "Labelled"))
	}
	if n := strings.Count(body, `class="label `); n != 5 {
		t.Errorf("page has %d chips, want 5: the plain item must draw none", n)
	}
	if n := strings.Count(body, `class="visually-hidden"`); n != 1 {
		t.Errorf("page has %d Labels cues, want 1: the plain item has no labels and must draw none", n)
	}
	for _, key := range labelKeys {
		if !strings.Contains(key, "-") && strings.Contains(key, " ") {
			t.Errorf("label key %q carries a space; keys are hyphenated", key)
		}
	}
}

// FR-1.10 AC3: the key is the normalised name when it is one of the six, "other" otherwise.
func TestLabelKeyNormalisesCaseAndSpaces(t *testing.T) {
	cases := map[string]string{
		"bug": "bug", "Bug": "bug", "  enhancement ": "enhancement", "Help Wanted": "help-wanted",
		"help-wanted": "help-wanted", "help   wanted": "help-wanted", "in progress": "in-progress",
		"documentation": "documentation", "question": "question", "content": "other", "": "other",
		"wontfix": "other",
	}
	for name, want := range cases {
		if got := labelKey(name); got != want {
			t.Errorf("labelKey(%q) = %q, want %q", name, got, want)
		}
	}
}

// FR-1.10 AC4: a quiet item is marked as such on the row and says the word in its meta line; a
// recently updated one is neither.
func TestQuietItemsAreDimmedAndSayQuiet(t *testing.T) {
	quiet := ghItem(1, "Old thing", testNow.Add(-100*24*time.Hour))
	live := ghItem(2, "Fresh thing", testNow.Add(-time.Hour))
	body := getAuthed(t, dashHandler(t, &fakeSource{items: []domain.Item{quiet, live}}), "/").Body.String()

	quietRow := openingTag(t, body, `<li class="item is-quiet"`)
	if quietRow == "" {
		t.Fatalf("no row carries is-quiet; the 100-day-old item must:\n%s", firstLineContaining(body, "Old thing"))
	}
	if !strings.Contains(body, "</time> · quiet") {
		t.Error("the quiet item's meta line does not say \"quiet\"")
	}
	if strings.Count(body, "is-quiet") != 1 || strings.Count(body, " · quiet") != 1 {
		t.Errorf("quiet marks = %d rows / %d words, want exactly 1 each: the fresh item must carry none",
			strings.Count(body, "is-quiet"), strings.Count(body, " · quiet"))
	}
}

// FR-1.10 AC1: the arc42 rainbow band sits on every page — the list, the Sites view, the sign-in
// page and the wait page — as one element the stylesheet paints; it is never the only thing that
// tells pages apart, so it is hidden from assistive technology.
func TestEveryPageCarriesTheRainbowBand(t *testing.T) {
	const band = `<div class="rainbow" aria-hidden="true"></div>`
	h := dashHandler(t, &fakeSource{items: representativeItems()})
	c := signIn(t, h)
	pages := map[string]string{
		"GET /":      getAs(h, "/", c).Body.String(),
		"GET /sites": getAs(h, "/sites", c).Body.String(),
		"GET /login": get(h, "/login").Body.String(),
	}
	wh, release := coldServer(t)
	defer close(release)
	pages["GET / (waiting)"] = getAs(wh, "/", signIn(t, wh)).Body.String()

	for name, body := range pages {
		if strings.Count(body, band) != 1 {
			t.Errorf("%s carries the band %d times, want exactly once", name, strings.Count(body, band))
		}
	}
	if strings.Contains(getAs(h, "/items", c).Body.String(), band) {
		t.Error("the list fragment carries the band; it belongs to the layout, not the list")
	}
}

// FR-1.10 AC2: a group carries the hue of the site that claims its repository, slate when none
// does — the same rule the Other tile follows.
func TestGroupsCarryTheirSitesHue(t *testing.T) {
	claimed := ghItem(1, "Claimed", testNow.Add(-time.Hour))
	claimed.Repo = "org/claimed"
	unclaimed := ghItem(2, "Unclaimed", testNow.Add(-time.Hour))
	unclaimed.Repo = "org/unclaimed"
	src := &fakeSource{items: []domain.Item{claimed, unclaimed}}
	h := newTestServerWith(t, func(o *Options) {
		o.Config.GitHub.Repos = []string{"org/claimed", "org/unclaimed"}
		o.Config.GitHub.Sites = []config.Site{{Name: "claimed.example", URL: "https://claimed.example", Repo: "org/claimed", Hue: "plum"}}
		o.Cache = snapshot.New(src, time.Hour, o.Clock)
	}).Handler()
	body := getAuthed(t, h, "/").Body.String()

	if !strings.Contains(body, `<section class="repo-group hue-plum">`) {
		t.Errorf("the claimed group does not carry hue-plum:\n%s", firstLineContaining(body, "repo-group"))
	}
	if !strings.Contains(body, `<section class="repo-group hue-slate">`) {
		t.Error("the unclaimed group does not carry hue-slate")
	}
	if strings.Contains(body, `style="`) {
		t.Error("a colour reached the page as a style attribute (QS-4.4)")
	}
}

// A row is two lines: the title, and one meta line with the kind, author, last update and
// description. "opened" is said only when it reads differently from "updated".
func TestARowSaysOpenedOnlyWhenItDiffersFromUpdated(t *testing.T) {
	fresh := ghItem(1, "Fresh", testNow)
	old := ghItem(2, "Old", testNow.Add(-72*time.Hour))
	old.CreatedAt = testNow.Add(-40 * 24 * time.Hour)
	old.Summary = "What the old one is about"
	body := getAuthed(t, dashHandler(t, &fakeSource{items: []domain.Item{fresh, old}}), "/?view=list").Body.String()

	row := func(title string) string {
		i := strings.Index(body, ">"+title+"<")
		return body[i : i+strings.Index(body[i:], "</li>")]
	}
	if strings.Contains(row("Fresh"), "opened") {
		t.Error("a row whose opened and updated read alike says both")
	}
	r := row("Old")
	for _, want := range []string{`class="item-kind item-kind-issue">Issue<`, "updated <time", "opened <time", `<span class="item-summary">What the old one is about</span></p>`} {
		if !strings.Contains(r, want) {
			t.Errorf("the old row lacks %s", want)
		}
	}
	if strings.Count(r, "<p class=\"item-meta\"") != 1 {
		t.Error("the row has more than one meta line")
	}
}
