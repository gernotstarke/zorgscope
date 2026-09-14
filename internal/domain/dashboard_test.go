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
		{Source: "github", ExternalID: "1", Repo: "arc42/b", Kind: domain.KindIssue, Title: "b1", CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now.Add(-time.Hour), FirstSeenAt: now.Add(-time.Hour)},
		{Source: "github", ExternalID: "2", Repo: "arc42/a", Kind: domain.KindPR, Title: "a1", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour), FirstSeenAt: now.Add(-5 * time.Hour)},
		{Source: "github", ExternalID: "3", Repo: "arc42/a", Kind: domain.KindIssue, Title: "a2", CreatedAt: now.Add(-9 * time.Hour), UpdatedAt: now.Add(-time.Minute), FirstSeenAt: now.Add(-time.Minute)},
		{Source: "github", ExternalID: "4", Repo: "gone/repo", Kind: domain.KindIssue, Title: "orphan", CreatedAt: now, UpdatedAt: now, FirstSeenAt: now.Add(-5 * time.Hour)},
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
	if a.Items[0].ExternalID != "3" { // new first, then most recently updated
		t.Fatalf("arc42/a first item = %s, want the new one", a.Items[0].ExternalID)
	}
	if d.NewTotal != 2 || d.Total != 4 || d.Shown != 4 {
		t.Fatalf("NewTotal %d Total %d Shown %d", d.NewTotal, d.Total, d.Shown)
	}
}

func TestBuildDashboardAppliesTheFilterButCountsNewUnfiltered(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	items := []domain.Item{
		{Source: "github", ExternalID: "1", Repo: "arc42/a", Kind: domain.KindIssue, Title: "issue", CreatedAt: now, UpdatedAt: now, FirstSeenAt: now},
		{Source: "github", ExternalID: "2", Repo: "arc42/a", Kind: domain.KindPR, Title: "pull", CreatedAt: now, UpdatedAt: now, FirstSeenAt: now},
		{Source: "github", ExternalID: "3", Repo: "arc42/b", Kind: domain.KindPR, Title: "pull", CreatedAt: now, UpdatedAt: now, FirstSeenAt: now.Add(-3 * time.Hour)},
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
		{Source: "github", ExternalID: "1", Repo: "arc42/a", Kind: domain.KindIssue, Title: "x", CreatedAt: now, UpdatedAt: now, FirstSeenAt: now},
	}
	d := domain.BuildDashboard(domain.DashboardInput{Now: now, Items: items, Repos: []string{"arc42/a", "arc42/b"}, Filter: domain.Filter{Text: "nothing"}})
	if len(d.Groups) != 0 || d.Shown != 0 {
		t.Fatalf("groups = %+v", d.Groups)
	}
}

func TestBuildDashboardReportsTheSourcesHealth(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	t.Run("disabled wins", func(t *testing.T) {
		d := domain.BuildDashboard(domain.DashboardInput{Now: now, Disabled: []string{"github"}, StaleAfter: time.Hour})
		if !d.Source.Disabled || d.Source.Stale || d.Source.Error != "" {
			t.Fatalf("Source = %+v", d.Source)
		}
	})
	// Switching a source off does not delete what it already fetched: the items stay on the page
	// and are exactly as old as the last success, so the time travels with the disabled state or
	// the page cannot say how old what it is showing is (FR-1.4 AC1). A state that has one is
	// stale by any measure, which is the case that proves disabled still wins.
	t.Run("disabled carries the last success", func(t *testing.T) {
		ok := now.Add(-5 * time.Hour)
		d := domain.BuildDashboard(domain.DashboardInput{
			Now: now, Disabled: []string{"github"}, StaleAfter: time.Hour,
			States: map[string]domain.SourceState{
				"github": {Source: "github", LastSuccessAt: ok, LastError: "boom", LastErrorAt: now},
			},
		})
		if !d.Source.Disabled || d.Source.Stale || d.Source.Error != "" {
			t.Fatalf("a disabled source is reported as stale or failing: %+v", d.Source)
		}
		if !d.Source.LastOKAt.Equal(ok) {
			t.Fatalf("LastOKAt = %v, want %v: a disabled source drops the age of its stored data",
				d.Source.LastOKAt, ok)
		}
	})
	// And a source that was switched off before it ever succeeded has no such time to carry.
	t.Run("disabled with no state has no last success", func(t *testing.T) {
		d := domain.BuildDashboard(domain.DashboardInput{Now: now, Disabled: []string{"github"}, StaleAfter: time.Hour})
		if !d.Source.LastOKAt.IsZero() {
			t.Fatalf("LastOKAt = %v, want the zero time", d.Source.LastOKAt)
		}
	})
	t.Run("failing carries the error and the last success", func(t *testing.T) {
		ok := now.Add(-30 * time.Minute)
		d := domain.BuildDashboard(domain.DashboardInput{Now: now, StaleAfter: time.Hour, States: map[string]domain.SourceState{
			"github": {Source: "github", LastSuccessAt: ok, LastError: "boom", LastErrorAt: now},
		}})
		if d.Source.Error != "boom" || !d.Source.LastOKAt.Equal(ok) || d.Source.Stale {
			t.Fatalf("Source = %+v", d.Source)
		}
	})
	t.Run("stale", func(t *testing.T) {
		d := domain.BuildDashboard(domain.DashboardInput{Now: now, StaleAfter: time.Hour, States: map[string]domain.SourceState{
			"github": {Source: "github", LastSuccessAt: now.Add(-2 * time.Hour)},
		}})
		if !d.Source.Stale || d.Source.Error != "" {
			t.Fatalf("Source = %+v", d.Source)
		}
	})
}

func TestBuildDashboardNeverMutatesItsInput(t *testing.T) {
	now := time.Now()
	items := []domain.Item{
		{Source: "github", ExternalID: "old", Repo: "r", UpdatedAt: now.Add(-time.Hour), FirstSeenAt: now.Add(-time.Hour)},
		{Source: "github", ExternalID: "new", Repo: "r", UpdatedAt: now, FirstSeenAt: now},
	}
	_ = domain.BuildDashboard(domain.DashboardInput{Now: now, LastVisitAt: now.Add(-time.Minute), Items: items})
	if items[0].ExternalID != "old" {
		t.Fatal("BuildDashboard sorted the caller's slice")
	}
}

func repoNames(gs []domain.RepoGroup) []string {
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.Repo)
	}
	return out
}

// FR-1.1 AC3. The header shows the time of the last *successful* refresh run, and the outcome
// travels with it so that a run which is open, or which failed, can never be presented as one.
func TestTheLastRunIsReportedWithItsOutcome(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	started := time.Date(2026, 8, 17, 11, 58, 0, 0, time.UTC)
	finished := time.Date(2026, 8, 17, 11, 59, 0, 0, time.UTC)

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
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	failed := domain.RefreshRun{ID: 9, StartedAt: time.Date(2026, 8, 17, 11, 58, 0, 0, time.UTC),
		FinishedAt: time.Date(2026, 8, 17, 11, 59, 0, 0, time.UTC)}
	succeeded := domain.RefreshRun{ID: 7, StartedAt: time.Date(2026, 8, 17, 9, 58, 0, 0, time.UTC),
		FinishedAt: time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC), OK: true}

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
	const detail = "github: 12; notify: post to slack: 404"
	d := domain.BuildDashboard(domain.DashboardInput{
		Now:     time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC),
		LastRun: domain.RefreshRun{ID: 4, StartedAt: time.Date(2026, 8, 17, 11, 58, 0, 0, time.UTC), FinishedAt: time.Date(2026, 8, 17, 11, 59, 0, 0, time.UTC), OK: true, Detail: detail},
	})
	if d.LastRunDetail != detail {
		t.Errorf("LastRunDetail = %q, want %q — the only signal a blocked webhook has", d.LastRunDetail, detail)
	}
}
