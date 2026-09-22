package domain

import "sort"

// SiteSpec is one tile's worth of configuration, as the web layer hands it to BuildSiteTiles: a
// configured site with its one repository, or the Other tile holding every watched repository no
// site claims (FR-1.8). Treating Other as just another spec is what lets one function build every
// tile.
type SiteSpec struct {
	Name, URL, Hue, Tag string
	Repos               []string
}

// SiteTilesInput is everything BuildSiteTiles needs.
type SiteTilesInput struct {
	// Items is every item the snapshot holds. It is shared with concurrent renders and is never
	// modified.
	Items []Item
	// Sites are the tiles to build, in the order they are drawn.
	Sites []SiteSpec
	// MaxPRs and MaxIssues are how many of each kind a tile lists. They are the web layer's
	// presentation numbers, passed in so that this package holds no opinion about page density.
	MaxPRs, MaxIssues int
}

// RepoCount is how many open pull requests and issues one repository of a tile holds, counted
// before the tile's cut.
type RepoCount struct {
	Repo        string
	PRs, Issues int
}

// SiteTile is one site's tile of the Sites view.
type SiteTile struct {
	Spec SiteSpec
	// PRs and Issues are sorted most recently updated first, and cut to MaxPRs and MaxIssues.
	PRs, Issues []Item
	// PRTotal, IssueTotal and Counts are taken before the cut, so a tile never understates what
	// is open — the rule the list already follows for its filter (FR-2.1 AC3).
	PRTotal, IssueTotal int
	Counts              []RepoCount
	// More says the tile listed fewer items than it holds.
	More bool
}

// BuildSiteTiles builds one tile per spec, in spec order (FR-1.8). A spec with nothing open still
// gets its tile: the Sites view is a fixed map of the family, and a tile that disappeared would
// read as a site that had gone. Items of a repository no spec names are ignored. It is a pure
// function and never modifies in.Items.
func BuildSiteTiles(in SiteTilesInput) []SiteTile {
	// append onto nil slices allocates fresh backing arrays, so sorting a tile's lists below can
	// never reorder the caller's Items.
	byRepo := make(map[string][]Item)
	for _, it := range in.Items {
		byRepo[it.Repo] = append(byRepo[it.Repo], it)
	}

	tiles := make([]SiteTile, 0, len(in.Sites))
	for _, spec := range in.Sites {
		tile := SiteTile{Spec: spec, Counts: make([]RepoCount, 0, len(spec.Repos))}
		var prs, issues []Item
		for _, repo := range spec.Repos {
			count := RepoCount{Repo: repo}
			for _, it := range byRepo[repo] {
				switch it.Kind {
				case KindPR:
					prs = append(prs, it)
					count.PRs++
				case KindIssue:
					issues = append(issues, it)
					count.Issues++
				default:
					// Kind is a closed two-value type today; a future third kind is left out of
					// both lists rather than silently counted as an issue.
				}
			}
			tile.Counts = append(tile.Counts, count)
		}

		SortItems(prs)
		SortItems(issues)
		tile.PRTotal, tile.IssueTotal = len(prs), len(issues)
		tile.PRs = prs[:min(len(prs), in.MaxPRs)]
		tile.Issues = issues[:min(len(issues), in.MaxIssues)]
		tile.More = tile.PRTotal > len(tile.PRs) || tile.IssueTotal > len(tile.Issues)
		tiles = append(tiles, tile)
	}
	return tiles
}

// TierTileInput is everything BuildTierTile needs. Items is shared with concurrent renders and is
// never modified.
type TierTileInput struct {
	Items             []Item
	MaxPRs, MaxIssues int
}

// TierTile is the Sites view's one cross-site tile: everything the page marks Security or
// Dependency, wherever it is open (FR-1.13). It is not a SiteTile — it has no site, no colour and
// no per-repository counts, and what it counts is the tiers rather than the kinds.
type TierTile struct {
	// PRs and Issues are sorted Security before Dependency, and only then most recently updated
	// first, cut to MaxPRs and MaxIssues.
	PRs, Issues []Item
	// PRTotal, IssueTotal, Security and Dependency are taken before the cut, for the reason
	// FR-2.1 AC3 gives the list: what is shown never understates what is open.
	PRTotal, IssueTotal  int
	Security, Dependency int
	// More says the tile listed fewer items than it holds.
	More bool
}

// BuildTierTile gathers every marked item across every repository. A snapshot with nothing marked
// yields an empty tile rather than none: the tile is a standing answer to "is anything
// security-related open?", and one that disappeared when the answer was no would read as a check
// that had stopped running. It is a pure function and never modifies in.Items.
func BuildTierTile(in TierTileInput) TierTile {
	var tile TierTile
	var prs, issues []Item
	for _, it := range in.Items {
		switch it.Tier() {
		case TierSecurity:
			tile.Security++
		case TierDependency:
			tile.Dependency++
		default:
			continue // unmarked: the site tiles and the list are where it belongs
		}
		switch it.Kind {
		case KindPR:
			prs = append(prs, it)
		case KindIssue:
			issues = append(issues, it)
		default:
			// As in BuildSiteTiles: a future third kind is left out rather than miscounted.
		}
	}

	sortByTier(prs)
	sortByTier(issues)
	tile.PRTotal, tile.IssueTotal = len(prs), len(issues)
	tile.PRs = prs[:min(len(prs), in.MaxPRs)]
	tile.Issues = issues[:min(len(issues), in.MaxIssues)]
	tile.More = tile.PRTotal > len(tile.PRs) || tile.IssueTotal > len(tile.Issues)
	return tile
}

// sortByTier puts the loudest tier first and, within a tier, the most recently updated first. It
// sorts the slice in place, which is safe because its caller built it by appending onto nil.
func sortByTier(items []Item) {
	SortItems(items)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Tier() > items[j].Tier() })
}
