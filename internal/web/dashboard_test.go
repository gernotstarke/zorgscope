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

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/snapshot"
)

// FR-1.1 AC1, FR-1.2 AC1/AC2.
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
		t.Error("the list does not state how many items are open (FR-1.2 AC2)")
	}
}

// design §5: NEW is "created after my previous mark seen". An item created exactly at the seen
// mark is not new either — only a first sighting strictly after it counts.
func TestNewMarkerAppearsOnlyForItemsCreatedAfterSeen(t *testing.T) {
	seen := testNow.Add(-2 * time.Hour)
	src := &fakeSource{items: []domain.Item{
		ghItem(1, "Created exactly at the seen mark", seen),
		ghItem(2, "Created after the seen mark", seen.Add(time.Minute)),
		ghItem(3, "Created well before", seen.Add(-time.Hour)),
	}}
	h := dashHandler(t, src)
	body := getAs(h, "/", mintSessionSeenAt(seen)).Body.String()

	if n := strings.Count(body, `<span class="badge badge-new">NEW</span>`); n != 1 {
		t.Errorf("the per-item NEW marker appears %d times, want 1", n)
	}
	if !strings.Contains(body, "NEW 1") {
		t.Error("the header's NEW total does not count the one item created after seen")
	}
}

// FR-1.2 AC3.
func TestTabTitleCarriesTheNewCount(t *testing.T) {
	src := &fakeSource{items: []domain.Item{ghItem(1, "Fresh", testNow.Add(-time.Hour))}}
	h := dashHandler(t, src)
	body := getAs(h, "/", mintSessionSeenAt(testNow.Add(-2*time.Hour))).Body.String()
	if !strings.Contains(body, "<title>(1) zorgscope") {
		t.Errorf("title does not carry the new count:\n%s", firstLineContaining(body, "<title>"))
	}
}

// FR-1.2 AC3: "when it is greater than zero" — nothing new means no prefix at all.
func TestTabTitleHasNoPrefixWhenNothingIsNew(t *testing.T) {
	src := &fakeSource{items: []domain.Item{ghItem(1, "Old news", testNow.Add(-72*time.Hour))}}
	h := dashHandler(t, src)
	body := getAs(h, "/", mintSessionSeenAt(testNow)).Body.String()
	if !strings.Contains(body, "<title>zorgscope</title>") {
		t.Errorf("title carries a prefix with nothing new:\n%s", firstLineContaining(body, "<title>"))
	}
}

// FR-1.3: POST /seen re-mints the cookie with seen = now, and the next GET shows no NEW.
func TestMarkSeenClearsTheNewBadgeOnTheNextGet(t *testing.T) {
	src := &fakeSource{items: []domain.Item{ghItem(1, "Fresh", testNow.Add(-time.Hour))}}
	h := dashHandler(t, src)
	// Seen before the item was created, so it starts out NEW; a fresh sign-in's seen = 0 would
	// show nothing as new at all (design §4), which is not what this test needs to prove.
	c := mintSessionSeenAt(testNow.Add(-2 * time.Hour))

	if body := getAs(h, "/", c).Body.String(); !strings.Contains(body, "badge-new") {
		t.Fatal("the fixture is wrong: nothing is new before the click")
	}

	rec := postAs(h, "/seen", nil, c)
	// A plain form post, so the action works with JavaScript disabled (FR-1.3 AC3).
	if rec.Code != http.StatusSeeOther {
		t.Errorf("POST /seen = %d, want %d: a no-JavaScript form post needs a redirect (FR-1.3 AC3)",
			rec.Code, http.StatusSeeOther)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Errorf("POST /seen redirects to %q, want %q", got, "/")
	}
	reminted := cookieNamed(rec, sessionCookieName)
	if reminted == nil {
		t.Fatal("POST /seen set no cookie")
	}

	if body := getAs(h, "/", reminted).Body.String(); strings.Contains(body, "badge-new") {
		t.Error("an item still carries NEW after mark all seen (FR-1.3 AC2)")
	}
}

// design §5: POST /refresh invalidates the cache, so the next GET fetches again.
func TestRefreshInvalidatesTheCacheSoTheNextGetFetches(t *testing.T) {
	src := &fakeSource{items: []domain.Item{ghItem(1, "One", testNow.Add(-time.Hour))}}
	h := dashHandler(t, src)
	c := signIn(t, h)

	getAs(h, "/", c) // the first view pays the one fetch
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

	getAs(h, "/", c)
	if n := src.CallCount(); n != 2 {
		t.Errorf("fetches after refresh = %d, want 2: /refresh must make the next GET fetch again", n)
	}
}

// Two page views within the TTL share one fetch: rendering reads the cache, it does not decide
// on its own to go to GitHub (FR-1.1 AC2's stateless equivalent).
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
	h := dashHandler(t, src)
	c := signIn(t, h)

	getAs(h, "/", c) // populate the cache with a good fetch

	src.setErr(errors.New("github: unexpected status 502"))
	postAs(h, "/refresh", nil, c)

	body := getAs(h, "/", c).Body.String()
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

// FR-1.6: GET /items narrows by the same filter as the page, and answers with the fragment alone.
func TestItemsFragmentNarrowsByRepoAndIsFragmentOnly(t *testing.T) {
	inRepo := ghItem(1, "In org/repo", testNow.Add(-time.Hour))
	inOther := domain.Item{
		Source: "github", ExternalID: "issue:org/other#2", Kind: domain.KindIssue,
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
		t.Error("the list fragment must be a fragment, not a page (FR-1.6 AC1)")
	}
	if !strings.Contains(body, "In org/repo") {
		t.Error("the fragment does not show the matching item")
	}
	if strings.Contains(body, "In org/other") {
		t.Error("the fragment did not narrow to the requested repository")
	}
}

// FR-1.6 AC1.
func TestTheItemsFragmentRendersWithoutTheLayout(t *testing.T) {
	src := &fakeSource{items: []domain.Item{ghItem(1, "An issue", testNow.Add(-time.Hour))}}
	rec := getAuthed(t, dashHandler(t, src), "/items")
	body := rec.Body.String()

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /items = %d, want 200", rec.Code)
	}
	if strings.Contains(body, "<html") {
		t.Error("the list fragment must be a fragment, not a page (FR-1.6 AC1)")
	}
	if !strings.Contains(body, `id="items"`) {
		t.Error("the fragment does not replace the list it came from (FR-1.6 AC1)")
	}
	if !strings.Contains(body, "An issue") {
		t.Error("the fragment carries no content")
	}
}

// FR-1.2: the filter narrows the list from the query string alone, so the narrowed page is a URL
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

// FR-1.3 AC3: the filter is a plain GET form with htmx on top, never a form that only works with
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
	for _, want := range []string{`name="repo"`, `name="kind"`, `name="since"`, `name="q"`} {
		if !strings.Contains(body, want) {
			t.Errorf("form lacks %s", want)
		}
	}
	if !strings.Contains(body, "<noscript><button type=\"submit\">Apply</button></noscript>") {
		t.Error("the form cannot be applied with JavaScript switched off (FR-1.3 AC3)")
	}
}

// FR-1.2: the NEW total and the tab title count what is new, not what is visible. A filter that
// moved either would make the dashboard lie about what has arrived the moment somebody typed in
// the box.
func TestTheNewBadgeIgnoresTheFilter(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: representativeItems()})
	c := mintSessionSeenAt(testNow.Add(-24 * time.Hour))

	all := getAs(h, "/", c).Body.String()
	some := getAs(h, "/?q=zzz-no-such-text", c).Body.String()
	title := func(s string) string {
		i := strings.Index(s, "<title>")
		j := strings.Index(s, "</title>")
		return s[i:j]
	}
	if title(all) != title(some) {
		t.Fatalf("tab title changed with the filter: %q vs %q", title(all), title(some))
	}
	badge := func(s string) string { return firstLineContaining(s, "badge-new") }
	if badge(all) != badge(some) {
		t.Errorf("the NEW badge changed with the filter:\n%s\n%s", badge(all), badge(some))
	}
}

// Rendering is never a visit: only POST /seen may move the seen-mark, so a plain GET must never
// re-mint the session cookie.
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
			t.Errorf("GET %s re-minted the session cookie; only POST /seen may move the seen-mark (FR-1.3 AC2)", p)
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
		"GET /":            getAs(h, "/", c).Body.String(),
		"GET /?kind=pr":    getAs(h, "/?kind=pr", c).Body.String(),
		"GET /login":       get(h, "/login").Body.String(),
		"GET /items":       getAs(h, "/items", c).Body.String(),
		"POST /seen (401)": post(h, "/seen", nil).Body.String(),
	}
	// The sign-in page's error state renders visitor-facing text, so it is swept too. A callback
	// with no state cookie is the refusal anyone who did not start here is answered with.
	pages["GET /auth/callback (refused)"] = get(h, "/auth/callback?code=x&state=y").Body.String()

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

// QS-2.2: the server-side share of the 200 ms budget. The cache is warm here, so what this
// measures is assembly and rendering alone.
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

// FR-2.2 AC1: a stored row whose created_at is zero must read as unknown, not render an empty
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
		t.Error("an item with no creation time does not say so (FR-2.2 AC1)")
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

// ---------------------------------------------------------------- helpers

// dashServer builds a fully configured server whose snapshot cache is backed by src, over the
// repositories these tests expect, in the order they rely on for grouping.
func dashServer(t *testing.T, src ports.Source) *Server {
	t.Helper()
	return newTestServerWith(t, func(o *Options) {
		// The one repository ghItem produces, plus the ten representativeItems holds: the
		// grouping follows configuration order, so a fixture repository nobody is watching
		// would be listed after the configured ones rather than where the tests expect it.
		o.Config.GitHub.Repos = append([]string{"org/repo"}, representativeRepos()...)
		o.Cache = snapshot.New(src, time.Hour, o.Clock)
	})
}

func dashHandler(t *testing.T, src ports.Source) http.Handler {
	t.Helper()
	return dashServer(t, src).Handler()
}

// mintSession is the cookie a browser holds right after signing in: seen is zero, so nothing is
// NEW until the visitor marks it. It is made directly from the client secret every test server is
// built with, rather than by walking the OAuth flow, which is signin_test.go's own subject.
func mintSession() *http.Cookie {
	return &http.Cookie{
		Name:  sessionCookieName,
		Value: newSessionCodec(testClientSecret).mint(session{Expiry: testNow.Add(sessionTTL)}),
	}
}

// mintSessionSeenAt is the cookie for a session whose seen-mark is seen, for tests of the NEW
// boundary itself.
func mintSessionSeenAt(seen time.Time) *http.Cookie {
	return &http.Cookie{
		Name:  sessionCookieName,
		Value: newSessionCodec(testClientSecret).mint(session{Expiry: testNow.Add(sessionTTL), Seen: seen}),
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
		Source:     "github",
		ExternalID: "issue:org/repo#" + id,
		Kind:       domain.KindIssue,
		Repo:       "org/repo",
		Number:     number,
		Title:      title,
		URL:        "https://github.com/org/repo/issues/" + id,
		Author:     "someone",
		State:      "OPEN",
		CreatedAt:  at,
		UpdatedAt:  at,
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
			items = append(items, domain.Item{
				Source:     "github",
				ExternalID: string(kind) + ":" + repo + "#" + strconv.Itoa(n),
				Kind:       kind,
				Repo:       repo,
				Number:     n,
				Title:      "A reasonably long issue title that describes some problem, number " + strconv.Itoa(n),
				URL:        "https://github.com/" + repo + "/issues/" + strconv.Itoa(n),
				Author:     "a-contributor",
				State:      "OPEN",
				CreatedAt:  testNow.Add(-time.Duration(n) * time.Hour),
				UpdatedAt:  testNow.Add(-time.Duration(i) * time.Hour),
			})
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
