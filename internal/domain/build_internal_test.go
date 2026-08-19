// The build rules are tested from inside the package: which GitHub conclusions count as broken is
// this package's decision, and buildStatus is not reachable from outside it.
package domain

import (
	"testing"
	"time"
)

var buildNow = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

// Which conclusions mean what. The line between broken and warning is the one that matters: a
// workflow somebody cancelled by hand must not light the front page red.
func TestBuildHealthByConclusion(t *testing.T) {
	tests := []struct {
		conclusion string
		want       BuildHealth
	}{
		{"success", BuildOK},
		{"failure", BuildBroken},
		{"timed_out", BuildBroken},
		{"startup_failure", BuildBroken},
		{"cancelled", BuildWarning},
		{"neutral", BuildWarning},
		{"skipped", BuildWarning},
		{"action_required", BuildWarning},
		{"stale", BuildWarning},
		{"", BuildWarning},
		{"something GitHub added since", BuildWarning},
	}
	for _, tc := range tests {
		t.Run(tc.conclusion, func(t *testing.T) {
			if got := (Build{Conclusion: tc.conclusion}).Health(); got != tc.want {
				t.Errorf("Health() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A run in progress says nothing about the repository's state. Treating it as a state of its own
// would make the indicator change colour every time CI starts, and would let a repository whose
// last completed run failed read as something other than broken.
func TestARunInProgressDoesNotChangeTheHealth(t *testing.T) {
	tests := []struct {
		name, conclusion string
		want             BuildHealth
	}{
		{"running over a failure", "failure", BuildBroken},
		{"running over a success", "success", BuildOK},
		{"running with nothing before it", "", BuildWarning},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := Build{Status: "in_progress", Conclusion: tc.conclusion}
			if !b.Running() {
				t.Fatal("Running() = false for an in-progress run")
			}
			if got := b.Health(); got != tc.want {
				t.Errorf("Health() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Silent is "nothing is known", which is not the same as "nothing completed".
func TestSilentIsOnlyWhenNothingAtAllIsKnown(t *testing.T) {
	if !(Build{Repo: "org/repo"}).Silent() {
		t.Error("a repository with no run at all is not reported as silent")
	}
	for _, b := range []Build{
		{Repo: "org/repo", Status: "in_progress"},
		{Repo: "org/repo", Conclusion: "success"},
		{Repo: "org/repo", RunURL: "https://github.com/org/repo/actions/runs/1"},
		{Repo: "org/repo", FinishedAt: buildNow},
	} {
		if b.Silent() {
			t.Errorf("%+v is reported as silent although something is known about it", b)
		}
	}
}

// buildStatusInput is a dashboard input with the build source healthy, so each test below changes
// only the thing it is about.
func buildStatusInput(repos []string, builds ...Build) DashboardInput {
	return DashboardInput{
		Now:        buildNow,
		StaleAfter: time.Hour,
		Repos:      repos,
		Builds:     builds,
		States: map[string]SourceState{
			"github-builds": {Source: "github-builds", LastSuccessAt: buildNow.Add(-time.Minute)},
		},
	}
}

// The indicator takes the worst state any repository is in — the whole point of one colour
// standing in for a list.
func TestTheIndicatorTakesTheWorstState(t *testing.T) {
	green := Build{Repo: "org/a", Conclusion: "success", FetchedAt: buildNow}
	amber := Build{Repo: "org/b", Conclusion: "cancelled", FetchedAt: buildNow}
	red := Build{Repo: "org/c", Conclusion: "failure", FetchedAt: buildNow}

	tests := []struct {
		name   string
		repos  []string
		builds []Build
		want   BuildHealth
	}{
		{"all green", []string{"org/a"}, []Build{green}, BuildOK},
		{"one warning", []string{"org/a", "org/b"}, []Build{green, amber}, BuildWarning},
		{"one broken", []string{"org/a", "org/b", "org/c"}, []Build{green, amber, red}, BuildBroken},
		{"broken outranks warning", []string{"org/b", "org/c"}, []Build{amber, red}, BuildBroken},
		{"nothing watched", nil, nil, BuildUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildDashboard(buildStatusInput(tc.repos, tc.builds...)).Builds
			if got.Health != tc.want {
				t.Errorf("health = %q, want %q", got.Health, tc.want)
			}
		})
	}
}

// Running is counted, and counted across the other three rather than as one of them: a green
// repository with a run in progress is still green and is still one of the running ones.
func TestRunsInProgressAreCountedSeparately(t *testing.T) {
	in := buildStatusInput(
		[]string{"org/a", "org/b"},
		Build{Repo: "org/a", Status: "in_progress", Conclusion: "success", FetchedAt: buildNow},
		Build{Repo: "org/b", Status: "completed", Conclusion: "success", FetchedAt: buildNow},
	)
	got := BuildDashboard(in).Builds

	if got.Running != 1 {
		t.Errorf("Running = %d, want 1", got.Running)
	}
	if got.OK != 2 || got.Health != BuildOK {
		t.Errorf("ok = %d, health = %q; a run in progress over a success is still green",
			got.OK, got.Health)
	}
}

// A configured repository that has never produced a run is a row, and a warning. Only
// repositories with a run are stored, so without this the indicator would be green while three
// workflows sat silent.
func TestAConfiguredRepositoryWithNoRunIsReported(t *testing.T) {
	in := buildStatusInput(
		[]string{"org/green", "org/silent"},
		Build{Repo: "org/green", Conclusion: "success", FetchedAt: buildNow},
	)
	got := BuildDashboard(in).Builds

	if got.Total() != 2 {
		t.Fatalf("Total = %d, want 2 — the silent repository has no row", got.Total())
	}
	if got.Health != BuildWarning {
		t.Errorf("health = %q, want %q: a repository that never reported is not green",
			got.Health, BuildWarning)
	}
	if got.Warning != 1 || got.OK != 1 {
		t.Errorf("warning/ok = %d/%d, want 1/1", got.Warning, got.OK)
	}
	if !got.Rows[0].Silent() {
		t.Error("the silent repository is not first; it is the one that needs looking at")
	}
}

// A repository dropped from the configuration whose rows are still stored is listed after the
// configured ones rather than hidden: a row that is there says something.
func TestAnUnconfiguredRepositoryIsStillListed(t *testing.T) {
	in := buildStatusInput(
		[]string{"org/kept"},
		Build{Repo: "org/kept", Conclusion: "success", FetchedAt: buildNow},
		Build{Repo: "org/dropped", Conclusion: "success", FetchedAt: buildNow},
	)
	got := BuildDashboard(in).Builds

	if got.Total() != 2 {
		t.Fatalf("Total = %d, want 2", got.Total())
	}
	var found bool
	for _, r := range got.Rows {
		if r.Repo == "org/dropped" {
			found = true
		}
	}
	if !found {
		t.Error("a stored repository that configuration no longer names vanished from the page")
	}
}

// A repository named twice in configuration gets one row, not two.
func TestADuplicateRepositoryIsListedOnce(t *testing.T) {
	in := buildStatusInput([]string{"org/a", "org/a"})
	if got := BuildDashboard(in).Builds.Total(); got != 1 {
		t.Errorf("Total = %d, want 1", got)
	}
}

// FR-8.2 AC2: no credential is a decision, not a fault. Disabled is checked before everything
// else, so a stored failure from before the credential went away cannot light the indicator red.
func TestADisabledBuildSourceIsUnknownRatherThanBroken(t *testing.T) {
	in := buildStatusInput([]string{"org/c"}, Build{Repo: "org/c", Conclusion: "failure", FetchedAt: buildNow})
	in.Disabled = []string{"github-builds"}

	got := BuildDashboard(in).Builds
	if !got.Disabled {
		t.Error("the status does not report the source as disabled")
	}
	if got.Health != BuildUnknown {
		t.Errorf("health = %q, want %q", got.Health, BuildUnknown)
	}
	// The rows are still there — the last known state is still worth reading.
	if got.Total() != 1 {
		t.Errorf("Total = %d, want 1", got.Total())
	}
}

// The source's own failure is carried through, which is a different question from any
// repository's: it means every row is as old as the last fetch that worked.
func TestTheBuildSourcesOwnFailureIsCarried(t *testing.T) {
	in := buildStatusInput([]string{"org/a"}, Build{Repo: "org/a", Conclusion: "success", FetchedAt: buildNow})
	in.States["github-builds"] = SourceState{
		Source:        "github-builds",
		LastSuccessAt: buildNow.Add(-3 * time.Hour),
		LastError:     "403 rate limited",
		LastErrorAt:   buildNow.Add(-time.Minute),
	}

	got := BuildDashboard(in).Builds
	if got.Error != "403 rate limited" {
		t.Errorf("Error = %q, want the source's own failure", got.Error)
	}
	if !got.Stale {
		t.Error("a source whose last success is three hours old is not reported as stale")
	}
}

// Worst first, then by name, so two renders of the same state list them identically.
func TestBuildRowsAreOrderedWorstFirstThenByName(t *testing.T) {
	in := buildStatusInput(
		[]string{"org/zebra", "org/apple", "org/broken", "org/amber"},
		Build{Repo: "org/zebra", Conclusion: "success", FetchedAt: buildNow},
		Build{Repo: "org/apple", Conclusion: "success", FetchedAt: buildNow},
		Build{Repo: "org/broken", Conclusion: "failure", FetchedAt: buildNow},
		Build{Repo: "org/amber", Conclusion: "cancelled", FetchedAt: buildNow},
	)

	var got []string
	for _, r := range BuildDashboard(in).Builds.Rows {
		got = append(got, r.Repo)
	}
	want := []string{"org/broken", "org/amber", "org/apple", "org/zebra"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// An unranked health sorts last rather than first: a state added without a rank must not take the
// top of the page from something that is actually broken.
func TestAnUnrankedBuildHealthDoesNotClaimTheTop(t *testing.T) {
	if buildRankOf(BuildHealth("something-new")) <= buildRankOf(BuildBroken) {
		t.Error("an unranked build health outranks a broken one")
	}
}
