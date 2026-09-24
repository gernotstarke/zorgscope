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

	band := strings.Index(body, `<section class="needs`)
	filter := strings.Index(body, `<form class="filter"`)
	list := strings.Index(body, `class="repo-group`)
	if band < 0 || filter < 0 || list < 0 || band > filter || filter > list {
		t.Fatalf("the band must come first, then the filter, then the list (band %d, filter %d, list %d)", band, filter, list)
	}
	if !strings.Contains(body, `<section class="needs has-security"`) {
		t.Error("a band holding a Security item is not framed as such")
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

// FR-1.14 AC4: the band answers "what needs me" whatever the list is narrowed to, so a filter
// leaves it where it is — the filter does not swap it, and the fragment the refresh poll swaps in
// carries it out of band, filtered or not.
func TestTheBandStaysUnderAFilter(t *testing.T) {
	h := needsHandler(t, needsItems())
	c := signIn(t, h)
	for _, path := range []string{"/?kind=pr", "/items?kind=pr", "/?tier=security"} {
		body := getAs(h, path, c).Body.String()
		if !strings.Contains(body, `<section class="needs`) || !strings.Contains(body, "Rework the glossary") {
			t.Errorf("%s hides the band, or narrows it, under a filter", path)
		}
	}
	fragment := getAs(h, "/items", c).Body.String()
	oob := strings.Index(fragment, `id="needs" hx-swap-oob="true">`)
	if oob < 0 || !strings.HasPrefix(strings.TrimSpace(fragment[oob+len(`id="needs" hx-swap-oob="true">`):]), `<section class="needs`) {
		t.Error("the unfiltered fragment does not carry the band out of band, so the refresh poll would leave it stale")
	}
	form := openingTag(t, getAs(h, "/", c).Body.String(), `<form class="filter"`)
	if strings.Contains(form, "#needs") {
		t.Errorf("the filter swaps the band, which it must leave alone:\n%s", form)
	}
}

// FR-1.14 AC5: nothing needs the owner — the band stays and says so.
func TestAnEmptyBandSaysSo(t *testing.T) {
	body := getAuthed(t, needsHandler(t, []domain.Item{ghItem(1, "Nobody is waiting", testNow)}), "/").Body.String()
	if !strings.Contains(body, `<section class="needs is-clear"`) || !strings.Contains(body, "Nothing right now.") {
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
	if strings.Contains(body, `<section class="needs`) {
		t.Error("an empty snapshot draws the band as well as \"Nothing open.\"")
	}
}

// The top bar leads with what needs the owner, linked to the band, and says so in words when it is
// nothing (FR-1.14).
func TestTheTopBarLeadsWithWhatNeedsYou(t *testing.T) {
	h := needsHandler(t, needsItems())
	c := signIn(t, h)
	for _, path := range []string{"/sites", "/contributors"} {
		if !strings.Contains(getAs(h, path, c).Body.String(), `<a class="open-count-needs" href="/?view=list#needs">3 need you</a>`) {
			t.Errorf("%s does not lead its top bar with the needs count", path)
		}
	}
	calm := needsHandler(t, []domain.Item{ghItem(1, "Nobody is waiting", testNow)})
	if !strings.Contains(getAuthed(t, calm, "/sites").Body.String(), `>Nothing needs you</a>`) {
		t.Error("a calm top bar does not say that nothing needs you")
	}
}

// FR-1.14 AC10: an item idle for more than six months leaves the top of the band, its count and
// the top bar's, and waits behind a disclosure; a Security item never does, however old.
func TestStaleItemsWaitBehindTheOlderDisclosure(t *testing.T) {
	old := testNow.Add(-200 * 24 * time.Hour)
	stale := prItem(7, "Forgotten contribution", "amy", old)
	vuln := ghItem(8, "Old vulnerability", old)
	vuln.Labels = []string{"security"}
	fresh := prItem(9, "Fresh contribution", "bob", testNow)
	h := needsHandler(t, []domain.Item{stale, vuln, fresh})
	body := getAuthed(t, h, "/").Body.String()

	band := body[strings.Index(body, `<section class="needs`):]
	band = band[:strings.Index(band, "</section>")]
	older := strings.Index(band, `<details class="needs-more needs-older">`)
	if older < 0 || !strings.Contains(band, "<summary>1 older than 6 months</summary>") {
		t.Fatal("the stale contribution is not behind an \"older\" disclosure")
	}
	if i := strings.Index(band, "Forgotten contribution"); i < older {
		t.Error("the stale contribution is listed on top")
	}
	if i := strings.Index(band, "Old vulnerability"); i < 0 || i > older {
		t.Error("the old Security item left the top of the band")
	}
	if !strings.Contains(band, `class="needs-count">2<`) {
		t.Error("the band's count includes the stale item")
	}
	if !strings.Contains(body, ">2 need you</a>") {
		t.Error("the top bar's count includes the stale item")
	}
	if strings.Count(body, `class="item is-needed`) != 2 {
		t.Error("the stale item is drawn at full weight in the list")
	}
}

// Only stale items: the band says nothing needs you now, and still offers the older ones.
func TestABandOfOnlyStaleItemsIsClearWithTheOlderOnesOffered(t *testing.T) {
	stale := prItem(7, "Forgotten contribution", "amy", testNow.Add(-200*24*time.Hour))
	body := getAuthed(t, needsHandler(t, []domain.Item{stale}), "/").Body.String()
	for _, want := range []string{`<section class="needs is-clear"`, "Nothing right now.", "<summary>1 older than 6 months</summary>", ">Nothing needs you</a>"} {
		if !strings.Contains(body, want) {
			t.Errorf("a band of only stale items lacks %s", want)
		}
	}
}
