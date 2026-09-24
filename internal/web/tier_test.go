package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// tierItems are one item of each tier, the security one old enough to be quiet by default so that
// the quiet override is exercised on every page that draws it.
func tierItems() []domain.Item {
	security := ghItem(1, "Bump concurrent-ruby", testNow.AddDate(0, 0, -400))
	security.Author = "dependabot"
	security.Advisories = []string{"CVE-2026-54904", "GHSA-6wx8-w4f5-wwcr"}

	dependency := ghItem(2, "Bump uri", testNow.Add(-time.Hour))
	dependency.Author = "dependabot"

	plain := ghItem(3, "Fix broken include", testNow.Add(-time.Hour))
	plain.Author = "copilot-swe-agent"

	return []domain.Item{security, dependency, plain}
}

// FR-1.13 AC2: both tiers draw a word, a class and — for security — the evidence, on every page
// that draws an item. Search builds its rows separately from the list, so it is checked on its
// own: a chip that appeared on one and not the other is precisely the seam this guards.
func TestTiersAreDrawnOnEveryPageThatDrawsAnItem(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: tierItems()})
	c := signIn(t, h)

	for _, path := range []string{"/", "/items", "/search?q=bump"} {
		body := getAs(h, path, c).Body.String()
		for _, want := range []string{
			`tier-security`, `tier-dependency`,
			`>Security<`, `>Dependency<`,
			`title="cites CVE-2026-54904`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %s", path, want)
			}
		}
		// The Copilot pull request is not a dependency update and must not be drawn as one. The
		// list's Needs-you band draws the same chips again (FR-1.14), so only the rows below it are
		// counted here.
		if i := strings.Index(body, `class="repo-group`); i >= 0 {
			body = body[i:]
		}
		if i := strings.Index(body, `class="needs-slot"`); i >= 0 {
			body = body[:i]
		}
		if n := strings.Count(body, `>Dependency<`); n != 1 {
			t.Errorf("%s draws %d Dependency chips, want exactly 1", path, n)
		}
	}
}

// FR-1.13 AC3: the security item is 400 days old and would be quiet by default; it must not be,
// on the list or on the search results.
func TestASecurityItemIsNeverQuiet(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: tierItems()})
	c := signIn(t, h)

	for _, path := range []string{"/", "/search?q=concurrent"} {
		body := getAs(h, path, c).Body.String()
		if strings.Contains(body, "is-quiet") {
			t.Errorf("%s dims the security item as quiet", path)
		}
	}
}

// FR-1.13 AC4: the header counts security items, says nothing when there are none, and counts
// over everything rather than the filtered view.
func TestTheHeaderCountsSecurityItems(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: tierItems()})
	c := signIn(t, h)
	if body := getAs(h, "/", c).Body.String(); !strings.Contains(body, ">1 security</a>") {
		t.Error("the list header does not count the open security item")
	}

	// Filtered to pull requests, the list shows none of these items — ghItem makes issues — yet
	// the count still reports the security one, because it counts everything open.
	if body := getAs(h, "/?kind=pr", c).Body.String(); !strings.Contains(body, ">1 security</a>") {
		t.Error("the security count followed the filter; it must count everything open")
	}

	// With no security item the clause is absent, not "0 security". Asserting the exact header
	// text rather than the absence of the word keeps this from tripping on unrelated copy.
	quiet := dashHandler(t, &fakeSource{items: []domain.Item{ghItem(9, "Nothing to see", testNow)}})
	qc := signIn(t, quiet)
	if body := getAs(quiet, "/", qc).Body.String(); !strings.Contains(body, ">1 open<") {
		t.Error("the header is not exactly \"1 open\" when no security item is open")
	}
}

// Advisory identifiers are borrowed text. One that carried markup must reach the page escaped, and
// the escaped form must still be there — a chip that stopped rendering altogether would also pass
// the negative half of this test.
func TestEvidenceIsEscaped(t *testing.T) {
	it := ghItem(1, "Bump thing", testNow)
	it.Advisories = []string{`CVE-2026-1111"><script>`}
	h := dashHandler(t, &fakeSource{items: []domain.Item{it}})
	c := signIn(t, h)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, `"><script>`) {
		t.Error("an advisory identifier reached the page unescaped")
	}
	// html/template renders " as &#34; and < as &lt;, so the evidence reaches the page as
	// title="cites CVE-2026-1111&#34;&gt;&lt;script&gt;.
	if !strings.Contains(body, `title="cites CVE-2026-1111&#34;&gt;&lt;script&gt;`) {
		t.Error("the escaped advisory identifier is not on the page: the chip may have stopped rendering entirely")
	}
}

// FR-1.13 AC2: the Sites tiles draw the chip too — a Dependabot PR citing a CVE must not be the
// one place a Security item goes unmarked, and a visitor whose landing view is Sites (FR-1.12)
// would otherwise see it there first, unmarked, before ever reaching the list. tierItems' three
// items are all issues in repo "org/repo" (ghItem's fixed repository); dashHandler configures
// that repository among its watched repos with no site claiming it, so all three land, unmarked
// by any site, on the one "Other" tile's Issues list, well inside its four-row cut.
func TestSitesTilesDrawTheTierChip(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: tierItems()})
	body := getAuthed(t, h, "/sites").Body.String()
	for _, want := range []string{`>Security<`, `title="cites CVE-2026-54904`} {
		if !strings.Contains(body, want) {
			t.Errorf("/sites lacks %s: a Dependabot PR citing a CVE would appear on a tile unmarked", want)
		}
	}
}

// FR-1.13: the tier is a filter axis of its own, read from the query and written back into it, so
// that a filtered list can be linked to, bookmarked and reloaded like every other filter.
func TestParseFilterReadsTheTier(t *testing.T) {
	for in, want := range map[string]domain.Tier{
		"security":   domain.TierSecurity,
		"dependency": domain.TierDependency,
		"":           domain.TierNone,
		"urgent":     domain.TierNone,
	} {
		if got := parseFilter(url.Values{"tier": {in}}, time.UTC).MinTier; got != want {
			t.Errorf("tier %q parsed as %v, want %v", in, got, want)
		}
	}
}

func TestQueryStringCarriesTheTier(t *testing.T) {
	for tier, want := range map[domain.Tier]string{
		domain.TierSecurity:   "?tier=security",
		domain.TierDependency: "?tier=dependency",
		domain.TierNone:       "",
	} {
		if got := queryString(domain.Filter{MinTier: tier}); got != want {
			t.Errorf("queryString(%v) = %q, want %q", tier, got, want)
		}
	}
}

// The axis is a floor: asking for dependencies shows the security items too, because a bump that
// fixes a vulnerability is still a bump, and the Security tile's link means "everything marked".
func TestTheTierFilterNarrowsTheList(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: tierItems()})
	c := signIn(t, h)

	cases := map[string][]string{
		"/?tier=security":   {"Bump concurrent-ruby"},
		"/?tier=dependency": {"Bump concurrent-ruby", "Bump uri"},
	}
	for path, want := range cases {
		body := getAs(h, path, c).Body.String()
		for _, title := range want {
			if !strings.Contains(body, title) {
				t.Errorf("%s does not show %q", path, title)
			}
		}
		if strings.Contains(body, "Fix broken include") {
			t.Errorf("%s shows an unmarked item", path)
		}
	}
}

// The count above the list is the one place a visitor learns that something is marked, so it
// leads to the items it counts (FR-1.13 AC4).
func TestTheSecurityCountLinksToTheFilteredList(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: tierItems()})
	c := signIn(t, h)
	body := getAs(h, "/", c).Body.String()
	if !strings.Contains(body, `href="/?tier=security"`) {
		t.Error("the security count is not a link to the list filtered to security items")
	}
}

// FR-2.1 AC2: every filter control works without JavaScript, which means it is a form field the
// page echoes the current value back into.
func TestTheFilterFormOffersTheTier(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: tierItems()})
	c := signIn(t, h)

	body := getAs(h, "/?tier=security", c).Body.String()
	for _, want := range []string{
		`name="tier" value=""`,
		`name="tier" value="dependency"`,
		`name="tier" value="security" checked`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the filter form lacks %s", want)
		}
	}
}

// securityTileItems are three marked items spread over two repositories, plus an unmarked one:
// the Security tile's whole point is that the marked ones are gathered wherever they are open.
func securityTileItems() []domain.Item {
	security := ghItem(1, "Bump concurrent-ruby", testNow.Add(-2*time.Hour))
	security.Repo = "org/repo"
	security.Advisories = []string{"CVE-2026-54904"}

	dependency := ghItem(2, "Bump uri", testNow.Add(-time.Hour))
	dependency.Repo = "arc42/other"
	dependency.Author = "dependabot"

	pr := ghItem(3, "Bump rack", testNow.Add(-3*time.Hour))
	pr.Kind = domain.KindPR
	pr.Author = "renovate"

	return []domain.Item{security, dependency, pr, ghItem(4, "Fix the header", testNow)}
}

// FR-1.13: the Sites view opens with one tile gathering everything marked, so that the loud thing
// is not spread over ten tiles. It names each item's repository, because it spans them all.
func TestTheSitesViewOpensWithTheSecurityTile(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: securityTileItems()})
	body := getAuthed(t, h, "/sites").Body.String()

	tile := strings.Index(body, `class="tile tile-tier`)
	if tile < 0 {
		t.Fatal("/sites has no security tile")
	}
	if first := strings.Index(body, `class="tile hue-`); first >= 0 && first < tile {
		t.Error("a site tile is drawn before the security tile")
	}
	if !strings.Contains(body, "tile-tier is-marked") {
		t.Error("a security tile with marked items does not raise its hazard tape (FR-1.13 AC6)")
	}
	if !strings.Contains(body, "1 security · 2 dependency") {
		t.Error("the tile does not count the tiers before the cut")
	}
	for _, want := range []string{"Bump concurrent-ruby", "Bump uri", "Bump rack", "arc42/other"} {
		if !strings.Contains(body, want) {
			t.Errorf("the security tile lacks %q", want)
		}
	}
}

// The red rule is the tile's alarm, so it is drawn only while a security item is open — a mark
// that is always on stops being read (ADR-0012, ADR-0014).
func TestTheSecurityTilesRuleFollowsTheSecurityItems(t *testing.T) {
	dependency := ghItem(1, "Bump uri", testNow)
	dependency.Author = "dependabot"

	cases := map[string]struct {
		items []domain.Item
		alert bool
	}{
		"a security item is open":   {securityTileItems(), true},
		"only a dependency is open": {[]domain.Item{dependency}, false},
		"nothing is marked":         {[]domain.Item{ghItem(1, "Fix the header", testNow)}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := dashHandler(t, &fakeSource{items: tc.items})
			body := getAuthed(t, h, "/sites").Body.String()
			if got := strings.Contains(body, ` is-alert"`); got != tc.alert {
				t.Errorf("the tile draws its rule = %v, want %v", got, tc.alert)
			}
		})
	}
}

// Nothing marked is the ordinary state (ADR-0014). The tile stays, and says so in words: a tile
// that vanished would read as a check that had stopped running.
func TestTheSecurityTileSaysWhenNothingIsMarked(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: []domain.Item{ghItem(1, "Fix the header", testNow)}})
	body := getAuthed(t, h, "/sites").Body.String()

	if !strings.Contains(body, "All clear — no security or dependency items open") {
		t.Error("the security tile does not say that nothing is marked")
	}
	// FR-1.13 AC6: the hazard tape is up only while something is marked.
	if strings.Contains(body, "is-marked") || !strings.Contains(body, `tile-tier is-clear"`) {
		t.Error("an empty security tile is drawn as marked")
	}
	if strings.Contains(body, `href="/?tier=dependency"`) {
		t.Error("an empty tile links on to a list that would be empty too")
	}
}

// FR-1.8 AC3's rule, applied to this tile: only a tile that cut something links on, and it links
// to the list narrowed to everything it marks — security items included, since the axis is a
// floor.
func TestTheSecurityTileLinksOnOnlyWhenItCutSomething(t *testing.T) {
	var many []domain.Item
	for i := range 6 {
		it := ghItem(i+1, "Bump something "+strconv.Itoa(i), testNow.Add(-time.Duration(i)*time.Hour))
		it.Author = "dependabot"
		many = append(many, it)
	}

	h := dashHandler(t, &fakeSource{items: many})
	if body := getAuthed(t, h, "/sites").Body.String(); !strings.Contains(body, `href="/?tier=dependency"`) {
		t.Error("a tile that cut items does not link to the filtered list")
	}

	few := dashHandler(t, &fakeSource{items: securityTileItems()})
	if body := getAuthed(t, few, "/sites").Body.String(); strings.Contains(body, `href="/?tier=dependency"`) {
		t.Error("a tile that cut nothing links on anyway")
	}
}

// A marked row wears hazard tape down its edge and a solid chip (FR-1.13 AC2). Drawn as it was
// first built, the Dependency chip took --muted at the labels' weight in the labels' pill, beside
// a "dependencies" label saying the same word: a mark that cannot be told from the furniture
// beside it is not a mark.
func TestAMarkedRowWearsHazardTape(t *testing.T) {
	raw, err := fs.ReadFile(embedded, "static/app.css")
	if err != nil {
		t.Fatalf("reading the embedded app.css: %v", err)
	}
	css := string(raw)
	for _, want := range []string{
		".item.tier-security", ".item.tier-dependency",
		".hit.tier-security", ".hit.tier-dependency",
		"--tier-tape: var(--danger)", "--tier-tape: var(--warn)",
		//nolint:misspell // repeating-linear-gradient is the CSS function's own name
		"repeating-linear-gradient(45deg, var(--tier-tape)",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css has no %s: the mark is drawn too quietly", want)
		}
	}
	//nolint:misspell // "color" is the CSS property, not a misspelling
	if strings.Contains(css, ".tier-chip-dependency { color: var(--muted)") {
		t.Error("the Dependency chip still draws in the label colour")
	}
}

// The tier chip says "Dependency"; the label chip beside it said "dependencies". One of the two is
// furniture. The label that produced the mark is dropped, on every page that draws a row.
func TestTheLabelThatProducedTheMarkIsNotDrawnTwice(t *testing.T) {
	dependency := ghItem(1, "Bump the dev-patches group", testNow)
	dependency.Author = "dependabot"
	dependency.Labels = []string{"dependencies", "documentation"}

	security := ghItem(2, "Bump concurrent-ruby", testNow)
	security.Advisories = []string{"CVE-2026-54904"}
	security.Labels = []string{"Security", "bug"}

	h := dashHandler(t, &fakeSource{items: []domain.Item{dependency, security}})
	c := signIn(t, h)

	for _, path := range []string{"/", "/items", "/search?q=bump"} {
		body := getAs(h, path, c).Body.String()
		// Matched as a label chip, not as bare text: the tier chip itself ends in
		// ">Security</span>", so a looser assertion would find the mark and call it the label.
		for _, gone := range []string{`class="label label-other">dependencies<`, `class="label label-other">Security<`} {
			if strings.Contains(body, gone) {
				t.Errorf("%s draws the label %s that its tier chip already says", path, gone)
			}
		}
		for _, kept := range []string{`class="label label-documentation">documentation<`, `class="label label-bug">bug<`} {
			if !strings.Contains(body, kept) {
				t.Errorf("%s dropped the label %s, which says something the chip does not", path, kept)
			}
		}
		// The chips themselves must still be there: dropping the labels must not have dropped the
		// marks with them.
		for _, want := range []string{`>Dependency<`, `>Security<`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lost the %s chip", path, want)
			}
		}
	}
}
