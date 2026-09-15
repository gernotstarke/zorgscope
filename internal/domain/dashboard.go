package domain

import "time"

// sourceOrder is every source the dashboard depends on, builds included, in the order they are
// reported in. It is what the problems page walks: an interface that stopped feeding the list did
// not stop being an interface that can fail.
var sourceOrder = []string{"github", "builds"}

// githubSource is the fetcher name behind every item on the page.
const githubSource = "github"

// SourceHealth is the GitHub source's own health, the same three facts reported for builds
// (FR-8.2 AC2): whether it is switched off, whether its last success is too old to trust, and
// its current error if it has one.
type SourceHealth struct {
	Disabled bool
	Stale    bool
	Error    string
	LastOKAt time.Time
}

// RepoGroup is one repository's row of the dashboard's list: its filtered items, alongside the
// unfiltered counts a filter must never be allowed to change (FR-1.2).
type RepoGroup struct {
	Repo  string
	Items []Item
	// NewCount and Total are taken over every item this repository holds, before the filter is
	// applied — see BuildDashboard's own comment on why the badge cannot follow the filter.
	NewCount int
	Total    int
}

// DashboardInput is everything the store holds that BuildDashboard needs: the current time, the
// visitor's last-seen watermark, the outcome of the last refresh run and of the last one that
// succeeded, every stored item, per-source health, and the list of sources disabled for lack of a
// credential.
type DashboardInput struct {
	Now         time.Time
	LastVisitAt time.Time
	LastRun     RefreshRun
	// LastSuccessfulRun is the most recent run that finished successfully, which is the run
	// FR-1.1 AC3 names. It is a second field rather than a filter over LastRun because the two
	// are different runs whenever the latest attempt failed or is still open — and those are
	// exactly the moments the header has to reach past the latest run to stay both honest and
	// useful. It is the zero RefreshRun when no run has ever succeeded.
	LastSuccessfulRun RefreshRun
	StaleAfter        time.Duration
	Items             []Item
	Builds            []Build
	States            map[string]SourceState
	Disabled          []string // sources without a credential (FR-8.2 AC2)
	// Repos is the configured repository list, in configuration order. It is what makes the
	// build status — and the list's own grouping — able to report a repository that has never
	// produced an item: without this a silent repository is indistinguishable from one nobody is
	// watching.
	Repos []string
	// Filter narrows which items each group shows. It never changes NewCount or Total: those
	// answer "what is out there", and the filter answers "what am I looking at right now".
	Filter Filter
}

// RunOutcome is what may honestly be said about the last refresh run (FR-1.1 AC3). It exists
// because one timestamp cannot carry the answer: a zero time means both "never" and "still
// running", and a non-zero one means both "succeeded then" and "failed then".
type RunOutcome string

// The four states a header line can be in.
const (
	// RunNever: nothing has ever been recorded.
	RunNever RunOutcome = "never"
	// RunRunning: a run is open right now. Its time is when it started.
	RunRunning RunOutcome = "running"
	// RunSucceeded: the last run finished with every source stored. Its time is the one FR-1.1
	// AC3 asks for — the time of the last successful refresh run.
	RunSucceeded RunOutcome = "succeeded"
	// RunFailed: the last run finished with at least one source failing. Its time is when that
	// failure happened, and it is deliberately not offered as a successful refresh: FR-1.1 AC3
	// names the successful run, so a failed one may say when it failed and nothing more.
	RunFailed RunOutcome = "failed"
)

// Dashboard is the fully assembled page: one filtered list, grouped by repository, plus the
// header's counts and timestamps (FR-1.1, FR-1.2).
type Dashboard struct {
	GeneratedAt time.Time
	LastVisitAt time.Time
	// LastRun says what the last refresh run did, and LastRunAt when: the finishing time of a run
	// that is over, the starting time of one still running, zero when there has never been one.
	// The two are read together — a time without its outcome is what made the header claim a
	// failed run as a refresh and an open one as no refresh at all.
	LastRun   RunOutcome
	LastRunAt time.Time
	// LastSuccessAt is when the last *successful* run finished, zero when none ever has. It is
	// what FR-1.1 AC3 asks the header for, and it is a field of its own because LastRunAt cannot
	// answer it: when the latest run failed or is still in flight, LastRunAt belongs to that run
	// and the last success lies further back. The header states both — what the latest attempt
	// did, and when the data on the page was last actually refreshed — so that a failing upstream
	// makes the page say so without also making it look like it has never worked.
	LastSuccessAt time.Time
	// LastRunDetail is the run record's per-source outcome, plus the reason the announcement step
	// stopped if it did. It is the only signal a permanently blocked Slack webhook has: such a
	// failure never fails a run (FR-6.1 AC3), so nothing else on the page changes when the hook is
	// revoked. It carries upstream error text and must be scrubbed before it is rendered (QS-4.3).
	LastRunDetail string
	// NewTotal, Total and Shown are counted at three different points: NewTotal and Total over
	// every item regardless of the filter, Shown over what the filter actually let through. A
	// filter that also moved the badge or the tab title would make the dashboard lie about what
	// is new the moment somebody typed into the search box.
	NewTotal, Total, Shown int
	// Filter is the filter that was applied, echoed back so the page can render it as the
	// visitor left it.
	Filter Filter
	Groups []RepoGroup
	Source SourceHealth
	// Builds is the front page's build indicator and the rows behind it (FR-2.3).
	Builds BuildStatus
	// Problems is the health of every external interface, worst first (FR-1.4). Every configured
	// interface is listed, healthy ones included, so that the details page can answer "is
	// anything wrong?" positively rather than with an empty list.
	Problems []Problem
	// ProblemCount is how many of them are actually wrong — errors and warnings, never an
	// interface that is merely switched off. It is what decides whether the dashboard shows its
	// warning box at all, so a page with nothing configured stays quiet.
	ProblemCount int
}

// BuildDashboard assembles the dashboard from everything the store holds. It is a pure function:
// no I/O and no clock of its own — the caller supplies Now — and it never mutates the slices in
// in. Items are always copied into a fresh slice before any sorting, so the caller's Items slice
// is untouched; Builds is never sorted in place and is shared with the caller directly.
func BuildDashboard(in DashboardInput) Dashboard {
	disabled := make(map[string]bool, len(in.Disabled))
	for _, s := range in.Disabled {
		disabled[s] = true
	}
	outcome, at := lastRun(in.LastRun)
	d := Dashboard{
		GeneratedAt:   in.Now,
		LastVisitAt:   in.LastVisitAt,
		LastRun:       outcome,
		LastRunAt:     at,
		LastRunDetail: in.LastRun.Detail,
		// Taken straight from the successful run's own finishing time, with no fallback to
		// LastRun: a run the store did not report as successful must never end up under the words
		// "last successful refresh", which is the whole of FR-1.1 AC3's honesty.
		LastSuccessAt: in.LastSuccessfulRun.FinishedAt,
		Filter:        in.Filter,
	}

	items := itemsBySource(in.Items, githubSource)
	d.Total = len(items)
	// Counted before filtering: the tab title and the summary say what is new, not what is
	// visible, and a filter must never make the badge lie.
	d.NewTotal = CountNew(items, in.LastVisitAt)
	d.Groups = groupByRepo(items, in.Repos, in.Filter, in.LastVisitAt)
	for _, g := range d.Groups {
		d.Shown += len(g.Items)
	}

	d.Source = sourceHealth(in, disabled)
	d.Builds = buildStatus(in, disabled)
	d.Problems = buildProblems(in, disabled)
	for _, p := range d.Problems {
		if p.Wrong() {
			d.ProblemCount++
		}
	}
	return d
}

// groupByRepo assembles one group per repository that has at least one item passing f, in the
// order the repositories are configured; repositories that still hold items but are no longer
// configured follow, in first-seen order. Total and NewCount are per repository before filtering.
func groupByRepo(items []Item, repos []string, f Filter, lastVisit time.Time) []RepoGroup {
	order := append([]string(nil), repos...)
	known := make(map[string]bool, len(repos))
	for _, r := range repos {
		known[r] = true
	}
	byRepo := make(map[string]*RepoGroup)
	for _, it := range items {
		g, ok := byRepo[it.Repo]
		if !ok {
			g = &RepoGroup{Repo: it.Repo}
			byRepo[it.Repo] = g
			if !known[it.Repo] {
				known[it.Repo] = true
				order = append(order, it.Repo)
			}
		}
		g.Total++
		if it.IsNew(lastVisit) {
			g.NewCount++
		}
		if f.Match(it) {
			g.Items = append(g.Items, it)
		}
	}
	out := make([]RepoGroup, 0, len(byRepo))
	for _, repo := range order {
		g, ok := byRepo[repo]
		if !ok || len(g.Items) == 0 {
			continue
		}
		SortItems(g.Items, lastVisit)
		out = append(out, *g)
	}
	return out
}

// sourceHealth reduces the GitHub source's state to what the page says above the list. Disabled
// is checked first (FR-8.2 AC2): a source that never runs is neither stale nor failing.
//
// A disabled source still carries its last success, because switching a source off does not
// delete what it already fetched: the items stay on the page and are exactly as old as that
// timestamp, so the page has to be able to say so (FR-1.4 AC1). Reporting the state as disabled
// while withholding the time left the list showing hour-old data under a sentence that could only
// claim nothing had ever been fetched.
func sourceHealth(in DashboardInput, disabled map[string]bool) SourceHealth {
	state := in.States[githubSource]
	if disabled[githubSource] {
		return SourceHealth{Disabled: true, LastOKAt: state.LastSuccessAt}
	}
	h := SourceHealth{Stale: state.Stale(in.Now, in.StaleAfter), LastOKAt: state.LastSuccessAt}
	if state.Failing() {
		h.Error = state.LastError
	}
	return h
}

// lastRun reduces the last refresh run to what the header may say about it, and when.
//
// The store hands back the most recently *started* run, which is three different situations
// wearing one shape, and the header used to read a single field of it — FinishedAt — as though it
// were one:
//
//   - A run that is still open has no finishing time. Reading FinishedAt made an in-flight refresh
//     indistinguishable from a database that has never refreshed at all, so the header announced
//     "no refresh has run yet" during every slow run, and always on the 409 page that exists to
//     say a refresh is already running.
//   - A run that failed has a finishing time like any other. Reading FinishedAt made a run in
//     which every upstream was down read as a refresh — while FR-1.1 AC3 asks for the last
//     *successful* run, and OK, stored and read back on every run, was consulted nowhere.
//
// What this deliberately does not do is reach back for the last successful run when the most
// recent one failed. It answers one question — what became of the latest attempt — and a failed
// attempt is reported as failed, at its own time; showing a failed run's timestamp under the words
// "last refresh" is the reading that is simply wrong, and it is the one that was there.
//
// The last *successful* run FR-1.1 AC3 asks for is a second, independent reading of the store
// (DashboardInput.LastSuccessfulRun, Dashboard.LastSuccessAt) precisely so that neither answer has
// to be bent into the other: the header can say that the latest refresh failed *and* when the data
// it is showing was last refreshed for real.
func lastRun(run RefreshRun) (RunOutcome, time.Time) {
	switch {
	case run.StartedAt.IsZero() && run.FinishedAt.IsZero():
		return RunNever, time.Time{}
	case run.Running():
		return RunRunning, run.StartedAt
	case run.OK:
		return RunSucceeded, run.FinishedAt
	default:
		return RunFailed, run.FinishedAt
	}
}

// itemsBySource returns the items belonging to source, in a freshly allocated slice. Because the
// result never shares a backing array with items, sorting it — or a per-group slice appended from
// it — in place cannot mutate the caller's slice.
func itemsBySource(items []Item, source string) []Item {
	var out []Item
	for _, it := range items {
		if it.Source == source {
			out = append(out, it)
		}
	}
	return out
}
