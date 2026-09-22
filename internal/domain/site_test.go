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

// FR-1.13: the Security tile gathers what is marked, wherever it is open, so that the one loud
// thing is not spread across ten tiles. Security comes before Dependency inside each kind, and
// only then the most recently updated first.
func TestBuildTierTileOrdersSecurityFirstAndCountsBeforeTheCut(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	items := []domain.Item{
		{Repo: "arc42/a", Kind: domain.KindPR, Number: 1, Author: "dependabot", UpdatedAt: now},
		{Repo: "arc42/b", Kind: domain.KindPR, Number: 2, Advisories: []string{"GHSA-aaaa-bbbb-cccc"}, UpdatedAt: now.Add(-3 * time.Hour)},
		{Repo: "arc42/a", Kind: domain.KindPR, Number: 3, Author: "renovate", UpdatedAt: now.Add(-time.Hour)},
		{Repo: "arc42/b", Kind: domain.KindPR, Number: 4, Labels: []string{"Security"}, UpdatedAt: now.Add(-2 * time.Hour)},
		{Repo: "arc42/a", Kind: domain.KindPR, Number: 5, Author: "copilot-swe-agent", UpdatedAt: now},
		{Repo: "arc42/b", Kind: domain.KindIssue, Number: 6, Labels: []string{"dependencies"}, UpdatedAt: now},
		{Repo: "arc42/a", Kind: domain.KindIssue, Number: 7, Title: "Fix the header", UpdatedAt: now},
	}

	tile := domain.BuildTierTile(domain.TierTileInput{Items: items, MaxPRs: 3, MaxIssues: 4})

	var numbers []int
	for _, it := range tile.PRs {
		numbers = append(numbers, it.Number)
	}
	// #4 and #2 are Security, most recently updated first; #1 is the loudest of the rest.
	if !slices.Equal(numbers, []int{4, 2, 1}) {
		t.Errorf("PR order = %v, want [4 2 1]", numbers)
	}
	if tile.PRTotal != 4 || tile.IssueTotal != 1 {
		t.Errorf("totals = %d PRs, %d issues, want 4 and 1", tile.PRTotal, tile.IssueTotal)
	}
	if tile.Security != 2 || tile.Dependency != 3 {
		t.Errorf("counts = %d security, %d dependency, want 2 and 3", tile.Security, tile.Dependency)
	}
	if !tile.More {
		t.Error("More = false, want true: the fourth marked pull request was cut")
	}
}

// Nothing marked is the ordinary state (ADR-0014): the tile is still built, empty, because a tile
// that vanished would read as a check that had stopped running.
func TestBuildTierTileIsEmptyWhenNothingIsMarked(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	items := []domain.Item{
		{Repo: "arc42/a", Kind: domain.KindPR, Number: 1, Author: "github-actions", UpdatedAt: now},
		{Repo: "arc42/a", Kind: domain.KindIssue, Number: 2, Title: "Fix the header", UpdatedAt: now},
	}

	tile := domain.BuildTierTile(domain.TierTileInput{Items: items, MaxPRs: 3, MaxIssues: 4})

	if len(tile.PRs) != 0 || len(tile.Issues) != 0 || tile.Security != 0 || tile.Dependency != 0 {
		t.Errorf("tile = %+v, want nothing marked", tile)
	}
	if tile.More {
		t.Error("More = true, want false: an empty tile has nothing to link on to")
	}
}

// The tile must never reorder the caller's slice: the same snapshot is shared with every other
// render, and the list's own order is its own business.
func TestBuildTierTileLeavesTheCallersItemsAlone(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	items := []domain.Item{
		{Repo: "arc42/a", Kind: domain.KindPR, Number: 1, Author: "dependabot", UpdatedAt: now.Add(-time.Hour)},
		{Repo: "arc42/a", Kind: domain.KindPR, Number: 2, Advisories: []string{"CVE-2026-1"}, UpdatedAt: now.Add(-2 * time.Hour)},
	}

	domain.BuildTierTile(domain.TierTileInput{Items: items, MaxPRs: 3, MaxIssues: 4})

	if items[0].Number != 1 || items[1].Number != 2 {
		t.Fatalf("the caller's items were reordered: %d, %d", items[0].Number, items[1].Number)
	}
}
