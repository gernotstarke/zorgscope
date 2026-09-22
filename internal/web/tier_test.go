package web

import (
	"net/http"
	"net/http/httptest"
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
		// The Copilot pull request is not a dependency update and must not be drawn as one.
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
	if body := getAs(h, "/", c).Body.String(); !strings.Contains(body, "· 1 security") {
		t.Error("the list header does not count the open security item")
	}

	// Filtered to pull requests, the list shows none of these items — ghItem makes issues — yet
	// the count still reports the security one, because it counts everything open.
	if body := getAs(h, "/?kind=pr", c).Body.String(); !strings.Contains(body, "· 1 security") {
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
