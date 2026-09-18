package domain

import (
	"reflect"
	"testing"
	"time"
)

func TestParseQuery(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Query
	}{
		{"", Query{}},
		{"  Header   Bug ", Query{Words: []string{"header", "bug"}}},
		{"issue gernot", Query{Words: []string{"gernot"}, Kind: KindIssue}},
		{"PRs", Query{Kind: KindPR}},
		{"pull issues", Query{Kind: KindIssue}},
	} {
		if got := ParseQuery(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseQuery(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func searchItem(n int, title, author, repo, summary string, labels ...string) Item {
	return Item{Kind: KindIssue, Number: n, Title: title, Author: author, Repo: repo, Summary: summary, Labels: labels, UpdatedAt: time.Date(2026, 9, 1, 0, n, 0, 0, time.UTC)}
}

func TestSearchScoresFields(t *testing.T) {
	for _, tc := range []struct {
		name    string
		item    Item
		q       string
		score   int
		matched []string
	}{
		{"title word start", searchItem(1, "Fix the header", "", "o/r", ""), "header", 4, []string{"title"}},
		{"title elsewhere", searchItem(1, "Subheader", "", "o/r", ""), "header", 2, []string{"title"}},
		{"contributor", searchItem(1, "x", "gernot", "o/r", ""), "gern", 3, []string{"contributor"}},
		{"label once", searchItem(1, "x", "", "o/r", "", "bug", "bugfix"), "bug", 3, []string{"label"}},
		{"repository", searchItem(1, "x", "", "arc42/faq.arc42.org-site", ""), "faq", 1, []string{"repository"}},
		{"summary", searchItem(1, "x", "", "o/r", "about the header"), "header", 1, []string{"summary"}},
		{"sums across fields", searchItem(1, "Bug in header", "bugsy", "o/r", "a bug", "bug"), "bug", 4 + 3 + 3 + 1, []string{"title", "contributor", "label", "summary"}},
		{"sums across words", searchItem(1, "Fix the header", "gernot", "o/r", ""), "header gernot", 7, []string{"title", "contributor"}},
	} {
		hits := Search([]Item{tc.item}, ParseQuery(tc.q))
		if len(hits) != 1 {
			t.Fatalf("%s: %d hits, want 1", tc.name, len(hits))
		}
		if hits[0].Score != tc.score || !reflect.DeepEqual(hits[0].Matched, tc.matched) {
			t.Errorf("%s: score %d matched %v, want %d %v", tc.name, hits[0].Score, hits[0].Matched, tc.score, tc.matched)
		}
	}
}

func TestSearchRequiresEveryWord(t *testing.T) {
	items := []Item{searchItem(1, "Fix the header", "gernot", "o/r", "")}
	if hits := Search(items, ParseQuery("header nobody")); len(hits) != 0 {
		t.Errorf("an item missing one word was found: %+v", hits)
	}
	if hits := Search(items, ParseQuery("")); hits != nil {
		t.Errorf("an empty query found %d hits, want nil", len(hits))
	}
}

func TestSearchKindFiltersAndScoresNothing(t *testing.T) {
	pr := searchItem(1, "Header", "", "o/r", "")
	pr.Kind = KindPR
	items := []Item{pr, searchItem(2, "Header", "", "o/r", "")}
	hits := Search(items, ParseQuery("pr header"))
	if len(hits) != 1 || hits[0].Item.Number != 1 || hits[0].Score != 4 {
		t.Errorf("pr header = %+v", hits)
	}
	if hits := Search(items, ParseQuery("issues")); len(hits) != 1 || hits[0].Item.Number != 2 || hits[0].Score != 0 || hits[0].Matched != nil {
		t.Errorf("issues alone = %+v", hits)
	}
}

func TestSearchOrdersByScoreThenUpdateThenRepoThenNumber(t *testing.T) {
	a := searchItem(1, "header", "", "b/r", "")    // score 4, updated 00:01
	b := searchItem(2, "subheader", "", "a/r", "") // score 2, updated 00:02
	c := searchItem(3, "header", "", "a/r", "")    // score 4, updated 00:03
	d := searchItem(4, "header", "", "a/r", "")
	d.UpdatedAt = c.UpdatedAt // tie with c on score and update, same repo a/r → number
	e := searchItem(5, "header", "", "0/r", "")
	e.UpdatedAt = c.UpdatedAt // tie with c and d on score and update, lower repo 0/r < a/r → repo
	hits := Search([]Item{a, b, c, d, e}, ParseQuery("header"))
	var got []int
	for _, h := range hits {
		got = append(got, h.Item.Number)
	}
	if want := []int{5, 3, 4, 1, 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestSearchTitleSpansMergeAndMapBack(t *testing.T) {
	hits := Search([]Item{searchItem(1, "Header headers ahead", "", "o/r", "")}, ParseQuery("head header"))
	if want := [][2]int{{0, 6}, {7, 13}, {16, 20}}; !reflect.DeepEqual(hits[0].TitleSpans, want) {
		t.Errorf("spans = %v, want %v", hits[0].TitleSpans, want)
	}
	// "İ" lower-cases to three bytes from two, so byte offsets cannot be mapped back.
	hits = Search([]Item{searchItem(1, "İstanbul header", "", "o/r", "")}, ParseQuery("header"))
	if len(hits) != 1 || hits[0].TitleSpans != nil {
		t.Errorf("a title whose lower-casing changes length: hits %+v", hits)
	}
}

func TestSearchNeverMutates(t *testing.T) {
	items := []Item{searchItem(1, "b", "", "o/r", ""), searchItem(2, "a", "", "o/r", "")}
	before := append([]Item(nil), items...)
	Search(items, ParseQuery("a b"))
	if !reflect.DeepEqual(items, before) {
		t.Error("Search reordered or changed the caller's items")
	}
}
