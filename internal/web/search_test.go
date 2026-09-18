package web

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// FR-12.1 AC1: the results page ranks the snapshot and marks the matched text.
func TestSearchPageRendersHitsWithMarks(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: []domain.Item{
		{Kind: domain.KindIssue, Repo: "arc42/arc42-template", Number: 7, Title: "Fix the <header> & footer", Author: "gernot", Labels: []string{"bug"}, UpdatedAt: testNow},
		{Kind: domain.KindPR, Repo: "arc42/arc42-template", Number: 8, Title: "Unrelated", Author: "alice", UpdatedAt: testNow},
	}})
	body := getAuthed(t, h, "/search?q=header+gernot").Body.String()
	for _, want := range []string{
		"1 result for “header gernot”",
		"Fix the &lt;<mark>header</mark>&gt; &amp; footer",
		"matched: title, contributor",
		`class="hit hue-slate"`, "label-bug", "#7",
		// Cmd-K is a file of our own, linked like every other asset: hashed and under 'self'
		// (FR-12.1 AC3, QS-4.4).
		`<script src="/static/search.js`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("results page lacks %s", want)
		}
	}
	if strings.Contains(body, "Unrelated") {
		t.Error("an item matching no word was listed")
	}
	if !strings.Contains(body, `value="header gernot"`) {
		t.Error("the top-bar box does not echo the query")
	}
}

func TestSearchPageReflectsNothingUnescaped(t *testing.T) {
	h := dashHandler(t, &fakeSource{})
	body := getAuthed(t, h, "/search?q=%3Cscript%3Ealert(1)%3C/script%3E").Body.String()
	if strings.Contains(body, "<script>alert") {
		t.Error("the query is reflected unescaped")
	}
	if !strings.Contains(body, "Nothing matches") {
		t.Error("no count line for a query without hits")
	}
}

func TestSearchWithoutQueryShowsTheHint(t *testing.T) {
	body := getAuthed(t, dashHandler(t, &fakeSource{}), "/search").Body.String()
	if !strings.Contains(body, `class="search-hint"`) || strings.Contains(body, `class="hits"`) {
		t.Error("an empty query should show the hint and no list")
	}
}

// The top-bar form asks for the page with HX-Request and selects main; the answer is the whole
// page, which htmx cuts down. A poll from the wait page is a different matter (waiting_test.go).
func TestSearchAnswersAnHtmxRequestWithThePage(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: []domain.Item{
		{Kind: domain.KindIssue, Repo: "o/r", Number: 1, Title: "Header", UpdatedAt: testNow},
	}})
	rec := getWith(h, "/search?q=header", signIn(t, h), map[string]string{"HX-Request": "true"})
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "<main") || !strings.Contains(body, "<mark>Header</mark>") {
		t.Errorf("htmx search = %d %s", rec.Code, firstLineContaining(body, "result"))
	}
}

// FR-1.9 AC1 and AC5 hold for /search as for /: a navigation during a fetch waits, an htmx
// request is answered from the current list.
func TestSearchDuringAFetchWaitsForNavigationAndAnswersHtmx(t *testing.T) {
	h, release := coldServer(t)
	defer close(release)
	c := signIn(t, h)

	if body := getAs(h, "/search?q=x", c).Body.String(); !strings.Contains(body, waitingMarker) || !strings.Contains(body, `hx-get="/search?q=x"`) {
		t.Error("a navigation to /search during a fetch did not get the wait page polling itself")
	}
	rec := getWith(h, "/search?q=x", c, map[string]string{"HX-Request": "true"})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), waitingMarker) {
		t.Errorf("an htmx search during a fetch = %d, body has wait page: %v",
			rec.Code, strings.Contains(rec.Body.String(), waitingMarker))
	}
}

func TestSearchCountLine(t *testing.T) {
	for _, tc := range []struct {
		q    string
		n    int
		want string
	}{
		{"x", 0, "Nothing matches “x”."},
		{"x", 1, "1 result for “x”"},
		{"a b", 7, "7 results for “a b”"},
	} {
		if got := searchCountLine(tc.q, tc.n); got != tc.want {
			t.Errorf("searchCountLine(%q, %d) = %q, want %q", tc.q, tc.n, got, tc.want)
		}
	}
}

func TestTitleRuns(t *testing.T) {
	got := titleRuns("Fix the header", [][2]int{{8, 14}})
	want := []titleRun{{Text: "Fix the ", Mark: false}, {Text: "header", Mark: true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("titleRuns = %+v, want %+v", got, want)
	}
	if got := titleRuns("plain", nil); !reflect.DeepEqual(got, []titleRun{{Text: "plain"}}) {
		t.Errorf("titleRuns(no spans) = %+v", got)
	}
}
