package web

import (
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/snapshot"
)

// radarGitHub is two sites and one watched repository no site claims, so the scope has three
// sectors: arc42.org, quality and Other.
var radarGitHub = config.GitHub{
	Owner: "gernotstarke",
	Repos: []string{"arc42/org", "arc42/quality", "arc42/stray"},
	Sites: []config.Site{
		{Name: "arc42.org", Repo: "arc42/org", Hue: "navy"},
		{Name: "quality", Repo: "arc42/quality", Hue: "plum"},
	},
}

// bearingOf is the blip's compass bearing in degrees, north 0, clockwise.
func bearingOf(b radarBlip) float64 {
	deg := math.Atan2(b.X-radarCX, radarCY-b.Y) * 180 / math.Pi
	if deg < 0 {
		deg += 360
	}
	return deg
}

func distanceOf(b radarBlip) float64 { return math.Hypot(b.X-radarCX, b.Y-radarCY) }

func blipFor(t *testing.T, v radarView, url string) radarBlip {
	t.Helper()
	for _, b := range v.Blips {
		if b.URL == url {
			return b
		}
	}
	t.Fatalf("no blip for %s", url)
	return radarBlip{}
}

// FR-1.15 AC2: an item stands in the sector of the site that claims its repository; one no site
// claims stands in Other.
func TestRadarPlacesItemsInTheirSitesSector(t *testing.T) {
	v := buildRadar([]domain.Item{
		{Kind: domain.KindIssue, Repo: "arc42/org", Number: 1, URL: "u1", UpdatedAt: testNow},
		{Kind: domain.KindIssue, Repo: "arc42/quality", Number: 2, URL: "u2", UpdatedAt: testNow},
		{Kind: domain.KindIssue, Repo: "arc42/stray", Number: 3, URL: "u3", UpdatedAt: testNow},
		{Kind: domain.KindIssue, Repo: "gone/repo", Number: 4, URL: "u4", UpdatedAt: testNow},
	}, radarGitHub, "", testNow)

	if len(v.Sectors) != 3 {
		t.Fatalf("sectors = %d, want 3 (two sites and Other)", len(v.Sectors))
	}
	for _, tc := range []struct {
		url    string
		sector int
	}{{"u1", 0}, {"u2", 1}, {"u3", 2}, {"u4", 2}} {
		b := bearingOf(blipFor(t, v, tc.url))
		lo, hi := float64(tc.sector)*120, float64(tc.sector+1)*120
		if b < lo || b > hi {
			t.Errorf("%s: bearing %.1f outside sector %d [%v, %v]", tc.url, b, tc.sector, lo, hi)
		}
	}
	if v.Sectors[2].Name != otherTileName {
		t.Errorf("third sector is %q, want %q", v.Sectors[2].Name, otherTileName)
	}
}

// A repository left over from an earlier configuration still gets a sector, even when every
// configured repository is claimed.
func TestRadarOtherSectorAppearsForUnconfiguredRepositories(t *testing.T) {
	gh := radarGitHub
	gh.Repos = gh.Repos[:2]
	v := buildRadar([]domain.Item{{Repo: "gone/repo", Number: 1, URL: "u", UpdatedAt: testNow}}, gh, "", testNow)
	if len(v.Sectors) != 3 || v.Sectors[2].Name != otherTileName {
		t.Fatalf("sectors = %+v, want the two sites and Other", v.Sectors)
	}
	if v := buildRadar(nil, gh, "", testNow); len(v.Sectors) != 2 {
		t.Errorf("with nothing unclaimed there are %d sectors, want 2", len(v.Sectors))
	}
}

// FR-1.15 AC2: the longer an item has been idle, the further out it stands; an unknown update time
// stands on the rim.
func TestRadarDistanceGrowsWithIdleTime(t *testing.T) {
	v := buildRadar([]domain.Item{
		{Repo: "arc42/org", Number: 1, URL: "fresh", UpdatedAt: testNow.Add(-time.Hour)},
		{Repo: "arc42/org", Number: 2, URL: "month", UpdatedAt: testNow.Add(-30 * 24 * time.Hour)},
		{Repo: "arc42/org", Number: 3, URL: "old", UpdatedAt: testNow.Add(-5 * 365 * 24 * time.Hour)},
		{Repo: "arc42/org", Number: 4, URL: "unknown"},
	}, radarGitHub, "", testNow)
	fresh, month, old := distanceOf(blipFor(t, v, "fresh")), distanceOf(blipFor(t, v, "month")), distanceOf(blipFor(t, v, "old"))
	if fresh >= month || month >= old {
		t.Errorf("distances fresh %.1f, month %.1f, old %.1f are not increasing", fresh, month, old)
	}
	if old > radarR {
		t.Errorf("a five-year-old item stands at %.1f, beyond the rim %d", old, radarR)
	}
	if u := distanceOf(blipFor(t, v, "unknown")); math.Abs(u-old) > 1.5 {
		t.Errorf("an unknown update time stands at %.1f, want the rim %.1f", u, old)
	}
}

// The same item stands in the same place on every render.
func TestRadarPositionIsStable(t *testing.T) {
	items := []domain.Item{{Repo: "arc42/org", Number: 7, URL: "u", UpdatedAt: testNow.Add(-48 * time.Hour)}}
	a, b := buildRadar(items, radarGitHub, "", testNow), buildRadar(items, radarGitHub, "", testNow)
	if a.Blips[0].X != b.Blips[0].X || a.Blips[0].Y != b.Blips[0].Y {
		t.Error("the same item moved between two renders")
	}
}

// FR-1.15 AC3: security and dependency items wear their tape and never fade; a pull request asking
// the owner for a review needs them; loud blips are drawn last, on top.
func TestRadarMarks(t *testing.T) {
	year := testNow.Add(-400 * 24 * time.Hour)
	v := buildRadar([]domain.Item{
		{Kind: domain.KindPR, Repo: "arc42/org", Number: 1, URL: "sec", Author: "dependabot", Advisories: []string{"CVE-2026-1"}, UpdatedAt: year},
		{Kind: domain.KindPR, Repo: "arc42/org", Number: 2, URL: "dep", Author: "dependabot", UpdatedAt: year},
		{Kind: domain.KindPR, Repo: "arc42/org", Number: 3, URL: "review", Author: "amy", ReviewRequested: []string{"gernotstarke"}, UpdatedAt: testNow},
		{Kind: domain.KindIssue, Repo: "arc42/org", Number: 4, URL: "plain", Author: "amy", UpdatedAt: year},
	}, radarGitHub, "", testNow)

	for url, want := range map[string]string{"sec": "security", "dep": "dependency", "review": "needs", "plain": ""} {
		if got := blipFor(t, v, url).Mark; got != want {
			t.Errorf("%s: mark %q, want %q", url, got, want)
		}
	}
	for _, url := range []string{"sec", "dep"} {
		if got := blipFor(t, v, url).Age; got != "age-0" {
			t.Errorf("%s: fades as %s; a marked item never fades", url, got)
		}
	}
	if got := blipFor(t, v, "plain").Age; got == "age-0" {
		t.Error("a year-old plain issue does not fade")
	}
	if v.Blips[0].URL != "plain" || v.Blips[len(v.Blips)-1].URL != "sec" {
		t.Errorf("draw order %s … %s, want the calm item first and the security item last",
			v.Blips[0].URL, v.Blips[len(v.Blips)-1].URL)
	}
	if v.Security != 1 || v.Dependency != 1 || v.Needs != 1 || v.PRs != 3 || v.Issues != 1 {
		t.Errorf("counts security %d dependency %d needs %d PRs %d issues %d",
			v.Security, v.Dependency, v.Needs, v.PRs, v.Issues)
	}
	if b := blipFor(t, v, "sec"); b.Tag != "⚠ #1" {
		t.Errorf("security tag %q", b.Tag)
	}
}

// Every blip carries a bearing class the stylesheet defines, and nothing else.
func TestRadarBearingClassesAreBounded(t *testing.T) {
	var items []domain.Item
	for n := range 200 {
		items = append(items, domain.Item{Repo: "arc42/quality", Number: n, URL: "u", UpdatedAt: testNow})
	}
	for _, b := range buildRadar(items, radarGitHub, "", testNow).Blips {
		var k int
		if _, err := fmt.Sscanf(b.Bearing, "bearing-%d", &k); err != nil || k < 0 || k >= radarBearings {
			t.Fatalf("bearing class %q outside bearing-0 … bearing-%d", b.Bearing, radarBearings-1)
		}
	}
}

// The data block wraps a long title and keeps at most three lines of it.
func TestRadarCardWrapsTheTitle(t *testing.T) {
	v := buildRadar([]domain.Item{{Kind: domain.KindIssue, Repo: "arc42/org", Number: 1, URL: "u", UpdatedAt: testNow,
		Title: "A title that is far too long to fit on one line of the data block, and then some more words besides"}},
		radarGitHub, "", testNow)
	lines := v.Blips[0].Card.Title
	if len(lines) < 2 || len(lines) > 3 {
		t.Fatalf("title wrapped into %d lines, want 2 or 3: %v", len(lines), lines)
	}
	for _, l := range lines {
		if len([]rune(l.Text)) > radarTitleWidth+1 {
			t.Errorf("line %q longer than %d", l.Text, radarTitleWidth)
		}
	}
}

// FR-1.15 AC4: every item is a blip, and every blip opens its item on GitHub in a new tab.
func TestRadarPageDrawsEveryItemAsALink(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: []domain.Item{
		{Kind: domain.KindPR, Repo: "o/a", Number: 1, Title: "Bump <x>", URL: "https://github.com/o/a/pull/1", Author: "dependabot", UpdatedAt: testNow},
		{Kind: domain.KindIssue, Repo: "o/a", Number: 2, Title: "Broken", URL: "https://github.com/o/a/issues/2", Author: "amy", UpdatedAt: testNow},
	}})
	body := getAuthed(t, h, "/radar").Body.String()
	if n := strings.Count(body, `<a class="blip `); n != 2 {
		t.Errorf("%d blips, want 2", n)
	}
	for _, want := range []string{
		"<title>Radar · zorgscope</title>",
		`href="https://github.com/o/a/pull/1" target="_blank" rel="noopener noreferrer"`,
		`href="https://github.com/o/a/issues/2" target="_blank" rel="noopener noreferrer"`,
		`<a href="/radar" aria-current="page">Radar</a>`,
		"Bump &lt;x&gt;", "mark-dependency",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("radar page lacks %s", want)
		}
	}
	if strings.Contains(body, "Bump <x>") {
		t.Error("a title reached the page unescaped")
	}
}

// QS-4.4: the policy grants no 'unsafe-inline', so a style attribute would not apply — every colour,
// delay and fade is a class.
func TestRadarPageHasNoStyleAttribute(t *testing.T) {
	body := getAuthed(t, dashHandler(t, &fakeSource{items: representativeItems()}), "/radar").Body.String()
	if strings.Contains(body, " style=") {
		t.Error("the radar page carries a style attribute")
	}
}

// QS-2.3: the Radar page over the representative fixture stays inside its budget.
func TestRadarPageStaysInsideItsBudget(t *testing.T) {
	body := getAuthed(t, dashHandler(t, &fakeSource{items: representativeItems()}), "/radar").Body.String()
	if n := len(body); n > 150*1024 {
		t.Errorf("Radar view is %d bytes, budget is 150 kB (QS-2.3)", n)
	} else {
		t.Logf("Radar view is %d bytes of the 150 kB budget (QS-2.3)", n)
	}
}

// An empty snapshot still draws the scope, and says nothing is open.
func TestRadarPageWithNothingOpen(t *testing.T) {
	body := getAuthed(t, dashHandler(t, &fakeSource{}), "/radar").Body.String()
	if !strings.Contains(body, `class="radar-scope"`) || !strings.Contains(body, "Nothing open") {
		t.Error("an empty radar should draw the scope and say nothing is open")
	}
}

// tenSites is the production shape of the scope: ten repositories, nine claimed by a site, so the
// sectors are as narrow as they get.
func tenSites() config.GitHub {
	gh := config.GitHub{Owner: "gernotstarke"}
	for i := range 10 {
		repo := "arc42/site-" + fmt.Sprint(i)
		gh.Repos = append(gh.Repos, repo)
		if i < 9 {
			gh.Sites = append(gh.Sites, config.Site{Name: "site " + fmt.Sprint(i), Repo: repo, Hue: "navy"})
		}
	}
	return gh
}

// footprint is how far from its centre a blip draws: its ring, or its mark.
func footprint(b radarBlip) float64 { return max(b.Ring, b.R+2) }

// tagBox is the rectangle a blip's tag covers: 14 px bold, about 9 px a character.
func tagBox(b radarBlip) (x0, y0, x1, y1 float64) {
	return b.TagX, b.TagY - 12, b.TagX + 9*float64(len([]rune(b.Tag))), b.TagY + 3
}

// assertNoCollisions fails for every two blips whose marks overlap, and every two tags that do.
func assertNoCollisions(t *testing.T, v radarView) {
	t.Helper()
	for i, a := range v.Blips {
		for _, b := range v.Blips[i+1:] {
			if d := math.Hypot(a.X-b.X, a.Y-b.Y); d < footprint(a)+footprint(b) {
				t.Errorf("%s and %s overlap: %.1f apart, footprints %.1f and %.1f", a.URL, b.URL, d, footprint(a), footprint(b))
			}
			if a.Tag == "" || b.Tag == "" {
				continue
			}
			ax0, ay0, ax1, ay1 := tagBox(a)
			bx0, by0, bx1, by1 := tagBox(b)
			if ax0 < bx1 && bx0 < ax1 && ay0 < by1 && by0 < ay1 {
				t.Errorf("the tags %q of %s and %q of %s overlap", a.Tag, a.URL, b.Tag, b.URL)
			}
		}
	}
}

// FR-1.15 AC4: two items touched the same afternoon stand side by side, not on top of each other —
// arc42-generator #67 and #68, both needing the owner, updated two hours and ten minutes ago, in a
// sector barely wider than one ringed blip at the centre.
func TestRadarBlipsDoNotOverlap(t *testing.T) {
	gh := tenSites()
	repo := gh.Repos[8]
	at := func(d time.Duration) time.Time { return testNow.Add(-d) }
	day := 24 * time.Hour
	items := []domain.Item{
		{Kind: domain.KindPR, Repo: repo, Number: 68, Author: "gernotstarke", URL: "#68", UpdatedAt: at(10 * time.Minute)},
		{Kind: domain.KindPR, Repo: repo, Number: 67, Author: "gernotstarke", URL: "#67", UpdatedAt: at(2 * time.Hour)},
		{Kind: domain.KindPR, Repo: repo, Number: 59, Author: "raifdmueller", State: "DRAFT", URL: "#59", UpdatedAt: at(294 * day)},
		{Kind: domain.KindPR, Repo: repo, Number: 57, Author: "raifdmueller", URL: "#57", UpdatedAt: at(217 * day)},
		{Kind: domain.KindPR, Repo: repo, Number: 56, Author: "dominikgoertz", URL: "#56", UpdatedAt: at(348 * day)},
		{Kind: domain.KindPR, Repo: repo, Number: 51, Author: "KemalSoysal", URL: "#51", UpdatedAt: at(441 * day)},
		{Kind: domain.KindIssue, Repo: repo, Number: 48, URL: "#48", UpdatedAt: at(522 * day)},
		{Kind: domain.KindIssue, Repo: repo, Number: 45, URL: "#45", UpdatedAt: at(1273 * day)},
	}
	v := buildRadar(items, gh, "", testNow)
	assertNoCollisions(t, v)
	lo, hi := 8*36.0, 9*36.0
	for _, b := range v.Blips {
		if deg := bearingOf(b); deg < lo || deg > hi {
			t.Errorf("%s was moved out of its sector: bearing %.1f outside [%v, %v]", b.URL, deg, lo, hi)
		}
	}
}

// The same holds over the representative fixture: 150 items over ten repositories.
func TestRadarRepresentativeFixtureHasNoCollisions(t *testing.T) {
	gh := tenSites()
	items := representativeItems()
	for i := range items {
		items[i].Repo = gh.Repos[i%10]
		items[i].URL = fmt.Sprintf("u%d", i)
		items[i].Number = i + 1
	}
	assertNoCollisions(t, buildRadar(items, gh, "", testNow))
}

// radarGroups is two arc42 sites, two iSAQB sites and one repository no site claims.
var radarGroups = config.GitHub{
	Owner: "gernotstarke",
	Repos: []string{"arc42/org", "arc42/quality", "isaqb-org/cf", "isaqb-org/glossary", "arc42/stray"},
	Sites: []config.Site{
		{Name: "arc42.org", Repo: "arc42/org", Hue: "navy"},
		{Name: "quality", Repo: "arc42/quality", Hue: "plum"},
		{Name: "curriculum-foundation", Repo: "isaqb-org/cf", Hue: "orange", Group: "iSAQB"},
		{Name: "glossary", Repo: "isaqb-org/glossary", Hue: "apricot", Group: "iSAQB"},
	},
}

func radarGroupItems() []domain.Item {
	return []domain.Item{
		{Kind: domain.KindIssue, Repo: "arc42/org", Number: 1, URL: "a1", UpdatedAt: testNow},
		{Kind: domain.KindIssue, Repo: "arc42/quality", Number: 2, URL: "a2", UpdatedAt: testNow},
		{Kind: domain.KindIssue, Repo: "isaqb-org/cf", Number: 3, URL: "i1", UpdatedAt: testNow},
		{Kind: domain.KindPR, Repo: "isaqb-org/glossary", Number: 4, URL: "i2", UpdatedAt: testNow},
		{Kind: domain.KindIssue, Repo: "arc42/stray", Number: 5, URL: "s1", UpdatedAt: testNow},
	}
}

func sectorNames(v radarView) []string {
	var out []string
	for _, s := range v.Sectors {
		out = append(out, s.Name)
	}
	return out
}

// FR-1.15 AC8: the arc42 view draws its own sites, the iSAQB group as one sector, then Other; every
// item is drawn, and a condensed blip keeps its own site's colour.
func TestRadarArc42ViewCondensesISAQB(t *testing.T) {
	v := buildRadar(radarGroupItems(), radarGroups, "arc42", testNow)
	if got, want := sectorNames(v), []string{"arc42.org", "quality", "iSAQB", otherTileName}; !slices.Equal(got, want) {
		t.Fatalf("sectors = %v, want %v", got, want)
	}
	if v.Sectors[2].Hue != "orange" {
		t.Errorf("iSAQB sector hue = %q, want orange (its first site's)", v.Sectors[2].Hue)
	}
	for url, hue := range map[string]string{"i1": "hue-orange", "i2": "hue-apricot", "a1": "hue-navy"} {
		if b := blipFor(t, v, url); b.Hue != hue {
			t.Errorf("%s: hue %q, want %q", url, b.Hue, hue)
		}
	}
	for _, url := range []string{"i1", "i2"} {
		if b := bearingOf(blipFor(t, v, url)); b < 180 || b > 270 {
			t.Errorf("%s: bearing %.1f outside the iSAQB sector [180, 270]", url, b)
		}
	}
	if v.Total != 5 {
		t.Errorf("Total = %d, want 5: nothing the list shows is missing (QG-1)", v.Total)
	}
}

// FR-1.15 AC8: the iSAQB view mirrors it.
func TestRadarISAQBViewCondensesArc42(t *testing.T) {
	v := buildRadar(radarGroupItems(), radarGroups, "iSAQB", testNow)
	if got, want := sectorNames(v), []string{"curriculum-foundation", "glossary", "arc42", otherTileName}; !slices.Equal(got, want) {
		t.Fatalf("sectors = %v, want %v", got, want)
	}
	if b := blipFor(t, v, "a2"); b.Hue != "hue-plum" {
		t.Errorf("a2 hue = %q, want hue-plum", b.Hue)
	}
	if b := bearingOf(blipFor(t, v, "a1")); b < 180 || b > 270 {
		t.Errorf("a1: bearing %.1f outside the arc42 sector [180, 270]", b)
	}
	if v.Total != 5 {
		t.Errorf("Total = %d, want 5", v.Total)
	}
}

// FR-1.15 AC8: ?group= matches ignoring case and falls back to the first group; only a configured
// name ever comes back.
func TestRadarGroupFromTheQuery(t *testing.T) {
	for asked, want := range map[string]string{
		"": "arc42", "isaqb": "iSAQB", "ISAQB": "iSAQB", "arc42": "arc42",
		"nope": "arc42", "<script>": "arc42",
	} {
		if got := radarGroup(radarGroups, asked); got != want {
			t.Errorf("radarGroup(%q) = %q, want %q", asked, got, want)
		}
	}
	if got := radarGroup(config.GitHub{}, "x"); got != "" {
		t.Errorf("without sites radarGroup = %q, want empty", got)
	}
}

// With one group there is no switch and the sectors are the Sites view's.
func TestRadarSingleGroupIsUnchanged(t *testing.T) {
	v := buildRadar(nil, tenSites(), "", testNow)
	if len(v.Groups) != 0 {
		t.Errorf("one group drew %d switch links, want none", len(v.Groups))
	}
	var want []string
	for _, spec := range siteSpecs(tenSites()) {
		want = append(want, spec.Name)
	}
	if got := sectorNames(v); !slices.Equal(got, want) {
		t.Errorf("sectors = %v, want %v", got, want)
	}
}

// radarGroupsHandler serves the radar over radarGroups' configuration.
func radarGroupsHandler(t *testing.T, src ports.Source) http.Handler {
	t.Helper()
	s, c := newColdServerWith(t, func(o *Options) {
		o.Config.GitHub.Repos = radarGroups.Repos
		o.Config.GitHub.Sites = radarGroups.Sites
		o.Cache = snapshot.New(src, time.Hour, o.Clock)
	})
	warmCache(t, c)
	return s.Handler()
}

// FR-1.15 AC8: the switch is plain links, the shown group marked, and the query never echoed.
func TestRadarPageDrawsTheGroupSwitch(t *testing.T) {
	h := radarGroupsHandler(t, &fakeSource{items: radarGroupItems()})
	body := getAuthed(t, h, "/radar?group=ISAQB").Body.String()
	for _, want := range []string{
		`<a href="/radar?group=arc42">arc42</a>`,
		`<a href="/radar?group=iSAQB" aria-current="page">iSAQB</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("radar page lacks %s", want)
		}
	}
	// The Refresh form carries the request URI URL-encoded (chromeView.Return), so only the decoded,
	// HTML-escaped form would betray the value reaching the switch.
	junk := getAuthed(t, h, "/radar?group=%3Cscript%3E").Body.String()
	if strings.Contains(junk, "&lt;script&gt;") {
		t.Error("the asked-for group reached the page")
	}
	if !strings.Contains(junk, `<a href="/radar?group=arc42" aria-current="page">arc42</a>`) {
		t.Error("an unknown group should show the first group, marked")
	}
}

// FR-1.8 / spec 2026-09-29 §3: the iSAQB sites are ordinary tiles on the Sites view, in their
// orange hues. (That an alerts-off repository, never reported on, is listed neither as off nor as
// unavailable is BuildTierTile's rule for CoverageUnknown, pinned in internal/domain/alert_test.go.)
func TestSitesViewDrawsTheISAQBTiles(t *testing.T) {
	body := getAuthed(t, radarGroupsHandler(t, &fakeSource{items: radarGroupItems()}), "/sites").Body.String()
	for _, want := range []string{`class="tile hue-orange"`, `class="tile hue-apricot"`, "curriculum-foundation", "glossary"} {
		if !strings.Contains(body, want) {
			t.Errorf("sites page lacks %s", want)
		}
	}
}
