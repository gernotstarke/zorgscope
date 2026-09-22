package web

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/snapshot"
)

// sitesHandler builds a server watching three sites and two repositories no site claims, backed
// by src.
func sitesHandler(t *testing.T, src ports.Source) http.Handler {
	t.Helper()
	return newTestServerWith(t, func(o *Options) {
		o.Config.GitHub.Repos = []string{"arc42/org", "arc42/de", "arc42/quality", "arc42/template", "gernotstarke/zorgscope"}
		o.Config.GitHub.Sites = []config.Site{
			{Name: "arc42.org", URL: "https://arc42.org", Repo: "arc42/org", Hue: "navy"},
			{Name: "arc42.de", URL: "https://arc42.de", Repo: "arc42/de", Hue: "navy", Tag: "DE"},
			{Name: "quality.arc42.org", URL: "https://quality.arc42.org", Repo: "arc42/quality", Hue: "plum"},
		}
		o.Cache = snapshot.New(src, time.Hour, o.Clock)
	}).Handler()
}

func siteItem(repo string, kind domain.Kind, number int, created time.Time) domain.Item {
	n := strconv.Itoa(number)
	return domain.Item{
		Kind: kind, Repo: repo, Number: number, Title: "Item " + n,
		URL:       "https://github.com/" + repo + "/issues/" + n,
		CreatedAt: created, UpdatedAt: created,
	}
}

// tileSection returns tile n's markup, from its opening section tag to its closing one.
func tileSection(t *testing.T, body string, n int) string {
	t.Helper()
	start := strings.Index(body, `aria-labelledby="tile-`+strconv.Itoa(n)+`-title"`)
	if start < 0 {
		t.Fatalf("no tile %d on the page", n)
	}
	end := strings.Index(body[start:], "</section>")
	if end < 0 {
		t.Fatalf("tile %d is never closed", n)
	}
	return body[start : start+end]
}

// FR-1.8 AC1: one tile per configured site in configuration order, then Other; a site with nothing
// open keeps its tile; the tag is written out; Other's heading is not a link.
func TestSitesListsATilePerSiteInConfigurationOrderThenOther(t *testing.T) {
	src := &fakeSource{items: []domain.Item{siteItem("arc42/template", domain.KindPR, 7, testNow)}}
	body := getAuthed(t, sitesHandler(t, src), "/sites").Body.String()

	last := -1
	for _, want := range []string{
		`class="tile hue-navy" aria-labelledby="tile-1-title"`,
		`class="tile hue-navy" aria-labelledby="tile-2-title"`,
		`class="tile hue-plum" aria-labelledby="tile-3-title"`,
		`class="tile hue-slate" aria-labelledby="tile-4-title"`,
	} {
		i := strings.Index(body, want)
		if i < 0 {
			t.Fatalf("no %s on the page", want)
		}
		if i < last {
			t.Errorf("%s is out of configuration order", want)
		}
		last = i
	}
	if strings.Count(body, `class="tile hue-`) != 4 {
		t.Errorf("tiles = %d, want 4", strings.Count(body, `class="tile hue-`))
	}
	if !strings.Contains(tileSection(t, body, 2), `<span class="tile-tag">DE</span>`) {
		t.Error("arc42.de carries no DE tag: colour would be the only thing telling it from arc42.org")
	}
	if !strings.Contains(tileSection(t, body, 3), `<a href="https://quality.arc42.org"`) {
		t.Error("a site's heading does not link its website")
	}
	if !regexp.MustCompile(`id="tile-4-title">\s*Other\s*<`).MatchString(body) {
		t.Error("the Other tile's heading is not the plain word Other")
	}
	if !strings.Contains(tileSection(t, body, 1), "no open pull requests") {
		t.Error("a site with nothing open lost its tile or its empty-state line")
	}
}

// FR-1.8 AC2 and AC3: a tile lists at most three pull requests and four issues, and only a tile that
// cut something links to the list filtered to its repository — on Other, one link per repository.
func TestSitesTileCutsAndLinksToTheFilteredList(t *testing.T) {
	var items []domain.Item
	for i := range 5 {
		items = append(items, siteItem("arc42/quality", domain.KindPR, 100+i, testNow.Add(-48*time.Hour)))
	}
	for i := range 6 {
		items = append(items, siteItem("arc42/quality", domain.KindIssue, 200+i, testNow.Add(-48*time.Hour)))
	}
	for i := range 4 {
		items = append(items, siteItem("arc42/template", domain.KindPR, 300+i, testNow.Add(-48*time.Hour)))
	}
	items = append(items, siteItem("gernotstarke/zorgscope", domain.KindIssue, 400, testNow.Add(-48*time.Hour)))
	body := getAuthed(t, sitesHandler(t, &fakeSource{items: items}), "/sites").Body.String()

	quality := tileSection(t, body, 3)
	if n := strings.Count(quality, `<li class="tile-item`); n != 7 {
		t.Errorf("quality tile lists %d items, want 3 pull requests and 4 issues", n)
	}
	if !strings.Contains(quality, "5 PRs · 6 issues") {
		t.Error("quality tile does not state its totals before the cut")
	}
	if !strings.Contains(quality, `href="/?repo=arc42%2Fquality"`) || !strings.Contains(quality, "all 5 PRs · 6 issues →") {
		t.Errorf("quality tile does not link to the filtered list:\n%s", quality)
	}
	if strings.Contains(tileSection(t, body, 1), "tile-more") {
		t.Error("a tile that cut nothing carries an all → link")
	}
	other := tileSection(t, body, 4)
	for _, want := range []string{
		`href="/?repo=arc42%2Ftemplate"`, "arc42/template: all 4 PRs · 0 issues →",
		`href="/?repo=gernotstarke%2Fzorgscope"`, "gernotstarke/zorgscope: all 0 PRs · 1 issue →",
	} {
		if !strings.Contains(other, want) {
			t.Errorf("the Other tile lacks %q:\n%s", want, other)
		}
	}
}

// FR-1.8 AC3: an Other tile holding exactly one unclaimed repository still names it in its link.
// len(tile.Counts) alone cannot tell Other apart from a site's own single-repository tile, so
// newTileView keys the repository name off the tile being Other, not off how many repositories it
// counts — otherwise this tile's "all →" link would read as a bare count with nothing on the page
// saying which repository it leads to, under a heading that is just the word "Other".
func TestOtherTileWithOneRepoStillNamesItInItsLink(t *testing.T) {
	var items []domain.Item
	for i := range 4 {
		items = append(items, siteItem("arc42/extra", domain.KindPR, 500+i, testNow.Add(-48*time.Hour)))
	}
	h := newTestServerWith(t, func(o *Options) {
		o.Config.GitHub.Repos = []string{"arc42/org", "arc42/extra"}
		o.Config.GitHub.Sites = []config.Site{
			{Name: "arc42.org", URL: "https://arc42.org", Repo: "arc42/org", Hue: "navy"},
		}
		o.Cache = snapshot.New(&fakeSource{items: items}, time.Hour, o.Clock)
	}).Handler()
	body := getAuthed(t, h, "/sites").Body.String()

	other := tileSection(t, body, 2)
	if !strings.Contains(other, "arc42/extra: all ") {
		t.Errorf("the Other tile with a single repository does not name it in its link:\n%s", other)
	}
}

// FR-1.8 AC2 with the domain's ordering (2026-09-18): a tile's rows are the most recently updated
// first, exactly as the list's groups are.
func TestSitesPageOrdersTileRowsByLastUpdate(t *testing.T) {
	src := &fakeSource{items: []domain.Item{
		siteItem("arc42/quality", domain.KindIssue, 1, testNow.Add(-3*time.Hour)),
		siteItem("arc42/quality", domain.KindIssue, 2, testNow.Add(-time.Hour)),
	}}
	body := getAuthed(t, sitesHandler(t, src), "/sites").Body.String()

	later, earlier := strings.Index(body, "Item 2"), strings.Index(body, "Item 1")
	if later < 0 || earlier < 0 {
		t.Fatalf("a tile row is missing:\n%s", body)
	}
	if later > earlier {
		t.Error("the tile lists the item updated longer ago first; rows go most recently updated first")
	}
}

// FR-1.8 AC5 and QS-4.4: colour arrives only as a class; nothing on the page carries a style
// attribute, which the Content-Security-Policy would refuse anyway.
func TestNoTileCarriesAStyleAttribute(t *testing.T) {
	src := &fakeSource{items: []domain.Item{siteItem("arc42/quality", domain.KindIssue, 1, testNow)}}
	body := getAuthed(t, sitesHandler(t, src), "/sites").Body.String()
	if strings.Contains(body, "style=") {
		t.Errorf("the page carries a style attribute: %s", firstLineContaining(body, "style="))
	}
}

// FR-1.8 AC4, FR-1.11: every view carries the switch in the top bar, marking the view being
// shown. Contributors is the third link; the page behind it arrives with FR-12.1.
func TestViewSwitchMarksTheCurrentView(t *testing.T) {
	h := sitesHandler(t, &fakeSource{})
	c := signIn(t, h)
	// List links to "/?view=list" rather than bare "/": with a landing preference other than
	// List, a bare "/" redirects on to the landing view (FR-1.12 AC3), and the query is what lets
	// the dashboard's redirect (dashboard.go) leave this link alone. The parameter is inert —
	// parseFilter does not read "view" — so it changes nothing about what the list shows.
	for _, tc := range []struct{ path, list, sites, contributors string }{
		{"/", `<a href="/?view=list" aria-current="page">List</a>`, `<a href="/sites">Sites</a>`, `<a href="/contributors">Contributors</a>`},
		{"/sites", `<a href="/?view=list">List</a>`, `<a href="/sites" aria-current="page">Sites</a>`, `<a href="/contributors">Contributors</a>`},
	} {
		body := getAs(h, tc.path, c).Body.String()
		// The switch is part of the top bar now, not of the page body.
		header := body[strings.Index(body, "<header"):strings.Index(body, "</header>")]
		for _, want := range []string{tc.list, tc.sites, tc.contributors} {
			if !strings.Contains(header, want) {
				t.Errorf("GET %s: the top bar's switch lacks %s:\n%s",
					tc.path, want, firstLineContaining(header, "view-switch"))
			}
		}
	}
}

// FR-1.8 AC1: without any configured site, the Sites view shows the one Other tile.
func TestSitesWithoutConfiguredSitesShowsOnlyOther(t *testing.T) {
	body := getAuthed(t, newTestServer(t).Handler(), "/sites").Body.String()
	if n := strings.Count(body, `class="tile hue-`); n != 1 {
		t.Errorf("tiles = %d, want the one Other tile", n)
	}
	if !strings.Contains(body, `class="tile hue-slate" aria-labelledby="tile-1-title"`) {
		t.Error("the only tile is not the slate Other tile")
	}
}

// FR-1.8 AC1: when every watched repository is claimed by a configured site, the Sites view
// carries no Other tile at all.
func TestSitesOmitsOtherWhenEveryRepositoryIsClaimed(t *testing.T) {
	h := newTestServerWith(t, func(o *Options) {
		o.Config.GitHub.Repos = []string{"arc42/org", "arc42/quality"}
		o.Config.GitHub.Sites = []config.Site{
			{Name: "arc42.org", URL: "https://arc42.org", Repo: "arc42/org", Hue: "navy"},
			{Name: "quality.arc42.org", URL: "https://quality.arc42.org", Repo: "arc42/quality", Hue: "plum"},
		}
		o.Cache = snapshot.New(&fakeSource{}, time.Hour, o.Clock)
	}).Handler()
	body := getAuthed(t, h, "/sites").Body.String()

	if n := strings.Count(body, `class="tile hue-`); n != 2 {
		t.Errorf("tiles = %d, want 2 (len(Sites), no Other)", n)
	}
	if strings.Contains(body, "hue-slate") {
		t.Error("every repository is claimed, but the page still carries the slate Other class")
	}
	if regexp.MustCompile(`id="tile-\d+-title">\s*Other\s*<`).MatchString(body) {
		t.Error("every repository is claimed, but the page still carries a tile named Other")
	}
}

// Spec §2 ("Filters on the Sites view: None") and §5.4 (a tile row carries no summary and no
// author, which is what tells a tile row from a list row): two things the Sites view deliberately
// omits, with no coverage before this test.
func TestSitesViewCarriesNoFilterAndTileRowsCarryNoSummaryOrAuthor(t *testing.T) {
	src := &fakeSource{items: []domain.Item{siteItem("arc42/quality", domain.KindIssue, 1, testNow)}}
	h := sitesHandler(t, src)
	c := signIn(t, h)

	list := getAs(h, "/", c).Body.String()
	if !strings.Contains(list, `class="filter"`) || !strings.Contains(list, `action="/"`) {
		t.Fatal(`the list page itself carries no class="filter" form with action="/"; the assertions below about what the Sites view omits would prove nothing`)
	}

	body := getAs(h, "/sites", c).Body.String()
	if strings.Contains(body, `class="filter"`) {
		t.Error("the Sites view carries a filter form; spec §2 says it has none")
	}
	if strings.Contains(body, `action="/"`) {
		t.Error("the Sites view carries a GET form back to the list; spec §2 says it has no filters")
	}

	tile := tileSection(t, body, 3)
	if strings.Contains(tile, `class="item-summary"`) {
		t.Error("a tile row carries a summary; spec §5.4 says a tile row has none")
	}
	if strings.Contains(tile, `class="item-meta"`) {
		t.Error("a tile row carries item-meta, the class the list uses to print an item's author; spec §5.4 says a tile row has none")
	}
}

// The template draws hue-<key> from whatever the Config says. config.Load refuses an unknown key;
// this is the second lock, for a Config built in code.
func TestTileHueFallsBackToSlateForAnUnknownKey(t *testing.T) {
	if got := tileHue("rose"); got != "rose" {
		t.Errorf("tileHue(rose) = %q", got)
	}
	if got := tileHue("lime"); got != "slate" {
		t.Errorf("tileHue(lime) = %q, want slate", got)
	}
}

// QS-2.3: the Sites view over the representative configuration stays inside the page budget.
func TestSitesPageStaysInsideItsBudget(t *testing.T) {
	src := &fakeSource{items: representativeItems()}
	h := newTestServerWith(t, func(o *Options) {
		o.Config.GitHub.Repos = representativeRepos()
		for i, repo := range representativeRepos()[:7] {
			n := strconv.Itoa(i)
			o.Config.GitHub.Sites = append(o.Config.GitHub.Sites, config.Site{
				Name: "site-" + n + ".example", URL: "https://site-" + n + ".example",
				Repo: repo, Hue: config.HueKeys[i%len(config.HueKeys)],
			})
		}
		o.Cache = snapshot.New(src, time.Hour, o.Clock)
	}).Handler()

	body := getAuthed(t, h, "/sites").Body.String()
	if n := strings.Count(body, `class="tile hue-`); n != 8 {
		t.Fatalf("site tiles = %d, want 7 sites and Other: the budget would be measured on the wrong page", n)
	}
	if n := len(body); n > 150*1024 {
		t.Errorf("Sites view is %d bytes, budget is 150 kB (QS-2.3)", n)
	} else {
		t.Logf("Sites view is %d bytes of the 150 kB budget (QS-2.3)", n)
	}
}
