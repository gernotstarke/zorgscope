// These tests live in package web rather than web_test for the same reason auth_test.go does:
// they reach for the route table's helpers and for the unexported view types that turn a
// domain.Dashboard into markup.
package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/refresh"
)

// FR-1.1 AC1, FR-1.2 AC1/AC2.
func TestDashboardRendersTilesAndNewBadges(t *testing.T) {
	store := &dashStore{
		lastVisit: testNow.Add(-2 * time.Hour),
		items: []domain.Item{
			ghItem(1, "An issue from last week", testNow.Add(-7*24*time.Hour)),
			ghItem(2, "An issue that arrived since", testNow.Add(-time.Hour)),
		},
		states: healthyStates(testNow),
	}
	body := getAuthed(t, dashHandler(t, store), "/").Body.String()

	for _, want := range []string{"GitHub", "Builds", "Sites", "Tasks"} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q tile on the dashboard (FR-1.1 AC1)", want)
		}
	}
	if !strings.Contains(body, "NEW") {
		t.Error("the new item has no NEW badge (FR-1.2 AC1)")
	}
	if n := strings.Count(body, "NEW"); n != 1 {
		t.Errorf("NEW appears %d times, want 1 (FR-1.2 AC1)", n)
	}
	if !strings.Contains(body, "1 new") {
		t.Error("the GitHub tile does not state how many of its items are new (FR-1.2 AC2)")
	}
}

// FR-1.2 AC3.
func TestTabTitleCarriesTheNewCount(t *testing.T) {
	store := &dashStore{
		lastVisit: testNow.Add(-2 * time.Hour),
		items:     []domain.Item{ghItem(1, "Fresh", testNow.Add(-time.Hour))},
		states:    healthyStates(testNow),
	}
	body := getAuthed(t, dashHandler(t, store), "/").Body.String()
	if !strings.Contains(body, "<title>(1) zorgscope") {
		t.Errorf("title does not carry the new count:\n%s", firstLineContaining(body, "<title>"))
	}
}

// FR-1.2 AC3: "when it is greater than zero" — nothing new means no prefix at all.
func TestTabTitleHasNoPrefixWhenNothingIsNew(t *testing.T) {
	store := &dashStore{
		lastVisit: testNow,
		items:     []domain.Item{ghItem(1, "Old news", testNow.Add(-72*time.Hour))},
		states:    healthyStates(testNow),
	}
	body := getAuthed(t, dashHandler(t, store), "/").Body.String()
	if !strings.Contains(body, "<title>zorgscope</title>") {
		t.Errorf("title carries a prefix with nothing new:\n%s", firstLineContaining(body, "<title>"))
	}
}

// FR-1.3 AC1/AC2/AC3.
func TestMarkAllSeenClearsTheBadges(t *testing.T) {
	store := &dashStore{
		lastVisit: testNow.Add(-2 * time.Hour),
		items:     []domain.Item{ghItem(1, "Fresh", testNow.Add(-time.Hour))},
		states:    healthyStates(testNow),
	}
	h := dashHandler(t, store)
	c := signIn(t, h)

	if body := getAs(h, "/", c).Body.String(); !strings.Contains(body, "NEW") {
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
	if len(store.visits) != 1 || !store.visits[0].Equal(testNow) {
		t.Errorf("last visit recorded as %v, want one write of %v (FR-1.3 AC1)", store.visits, testNow)
	}

	if body := getAs(h, "/", c).Body.String(); strings.Contains(body, "NEW") {
		t.Error("an item still carries NEW after mark all seen (FR-1.3 AC2)")
	}
}

// FR-1.1 AC2.
func TestDashboardMakesNoUpstreamRequest(t *testing.T) {
	fetcher := &ports.FakeFetcher{SourceName: "github"}
	store := &dashStore{
		items:  []domain.Item{ghItem(1, "Stored already", testNow.Add(-time.Hour))},
		states: healthyStates(testNow),
	}
	s := newTestServerWith(t, func(o *Options) {
		credentialAllSources(o)
		o.Store = store
		o.Runner = refresh.New(store, []ports.SourceFetcher{fetcher}, o.Clock, nil,
			slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	h := s.Handler()

	getAuthed(t, h, "/")
	getAuthed(t, h, "/tile/github")

	if n := fetcher.CallCount(); n != 0 {
		t.Errorf("rendering the dashboard fetched %d time(s) upstream, want 0 (FR-1.1 AC2)", n)
	}
}

// FR-1.1 AC3.
func TestHeaderCarriesTheLogoAndTheLastRunTime(t *testing.T) {
	store := &dashStore{
		lastRun: domain.RefreshRun{FinishedAt: testNow.Add(-20 * time.Minute), OK: true},
		states:  healthyStates(testNow),
	}
	body := getAuthed(t, dashHandler(t, store), "/").Body.String()
	if !strings.Contains(body, `class="logo"`) {
		t.Error("the header carries no logo (FR-1.1 AC3)")
	}
	if !strings.Contains(body, "20 minutes ago") {
		t.Error("the header does not state when the last refresh run finished (FR-1.1 AC3)")
	}
}

// FR-1.6 AC1.
func TestTileFragmentRendersWithoutTheLayout(t *testing.T) {
	store := &dashStore{
		items:  []domain.Item{ghItem(1, "An issue", testNow.Add(-time.Hour))},
		states: healthyStates(testNow),
	}
	rec := getAuthed(t, dashHandler(t, store), "/tile/github")
	body := rec.Body.String()

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /tile/github = %d, want 200", rec.Code)
	}
	if strings.Contains(body, "<html") {
		t.Error("a tile fragment must be a fragment, not a page (FR-1.6 AC1)")
	}
	if !strings.Contains(body, `id="tile-github"`) {
		t.Error("the fragment does not replace the tile it came from (FR-1.6 AC1)")
	}
	if !strings.Contains(body, "An issue") {
		t.Error("the fragment carries no content")
	}
}

// FR-1.6 AC1: the tiles poll themselves at the configured refresh interval.
func TestTilesPollAtTheConfiguredInterval(t *testing.T) {
	store := &dashStore{states: healthyStates(testNow)}
	body := getAuthed(t, dashHandler(t, store), "/").Body.String()

	if !strings.Contains(body, `hx-get="/tile/github"`) {
		t.Error("the GitHub tile does not poll its own fragment (FR-1.6 AC1)")
	}
	// testOptions configures a 15-minute interval.
	if !strings.Contains(body, "every 900s") {
		t.Error("the poll trigger does not follow the configured refresh interval (FR-1.6 AC1)")
	}
}

// FR-1.6 AC2.
func TestRenderingIsNeverAVisit(t *testing.T) {
	store := &dashStore{
		lastVisit: testNow.Add(-2 * time.Hour),
		items:     []domain.Item{ghItem(1, "Fresh", testNow.Add(-time.Hour))},
		states:    healthyStates(testNow),
	}
	h := dashHandler(t, store)
	getAuthed(t, h, "/")
	getAuthed(t, h, "/tile/github")

	if len(store.visits) != 0 {
		t.Errorf("rendering wrote the last visit %d time(s), want 0 (FR-1.6 AC2)", len(store.visits))
	}
}

// FR-1.4 AC2/AC3.
func TestFailingSourceShowsItsErrorAndKeepsContent(t *testing.T) {
	store := &dashStore{
		items: []domain.Item{ghItem(1, "Still visible", testNow.Add(-72*time.Hour))},
		states: map[string]domain.SourceState{
			"github": {
				Source:        "github",
				LastSuccessAt: testNow.Add(-30 * time.Minute),
				LastError:     "github: unexpected status 502",
				LastErrorAt:   testNow.Add(-time.Minute),
			},
		},
	}
	body := getAuthed(t, dashHandler(t, store), "/tile/github").Body.String()

	if !strings.Contains(body, "github: unexpected status 502") {
		t.Error("the tile does not show the error text (FR-1.4 AC2)")
	}
	if !strings.Contains(body, "30 minutes ago") {
		t.Error("the tile does not show the time of its last success (FR-1.4 AC2)")
	}
	if !strings.Contains(body, "Still visible") {
		t.Error("a failing source blanked the tile (FR-1.4 AC3)")
	}
}

// FR-1.4 AC1.
func TestEveryTileStatesTheAgeOfItsData(t *testing.T) {
	store := &dashStore{states: map[string]domain.SourceState{
		"github":        {LastSuccessAt: testNow.Add(-5 * time.Minute)},
		"github-builds": {LastSuccessAt: testNow.Add(-5 * time.Minute)},
		"plausible":     {LastSuccessAt: testNow.Add(-5 * time.Minute)},
		"todoist":       {LastSuccessAt: testNow.Add(-5 * time.Minute)},
	}}
	body := getAuthed(t, dashHandler(t, store), "/").Body.String()

	if n := strings.Count(body, "5 minutes ago"); n != 4 {
		t.Errorf("%d of 4 tiles state the age of their data (FR-1.4 AC1)", n)
	}
}

// FR-1.4 AC1: a source whose last success is older than stale_after says so in words, not only in
// colour (FR-1.5 AC2).
func TestStaleTileSaysSoInWords(t *testing.T) {
	store := &dashStore{states: map[string]domain.SourceState{
		"github": {LastSuccessAt: testNow.Add(-3 * time.Hour)}, // stale_after is 45m
	}}
	body := getAuthed(t, dashHandler(t, store), "/tile/github").Body.String()
	if !strings.Contains(body, "Stale") {
		t.Error("a stale tile does not say so (FR-1.4 AC1, FR-1.5 AC2)")
	}
}

// FR-8.2 AC2.
func TestSourceWithoutACredentialSaysSoOnItsTile(t *testing.T) {
	// The default test options configure no upstream credential at all.
	body := getAuthed(t, newTestServerWith(t, func(o *Options) {
		o.Store = &dashStore{}
	}).Handler(), "/").Body.String()

	if n := strings.Count(body, "no credential is configured"); n != 4 {
		t.Errorf("%d of 4 tiles say their source is disabled for lack of a credential (FR-8.2 AC2)", n)
	}
}

// QS-4.3: a tile renders upstream error text, and upstream error text can quote a credential.
func TestATileErrorIsScrubbedOfSecrets(t *testing.T) {
	const secret = "ghp-canary-value-0123456789abcdef"
	store := &dashStore{states: map[string]domain.SourceState{
		"github": {
			LastSuccessAt: testNow.Add(-time.Hour),
			LastError:     "github: 401 for token " + secret,
			LastErrorAt:   testNow,
		},
	}}
	s := newTestServerWith(t, func(o *Options) {
		credentialAllSources(o)
		o.Config.Secrets.GitHubToken = secret
		o.Store = store
	})
	body := getAuthed(t, s.Handler(), "/").Body.String()

	if strings.Contains(body, secret) {
		t.Error("a tile rendered a secret out of an upstream error (QS-4.3)")
	}
	if !strings.Contains(body, "[redacted]") {
		t.Error("the error text was dropped rather than scrubbed; the visitor loses the diagnosis")
	}
}

// FR-3.1 AC1/AC2/AC3.
func TestSitesTileShowsBothWindowsWithTheirChange(t *testing.T) {
	store := &dashStore{
		metrics: []domain.Metric{
			{Site: "one.example", WindowDays: 7, Visitors: 120, Pageviews: 400, PrevVisitors: 100, PrevPageviews: 500},
			{Site: "one.example", WindowDays: 30, Visitors: 500, Pageviews: 1500, PrevVisitors: 500, PrevPageviews: 1000},
			{Site: "two.example", WindowDays: 7, Visitors: 10, Pageviews: 20, PrevVisitors: 0, PrevPageviews: 0},
		},
		states: healthyStates(testNow),
	}
	body := getAuthed(t, dashHandler(t, store), "/tile/sites").Body.String()

	for _, want := range []string{"one.example", "two.example", "120", "400", "500", "1500"} {
		if !strings.Contains(body, want) {
			t.Errorf("the sites tile does not show %q (FR-3.1 AC1)", want)
		}
	}
	if !strings.Contains(body, "up 20.0%") {
		t.Error("no upward change on visitors for the 7-day window (FR-3.1 AC2)")
	}
	if !strings.Contains(body, "down 20.0%") {
		t.Error("no downward change on pageviews for the 7-day window (FR-3.1 AC2)")
	}
	if !strings.Contains(body, "no baseline") {
		t.Error("a change with no previous period must say so rather than read as 0% (FR-3.1 AC2)")
	}
	if strings.Index(body, "one.example") > strings.Index(body, "two.example") {
		t.Error("sites are not listed in configuration order (FR-3.1 AC3)")
	}
}

// A partial Plausible failure leaves a site holding one of its two windows. The missing one must
// read as missing, not as zero.
func TestSiteWithOnlyOneWindowRendersTheOtherAsMissing(t *testing.T) {
	store := &dashStore{
		metrics: []domain.Metric{
			{Site: "one.example", WindowDays: 7, Visitors: 120, Pageviews: 400, PrevVisitors: 100},
		},
		states: healthyStates(testNow),
	}
	body := getAuthed(t, dashHandler(t, store), "/tile/sites").Body.String()

	if !strings.Contains(body, "not available") {
		t.Error("a window that never arrived is not marked as missing")
	}
	if strings.Contains(body, ">0<") {
		t.Error("a window that never arrived rendered as a zero figure")
	}
}

// FR-2.3 AC2.
func TestBuildsTileShowsARunInProgressNextToThePreviousConclusion(t *testing.T) {
	store := &dashStore{
		builds: []domain.Build{
			{Repo: "org/green", Workflow: "CI", Status: "completed", Conclusion: "success",
				RunURL: "https://github.com/org/green/actions/runs/1", FinishedAt: testNow.Add(-time.Hour)},
			{Repo: "org/busy", Workflow: "CI", Status: "in_progress", Conclusion: "failure",
				RunURL: "https://github.com/org/busy/actions/runs/2", FinishedAt: testNow.Add(-3 * time.Hour)},
		},
		states: healthyStates(testNow),
	}
	body := getAuthed(t, dashHandler(t, store), "/tile/builds").Body.String()

	if !strings.Contains(body, "success") {
		t.Error("a completed run does not show its conclusion (FR-2.3 AC1)")
	}
	if !strings.Contains(body, "running") {
		t.Error("a run in progress is not shown as such (FR-2.3 AC2)")
	}
	if !strings.Contains(body, "previously failure") {
		t.Error("a run in progress does not show the previous conclusion next to it (FR-2.3 AC2)")
	}
}

// FR-2.3 AC3: no builds at all is an empty tile, not an error.
func TestBuildsTileWithNoBuildsIsEmptyNotFailing(t *testing.T) {
	store := &dashStore{states: healthyStates(testNow)}
	body := getAuthed(t, dashHandler(t, store), "/tile/builds").Body.String()
	if strings.Contains(body, "Failing") {
		t.Error("a repository without workflows must show no build state rather than an error (FR-2.3 AC3)")
	}
}

// FR-4.1 AC1/AC2. BuildDashboard already orders the tasks tile by due date; the renderer must not
// reorder it back into SortItems' new-first-then-recently-updated order.
func TestTasksTileKeepsTheDomainsUrgencyOrder(t *testing.T) {
	seen := testNow.Add(-100 * time.Hour)
	store := &dashStore{
		lastVisit: testNow,
		items: []domain.Item{
			{Source: "todoist", ExternalID: "todoist:2", Kind: domain.KindTask, Title: "Due this evening",
				URL: "https://todoist.com/showTask?id=2", DueAt: testNow.Add(6 * time.Hour),
				UpdatedAt: testNow, Priority: 1, FirstSeenAt: seen},
			{Source: "todoist", ExternalID: "todoist:1", Kind: domain.KindTask, Title: "Overdue by a week",
				URL: "https://todoist.com/showTask?id=1", DueAt: testNow.Add(-7 * 24 * time.Hour),
				UpdatedAt: testNow.Add(-30 * 24 * time.Hour), Priority: 4, FirstSeenAt: seen},
		},
		states: healthyStates(testNow),
	}
	body := getAuthed(t, dashHandler(t, store), "/tile/tasks").Body.String()

	overdue, evening := strings.Index(body, "Overdue by a week"), strings.Index(body, "Due this evening")
	if overdue < 0 || evening < 0 {
		t.Fatalf("a task is missing from the tile: overdue=%d evening=%d (FR-4.1 AC1)", overdue, evening)
	}
	if overdue > evening {
		t.Error("the renderer re-sorted the tasks tile away from due-date order (FR-4.1 AC2)")
	}
	if !strings.Contains(body, "overdue by 7 days") {
		t.Error("an overdue task is not distinguished in words from one due later (FR-4.1 AC2, FR-1.5 AC2)")
	}
	if !strings.Contains(body, "P1") {
		t.Error("a task does not show its priority (FR-4.1 AC1)")
	}
}

// Nothing derived from upstream data may reach the page unescaped.
func TestUpstreamTextIsEscaped(t *testing.T) {
	store := &dashStore{
		items:  []domain.Item{ghItem(1, `<script>alert("xss")</script>`, testNow.Add(-time.Hour))},
		states: healthyStates(testNow),
	}
	body := getAuthed(t, dashHandler(t, store), "/").Body.String()

	if strings.Contains(body, `<script>alert`) {
		t.Error("an item title reached the page as live markup")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("the item title is missing entirely; it should be present but escaped")
	}
}

// QS-4.4: the Content-Security-Policy carries no 'unsafe-inline', so the page must contain no
// inline style, no inline script and no inline event handler.
func TestThePageNeedsNoUnsafeInline(t *testing.T) {
	store := &dashStore{
		items:   []domain.Item{ghItem(1, "An issue", testNow.Add(-time.Hour))},
		builds:  []domain.Build{{Repo: "org/repo", Workflow: "CI", Status: "completed", Conclusion: "success"}},
		metrics: []domain.Metric{{Site: "one.example", WindowDays: 7, Visitors: 1}},
		states:  healthyStates(testNow),
	}
	body := getAuthed(t, dashHandler(t, store), "/").Body.String()

	for _, forbidden := range []string{`style="`, "<style", " onclick=", " onload=", " onerror="} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the page contains %q, which the CSP forbids (QS-4.4)", forbidden)
		}
	}
	if n := strings.Count(body, "<script"); n != 1 {
		t.Errorf("the page has %d script tags, want exactly the vendored htmx one (QS-4.4)", n)
	}
	if !strings.Contains(body, `<script src="/static/htmx.min.js"`) {
		t.Error("the only script must be the vendored htmx file (QS-4.4)")
	}
}

// The store is the only thing between the handler and a 500; its error must never reach the page.
func TestAStoreFailureRendersTheFixedMessage(t *testing.T) {
	store := &dashStore{fakeStore: fakeStore{err: errors.New("dial libsql://db.example: refused")}}
	h := dashHandler(t, store)

	for _, path := range []string{"/", "/tile/github"} {
		rec := getAuthed(t, h, path)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("GET %s with a broken store = %d, want 500", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "libsql") {
			t.Errorf("GET %s leaked the store's error text (QS-4.3)", path)
		}
	}
}

func TestUnknownTileIsNotFound(t *testing.T) {
	rec := getAuthed(t, dashHandler(t, &dashStore{}), "/tile/nonsense")
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /tile/nonsense = %d, want 404", rec.Code)
	}
}

// QS-2.3.
func TestRenderedPageStaysInsideItsBudget(t *testing.T) {
	body := getAuthed(t, dashHandler(t, representativeStore()), "/").Body.String()
	if n := len(body); n > 150*1024 {
		t.Errorf("dashboard is %d bytes, budget is 150 kB (QS-2.3)", n)
	} else {
		t.Logf("dashboard is %d bytes of the 150 kB budget (QS-2.3)", n)
	}
}

// QS-2.2: the server-side share of the 200 ms budget. The store is in memory here, so what this
// measures is assembly and rendering alone — the part this task owns.
func BenchmarkDashboard(b *testing.B) {
	s, err := New(func() Options {
		o := testOptions()
		credentialAllSources(&o)
		o.Store = representativeStore()
		return o
	}())
	if err != nil {
		b.Fatalf("New() error = %v", err)
	}
	h := s.Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{"token": {testToken}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	c := cookieNamed(rec, sessionCookieName)
	if c == nil {
		b.Fatal("no session cookie")
	}

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

// ---------------------------------------------------------------- helpers

// credentialAllSources gives every source a credential and something to watch, so that no tile
// renders as disabled. The default test options deliberately carry no upstream secret.
func credentialAllSources(o *Options) {
	o.Config.Plausible.Sites = []string{"one.example", "two.example"}
	o.Config.Todoist.Filter = "today | overdue"
	o.Config.Secrets.GitHubToken = "github-token-for-tests"
	o.Config.Secrets.PlausibleKey = "plausible-key-for-tests"
	o.Config.Secrets.TodoistToken = "todoist-token-for-tests"
}

// dashHandler builds a fully credentialed server over store and returns its handler.
func dashHandler(t *testing.T, store ports.Store) http.Handler {
	t.Helper()
	return newTestServerWith(t, func(o *Options) {
		credentialAllSources(o)
		o.Store = store
	}).Handler()
}

func signIn(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	c := cookieNamed(post(h, "/login", url.Values{"token": {testToken}}), sessionCookieName)
	if c == nil {
		t.Fatal("signing in issued no session cookie")
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
		// The store owns FirstSeenAt; here it is the fixture's whole point.
		FirstSeenAt: at,
	}
}

func healthyStates(at time.Time) map[string]domain.SourceState {
	m := make(map[string]domain.SourceState, 4)
	for _, s := range []string{"github", "github-builds", "plausible", "todoist"} {
		m[s] = domain.SourceState{Source: s, LastSuccessAt: at}
	}
	return m
}

// representativeStore is QS-2.3's representative configuration: 10 repositories, 4 sites and
// 30 tasks.
func representativeStore() *dashStore {
	s := &dashStore{lastVisit: testNow.Add(-24 * time.Hour), states: healthyStates(testNow)}
	for r := range 10 {
		repo := "org/repository-number-" + strconv.Itoa(r)
		for i := range 15 {
			n := r*100 + i
			s.items = append(s.items, domain.Item{
				Source:     "github",
				ExternalID: "issue:" + repo + "#" + strconv.Itoa(n),
				Kind:       domain.KindIssue,
				Repo:       repo,
				Number:     n,
				Title:      "A reasonably long issue title that describes some problem, number " + strconv.Itoa(n),
				URL:        "https://github.com/" + repo + "/issues/" + strconv.Itoa(n),
				Author:     "a-contributor",
				State:      "OPEN",
				CreatedAt:  testNow.Add(-time.Duration(n) * time.Hour),
				UpdatedAt:  testNow.Add(-time.Duration(i) * time.Hour),
				// Half of them are new, which is the expensive case for the badges.
				FirstSeenAt: testNow.Add(-time.Duration(2*i) * time.Hour),
			})
		}
		s.builds = append(s.builds, domain.Build{
			Repo: repo, Workflow: "Continuous integration", Status: "completed", Conclusion: "success",
			RunURL:     "https://github.com/" + repo + "/actions/runs/" + strconv.Itoa(r),
			FinishedAt: testNow.Add(-time.Duration(r) * time.Hour),
		})
	}
	for i := range 4 {
		site := "site-number-" + strconv.Itoa(i) + ".example"
		for _, w := range []int{7, 30} {
			s.metrics = append(s.metrics, domain.Metric{
				Site: site, WindowDays: w, Visitors: 1000 + i, Pageviews: 5000 + i,
				PrevVisitors: 900 + i, PrevPageviews: 5100 + i, FetchedAt: testNow,
			})
		}
	}
	for i := range 30 {
		s.items = append(s.items, domain.Item{
			Source:      "todoist",
			ExternalID:  "todoist:" + strconv.Itoa(i),
			Kind:        domain.KindTask,
			Title:       "A task with a reasonably descriptive title, number " + strconv.Itoa(i),
			URL:         "https://app.todoist.com/app/task/" + strconv.Itoa(i),
			DueAt:       testNow.Add(time.Duration(i-15) * 24 * time.Hour),
			Priority:    1 + i%4,
			UpdatedAt:   testNow.Add(-time.Duration(i) * time.Hour),
			FirstSeenAt: testNow.Add(-time.Duration(i) * time.Hour),
		})
	}
	return s
}

// dashStore is a ports.Store holding a fixture in memory. It extends auth_test.go's fakeStore
// rather than replacing it: the error behaviour the canary test relies on is inherited, and only
// the read methods the dashboard uses — plus the one write it performs — are overridden.
type dashStore struct {
	fakeStore
	items     []domain.Item
	builds    []domain.Build
	metrics   []domain.Metric
	states    map[string]domain.SourceState
	lastRun   domain.RefreshRun
	lastVisit time.Time
	// visits records every SetLastVisit, so a test can assert both that "mark all seen" writes
	// one and that rendering writes none (FR-1.6 AC2).
	visits []time.Time
}

func (s *dashStore) Items(context.Context) ([]domain.Item, error)   { return s.items, s.err }
func (s *dashStore) Builds(context.Context) ([]domain.Build, error) { return s.builds, s.err }
func (s *dashStore) Metrics(context.Context) ([]domain.Metric, error) {
	return s.metrics, s.err
}

func (s *dashStore) SourceStates(context.Context) (map[string]domain.SourceState, error) {
	return s.states, s.err
}

func (s *dashStore) LastRun(context.Context) (domain.RefreshRun, error) {
	return s.lastRun, s.err
}

func (s *dashStore) LastVisit(context.Context) (time.Time, error) { return s.lastVisit, s.err }

func (s *dashStore) SetLastVisit(_ context.Context, t time.Time) error {
	if s.err != nil {
		return s.err
	}
	s.visits = append(s.visits, t)
	s.lastVisit = t
	return nil
}
