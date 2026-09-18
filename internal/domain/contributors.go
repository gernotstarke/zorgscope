package domain

import (
	"slices"
	"sort"
	"strings"
	"time"
)

// Contributor is one person who opened at least one open item (FR-12.2).
type Contributor struct {
	// Login is the GitHub login; "" when GitHub no longer knows the author.
	Login string
	// PRs and Issues are how many open items of each kind they opened.
	PRs, Issues int
	// Repos are the repositories they opened something in: the ones repos names, in that order,
	// then any it does not, in first-seen order.
	Repos []string
	// LastActive is the latest UpdatedAt among their items.
	LastActive time.Time
}

// BuildContributors groups items by author. The order is by open items descending, then
// LastActive descending, then Login case-insensitively; the unknown author, if present, is last.
// It is a pure function and never modifies items. nil for no items.
func BuildContributors(items []Item, repos []string) []Contributor {
	byLogin := make(map[string]*Contributor)
	var order []string
	for _, it := range items {
		c, ok := byLogin[it.Author]
		if !ok {
			c = &Contributor{Login: it.Author}
			byLogin[it.Author] = c
			order = append(order, it.Author)
		}
		switch it.Kind {
		case KindPR:
			c.PRs++
		case KindIssue:
			c.Issues++
		default:
			// A third kind, should one arrive, is neither; the person is still listed.
		}
		if it.UpdatedAt.After(c.LastActive) {
			c.LastActive = it.UpdatedAt
		}
		if !slices.Contains(c.Repos, it.Repo) {
			c.Repos = append(c.Repos, it.Repo)
		}
	}
	if len(order) == 0 {
		return nil
	}
	out := make([]Contributor, 0, len(order))
	for _, login := range order {
		c := *byLogin[login]
		c.Repos = orderRepos(c.Repos, repos)
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Login == "") != (b.Login == "") {
			return b.Login == ""
		}
		if ta, tb := a.PRs+a.Issues, b.PRs+b.Issues; ta != tb {
			return ta > tb
		}
		if !a.LastActive.Equal(b.LastActive) {
			return a.LastActive.After(b.LastActive)
		}
		return strings.ToLower(a.Login) < strings.ToLower(b.Login)
	})
	return out
}

// orderRepos puts the configured repositories first, in configuration order, then the rest in
// the order given.
func orderRepos(seen, configured []string) []string {
	out := make([]string, 0, len(seen))
	for _, r := range configured {
		if slices.Contains(seen, r) {
			out = append(out, r)
		}
	}
	for _, r := range seen {
		if !slices.Contains(configured, r) {
			out = append(out, r)
		}
	}
	return out
}
