package domain

import "time"

// RepoGroup is one repository's row of the dashboard's list: its filtered items, alongside the
// unfiltered count a filter must never be allowed to change (FR-2.1 AC3).
type RepoGroup struct {
	Repo  string
	Items []Item
	// Total is taken over every item this repository holds, before the filter is applied: it
	// answers "what is out there", and the filter answers "what am I looking at right now".
	Total int
}

// DashboardInput is everything BuildDashboard needs to assemble one dashboard: the current time,
// every item the snapshot currently holds, the configured repository list, and the filter
// narrowing what is shown.
type DashboardInput struct {
	Now   time.Time
	Items []Item
	// Repos is the configured repository list, in configuration order. It is what makes the
	// list's own grouping able to report a repository that has never produced an item: without
	// this a silent repository is indistinguishable from one nobody is watching.
	Repos []string
	// Filter narrows which items each group shows. It never changes Total: that answers "what is
	// out there", and the filter answers "what am I looking at right now".
	Filter Filter
}

// Dashboard is the fully assembled page: one filtered list, grouped by repository, plus the
// header's counts (FR-1.1, FR-2.1 AC3).
type Dashboard struct {
	GeneratedAt time.Time
	// Total and Shown are counted at two different points: Total over every item regardless of
	// the filter, Shown over what the filter actually let through. A filter that also moved the
	// total would make the dashboard lie about what is out there the moment somebody typed into
	// the search box.
	Total, Shown int
	// Filter is the filter that was applied, echoed back so the page can render it as the
	// visitor left it.
	Filter Filter
	Groups []RepoGroup
}

// BuildDashboard assembles the dashboard from the snapshot's items. It is a pure function: no I/O
// and no clock of its own — the caller supplies Now — and it never mutates the slices in in.
// Items are always copied into a fresh slice before any sorting, so the caller's Items slice is
// untouched.
func BuildDashboard(in DashboardInput) Dashboard {
	d := Dashboard{
		GeneratedAt: in.Now,
		Filter:      in.Filter,
	}

	d.Total = len(in.Items)
	d.Groups = groupByRepo(in.Items, in.Repos, in.Filter)
	for _, g := range d.Groups {
		d.Shown += len(g.Items)
	}
	return d
}

// groupByRepo assembles one group per repository that has at least one item passing f, in the
// order the repositories are configured; repositories that still hold items but are no longer
// configured follow, in first-seen order. Total is per repository before filtering.
func groupByRepo(items []Item, repos []string, f Filter) []RepoGroup {
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
		SortItems(g.Items)
		out = append(out, *g)
	}
	return out
}
