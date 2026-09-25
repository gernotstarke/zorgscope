package web

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
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
	}, radarGitHub, testNow)

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
	v := buildRadar([]domain.Item{{Repo: "gone/repo", Number: 1, URL: "u", UpdatedAt: testNow}}, gh, testNow)
	if len(v.Sectors) != 3 || v.Sectors[2].Name != otherTileName {
		t.Fatalf("sectors = %+v, want the two sites and Other", v.Sectors)
	}
	if v := buildRadar(nil, gh, testNow); len(v.Sectors) != 2 {
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
	}, radarGitHub, testNow)
	fresh, month, old := distanceOf(blipFor(t, v, "fresh")), distanceOf(blipFor(t, v, "month")), distanceOf(blipFor(t, v, "old"))
	if fresh >= month || month >= old {
		t.Errorf("distances fresh %.1f, month %.1f, old %.1f are not increasing", fresh, month, old)
	}
	if old > radarR {
		t.Errorf("a five-year-old item stands at %.1f, beyond the rim %d", old, radarR)
	}
	if u := distanceOf(blipFor(t, v, "unknown")); math.Abs(u-old) > 0.2 {
		t.Errorf("an unknown update time stands at %.1f, want the rim %.1f", u, old)
	}
}

// The same item stands in the same place on every render.
func TestRadarPositionIsStable(t *testing.T) {
	items := []domain.Item{{Repo: "arc42/org", Number: 7, URL: "u", UpdatedAt: testNow.Add(-48 * time.Hour)}}
	a, b := buildRadar(items, radarGitHub, testNow), buildRadar(items, radarGitHub, testNow)
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
	}, radarGitHub, testNow)

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
	for _, b := range buildRadar(items, radarGitHub, testNow).Blips {
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
		radarGitHub, testNow)
	lines := v.Blips[0].Card.Title
	if len(lines) < 2 || len(lines) > 3 {
		t.Fatalf("title wrapped into %d lines, want 2 or 3: %q", len(lines), lines)
	}
	for _, l := range lines {
		if len([]rune(l)) > radarTitleWidth+1 {
			t.Errorf("line %q longer than %d", l, radarTitleWidth)
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
