package web

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/snapshot"
)

const testOwner = "gernotstarke"

// needsHandler is dashHandler with github.owner set, so the band has somebody to work for.
func needsHandler(t *testing.T, items []domain.Item) http.Handler {
	t.Helper()
	src := &fakeSource{items: items}
	s, c := newColdServerWith(t, func(o *Options) {
		o.Config.GitHub.Repos = []string{"org/repo"}
		o.Config.GitHub.Owner = testOwner
		o.Cache = snapshot.New(src, time.Hour, o.Clock)
	})
	warmCache(t, c)
	return s.Handler()
}

func prItem(number int, title, author string, at time.Time) domain.Item {
	it := ghItem(number, title, at)
	it.Kind = domain.KindPR
	it.Author = author
	it.URL = "https://github.com/org/repo/pull/" + strconv.Itoa(number)
	return it
}

func needsItems() []domain.Item {
	security := ghItem(1, "Leaked token in the build log", testNow.Add(-48*time.Hour))
	security.Labels = []string{"security"}
	review := prItem(2, "Rework the glossary", "amy", testNow.Add(-3*time.Hour))
	review.ReviewRequested = []string{testOwner}
	contribution := prItem(3, "Fix a typo in chapter 5", "bob", testNow.Add(-time.Hour))
	own := prItem(4, "My own refactoring", testOwner, testNow)
	plain := ghItem(5, "Nobody is waiting on this", testNow)
	return []domain.Item{plain, own, contribution, review, security}
}

// FR-1.14 AC1–AC3: the unfiltered list opens with the band, loudest reason first, each reason in
// words; every item of the band is still in the list below, drawn at full weight, and the rest are
// drawn a step quieter.
func TestTheListOpensWithTheNeedsYouBand(t *testing.T) {
	body := getAuthed(t, needsHandler(t, needsItems()), "/").Body.String()

	band := strings.Index(body, `class="needs"`)
	list := strings.Index(body, `class="repo-group`)
	if band < 0 || list < 0 || band > list {
		t.Fatalf("the band must come before the list (band %d, list %d)", band, list)
	}
	section := body[band:list]
	order := []string{"Leaked token", "Rework the glossary", "Fix a typo"}
	last := -1
	for _, title := range order {
		i := strings.Index(section, title)
		if i < 0 {
			t.Fatalf("the band lacks %q", title)
		}
		if i < last {
			t.Errorf("%q is out of order in the band", title)
		}
		last = i
	}
	for _, want := range []string{">Security<", ">Review requested<", ">Contribution<", `class="needs-count">3<`} {
		if !strings.Contains(section, want) {
			t.Errorf("the band lacks %s", want)
		}
	}
	for _, absent := range []string{"My own refactoring", "Nobody is waiting"} {
		if strings.Contains(section, absent) {
			t.Errorf("the band holds %q, which needs nobody", absent)
		}
	}

	rows := body[list:]
	if n := strings.Count(rows, `class="item is-needed`); n != 3 {
		t.Errorf("the list marks %d rows as needed, want 3", n)
	}
	for _, title := range []string{"My own refactoring", "Nobody is waiting", "Leaked token", "Fix a typo"} {
		if !strings.Contains(rows, title) {
			t.Errorf("the list lost %q: the band must never take an item out of it (QG-1)", title)
		}
	}
}

// FR-1.14 AC4: a filter is a question of its own, and the band gives way to it — on the page and
// in the fragment the filter swaps in. The fragment of the unfiltered list carries it back.
func TestTheBandGivesWayToAFilter(t *testing.T) {
	h := needsHandler(t, needsItems())
	c := signIn(t, h)
	for _, path := range []string{"/?kind=pr", "/items?kind=pr", "/?tier=security"} {
		if strings.Contains(getAs(h, path, c).Body.String(), `class="needs`) {
			t.Errorf("%s draws the band under a filter", path)
		}
	}
	if !strings.Contains(getAs(h, "/items", c).Body.String(), `class="needs"`) {
		t.Error("the unfiltered fragment lacks the band, so clearing a filter would not bring it back")
	}
}

// FR-1.14 AC5: nothing needs the owner — the band stays and says so.
func TestAnEmptyBandSaysSo(t *testing.T) {
	body := getAuthed(t, needsHandler(t, []domain.Item{ghItem(1, "Nobody is waiting", testNow)}), "/").Body.String()
	if !strings.Contains(body, `class="needs is-clear"`) || !strings.Contains(body, "Nothing right now.") {
		t.Error("an empty band does not say that nothing needs you")
	}
}

// FR-1.14 AC6: a long band opens ten rows and keeps the rest one click away, without script.
func TestALongBandKeepsTheRestBehindADisclosure(t *testing.T) {
	var items []domain.Item
	for i := range 13 {
		items = append(items, prItem(i+1, "Contribution "+strconv.Itoa(i+1), "amy", testNow.Add(-time.Duration(i)*time.Hour)))
	}
	body := getAuthed(t, needsHandler(t, items), "/").Body.String()
	if !strings.Contains(body, `<details class="needs-more">`) || !strings.Contains(body, "<summary>3 more</summary>") {
		t.Error("the thirteen-row band does not keep three rows behind a disclosure")
	}
	if !strings.Contains(body, `class="needs-count">13<`) {
		t.Error("the heading does not count every row, shown or not")
	}
}

// With nothing open at all the list says "Nothing open." and the band would only repeat it.
func TestNothingOpenDrawsNoBand(t *testing.T) {
	body := getAuthed(t, needsHandler(t, nil), "/").Body.String()
	if strings.Contains(body, `class="needs`) {
		t.Error("an empty snapshot draws the band as well as \"Nothing open.\"")
	}
}
