package domain_test

import (
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

func TestBuildDashboardCountsNewPerTileAndOverall(t *testing.T) {
	// FR-1.2 AC2/AC3
	visit := at("2026-08-17T10:00:00Z")
	now := at("2026-08-17T12:00:00Z")

	in := domain.DashboardInput{
		Now:         now,
		LastVisitAt: visit,
		Items: []domain.Item{
			{Source: "github", ExternalID: "gh-new", Kind: domain.KindIssue, FirstSeenAt: at("2026-08-17T11:00:00Z")},
			{Source: "github", ExternalID: "gh-old", Kind: domain.KindIssue, FirstSeenAt: at("2026-08-01T00:00:00Z")},
			{Source: "todoist", ExternalID: "td-new", Kind: domain.KindTask, FirstSeenAt: at("2026-08-17T11:30:00Z")},
			{Source: "todoist", ExternalID: "td-old", Kind: domain.KindTask, FirstSeenAt: at("2026-08-01T00:00:00Z")},
		},
	}

	d := domain.BuildDashboard(in)

	got := map[string]int{}
	for _, tile := range d.Tiles {
		got[tile.Name] = tile.NewCount
	}
	if got["github"] != 1 {
		t.Errorf("github tile NewCount = %d, want 1", got["github"])
	}
	if got["tasks"] != 1 {
		t.Errorf("tasks tile NewCount = %d, want 1", got["tasks"])
	}
	if got["builds"] != 0 || got["sites"] != 0 {
		t.Errorf("builds/sites NewCount = %d/%d, want 0/0 (they carry no Items)", got["builds"], got["sites"])
	}
	if d.NewTotal != 2 {
		t.Errorf("NewTotal = %d, want 2", d.NewTotal)
	}
}

func TestFailingSourceKeepsItsItemsAndShowsTheError(t *testing.T) {
	// FR-1.4 AC3: Tile.Items is non-empty and Tile.Error is set
	now := at("2026-08-17T12:00:00Z")
	success := at("2026-08-17T09:00:00Z")
	errAt := at("2026-08-17T11:00:00Z")

	in := domain.DashboardInput{
		Now:        now,
		StaleAfter: time.Hour,
		Items: []domain.Item{
			{Source: "github", ExternalID: "gh-1", Kind: domain.KindIssue, UpdatedAt: at("2026-08-16T00:00:00Z")},
		},
		States: map[string]domain.SourceState{
			"github": {Source: "github", LastSuccessAt: success, LastError: "rate limited", LastErrorAt: errAt},
		},
	}

	d := domain.BuildDashboard(in)

	tile := tileByName(t, d, "github")
	if len(tile.Items) == 0 {
		t.Fatal("github tile has no items, want the previous items to survive the failure")
	}
	if tile.Error != "rate limited" {
		t.Errorf("tile.Error = %q, want %q", tile.Error, "rate limited")
	}
}

func TestDisabledSourceIsMarkedNotFailing(t *testing.T) {
	// FR-8.2 AC2: Disabled == true, Error == ""
	now := at("2026-08-17T12:00:00Z")

	in := domain.DashboardInput{
		Now:      now,
		Disabled: []string{"todoist"},
		// Even a stale error on record must not surface once the source is disabled.
		States: map[string]domain.SourceState{
			"todoist": {Source: "todoist", LastError: "stale error from before the token was pulled", LastErrorAt: now},
		},
	}

	d := domain.BuildDashboard(in)

	tile := tileByName(t, d, "tasks")
	if !tile.Disabled {
		t.Error("tasks tile Disabled = false, want true")
	}
	if tile.Error != "" {
		t.Errorf("tile.Error = %q, want empty — a disabled source is not a failing one", tile.Error)
	}
	if tile.Stale {
		t.Error("tile.Stale = true, want false — staleness is meaningless for a source that never runs")
	}
}

func TestStaleIsDerivedFromLastSuccessAndStaleAfter(t *testing.T) {
	// FR-1.4 AC1
	now := at("2026-08-17T12:00:00Z")
	staleAfter := time.Hour

	tests := []struct {
		name          string
		lastSuccessAt time.Time
		want          bool
	}{
		{"succeeded within the window is fresh", at("2026-08-17T11:30:00Z"), false},
		{"succeeded before the window is stale", at("2026-08-17T10:00:00Z"), true},
		{"never succeeded is stale", time.Time{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := domain.DashboardInput{
				Now:        now,
				StaleAfter: staleAfter,
				States: map[string]domain.SourceState{
					"plausible": {Source: "plausible", LastSuccessAt: tc.lastSuccessAt},
				},
			}
			d := domain.BuildDashboard(in)
			tile := tileByName(t, d, "sites")
			if tile.Stale != tc.want {
				t.Errorf("Stale = %v, want %v", tile.Stale, tc.want)
			}
		})
	}
}

func TestSiteMetricsPairsTheTwoWindowsInConfigOrder(t *testing.T) {
	// FR-3.1 AC3
	now := at("2026-08-17T12:00:00Z")
	fetched := at("2026-08-17T09:00:00Z")

	// Interleaved on purpose: pairing by position would cross-wire siteB's week figures with
	// siteA's month figures. siteB's month window never arrived (a partial failure).
	in := domain.DashboardInput{
		Now: now,
		Metrics: []domain.Metric{
			{Site: "a.example", WindowDays: 7, Visitors: 100, FetchedAt: fetched},
			{Site: "b.example", WindowDays: 7, Visitors: 5, FetchedAt: fetched},
			{Site: "a.example", WindowDays: 30, Visitors: 400, FetchedAt: fetched},
		},
	}

	d := domain.BuildDashboard(in)
	tile := tileByName(t, d, "sites")

	if len(tile.Sites) != 2 {
		t.Fatalf("len(Sites) = %d, want 2", len(tile.Sites))
	}
	if tile.Sites[0].Site != "a.example" || tile.Sites[1].Site != "b.example" {
		t.Fatalf("site order = %v, want [a.example b.example] — configuration order (FR-3.1 AC3)",
			[]string{tile.Sites[0].Site, tile.Sites[1].Site})
	}

	a := tile.Sites[0]
	if a.Week.Visitors != 100 || a.Month.Visitors != 400 {
		t.Errorf("a.example week/month visitors = %d/%d, want 100/400", a.Week.Visitors, a.Month.Visitors)
	}

	b := tile.Sites[1]
	if b.Week.Visitors != 5 {
		t.Errorf("b.example week visitors = %d, want 5", b.Week.Visitors)
	}
	if b.Month.Site != "" {
		t.Errorf("b.example Month = %+v, want the zero Metric — its 30-day window never arrived", b.Month)
	}
}

func TestTasksTileSortsByDueDateNotLastUpdate(t *testing.T) {
	// Ruling 2: SortItems orders new-first then most-recently-updated, which would put an
	// overdue task that hasn't been touched in weeks below a task due tonight that was just
	// edited — the opposite of FR-4.1 AC2. This test fails if the tasks tile used SortItems.
	visit := at("2026-08-10T00:00:00Z")
	now := at("2026-08-17T12:00:00Z")

	in := domain.DashboardInput{
		Now:         now,
		LastVisitAt: visit,
		Items: []domain.Item{
			{
				Source: "todoist", ExternalID: "due-tonight", Kind: domain.KindTask,
				DueAt:     at("2026-08-17T20:00:00Z"),
				UpdatedAt: at("2026-08-17T11:00:00Z"), // edited moments ago
			},
			{
				Source: "todoist", ExternalID: "overdue-a-week", Kind: domain.KindTask,
				DueAt:     at("2026-08-10T09:00:00Z"),
				UpdatedAt: at("2026-08-10T09:00:00Z"), // untouched since it was created
			},
			// Same due date as each other: the tie-break must be deterministic, not a coin
			// flip, so the order cannot change between refreshes that touch neither task.
			{Source: "todoist", ExternalID: "tie-b", Kind: domain.KindTask, DueAt: at("2026-08-11T09:00:00Z")},
			{Source: "todoist", ExternalID: "tie-a", Kind: domain.KindTask, DueAt: at("2026-08-11T09:00:00Z")},
		},
	}

	d := domain.BuildDashboard(in)
	tile := tileByName(t, d, "tasks")

	if len(tile.Items) != 4 {
		t.Fatalf("len(Items) = %d, want 4", len(tile.Items))
	}
	if tile.Items[0].ExternalID != "overdue-a-week" {
		t.Errorf("Items[0] = %q, want %q — overdue must sort before due-today regardless of last update",
			tile.Items[0].ExternalID, "overdue-a-week")
	}
	if tile.Items[1].ExternalID != "tie-a" || tile.Items[2].ExternalID != "tie-b" {
		t.Errorf("tie-break order = [%s %s], want [tie-a tie-b] — equal due dates break on ExternalID",
			tile.Items[1].ExternalID, tile.Items[2].ExternalID)
	}
}

func TestTilesAppearEvenWhenEmpty(t *testing.T) {
	now := at("2026-08-17T12:00:00Z")
	in := domain.DashboardInput{Now: now}
	d := domain.BuildDashboard(in)
	if len(d.Tiles) != 4 {
		t.Fatalf("len(tiles) = %d, want 4 — an empty tile still has an empty state (FR-1.4)", len(d.Tiles))
	}
}

func tileByName(t *testing.T, d domain.Dashboard, name string) domain.Tile {
	t.Helper()
	for _, tile := range d.Tiles {
		if tile.Name == name {
			return tile
		}
	}
	t.Fatalf("no tile named %q", name)
	return domain.Tile{}
}
