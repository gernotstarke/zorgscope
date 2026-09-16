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
	if strings.Count(body, `class="tile `) != 4 {
		t.Errorf("tiles = %d, want 4", strings.Count(body, `class="tile `))
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

// FR-1.8 AC1 with FR-1.2: the Sites view counts NEW exactly as the list does, in the header, the tab
// title and the tile that holds the new item.
func TestSitesPageCountsNewLikeTheList(t *testing.T) {
	seen := testNow.Add(-2 * time.Hour)
	src := &fakeSource{items: []domain.Item{
		siteItem("arc42/quality", domain.KindIssue, 1, testNow.Add(-time.Hour)),
		siteItem("arc42/org", domain.KindPR, 2, testNow.Add(-3*time.Hour)),
	}}
	h := sitesHandler(t, src)
	c := mintSessionSeenAt(seen)

	body := getAs(h, "/sites", c).Body.String()
	if !strings.Contains(body, "<title>(1) Sites · zorgscope</title>") {
		t.Errorf("tab title = %s", firstLineContaining(body, "<title>"))
	}
	if !strings.Contains(body, "NEW 1") {
		t.Error("the Sites header does not carry the NEW total")
	}
	if !strings.Contains(tileSection(t, body, 3), "badge-new") {
		t.Error("the new item's tile does not mark it NEW")
	}
	if strings.Contains(tileSection(t, body, 1), "badge-new") {
		t.Error("an item created before the seen mark is marked NEW")
	}
	if list := getAs(h, "/", c).Body.String(); !strings.Contains(list, "NEW 1") {
		t.Error("the list and the Sites view disagree about what is new")
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

// FR-1.8 AC4: both views carry the switch, marking the view being shown.
func TestViewSwitchMarksTheCurrentView(t *testing.T) {
	h := sitesHandler(t, &fakeSource{})
	c := signIn(t, h)
	for _, tc := range []struct{ path, list, sites string }{
		{"/", `<a href="/" aria-current="page">List</a>`, `<a href="/sites">Sites</a>`},
		{"/sites", `<a href="/">List</a>`, `<a href="/sites" aria-current="page">Sites</a>`},
	} {
		body := getAs(h, tc.path, c).Body.String()
		if !strings.Contains(body, tc.list) || !strings.Contains(body, tc.sites) {
			t.Errorf("GET %s: the switch does not mark the current view:\n%s",
				tc.path, firstLineContaining(body, "view-switch"))
		}
	}
}

// FR-1.8 AC1: without any configured site, the Sites view shows the one Other tile.
func TestSitesWithoutConfiguredSitesShowsOnlyOther(t *testing.T) {
	body := getAuthed(t, newTestServer(t).Handler(), "/sites").Body.String()
	if n := strings.Count(body, `class="tile `); n != 1 {
		t.Errorf("tiles = %d, want the one Other tile", n)
	}
	if !strings.Contains(body, `class="tile hue-slate" aria-labelledby="tile-1-title"`) {
		t.Error("the only tile is not the slate Other tile")
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
	if n := strings.Count(body, `class="tile `); n != 8 {
		t.Fatalf("tiles = %d, want 7 sites and Other: the budget would be measured on the wrong page", n)
	}
	if n := len(body); n > 150*1024 {
		t.Errorf("Sites view is %d bytes, budget is 150 kB (QS-2.3)", n)
	} else {
		t.Logf("Sites view is %d bytes of the 150 kB budget (QS-2.3)", n)
	}
}
