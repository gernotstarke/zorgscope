package domain_test

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// FR-1.8 AC2: a tile lists at most MaxPRs pull requests and MaxIssues issues, most recently
// updated first, and counts its totals and per-repository counts before the cut.
func TestBuildSiteTilesCutsEachKindAndCountsBeforeTheCut(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	var items []domain.Item
	for i := range 5 { // five pull requests
		items = append(items, domain.Item{
			Repo: "arc42/q", Kind: domain.KindPR, Number: 100 + i,
			UpdatedAt: now.Add(-time.Duration(i) * time.Hour),
		})
	}
	for i := range 6 { // six issues
		items = append(items, domain.Item{
			Repo: "arc42/q", Kind: domain.KindIssue, Number: 200 + i,
			UpdatedAt: now.Add(-time.Duration(10+i) * time.Hour),
		})
	}

	tiles := domain.BuildSiteTiles(domain.SiteTilesInput{
		Items: items, MaxPRs: 3, MaxIssues: 4,
		Sites: []domain.SiteSpec{{Name: "quality.arc42.org", Hue: "plum", Repos: []string{"arc42/q"}}},
	})

	if len(tiles) != 1 {
		t.Fatalf("tiles = %d, want 1", len(tiles))
	}
	q := tiles[0]
	if len(q.PRs) != 3 || len(q.Issues) != 4 {
		t.Fatalf("listed %d PRs and %d issues, want 3 and 4", len(q.PRs), len(q.Issues))
	}
	if q.PRTotal != 5 || q.IssueTotal != 6 {
		t.Errorf("totals = %d PRs, %d issues, want 5 and 6: counted after the cut", q.PRTotal, q.IssueTotal)
	}
	if !q.More {
		t.Error("More = false, but the tile cut two items")
	}
	if got := []int{q.PRs[0].Number, q.PRs[1].Number, q.PRs[2].Number}; !slices.Equal(got, []int{100, 101, 102}) {
		t.Errorf("PR order = %v, want the most recently updated first", got)
	}
	if got := []int{q.Issues[0].Number, q.Issues[1].Number}; !slices.Equal(got, []int{200, 201}) {
		t.Errorf("issue order starts %v, want the most recently updated first", got)
	}
	if want := []domain.RepoCount{{Repo: "arc42/q", PRs: 5, Issues: 6}}; !slices.Equal(q.Counts, want) {
		t.Errorf("counts = %+v, want %+v", q.Counts, want)
	}
}

// FR-1.8 AC1: one tile per spec in spec order, a spec with nothing open still gets its tile, the
// Other tile gathers several repositories, and an item of a repository no spec names is ignored.
func TestBuildSiteTilesKeepsSpecOrderAndEmptyTiles(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	items := []domain.Item{
		{Repo: "arc42/b", Kind: domain.KindIssue, Number: 1, CreatedAt: now, UpdatedAt: now},
		{Repo: "arc42/template", Kind: domain.KindPR, Number: 2, CreatedAt: now, UpdatedAt: now},
		{Repo: "gernotstarke/zorgscope", Kind: domain.KindIssue, Number: 3, CreatedAt: now, UpdatedAt: now},
		{Repo: "nobody/watches-this", Kind: domain.KindIssue, Number: 4, CreatedAt: now, UpdatedAt: now},
	}

	tiles := domain.BuildSiteTiles(domain.SiteTilesInput{
		Items: items, MaxPRs: 3, MaxIssues: 4,
		Sites: []domain.SiteSpec{
			{Name: "a", Repos: []string{"arc42/a"}},
			{Name: "b", Repos: []string{"arc42/b"}},
			{Name: "Other", Hue: "slate", Repos: []string{"arc42/template", "gernotstarke/zorgscope"}},
		},
	})

	var names []string
	total := 0
	for _, tl := range tiles {
		names = append(names, tl.Spec.Name)
		total += tl.PRTotal + tl.IssueTotal
	}
	if !slices.Equal(names, []string{"a", "b", "Other"}) {
		t.Fatalf("tile order = %v, want spec order", names)
	}
	if total != 3 {
		t.Errorf("tiles hold %d items, want 3: an item of a repository no spec names was counted", total)
	}
	a := tiles[0]
	if a.PRTotal != 0 || a.IssueTotal != 0 || len(a.PRs) != 0 || len(a.Issues) != 0 || a.More {
		t.Errorf("empty tile = %+v, want a tile with nothing in it", a)
	}
	if tiles[1].More {
		t.Error("More = true on a tile that cut nothing")
	}
	other := tiles[2]
	if other.PRTotal != 1 || other.IssueTotal != 1 {
		t.Errorf("Other = %d PRs, %d issues, want 1, 1", other.PRTotal, other.IssueTotal)
	}
	want := []domain.RepoCount{{Repo: "arc42/template", PRs: 1}, {Repo: "gernotstarke/zorgscope", Issues: 1}}
	if !slices.Equal(other.Counts, want) {
		t.Errorf("Other counts = %+v, want %+v", other.Counts, want)
	}
}

// The snapshot's item slice is shared by every concurrent page render, so building tiles must never
// reorder or modify it.
func TestBuildSiteTilesLeavesTheInputUntouched(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	items := []domain.Item{
		{Repo: "arc42/a", Kind: domain.KindIssue, Number: 1, UpdatedAt: now.Add(-time.Hour)},
		{Repo: "arc42/a", Kind: domain.KindIssue, Number: 2, UpdatedAt: now},
	}
	before := slices.Clone(items)

	domain.BuildSiteTiles(domain.SiteTilesInput{
		Items: items, MaxPRs: 3, MaxIssues: 1,
		Sites: []domain.SiteSpec{{Name: "a", Repos: []string{"arc42/a"}}},
	})

	if !reflect.DeepEqual(items, before) {
		t.Errorf("input = %+v, want it unchanged %+v", items, before)
	}
}
