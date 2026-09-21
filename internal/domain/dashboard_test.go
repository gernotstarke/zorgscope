package domain_test

import (
	"slices"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

func TestBuildDashboardGroupsByRepositoryInConfigurationOrder(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	items := []domain.Item{
		{Title: "b1", Repo: "arc42/b", Kind: domain.KindIssue, CreatedAt: now.Add(-5 * time.Hour), UpdatedAt: now.Add(-time.Hour)},
		{Title: "a1", Repo: "arc42/a", Kind: domain.KindPR, CreatedAt: now.Add(-9 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour)},
		{Title: "a2", Repo: "arc42/a", Kind: domain.KindIssue, CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute)},
		{Title: "orphan", Repo: "gone/repo", Kind: domain.KindIssue, CreatedAt: now.Add(-5 * time.Hour), UpdatedAt: now},
	}
	d := domain.BuildDashboard(domain.DashboardInput{
		Now: now, Items: items,
		Repos: []string{"arc42/a", "arc42/b"},
	})

	if got := repoNames(d.Groups); !slices.Equal(got, []string{"arc42/a", "arc42/b", "gone/repo"}) {
		t.Fatalf("group order = %v", got)
	}
	a := d.Groups[0]
	if a.Total != 2 || len(a.Items) != 2 {
		t.Fatalf("arc42/a: total %d shown %d", a.Total, len(a.Items))
	}
	if a.Items[0].Title != "a2" { // most recently updated first
		t.Fatalf("arc42/a first item = %s, want the most recently updated one", a.Items[0].Title)
	}
	if d.Total != 4 || d.Shown != 4 {
		t.Fatalf("Total %d Shown %d", d.Total, d.Shown)
	}
}

func TestBuildDashboardAppliesTheFilterButCountsTotalsUnfiltered(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	items := []domain.Item{
		{Title: "issue", Repo: "arc42/a", Kind: domain.KindIssue, CreatedAt: now, UpdatedAt: now},
		{Title: "pull", Repo: "arc42/a", Kind: domain.KindPR, CreatedAt: now, UpdatedAt: now},
		{Title: "pull", Repo: "arc42/b", Kind: domain.KindPR, CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now.Add(-3 * time.Hour)},
	}
	d := domain.BuildDashboard(domain.DashboardInput{
		Now: now, Items: items,
		Repos:  []string{"arc42/a", "arc42/b"},
		Filter: domain.Filter{Kind: domain.KindPR},
	})
	if d.Total != 3 || d.Shown != 2 {
		t.Fatalf("Total %d Shown %d", d.Total, d.Shown)
	}
	if len(d.Groups) != 2 || len(d.Groups[0].Items) != 1 || d.Groups[0].Total != 2 {
		t.Fatalf("groups = %+v", d.Groups)
	}
	if d.Filter.Kind != domain.KindPR {
		t.Fatal("the applied filter is echoed back")
	}
}

func TestBuildDashboardOmitsARepositoryWithNoMatch(t *testing.T) {
	now := time.Now()
	items := []domain.Item{
		{Title: "x", Repo: "arc42/a", Kind: domain.KindIssue, CreatedAt: now, UpdatedAt: now},
	}
	d := domain.BuildDashboard(domain.DashboardInput{Now: now, Items: items, Repos: []string{"arc42/a", "arc42/b"}, Filter: domain.Filter{Text: "nothing"}})
	if len(d.Groups) != 0 || d.Shown != 0 {
		t.Fatalf("groups = %+v", d.Groups)
	}
}

func TestBuildDashboardNeverMutatesItsInput(t *testing.T) {
	now := time.Now()
	items := []domain.Item{
		{Title: "old", Repo: "r", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)},
		{Title: "new", Repo: "r", CreatedAt: now, UpdatedAt: now},
	}
	_ = domain.BuildDashboard(domain.DashboardInput{Now: now, Items: items})
	if items[0].Title != "old" {
		t.Fatal("BuildDashboard sorted the caller's slice")
	}
}

// FR-1.10 AC3: labels are borrowed text the domain only carries, never inspects — BuildDashboard
// must hand them on unchanged, all the way to the grouped item the page renders, and SortItems
// (called on every group internally, and here again directly on a copy) must not drop them either.
func TestBuildDashboardCarriesLabelsThrough(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	items := []domain.Item{
		{Title: "labelled", Repo: "arc42/a", Kind: domain.KindIssue, Labels: []string{"bug", "Help Wanted"}, CreatedAt: now, UpdatedAt: now},
		{Title: "plain", Repo: "arc42/a", Kind: domain.KindIssue, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)},
	}
	d := domain.BuildDashboard(domain.DashboardInput{Now: now, Items: items, Repos: []string{"arc42/a"}})

	if len(d.Groups) != 1 || len(d.Groups[0].Items) != 2 {
		t.Fatalf("groups = %+v", d.Groups)
	}
	var got []string
	var found bool
	for _, it := range d.Groups[0].Items {
		if it.Title == "labelled" {
			got, found = it.Labels, true
		}
	}
	if !found {
		t.Fatal("the labelled item did not survive grouping")
	}
	if !slices.Equal(got, items[0].Labels) {
		t.Fatalf("grouped item's Labels = %v, want %v", got, items[0].Labels)
	}

	sorted := slices.Clone(items)
	domain.SortItems(sorted)
	for _, it := range sorted {
		if it.Title == "labelled" && !slices.Equal(it.Labels, items[0].Labels) {
			t.Errorf("SortItems dropped Labels: %v, want %v", it.Labels, items[0].Labels)
		}
	}
}

// FR-1.13 AC4: the security count is taken over every item, whatever the filter. A visitor
// filtered to one repository still needs to know that another has an open vulnerability — the
// principle FR-2.1 AC3 already applies to Total.
func TestDashboardCountsSecurityOverEverything(t *testing.T) {
	items := []domain.Item{
		{Repo: "a/one", Number: 1, Advisories: []string{"CVE-2026-1111"}},
		{Repo: "a/two", Number: 2, Labels: []string{"security"}},
		{Repo: "a/two", Number: 3, Author: "dependabot"},
		{Repo: "a/two", Number: 4},
	}
	d := domain.BuildDashboard(domain.DashboardInput{
		Now:    time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC),
		Items:  items,
		Repos:  []string{"a/one", "a/two"},
		Filter: domain.Filter{Repo: "a/two"},
	})
	if d.Security != 2 {
		t.Errorf("Security = %d, want 2: one of them is in a repository the filter excludes", d.Security)
	}
}

func repoNames(gs []domain.RepoGroup) []string {
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.Repo)
	}
	return out
}
