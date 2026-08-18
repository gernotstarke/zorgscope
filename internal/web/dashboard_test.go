// These tests live in package web rather than web_test for the same reason auth_test.go does:
// they reach for the route table's helpers and for the unexported view types that turn a
// domain.Dashboard into markup.
package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
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
	// The healthy case: the latest run is also the last successful one, so the store answers with
	// the same run twice — and the header says it once.
	run := domain.RefreshRun{StartedAt: testNow.Add(-21 * time.Minute),
		FinishedAt: testNow.Add(-20 * time.Minute), OK: true}
	store := &dashStore{lastRun: run, lastSuccessfulRun: run, states: healthyStates(testNow)}
	body := getAuthed(t, dashHandler(t, store), "/").Body.String()
	if !strings.Contains(body, `class="logo"`) {
		t.Error("the header carries no logo (FR-1.1 AC3)")
	}
	if !strings.Contains(body, "20 minutes ago") {
		t.Error("the header does not state when the last refresh run finished (FR-1.1 AC3)")
	}
	if n := strings.Count(body, "Last successful refresh"); n != 1 {
		t.Errorf("the header says \"Last successful refresh\" %d times, want 1: when the latest "+
			"run is the successful one there is only one time to state", n)
	}
}

// FR-1.1 AC3, the case the header could not answer while the store only offered the most recently
// started run: the latest attempt failed, and the time AC3 asks for belongs to an earlier run.
//
// Both halves have to be on the page. Naming only the failure leaves the header silent about how
// old the data being read actually is — the tiles are full of the last good fetch and nothing says
// when that was — and naming only the success would present a broken refresh as a working one,
// which is the reading FR-1.1 AC3 exists to forbid.
func TestHeaderNamesTheLastSuccessfulRunWhenTheLatestOneFailed(t *testing.T) {
	store := &dashStore{
		lastRun: domain.RefreshRun{StartedAt: testNow.Add(-6 * time.Minute),
			FinishedAt: testNow.Add(-5 * time.Minute), OK: false, Detail: "github: fetch: 500"},
		lastSuccessfulRun: domain.RefreshRun{StartedAt: testNow.Add(-3 * time.Hour),
			FinishedAt: testNow.Add(-2 * time.Hour), OK: true},
		states: healthyStates(testNow),
	}
	body := getAuthed(t, dashHandler(t, store), "/").Body.String()

	if !strings.Contains(body, "Last refresh failed") || !strings.Contains(body, "5 minutes ago") {
		t.Error("the header does not say that the latest refresh failed, and when (FR-1.1 AC3)")
	}
	if !strings.Contains(body, "last successful refresh") || !strings.Contains(body, "2 hours ago") {
		t.Error("the header does not state the time of the last successful refresh run although " +
			"one exists (FR-1.1 AC3)")
	}
}

// The same reach-back while a run is open — the state the 409 "a refresh is already running" page
// is always in. An open run has no finishing time of its own, so without the last successful run
// the header can say only that something is happening, never how old what is on the screen is.
func TestHeaderNamesTheLastSuccessfulRunWhileARefreshIsRunning(t *testing.T) {
	store := &dashStore{
		lastRun: domain.RefreshRun{StartedAt: testNow.Add(-30 * time.Second)},
		lastSuccessfulRun: domain.RefreshRun{StartedAt: testNow.Add(-3 * time.Hour),
			FinishedAt: testNow.Add(-2 * time.Hour), OK: true},
		states: healthyStates(testNow),
	}
	body := getAuthed(t, dashHandler(t, store), "/").Body.String()

	if !strings.Contains(body, "Refreshing now") {
		t.Error("the header does not say that a refresh is in flight (FR-1.1 AC3)")
	}
	if !strings.Contains(body, "last successful refresh") || !strings.Contains(body, "2 hours ago") {
		t.Error("the header does not state the time of the last successful refresh run while a " +
			"refresh is running (FR-1.1 AC3)")
	}
}

// A store that has never completed a good run has no time to state, and the header must not
// invent one from the failed attempt it does have.
func TestHeaderStatesNoSuccessfulRunWhenThereHasNeverBeenOne(t *testing.T) {
	store := &dashStore{
		lastRun: domain.RefreshRun{StartedAt: testNow.Add(-2 * time.Minute),
			FinishedAt: testNow.Add(-time.Minute), OK: false},
		states: healthyStates(testNow),
	}
	body := getAuthed(t, dashHandler(t, store), "/").Body.String()

	if !strings.Contains(body, "Last refresh failed") {
		t.Error("the header does not say that the refresh failed (FR-1.1 AC3)")
	}
	if strings.Contains(body, "successful refresh") {
		t.Error("the header claims a successful refresh although no run has ever succeeded " +
			"(FR-1.1 AC3)")
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
	// The swap has to be outerHTML: the fragment is a whole <section class="tile">, so htmx's
	// default innerHTML swap would nest the returned tile inside the existing one on every poll,
	// duplicating its items and its id, four levels deep after an hour.
	if !strings.Contains(body, `hx-get="/tile/github" hx-trigger="every 900s" hx-swap="outerHTML"`) {
		t.Error("the GitHub tile does not replace itself on a poll (FR-1.6 AC1)")
	}
	if n := strings.Count(body, `hx-swap="outerHTML"`); n != 4 {
		t.Errorf("%d of 4 tiles replace themselves on a poll (FR-1.6 AC1)", n)
	}
	// And the fragment carries it too, or polling stops after the first swap.
	fragment := getAuthed(t, dashHandler(t, store), "/tile/github").Body.String()
	if !strings.Contains(fragment, `hx-swap="outerHTML"`) {
		t.Error("the tile fragment does not carry its own swap, so the tile stops polling " +
			"after the first one (FR-1.6 AC1)")
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
	// As one contiguous string: "30 minutes ago" on its own is equally satisfied by the tile
	// header's age span, so the whole "(last success …)" clause could be deleted unnoticed.
	lastSuccess := `(last success <time datetime="` +
		testNow.Add(-30*time.Minute).UTC().Format(time.RFC3339) + `">30 minutes ago</time>)`
	if !strings.Contains(body, lastSuccess) {
		t.Errorf("the failing tile does not state when the source last succeeded (FR-1.4 AC2).\nwant: %s", lastSuccess)
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

	for _, want := range []string{"one.example", "two.example"} {
		if !strings.Contains(body, want) {
			t.Errorf("the sites tile does not show %q (FR-3.1 AC1)", want)
		}
	}

	// Each figure is asserted together with the change beside it, as one contiguous string. The
	// presence of "120", "400", "up 20.0%" and "down 20.0%" somewhere in the body proves nothing:
	// swapping visitors with pageviews, or the up arm of newChangeView with the down arm, leaves
	// every one of those strings present while the tile tells the visitor the opposite of the
	// truth — a growing site reported as shrinking, in words and in colour.
	for _, want := range []string{
		// one.example, 7 days: visitors 100 → 120, pageviews 500 → 400.
		`visitors <b>120</b> <span class="change change-up">up 20.0%</span>`,
		`pageviews <b>400</b> <span class="change change-down">down 20.0%</span>`,
		// one.example, 30 days: visitors flat at 500, pageviews 1000 → 1500.
		`visitors <b>500</b> <span class="change change-flat">no change</span>`,
		`pageviews <b>1500</b> <span class="change change-up">up 50.0%</span>`,
		// two.example, 7 days: no preceding period at all, so no percentage exists.
		`visitors <b>10</b> <span class="change change-unknown">no baseline</span>`,
		`pageviews <b>20</b> <span class="change change-unknown">no baseline</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the sites tile does not pair a figure with its change (FR-3.1 AC1/AC2).\nwant: %s", want)
		}
	}
	if strings.Index(body, "one.example") > strings.Index(body, "two.example") {
		t.Error("sites are not listed in configuration order (FR-3.1 AC3)")
	}
}

// FR-3.1 AC3: "in configuration order". The order has to come from the configuration, not from
// whatever order the store happened to return the metrics in — so this fixture configures the
// sites in the opposite order to the one the data arrives in, which is the only way the assertion
// can fail when the handler ignores the configuration.
func TestSitesFollowConfigurationOrderRatherThanTheStoresOrder(t *testing.T) {
	store := &dashStore{
		metrics: []domain.Metric{
			{Site: "first-in-the-data.example", WindowDays: 7, Visitors: 1, Pageviews: 2},
			{Site: "second-in-the-data.example", WindowDays: 7, Visitors: 3, Pageviews: 4},
		},
		states: healthyStates(testNow),
	}
	s := newTestServerWith(t, func(o *Options) {
		credentialAllSources(o)
		o.Config.Plausible.Sites = []string{
			"second-in-the-data.example",
			"first-in-the-data.example",
			"configured-but-never-fetched.example",
			// A site named twice in the YAML is one site, not two rows.
			"second-in-the-data.example",
		}
		o.Store = store
	})
	body := getAuthed(t, s.Handler(), "/tile/sites").Body.String()

	second := strings.Index(body, "second-in-the-data.example")
	first := strings.Index(body, "first-in-the-data.example")
	if second < 0 || first < 0 {
		t.Fatalf("a site is missing from the tile: second=%d first=%d", second, first)
	}
	if second > first {
		t.Error("the sites tile follows the store's order, not the configured one (FR-3.1 AC3)")
	}

	// A site named in the configuration that has no metrics at all must read as missing rather
	// than quietly vanish: "I configured this site and cannot see it" needs an answer on the tile.
	absent := strings.Index(body, "configured-but-never-fetched.example")
	if absent < 0 {
		t.Fatal("a configured site with no metrics is not shown at all (FR-3.1 AC3)")
	}
	if absent < first {
		t.Error("the configured order was not preserved for the site with no metrics")
	}
	if !strings.Contains(body[absent:], "not available") {
		t.Error("a configured site with no metrics does not read as missing")
	}
	if n := strings.Count(body, "second-in-the-data.example"); n != 1 {
		t.Errorf("a site configured twice appears %d times, want 1", n)
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

// inlineEventHandler matches any HTML attribute of the on* family — onclick, onmouseover, onfocus,
// onsubmit and the rest of them — rather than the three the first version of this test happened to
// list. The CSP forbids the whole family, so the test has to cover the whole family.
var inlineEventHandler = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)

// QS-4.4: the Content-Security-Policy carries no 'unsafe-inline', so nothing this application
// renders may contain an inline style, an inline script or an inline event handler. Every route
// that produces HTML is swept, not only the dashboard: a fragment is markup the browser applies to
// the very same document.
func TestNoRenderedHTMLNeedsUnsafeInline(t *testing.T) {
	store := &dashStore{
		items: []domain.Item{
			ghItem(1, "An issue", testNow.Add(-time.Hour)),
			{Source: "todoist", ExternalID: "todoist:1", Kind: domain.KindTask, Title: "A task",
				URL: "https://todoist.com/showTask?id=1", DueAt: testNow.Add(-time.Hour),
				Priority: 4, FirstSeenAt: testNow.Add(-time.Hour)},
		},
		builds:  []domain.Build{{Repo: "org/repo", Workflow: "CI", Status: "completed", Conclusion: "success"}},
		metrics: []domain.Metric{{Site: "one.example", WindowDays: 7, Visitors: 1}},
		states:  healthyStates(testNow),
	}
	h := dashHandler(t, store)
	c := signIn(t, h)

	// Every HTML-producing route: the page, the sign-in form, the documentation, all four tile
	// fragments, and the body of a 401 on a fragment route.
	pages := map[string]string{
		"GET /":            getAs(h, "/", c).Body.String(),
		"GET /login":       get(h, "/login").Body.String(),
		"GET /docs":        get(h, "/docs").Body.String(),
		"GET /tile/github": getAs(h, "/tile/github", c).Body.String(),
		"GET /tile/builds": getAs(h, "/tile/builds", c).Body.String(),
		"GET /tile/sites":  getAs(h, "/tile/sites", c).Body.String(),
		"GET /tile/tasks":  getAs(h, "/tile/tasks", c).Body.String(),
		"POST /seen (401)": post(h, "/seen", nil).Body.String(),
	}
	// The sign-in form's error state renders visitor-facing text, so it is swept too.
	pages["POST /login (rejected)"] = post(h, "/login", url.Values{"token": {"wrong"}}).Body.String()

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
			// Any script at all has to be the vendored htmx file: a full page carries exactly
			// that one, and a fragment carries none.
			vendored := strings.Count(body, `<script src="/static/htmx.min.js"`)
			if n := strings.Count(body, "<script"); n != vendored {
				t.Errorf("has %d script tags of which %d are the vendored htmx file; the rest "+
					"would need 'unsafe-inline' (QS-4.4)", n, vendored)
			}
			if name == "GET /" && vendored != 1 {
				t.Errorf("the dashboard links the vendored htmx file %d times, want 1 (QS-4.4)", vendored)
			}
			if strings.HasPrefix(name, "GET /tile/") && vendored != 0 {
				t.Error("a tile fragment carries a script tag (QS-4.4)")
			}
		})
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

// FR-1.2 AC1 on the tasks tile. The badge is the product's core feature — NEW means the item was
// first seen after the last visit — and it is rendered by a second template, which the GitHub
// test cannot cover. Deleting the badge from tasks.html must not leave a green suite.
func TestTasksTileBadgesATaskFirstSeenSinceTheLastVisit(t *testing.T) {
	store := &dashStore{
		lastVisit: testNow.Add(-2 * time.Hour),
		items: []domain.Item{
			{
				Source: "todoist", ExternalID: "todoist:1", Kind: domain.KindTask,
				Title: "Stood there before your last visit", URL: "https://todoist.com/showTask?id=1",
				DueAt: testNow.Add(3 * time.Hour), Priority: 1,
				FirstSeenAt: testNow.Add(-72 * time.Hour),
			},
			{
				Source: "todoist", ExternalID: "todoist:2", Kind: domain.KindTask,
				Title: "Arrived since your last visit", URL: "https://todoist.com/showTask?id=2",
				DueAt: testNow.Add(6 * time.Hour), Priority: 1,
				FirstSeenAt: testNow.Add(-time.Hour),
			},
		},
		states: healthyStates(testNow),
	}
	body := getAuthed(t, dashHandler(t, store), "/tile/tasks").Body.String()

	if n := strings.Count(body, "NEW"); n != 1 {
		t.Fatalf("the tasks tile carries %d NEW badges, want exactly 1 (FR-1.2 AC1)", n)
	}
	// Which task carries it matters as much as that one does: a badge on the wrong row tells the
	// visitor the wrong thing. Each rendered item is inspected on its own.
	for _, item := range strings.Split(body, `<li class="item`)[1:] {
		hasBadge := strings.Contains(item, `class="badge badge-new">NEW<`)
		switch {
		case strings.Contains(item, "Arrived since your last visit") && !hasBadge:
			t.Error("the task first seen since the last visit carries no NEW badge (FR-1.2 AC1)")
		case strings.Contains(item, "Stood there before your last visit") && hasBadge:
			t.Error("a task seen before the last visit carries a NEW badge (FR-1.2 AC1)")
		}
	}
	// And the tile's own count agrees with the badges, so the header can never say "1 new" above
	// a list in which nothing is marked.
	if !strings.Contains(body, "1 new") {
		t.Error("the tasks tile does not state how many of its items are new (FR-1.2 AC2)")
	}
}

// FR-1.4 AC1 read together with FR-8.2 AC2: a source disabled for lack of a credential still shows
// whatever was stored while it had one, so the tile must not claim it was never fetched.
func TestADisabledSourceHoldingStoredItemsIsHonestAboutTheirAge(t *testing.T) {
	// The default test options carry no GitHub credential, so the source is disabled.
	store := &dashStore{
		items:  []domain.Item{ghItem(1, "Stored while the token still worked", testNow.Add(-time.Hour))},
		states: healthyStates(testNow),
	}
	h := newTestServerWith(t, func(o *Options) { o.Store = store }).Handler()
	body := getAuthed(t, h, "/tile/github").Body.String()

	if !strings.Contains(body, "Stored while the token still worked") {
		t.Fatal("the fixture is wrong: the disabled tile shows no stored item")
	}
	if strings.Contains(body, "never fetched") {
		t.Error("a disabled tile above items stamped '1 hour ago' claims the source was never " +
			"fetched, which is false (FR-1.4 AC1)")
	}
	if !strings.Contains(body, "not fetching") {
		t.Error("a disabled tile does not say that nothing is being fetched (FR-8.2 AC2)")
	}
	if !strings.Contains(body, "no credential is configured") {
		t.Error("a disabled tile no longer says why it is disabled (FR-8.2 AC2)")
	}
	if !strings.Contains(body, "stored before it went away") {
		t.Error("a disabled tile showing stored data does not say that is what it is")
	}
}

// FR-2.2 AC1: a stored row whose created_at is zero must read as unknown, not render an empty
// <time datetime="">, which is invalid HTML and shows the word "opened" followed by nothing.
func TestAnItemWithoutACreationTimeSaysSo(t *testing.T) {
	item := ghItem(1, "An issue whose creation time did not survive", testNow.Add(-time.Hour))
	item.CreatedAt = time.Time{}
	store := &dashStore{items: []domain.Item{item}, states: healthyStates(testNow)}
	body := getAuthed(t, dashHandler(t, store), "/tile/github").Body.String()

	if strings.Contains(body, `datetime=""`) {
		t.Error("a zero timestamp rendered as an empty <time datetime=\"\">")
	}
	if !strings.Contains(body, "opened at an unknown time") {
		t.Error("an item with no creation time does not say so (FR-2.2 AC1)")
	}
	// The stamp that is known still renders.
	if !strings.Contains(body, "updated <time") {
		t.Error("the update time went missing along with the creation time")
	}
}

// FR-1.6 AC1: the poll interval follows the configured refresh interval, but never below a floor.
// A misconfigured `refresh.interval: 5s` would otherwise turn every open tab into four requests
// every five seconds against a Machine that is billed for being awake.
func TestThePollIntervalNeverDropsBelowItsFloor(t *testing.T) {
	for _, tc := range []struct {
		interval time.Duration
		want     int
	}{
		{0, minPollSeconds},
		{time.Second, minPollSeconds},
		{29 * time.Second, minPollSeconds},
		{minPollSeconds * time.Second, minPollSeconds},
		{31 * time.Second, 31},
		{15 * time.Minute, 900},
	} {
		s := newTestServerWith(t, func(o *Options) { o.Config.Refresh.Interval = tc.interval })
		if got := s.pollSeconds(); got != tc.want {
			t.Errorf("pollSeconds() with interval %v = %d, want %d", tc.interval, got, tc.want)
		}
	}

	// And the floor reaches the page, rather than only the helper.
	s := newTestServerWith(t, func(o *Options) {
		credentialAllSources(o)
		o.Config.Refresh.Interval = 5 * time.Second
		o.Store = &dashStore{states: healthyStates(testNow)}
	})
	body := getAuthed(t, s.Handler(), "/").Body.String()
	if !strings.Contains(body, "every 30s") {
		t.Error("a five-second refresh interval was not floored on the page (FR-1.6 AC1)")
	}
	if strings.Contains(body, "every 5s") {
		t.Error("the page polls at the misconfigured five-second interval (FR-1.6 AC1)")
	}
}

// QS-2.3, the static half of the budget, measured where the requirement's goal clause points: on
// the wire. Uncompressed the vendored htmx alone is 50 kB, so the budget is unreachable by
// construction unless it means transferred bytes — which is what "small enough for a slow
// connection" is about. Every asset the rendered page links is fetched exactly as a browser would
// fetch it, with Accept-Encoding: gzip, and what crosses the wire is added up.
func TestStaticAssetsFitTheirBudgetOnTheWire(t *testing.T) {
	const budget = 50 * 1024

	h := dashHandler(t, representativeStore())
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
		if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
			t.Errorf("GET %s: Content-Encoding = %q, want gzip (QS-2.3)", asset, got)
		}
		if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
			t.Errorf("GET %s: Vary = %q, want it to name Accept-Encoding", asset, got)
		}

		transferred := rec.Body.Len()
		total += transferred
		t.Logf("%s: %d bytes on the wire", asset, transferred)

		// What arrives has to be the file itself, not merely something small.
		stored, err := fs.ReadFile(embedded, "static"+strings.TrimPrefix(asset, "/static"))
		if err != nil {
			t.Fatalf("reading the embedded %s: %v", asset, err)
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
	// lastSuccessfulRun is the store's second answer about runs, and the fixture keeps it
	// separate from lastRun on purpose: a test that sets only lastRun describes a database whose
	// latest run is its only one, which is what most of them mean.
	lastSuccessfulRun domain.RefreshRun
	lastVisit         time.Time
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

func (s *dashStore) LastSuccessfulRun(context.Context) (domain.RefreshRun, error) {
	return s.lastSuccessfulRun, s.err
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

// linkedStaticAssets returns every /static/ URL the rendered page references, deduplicated and in
// a stable order. It is derived from the page rather than hard-coded so that an asset added to the
// layout is measured against the budget without anyone having to remember to add it here.
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

// FR-1.1 AC3, the in-flight case. The store returns the most recently *started* run, so during a
// refresh the run the page reads has no finishing time — the same zero a database with no runs at
// all returns. Read as a timestamp alone it made the header announce "No refresh has run yet" on a
// database holding a month of them, for the whole length of every slow run.
func TestTheHeaderSaysARefreshIsRunningRatherThanThatNoneHasEverRun(t *testing.T) {
	store := &dashStore{
		lastRun: domain.RefreshRun{ID: 9, StartedAt: testNow.Add(-90 * time.Second)},
		states:  healthyStates(testNow),
	}
	body := getAuthed(t, dashHandler(t, store), "/").Body.String()

	if strings.Contains(body, "No refresh has run yet") {
		t.Errorf("the header calls an open run no run at all (FR-1.1 AC3):\n%s",
			firstLineContaining(body, "topbar-status"))
	}
	if !strings.Contains(body, "Refreshing now") {
		t.Errorf("the header does not say a refresh is under way:\n%s",
			firstLineContaining(body, "topbar-status"))
	}
}

// FR-1.1 AC3 names the last *successful* refresh run, and OK is stored on every run for exactly
// this. A run in which every upstream was down must not be offered as a refresh that worked; with
// only the last run to read from, the header says the last one failed and when, which is the
// truthful half of what the requirement asks for.
func TestAFailedRunIsNeverPresentedAsASuccessfulRefresh(t *testing.T) {
	store := &dashStore{
		lastRun: domain.RefreshRun{
			ID:         9,
			StartedAt:  testNow.Add(-21 * time.Minute),
			FinishedAt: testNow.Add(-20 * time.Minute),
			OK:         false,
		},
		states: healthyStates(testNow),
	}
	body := getAuthed(t, dashHandler(t, store), "/").Body.String()

	if strings.Contains(body, "Last successful refresh") {
		t.Errorf("a failed run is shown as the last successful refresh (FR-1.1 AC3):\n%s",
			firstLineContaining(body, "topbar-status"))
	}
	if !strings.Contains(body, "Last refresh failed") {
		t.Errorf("the header does not say the last run failed:\n%s",
			firstLineContaining(body, "topbar-status"))
	}
	if !strings.Contains(body, "20 minutes ago") {
		t.Error("the header drops when the failure happened; the visitor cannot tell how stale the page is")
	}
}

// FR-6.1 AC3 + QS-4.3. A Slack failure never fails a refresh, so nothing else on the page changes
// when the webhook is revoked: the run record's detail is the only signal, and it is worth nothing
// until it is rendered. It carries upstream error text, so it goes through Redact like every other
// borrowed string here.
func TestTheRunDetailIsRenderedAndScrubbed(t *testing.T) {
	const secret = "https://hooks.slack.example/services/T0/B0/canary-value"
	store := &dashStore{
		lastRun: domain.RefreshRun{
			ID:         9,
			StartedAt:  testNow.Add(-2 * time.Minute),
			FinishedAt: testNow.Add(-time.Minute),
			OK:         true,
			Detail:     "github: 12; todoist: 4; notify: post to " + secret + ": 404 no_service",
		},
		states: healthyStates(testNow),
	}
	s := newTestServerWith(t, func(o *Options) {
		credentialAllSources(o)
		o.Config.Secrets.SlackWebhook = secret
		o.Store = store
	})
	body := getAuthed(t, s.Handler(), "/").Body.String()

	if !strings.Contains(body, "404 no_service") {
		t.Error("the run detail is not rendered anywhere, so a revoked webhook has no visible " +
			"signal at all (FR-6.1 AC3)")
	}
	if !strings.Contains(body, "github: 12") {
		t.Error("the run detail is rendered without its per-source outcome")
	}
	if strings.Contains(body, secret) {
		t.Error("the run detail put the webhook URL on the page (QS-4.3)")
	}
	if !strings.Contains(body, "[redacted]") {
		t.Error("the detail was dropped rather than scrubbed; the operator loses the diagnosis")
	}
}

// FR-1.2 AC3 under polling. A poll swaps one tile, and the total count lives in two places outside
// every tile — the tab title and the summary line. Without them coming back with the fragment the
// page starts contradicting itself at the first poll: the tile says three new, the tab says none.
func TestAPolledTileBringsTheTotalCountBackWithIt(t *testing.T) {
	store := &dashStore{
		lastVisit: testNow.Add(-2 * time.Hour),
		items: []domain.Item{
			ghItem(1, "One", testNow.Add(-time.Hour)),
			ghItem(2, "Two", testNow.Add(-time.Hour)),
		},
		states: healthyStates(testNow),
	}
	h := dashHandler(t, store)
	fragment := getAuthed(t, h, "/tile/github").Body.String()

	for _, want := range []string{
		// htmx lifts a top-level <title> out of the response and writes it into the document's
		// own, so the title needs no out-of-band marker — see templates/tiles/counts.html.
		"<title>(2) zorgscope</title>",
		`id="dash-summary" hx-swap-oob="true"`,
		"2 new since your last visit",
	} {
		if !strings.Contains(fragment, want) {
			t.Errorf("the polled fragment does not carry %q, so the count goes stale after the "+
				"first swap (FR-1.2 AC3):\n%s", want, fragment)
		}
	}
	// QS-4.4: the mechanism is markup, not script. The CSP carries no 'unsafe-inline', and the
	// page has to stay correct for a visitor with JavaScript switched off (FR-1.3 AC3).
	if strings.Contains(fragment, "<script") || regexp.MustCompile(`\son[a-z]+\s*=`).MatchString(fragment) {
		t.Errorf("the fragment updates the count with script rather than markup (QS-4.4):\n%s", fragment)
	}

	// The page itself draws the very same two elements, plainly: an out-of-band marker on a full
	// page load would ask htmx to swap elements into a document it has just replaced.
	page := getAuthed(t, h, "/").Body.String()
	if strings.Contains(page, "hx-swap-oob") {
		t.Errorf("the page marks its own elements as out-of-band swaps:\n%s",
			firstLineContaining(page, "hx-swap-oob"))
	}
	if !strings.Contains(page, `id="dash-summary"`) {
		t.Error("the page does not carry the id a swap addresses, so a poll can never find it")
	}
}

// GET / used to be registered as the mux's catch-all, so every path no other route claimed —
// /admin, a typo, a fragment path with a segment too many — answered 200 with the whole dashboard.
func TestAnUnknownPathIsNotTheDashboard(t *testing.T) {
	h := dashHandler(t, &dashStore{states: healthyStates(testNow)})
	c := signIn(t, h)

	for _, path := range []string{"/admin", "/no-such-page", "/tile/github/extra"} {
		rec := getAs(h, path, c)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404: an unknown path must not render the dashboard",
				path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), `class="tiles"`) {
			t.Errorf("GET %s answered with the dashboard", path)
		}
	}
	if rec := getAs(h, "/", c); rec.Code != http.StatusOK {
		t.Errorf("GET / = %d, want 200: the dashboard itself still has to answer", rec.Code)
	}
}

// FR-8.2 AC2 is about the credential. Config.Enabled is false both when the secret is missing and
// when there is nothing configured to watch, and the tile said "no credential is configured" for
// both — sending an operator who had commented out the repos: list after a Fly secrets problem
// that did not exist.
func TestAConfiguredSourceWithNothingToWatchDoesNotBlameTheCredential(t *testing.T) {
	s := newTestServerWith(t, func(o *Options) {
		credentialAllSources(o)
		o.Config.GitHub.Repos = nil // the token is set and correct; the watch list is empty
		o.Store = &dashStore{states: healthyStates(testNow)}
	})
	body := getAuthed(t, s.Handler(), "/").Body.String()

	if strings.Contains(body, "no credential is configured") {
		t.Error("an empty repos list is reported as a missing credential (FR-8.2 AC2)")
	}
	if n := strings.Count(body, "no repositories are configured to watch"); n != 2 {
		t.Errorf("%d of the 2 GitHub tiles name the empty watch list, want 2", n)
	}
}
