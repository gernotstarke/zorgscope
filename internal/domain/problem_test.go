package domain

import (
	"strings"
	"testing"
	"time"
)

var problemNow = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

const problemStaleAfter = 45 * time.Minute

// findProblem returns the entry for one source, so a test can name the interface it is about
// instead of indexing into a list whose order is itself under test.
func findProblem(t *testing.T, ps []Problem, source string) Problem {
	t.Helper()
	for _, p := range ps {
		if p.Source == source {
			return p
		}
	}
	t.Fatalf("no problem entry for %q; got %d entries", source, len(ps))
	return Problem{}
}

// problemInput is a dashboard input with every source healthy and Slack configured, so each test
// below changes exactly the one thing it is about.
func problemInput() DashboardInput {
	states := make(map[string]SourceState, 4)
	for _, s := range []string{"github", "github-builds", "plausible", "todoist"} {
		states[s] = SourceState{Source: s, LastSuccessAt: problemNow.Add(-time.Minute)}
	}
	return DashboardInput{
		Now:               problemNow,
		StaleAfter:        problemStaleAfter,
		States:            states,
		LastRun:           RefreshRun{StartedAt: problemNow.Add(-2 * time.Minute), FinishedAt: problemNow.Add(-time.Minute), OK: true},
		LastSuccessfulRun: RefreshRun{FinishedAt: problemNow.Add(-time.Minute)},
	}
}

// FR-1.4: every external interface is reported, and a healthy deployment reports nothing wrong.
func TestAHealthyDashboardHasNothingWrong(t *testing.T) {
	d := BuildDashboard(problemInput())

	if d.ProblemCount != 0 {
		t.Errorf("ProblemCount = %d on a healthy dashboard, want 0", d.ProblemCount)
	}
	// Five interfaces: the four fetching sources and the notifier.
	if len(d.Problems) != 5 {
		t.Fatalf("got %d problem entries, want 5 (four sources and the notifier)", len(d.Problems))
	}
	for _, p := range d.Problems {
		if p.Severity != SeverityOK {
			t.Errorf("%s = %q, want %q on a healthy dashboard", p.Source, p.Severity, SeverityOK)
		}
		if p.Title == "" {
			t.Errorf("%s has no display title", p.Source)
		}
	}
}

// The states a fetching source can be reported in. Each row changes one source's health and
// asserts what the page then says about it.
func TestASourceIsReportedInTheStateItIsActuallyIn(t *testing.T) {
	const upstream = "502 Bad Gateway from api.github.com"

	tests := []struct {
		name         string
		state        SourceState
		disabled     []string
		wantSeverity Severity
		wantSummary  string
		wantDetail   string
		wantWrong    bool
	}{
		{
			name:         "healthy",
			state:        SourceState{LastSuccessAt: problemNow.Add(-time.Minute)},
			wantSeverity: SeverityOK,
			wantSummary:  summaryOK,
		},
		{
			name: "failing after having worked",
			state: SourceState{
				LastSuccessAt: problemNow.Add(-time.Hour),
				LastError:     upstream,
				LastErrorAt:   problemNow.Add(-time.Minute),
			},
			wantSeverity: SeverityError,
			wantSummary:  summaryFailing,
			wantDetail:   upstream,
			wantWrong:    true,
		},
		{
			name: "failing and never having worked",
			state: SourceState{
				LastError:   upstream,
				LastErrorAt: problemNow.Add(-time.Minute),
			},
			wantSeverity: SeverityError,
			wantSummary:  summaryNeverOK,
			wantDetail:   upstream,
			wantWrong:    true,
		},
		{
			name:         "stale",
			state:        SourceState{LastSuccessAt: problemNow.Add(-2 * problemStaleAfter)},
			wantSeverity: SeverityWarning,
			wantSummary:  summaryStale,
			wantWrong:    true,
		},
		{
			// An error older than the last success is not a current failure: the source
			// recovered, and reporting the stale error would keep a fixed problem on the page.
			name: "recovered since the last error",
			state: SourceState{
				LastSuccessAt: problemNow.Add(-time.Minute),
				LastError:     upstream,
				LastErrorAt:   problemNow.Add(-time.Hour),
			},
			wantSeverity: SeverityOK,
			wantSummary:  summaryOK,
		},
		{
			// FR-8.2 AC2: no credential is a decision, not a fault. It is checked before both
			// the failure and the staleness, so a source that never ran is never reported as
			// broken — that is what sends an operator hunting a fault that is a line of
			// configuration.
			name:         "not configured",
			state:        SourceState{LastError: upstream, LastErrorAt: problemNow},
			disabled:     []string{"github"},
			wantSeverity: SeverityOff,
			wantSummary:  summaryOff,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := problemInput()
			in.States["github"] = tc.state
			in.Disabled = tc.disabled

			p := findProblem(t, BuildDashboard(in).Problems, "github")
			if p.Severity != tc.wantSeverity {
				t.Errorf("severity = %q, want %q", p.Severity, tc.wantSeverity)
			}
			if p.Summary != tc.wantSummary {
				t.Errorf("summary = %q, want %q", p.Summary, tc.wantSummary)
			}
			if p.Detail != tc.wantDetail {
				t.Errorf("detail = %q, want %q", p.Detail, tc.wantDetail)
			}
			if p.Wrong() != tc.wantWrong {
				t.Errorf("Wrong() = %v, want %v", p.Wrong(), tc.wantWrong)
			}
		})
	}
}

// A failing source carries the times a reader needs to tell "broken since ten minutes" from
// "broken since March".
func TestAFailingSourceCarriesBothTimes(t *testing.T) {
	in := problemInput()
	in.States["todoist"] = SourceState{
		LastSuccessAt: problemNow.Add(-3 * time.Hour),
		LastError:     "401 from the Todoist API",
		LastErrorAt:   problemNow.Add(-5 * time.Minute),
	}

	p := findProblem(t, BuildDashboard(in).Problems, "todoist")
	if !p.At.Equal(problemNow.Add(-5 * time.Minute)) {
		t.Errorf("At = %v, want the time of the error", p.At)
	}
	if !p.LastOKAt.Equal(problemNow.Add(-3 * time.Hour)) {
		t.Errorf("LastOKAt = %v, want the time of the last success", p.LastOKAt)
	}
}

// FR-6.1 AC3 is why this row exists at all: a rejected announcement never fails the run, so the
// run record's detail is the only place it is written down. It is a warning and not an error,
// because the dashboard itself is fine — but it is not silence, which is what it was before.
func TestARejectedAnnouncementIsReportedAsAWarning(t *testing.T) {
	in := problemInput()
	in.LastRun.Detail = "github: 12" + DetailSeparator + NotifyDetailPrefix + "webhook returned 404"

	d := BuildDashboard(in)
	p := findProblem(t, d.Problems, "slack")
	if p.Severity != SeverityWarning {
		t.Errorf("severity = %q, want %q — a failed announcement does not fail the run (FR-6.1 AC3)",
			p.Severity, SeverityWarning)
	}
	if p.Summary != summaryNotifyFail {
		t.Errorf("summary = %q, want %q", p.Summary, summaryNotifyFail)
	}
	if p.Detail != "webhook returned 404" {
		t.Errorf("detail = %q, want the notifier's own message", p.Detail)
	}
	if d.ProblemCount != 1 {
		t.Errorf("ProblemCount = %d, want 1", d.ProblemCount)
	}
}

// Slack switched off is listed and never counted, exactly as an unconfigured source is.
func TestNotificationsSwitchedOffAreNotAProblem(t *testing.T) {
	in := problemInput()
	in.Disabled = []string{"slack"}
	in.LastRun.Detail = NotifyDetailPrefix + "webhook returned 404"

	d := BuildDashboard(in)
	p := findProblem(t, d.Problems, "slack")
	if p.Severity != SeverityOff {
		t.Errorf("severity = %q, want %q", p.Severity, SeverityOff)
	}
	if p.Detail != "" {
		t.Errorf("detail = %q; a notifier that is switched off reports no failure", p.Detail)
	}
	if d.ProblemCount != 0 {
		t.Errorf("ProblemCount = %d, want 0", d.ProblemCount)
	}
}

// The marker is searched for from the back, because everything before it is upstream text. A
// source error that happens to contain the marker's characters must not be able to cut the
// notifier's message short — or, worse, invent one.
func TestTheNotifyMarkerIsReadFromTheEnd(t *testing.T) {
	tests := []struct {
		name       string
		detail     string
		wantMsg    string
		wantFailed bool
	}{
		{name: "empty", detail: ""},
		{name: "no notify part", detail: "github: 12" + DetailSeparator + "todoist: 3"},
		{name: "notify alone", detail: NotifyDetailPrefix + "boom", wantMsg: "boom", wantFailed: true},
		{
			name:       "notify last",
			detail:     "github: 12" + DetailSeparator + NotifyDetailPrefix + "boom",
			wantMsg:    "boom",
			wantFailed: true,
		},
		{
			// An upstream error quoting the marker. The real one is still the last.
			name: "an upstream error impersonating the marker",
			detail: "github: fetch failed" + DetailSeparator + NotifyDetailPrefix + "not the real one" +
				DetailSeparator + NotifyDetailPrefix + "the real one",
			wantMsg:    "the real one",
			wantFailed: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg, failed := notifyFailure(tc.detail)
			if failed != tc.wantFailed {
				t.Fatalf("failed = %v, want %v", failed, tc.wantFailed)
			}
			if msg != tc.wantMsg {
				t.Errorf("message = %q, want %q", msg, tc.wantMsg)
			}
		})
	}
}

// Worst first, and within a severity the order the tiles themselves are in — so the details page
// and the dashboard read the same way round.
func TestProblemsAreOrderedWorstFirst(t *testing.T) {
	in := problemInput()
	in.States["plausible"] = SourceState{
		LastSuccessAt: problemNow.Add(-time.Hour),
		LastError:     "500",
		LastErrorAt:   problemNow,
	}
	in.States["github-builds"] = SourceState{LastSuccessAt: problemNow.Add(-2 * problemStaleAfter)}
	in.Disabled = []string{"todoist"}

	d := BuildDashboard(in)
	got := make([]string, 0, len(d.Problems))
	for _, p := range d.Problems {
		got = append(got, p.Source+"="+string(p.Severity))
	}
	want := "plausible=error github-builds=warning todoist=off github=ok slack=ok"
	if strings.Join(got, " ") != want {
		t.Errorf("order = %q,\n want %q", strings.Join(got, " "), want)
	}
	if d.ProblemCount != 2 {
		t.Errorf("ProblemCount = %d, want 2 — off and ok are not things that went wrong", d.ProblemCount)
	}
}

// An unknown severity sorts last rather than first: a value added without a rank must not be able
// to claim the top of the page, where the thing that is actually broken belongs.
func TestAnUnrankedSeverityDoesNotClaimTheTop(t *testing.T) {
	ps := []Problem{
		{Source: "unranked", Severity: Severity("something-new")},
		{Source: "broken", Severity: SeverityError},
	}
	sortProblems(ps)
	if ps[0].Source != "broken" {
		t.Errorf("first entry is %q, want the error", ps[0].Source)
	}
}
