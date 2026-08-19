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
	if got["sites"] != 0 {
		t.Errorf("sites NewCount = %d, want 0 (it carries no Items)", got["sites"])
	}
	if _, ok := got["builds"]; ok {
		t.Error("builds are still a tile; they are one indicator and a page of their own (FR-2.3)")
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

// FR-4.1 AC2: "Overdue tasks are distinguished from those due today and listed first." That holds
// unconditionally, so a task first seen since the last visit — which carries a NEW badge — must
// still sort below a task that is overdue. On the tasks tile newness is a badge, not a sort key.
func TestTasksTileListsOverdueBeforeANewerTaskDueLater(t *testing.T) {
	visit := at("2026-08-16T00:00:00Z")
	now := at("2026-08-17T12:00:00Z")

	in := domain.DashboardInput{
		Now:         now,
		LastVisitAt: visit,
		Items: []domain.Item{
			{
				Source: "todoist", ExternalID: "new-due-tonight", Kind: domain.KindTask,
				DueAt: at("2026-08-17T20:00:00Z"),
				// First seen since the last visit, so this one is new.
				FirstSeenAt: at("2026-08-17T09:00:00Z"),
				UpdatedAt:   at("2026-08-17T09:00:00Z"),
			},
			{
				Source: "todoist", ExternalID: "overdue-a-week", Kind: domain.KindTask,
				DueAt: at("2026-08-10T09:00:00Z"),
				// Seen long before the last visit, so this one carries no badge.
				FirstSeenAt: at("2026-08-01T09:00:00Z"),
				UpdatedAt:   at("2026-08-10T09:00:00Z"),
			},
		},
	}

	tile := tileByName(t, domain.BuildDashboard(in), "tasks")
	if len(tile.Items) != 2 {
		t.Fatalf("len(Items) = %d, want 2", len(tile.Items))
	}
	if tile.Items[0].ExternalID != "overdue-a-week" {
		t.Errorf("Items[0] = %q, want %q — an overdue task is listed first even when a newer "+
			"task is due later today (FR-4.1 AC2)", tile.Items[0].ExternalID, "overdue-a-week")
	}
	// The badge survives the sort: newness is still reported, it just does not reorder the tile.
	if tile.NewCount != 1 {
		t.Errorf("NewCount = %d, want 1 — NEW is a badge on this tile, not a rank", tile.NewCount)
	}
	if !tile.Items[1].IsNew(visit) {
		t.Error("the task due tonight lost its new flag")
	}
}

func TestTilesAppearEvenWhenEmpty(t *testing.T) {
	now := at("2026-08-17T12:00:00Z")
	in := domain.DashboardInput{Now: now}
	d := domain.BuildDashboard(in)
	if len(d.Tiles) != 3 {
		t.Fatalf("len(tiles) = %d, want 3 — an empty tile still has an empty state (FR-1.4)", len(d.Tiles))
	}
	// Builds are not a tile, but they are still reported: an empty deployment has an indicator
	// saying so rather than no indicator at all.
	if d.Builds.Health != domain.BuildUnknown {
		t.Errorf("build health = %q, want %q with nothing configured", d.Builds.Health, domain.BuildUnknown)
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

// FR-1.1 AC3. The header shows the time of the last *successful* refresh run, and the outcome
// travels with it so that a run which is open, or which failed, can never be presented as one.
func TestTheLastRunIsReportedWithItsOutcome(t *testing.T) {
	now := at("2026-08-17T12:00:00Z")
	started := at("2026-08-17T11:58:00Z")
	finished := at("2026-08-17T11:59:00Z")

	tests := []struct {
		name    string
		run     domain.RefreshRun
		outcome domain.RunOutcome
		at      time.Time
	}{
		{
			name:    "never",
			run:     domain.RefreshRun{},
			outcome: domain.RunNever,
		},
		{
			// The case the 409 "a refresh is already running" page is always in.
			name:    "still running",
			run:     domain.RefreshRun{ID: 4, StartedAt: started},
			outcome: domain.RunRunning,
			at:      started,
		},
		{
			name:    "succeeded",
			run:     domain.RefreshRun{ID: 4, StartedAt: started, FinishedAt: finished, OK: true},
			outcome: domain.RunSucceeded,
			at:      finished,
		},
		{
			// Every upstream down: each tick opens a run, fails every source and closes it. The
			// header must not offer that as a refresh (FR-1.1 AC3 says "successful").
			name:    "failed",
			run:     domain.RefreshRun{ID: 4, StartedAt: started, FinishedAt: finished},
			outcome: domain.RunFailed,
			at:      finished,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := domain.BuildDashboard(domain.DashboardInput{Now: now, LastRun: tc.run})
			if d.LastRun != tc.outcome {
				t.Errorf("LastRun = %q, want %q", d.LastRun, tc.outcome)
			}
			if !d.LastRunAt.Equal(tc.at) {
				t.Errorf("LastRunAt = %v, want %v", d.LastRunAt, tc.at)
			}
		})
	}
}

// FR-1.1 AC3: the time the header states is the last *successful* run's, which is a different run
// from the last one as soon as the latest attempt failed or is still open. The two are reported
// side by side and neither is derived from the other — the failure keeps its own time, and the
// success is taken only from the run the store reported as successful.
func TestTheLastSuccessfulRunIsReportedBesideAFailedLatestRun(t *testing.T) {
	now := at("2026-08-17T12:00:00Z")
	failed := domain.RefreshRun{ID: 9, StartedAt: at("2026-08-17T11:58:00Z"),
		FinishedAt: at("2026-08-17T11:59:00Z")}
	succeeded := domain.RefreshRun{ID: 7, StartedAt: at("2026-08-17T09:58:00Z"),
		FinishedAt: at("2026-08-17T10:00:00Z"), OK: true}

	d := domain.BuildDashboard(domain.DashboardInput{
		Now: now, LastRun: failed, LastSuccessfulRun: succeeded,
	})
	if d.LastRun != domain.RunFailed || !d.LastRunAt.Equal(failed.FinishedAt) {
		t.Errorf("last run = %q at %v, want %q at %v — a failed run keeps its own time",
			d.LastRun, d.LastRunAt, domain.RunFailed, failed.FinishedAt)
	}
	if !d.LastSuccessAt.Equal(succeeded.FinishedAt) {
		t.Errorf("LastSuccessAt = %v, want %v — the header cannot state the time of the last "+
			"successful refresh if the dashboard does not carry it (FR-1.1 AC3)",
			d.LastSuccessAt, succeeded.FinishedAt)
	}

	// And nothing invents one: a store that has never completed a good run reports none.
	none := domain.BuildDashboard(domain.DashboardInput{Now: now, LastRun: failed})
	if !none.LastSuccessAt.IsZero() {
		t.Errorf("LastSuccessAt = %v with no successful run on record, want the zero time: the "+
			"failed run must never be presented as the successful one", none.LastSuccessAt)
	}
}

// FR-6.1 AC3 has the announcement step record its failure without failing the run, and the run
// record's detail is where it is recorded. It reaches the dashboard or it reaches nobody: a
// revoked Slack webhook changes nothing else on the page.
func TestTheRunDetailReachesTheDashboard(t *testing.T) {
	const detail = "github: 12; todoist: 4; notify: post to slack: 404"
	d := domain.BuildDashboard(domain.DashboardInput{
		Now:     at("2026-08-17T12:00:00Z"),
		LastRun: domain.RefreshRun{ID: 4, StartedAt: at("2026-08-17T11:58:00Z"), FinishedAt: at("2026-08-17T11:59:00Z"), OK: true, Detail: detail},
	})
	if d.LastRunDetail != detail {
		t.Errorf("LastRunDetail = %q, want %q — the only signal a blocked webhook has", d.LastRunDetail, detail)
	}
}
