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
		Now: now, LastVisitAt: now.Add(-2 * time.Hour), Items: items,
		Repos: []string{"arc42/a", "arc42/b"},
	})

	if got := repoNames(d.Groups); !slices.Equal(got, []string{"arc42/a", "arc42/b", "gone/repo"}) {
		t.Fatalf("group order = %v", got)
	}
	a := d.Groups[0]
	if a.Total != 2 || a.NewCount != 1 || len(a.Items) != 2 {
		t.Fatalf("arc42/a: total %d new %d shown %d", a.Total, a.NewCount, len(a.Items))
	}
	if a.Items[0].Title != "a2" { // new first, then most recently updated
		t.Fatalf("arc42/a first item = %s, want the new one", a.Items[0].Title)
	}
	if d.NewTotal != 1 || d.Total != 4 || d.Shown != 4 {
		t.Fatalf("NewTotal %d Total %d Shown %d", d.NewTotal, d.Total, d.Shown)
	}
}

func TestBuildDashboardAppliesTheFilterButCountsNewUnfiltered(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	items := []domain.Item{
		{Title: "issue", Repo: "arc42/a", Kind: domain.KindIssue, CreatedAt: now, UpdatedAt: now},
		{Title: "pull", Repo: "arc42/a", Kind: domain.KindPR, CreatedAt: now, UpdatedAt: now},
		{Title: "pull", Repo: "arc42/b", Kind: domain.KindPR, CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now.Add(-3 * time.Hour)},
	}
	d := domain.BuildDashboard(domain.DashboardInput{
		Now: now, LastVisitAt: now.Add(-time.Hour), Items: items,
		Repos:  []string{"arc42/a", "arc42/b"},
		Filter: domain.Filter{Kind: domain.KindPR},
	})
	if d.NewTotal != 2 {
		t.Fatalf("NewTotal = %d; the badge must not follow the filter", d.NewTotal)
	}
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
	_ = domain.BuildDashboard(domain.DashboardInput{Now: now, LastVisitAt: now.Add(-time.Minute), Items: items})
	if items[0].Title != "old" {
		t.Fatal("BuildDashboard sorted the caller's slice")
	}
}

// LastVisitAt is echoed back onto the assembled dashboard exactly as it came in: the page reads
// it straight from here to decide, per item, whether the NEW marker is shown.
func TestBuildDashboardEchoesLastVisitAt(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	visit := now.Add(-3 * time.Hour)
	d := domain.BuildDashboard(domain.DashboardInput{Now: now, LastVisitAt: visit})
	if !d.LastVisitAt.Equal(visit) {
		t.Fatalf("LastVisitAt = %v, want %v", d.LastVisitAt, visit)
	}
	if !d.GeneratedAt.Equal(now) {
		t.Fatalf("GeneratedAt = %v, want %v", d.GeneratedAt, now)
	}
}

func repoNames(gs []domain.RepoGroup) []string {
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.Repo)
	}
	return out
}
