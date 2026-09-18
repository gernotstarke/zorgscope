package domain

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
