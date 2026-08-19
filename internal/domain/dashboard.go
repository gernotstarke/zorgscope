package domain

import (
	"sort"
	"time"
)

// tileSource maps each dashboard tile to the name of the fetcher that fills it. Tile names are a
// display concept ("builds", "sites"); fetcher/source names are a wire concept
// ("github-builds", "plausible") recorded in States and Disabled. The two vocabularies differ, so
// this is the one place that translates between them — everything below looks a tile's source up
// here rather than repeating the string literals.
var tileSource = map[string]string{
	"github": "github",
	"builds": "github-builds",
	"sites":  "plausible",
	"tasks":  "todoist",
}

// tileTitle gives each tile its display title.
var tileTitle = map[string]string{
	"github": "GitHub",
	"builds": "Builds",
	"sites":  "Sites",
	"tasks":  "Tasks",
}

// tileOrder fixes the dashboard tiles' assembly and rendering order (FR-1.1 AC1).
//
// Builds is deliberately not among them. The front page is for what needs handling — new and
// unhandled issues and pull requests — and a list of one row per repository is a report, not a
// prompt. Builds are one indicator on the front page and a page of their own behind it; see
// BuildStatus.
var tileOrder = []string{"github", "sites", "tasks"}

// sourceOrder is every source the dashboard depends on, builds included, in the order they are
// reported in. It is what the problems page walks: an interface that stopped having a tile did
// not stop being an interface that can fail.
var sourceOrder = []string{"github", "builds", "sites", "tasks"}

// githubTileLimit is how many issues and pull requests the GitHub tile shows.
//
// The dashboard answers "does anything need me right now", and a list of every open issue across
// eight repositories answers a different question — one nobody scrolls to the bottom of. Five is
// the number that fits beside the other three tiles without the page becoming a report. The tile
// still says how many more there are, because the count is the part a truncated list would
// otherwise destroy: "5 shown" and "5 open" must not look the same.
//
// Only the list is cut. NewCount is counted over every item the source holds, so the badge and
// the total in the tab title stay true — truncating before counting would make the dashboard
// report fewer new items the more there were.
const githubTileLimit = 5

// The two Plausible windows the dashboard shows per site (FR-3.1 AC1). No other window length is
// paired into a SiteMetrics.
const (
	weekWindowDays  = 7
	monthWindowDays = 30
)

// DashboardInput is everything the store holds that BuildDashboard needs: the current time, the
// visitor's last-seen watermark, the outcome of the last refresh run and of the last one that
// succeeded, every stored item across sources, per-source health, and the list of sources disabled
// for lack of a credential.
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
	Metrics           []Metric
	States            map[string]SourceState
	Disabled          []string // sources without a credential (FR-8.2 AC2)
	// Repos is the configured repository list, in configuration order. It is what makes the
	// build status able to report a repository that has never produced a run: only repositories
	// with a run are stored, so without this a silent workflow is indistinguishable from a
	// repository nobody is watching.
	Repos []string
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

// Dashboard is the fully assembled page: one tile per source group, plus the header's counts and
// timestamps.
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
	NewTotal      int
	Tiles         []Tile
	// Problems is the health of every external interface, worst first (FR-1.4). Every configured
	// interface is listed, healthy ones included, so that the details page can answer "is
	// anything wrong?" positively rather than with an empty list.
	// Builds is the front page's build indicator and the rows behind it (FR-2.3).
	Builds   BuildStatus
	Problems []Problem
	// ProblemCount is how many of them are actually wrong — errors and warnings, never an
	// interface that is merely switched off. It is what decides whether the dashboard shows its
	// warning box at all, so a page with nothing configured stays quiet.
	ProblemCount int
}

// Tile is one section of the dashboard — GitHub items, builds, site statistics or tasks (FR-1.1
// AC1). Which of Items, Builds and Sites is populated depends on Name; the other two stay empty.
type Tile struct {
	Name     string // "github", "builds", "sites", "tasks"
	Title    string
	NewCount int
	Stale    bool
	Disabled bool
	Error    string
	LastOKAt time.Time
	// Items is what the tile shows, which on a truncated tile is not everything the source holds
	// — see Total.
	Items []Item
	// Total is how many items the source holds in all. It equals len(Items) on a tile that shows
	// everything, and is larger on one that shows only its first few, so the tile can say how
	// many it is not showing rather than quietly implying there are none.
	Total  int
	Builds []Build
	Sites  []SiteMetrics
}

// SiteMetrics pairs one site's week and month Plausible windows (FR-3.1 AC1). A window that never
// arrived — a partial Plausible failure can leave a site holding only one of the two — is the
// zero Metric, distinguishable from a real result by its empty Site field.
type SiteMetrics struct {
	Site  string
	Week  Metric
	Month Metric
}

// BuildDashboard assembles the dashboard from everything the store holds. It is a pure function:
// no I/O and no clock of its own — the caller supplies Now — and it never mutates the slices in
// in. Items destined for a tile are always copied into a fresh slice before any sorting, so the
// caller's Items slice is untouched; Builds and Metrics are never sorted, so they are shared with
// the caller directly.
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
		Tiles:         make([]Tile, 0, len(tileOrder)),
	}

	for _, name := range tileOrder {
		tile := buildTile(name, in, disabled)
		d.NewTotal += tile.NewCount
		d.Tiles = append(d.Tiles, tile)
	}

	d.Builds = buildStatus(in, disabled)
	d.Problems = buildProblems(in, disabled)
	for _, p := range d.Problems {
		if p.Wrong() {
			d.ProblemCount++
		}
	}
	return d
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

// buildTile assembles a single tile: its content first, then its health. Disabled is checked
// before Stale and before Error (FR-8.2 AC2) — a disabled source is never stale (staleness is
// meaningless for something that never runs) and never failing (it never ran to fail).
func buildTile(name string, in DashboardInput, disabled map[string]bool) Tile {
	source := tileSource[name]
	tile := Tile{Name: name, Title: tileTitle[name]}

	switch name {
	case "github":
		items := itemsBySource(in.Items, source)
		SortItems(items, in.LastVisitAt)
		// Counted over everything, shown as the first few: the badge is about the source, the
		// list is about the screen.
		tile.NewCount = CountNew(items, in.LastVisitAt)
		tile.Total = len(items)
		tile.Items = firstN(items, githubTileLimit)
	case "tasks":
		// Not SortItems: that orders new-first then most-recently-updated, which would
		// silently destroy due-date order (FR-4.1 AC2 needs overdue before due-today).
		// NewCount is still counted — on this tile newness is a badge, not a rank.
		items := itemsBySource(in.Items, source)
		sortTasks(items)
		tile.Items = items
		tile.Total = len(items)
		tile.NewCount = CountNew(items, in.LastVisitAt)
	case "sites":
		tile.Sites = siteMetrics(in.Metrics)
	}

	if disabled[source] {
		tile.Disabled = true
		return tile
	}

	state := in.States[source]
	tile.Stale = state.Stale(in.Now, in.StaleAfter)
	tile.LastOKAt = state.LastSuccessAt
	if state.Failing() {
		tile.Error = state.LastError
	}
	return tile
}

// itemsBySource returns the items belonging to source, in a freshly allocated slice. Because the
// result never shares a backing array with items, sorting it in place cannot mutate the caller's
// slice.
func itemsBySource(items []Item, source string) []Item {
	var out []Item
	for _, it := range items {
		if it.Source == source {
			out = append(out, it)
		}
	}
	return out
}

// firstN returns the first n items, or all of them when there are fewer. The result shares its
// backing array with items, which is safe because nothing downstream sorts or appends to a tile's
// Items — and it is the same slice the caller already owns exclusively, itemsBySource having
// allocated it.
func firstN(items []Item, n int) []Item {
	if len(items) <= n {
		return items
	}
	return items[:n]
}

// sortTasks orders the tasks tile by due date ascending — overdue tasks before those due today
// (FR-4.1 AC2) — the order the Todoist adapter already fetches in. Equal due dates tie-break on
// ExternalID so the order is deterministic and cannot flip between refreshes that change nothing
// about the tasks themselves.
//
// Newness is deliberately not a sort key here. On this tile NEW is a badge, not a rank: a task
// first seen since the last visit is not more urgent than one overdue by a week, and sorting new
// first would bury the overdue one below it — which is exactly what FR-4.1 AC2 forbids. The GitHub
// tile is the other way round (SortItems, new first), because there newness *is* the urgency.
func sortTasks(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].DueAt.Equal(items[j].DueAt) {
			return items[i].DueAt.Before(items[j].DueAt)
		}
		return items[i].ExternalID < items[j].ExternalID
	})
}

// siteMetrics pairs each site's week and month Metric by (Site, WindowDays), never by position —
// a partial Plausible failure can leave a site holding only one window, and indexing by position
// would pair it with a neighbouring site's figures. Site order follows first appearance in
// metrics, which preserves configuration order (FR-3.1 AC3) without sorting or ranging a map.
func siteMetrics(metrics []Metric) []SiteMetrics {
	var order []string
	windows := make(map[string]map[int]Metric)

	for _, m := range metrics {
		if _, ok := windows[m.Site]; !ok {
			order = append(order, m.Site)
			windows[m.Site] = make(map[int]Metric)
		}
		windows[m.Site][m.WindowDays] = m
	}

	out := make([]SiteMetrics, 0, len(order))
	for _, site := range order {
		w := windows[site]
		out = append(out, SiteMetrics{
			Site:  site,
			Week:  w[weekWindowDays],
			Month: w[monthWindowDays],
		})
	}
	return out
}
