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

// tileOrder fixes the tiles' assembly and rendering order (FR-1.1 AC1).
var tileOrder = []string{"github", "builds", "sites", "tasks"}

// The two Plausible windows the dashboard shows per site (FR-3.1 AC1). No other window length is
// paired into a SiteMetrics.
const (
	weekWindowDays  = 7
	monthWindowDays = 30
)

// DashboardInput is everything the store holds that BuildDashboard needs: the current time, the
// visitor's last-seen watermark, the outcome of the last refresh run, every stored item across
// sources, per-source health, and the list of sources disabled for lack of a credential.
type DashboardInput struct {
	Now         time.Time
	LastVisitAt time.Time
	LastRun     RefreshRun
	StaleAfter  time.Duration
	Items       []Item
	Builds      []Build
	Metrics     []Metric
	States      map[string]SourceState
	Disabled    []string // sources without a credential (FR-8.2 AC2)
}

// Dashboard is the fully assembled page: one tile per source group, plus the header's counts and
// timestamps.
type Dashboard struct {
	GeneratedAt time.Time
	LastVisitAt time.Time
	LastRunAt   time.Time
	NewTotal    int
	Tiles       []Tile
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
	Items    []Item
	Builds   []Build
	Sites    []SiteMetrics
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

	d := Dashboard{
		GeneratedAt: in.Now,
		LastVisitAt: in.LastVisitAt,
		LastRunAt:   in.LastRun.FinishedAt,
		Tiles:       make([]Tile, 0, len(tileOrder)),
	}

	for _, name := range tileOrder {
		tile := buildTile(name, in, disabled)
		d.NewTotal += tile.NewCount
		d.Tiles = append(d.Tiles, tile)
	}
	return d
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
		tile.Items = items
		tile.NewCount = CountNew(items, in.LastVisitAt)
	case "tasks":
		// Not SortItems: that orders new-first then most-recently-updated, which would
		// silently destroy due-date order (FR-4.1 AC2 needs overdue before due-today).
		// NewCount is still counted — on this tile newness is a badge, not a rank.
		items := itemsBySource(in.Items, source)
		sortTasks(items)
		tile.Items = items
		tile.NewCount = CountNew(items, in.LastVisitAt)
	case "builds":
		tile.Builds = in.Builds
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
