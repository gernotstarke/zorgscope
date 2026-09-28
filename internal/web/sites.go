package web

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
)

// tileMaxPRs and tileMaxIssues are how much of each kind a tile lists (FR-1.8 AC2): enough to say
// what is going on at a site, few enough that seven tiles still fit a screen. The list, one click
// away, shows the rest.
const (
	tileMaxPRs    = 3
	tileMaxIssues = 4
	// tileMaxAlerts is how many Dependabot alerts the Security tile lists (FR-1.16 AC5).
	tileMaxAlerts = 4
)

// otherTileName is the tile holding every watched repository no configured site claims, so the
// Sites view never hides something the list shows.
const otherTileName = "Other"

// unclaimedHue is the colour key drawn for a repository no configured site claims: the Other
// tile, and now the list's own group stripe for the same repository (FR-1.10 AC2).
const unclaimedHue = "slate"

// handleSites renders the Sites view (FR-1.8). Like render, it reads only the snapshot.
func (s *Server) handleSites(w http.ResponseWriter, r *http.Request) {
	snap, waiting := s.answeredWaiting(w, r)
	if waiting {
		return
	}
	now := s.clock.Now()

	tier := domain.BuildTierTile(domain.TierTileInput{
		Items: snap.Items, MaxPRs: tileMaxPRs, MaxIssues: tileMaxIssues, MaxAlerts: tileMaxAlerts,
		Repos: s.cfg.GitHub.Repos, Coverage: snap.Coverage,
	})
	tiles := domain.BuildSiteTiles(domain.SiteTilesInput{
		Items: snap.Items, Sites: siteSpecs(s.cfg.GitHub),
		MaxPRs: tileMaxPRs, MaxIssues: tileMaxIssues,
	})
	view := sitesView{
		headerView: s.headerView(snap),
		Security:   newTierTileView(tier, now),
		Tiles:      make([]tileView, 0, len(tiles)),
	}
	for i, tile := range tiles {
		view.Tiles = append(view.Tiles, newTileView(i+1, tile, now))
	}
	s.execute(w, r, http.StatusOK, "sites.html", pageData{
		Title:  "Sites",
		Sites:  &view,
		Chrome: chromeFor(r, snap, s.cfg.GitHub.Owner, s.clock.Now()),
	})
}

// siteSpecs turns the configured sites into tile specs, in configuration order, followed by an
// Other tile for the watched repositories no site claims — omitted when every one is claimed.
func siteSpecs(gh config.GitHub) []domain.SiteSpec {
	specs := make([]domain.SiteSpec, 0, len(gh.Sites)+1)
	claimed := make(map[string]bool, len(gh.Sites))
	for _, site := range gh.Sites {
		specs = append(specs, domain.SiteSpec{
			Name: site.Name, URL: site.URL, Hue: tileHue(site.Hue), Tag: site.Tag,
			Repos: []string{site.Repo},
		})
		claimed[site.Repo] = true
	}
	var other []string
	for _, repo := range gh.Repos {
		if !claimed[repo] {
			other = append(other, repo)
		}
	}
	if len(other) > 0 {
		specs = append(specs, domain.SiteSpec{Name: otherTileName, Hue: unclaimedHue, Repos: other})
	}
	return specs
}

// tileHue is key when the stylesheet defines it, and unclaimedHue otherwise. config.Load already
// refuses an unknown key; this is the second lock on the same door, for a Config built in code, so
// that nothing but a known class name can ever reach the template.
func tileHue(key string) string {
	if slices.Contains(config.HueKeys, key) {
		return key
	}
	return unclaimedHue
}

// hueForRepo is the colour key of the site that claims repo, unclaimedHue when no site does — the
// rule the Other tile follows, now shared with the list's groups (FR-1.10 AC2).
func hueForRepo(gh config.GitHub, repo string) string {
	for _, site := range gh.Sites {
		if site.Repo == repo {
			return tileHue(site.Hue)
		}
	}
	return unclaimedHue
}

// sitesView is the whole Sites page.
type sitesView struct {
	headerView
	// Security is the cross-site tile, drawn before the site tiles (FR-1.13). It is always
	// present, empty or not.
	Security tierTileView
	Tiles    []tileView
}

// tierTileView is the Security tile: the same rows as a site tile, but no site, no colour and a
// count of the tiers rather than of the kinds.
type tierTileView struct {
	// ID names the heading, as on a site tile.
	ID string
	// CountLine is "4 high, 2 low alerts · 1 security · 2 dependency", taken before the cut, each
	// term left out when it is zero, and empty when nothing at all is marked or alerted.
	CountLine string
	// Clear says there is nothing to report at all, every repository's alerts included (FR-1.16
	// AC5): only then does the tile say "All clear".
	Clear bool
	// Band is how the heading is drawn: "security" (red hazard tape) while a Security or Dependency
	// item is open, "alert" (the alert crosshatch) while a serious alert is and nothing marked is,
	// "" (the neutral band) otherwise. Red wins because it is the older, louder signal.
	Band string
	// Alert says a security item is open, and is the only thing that puts the red rule on the
	// tile: a mark that is always on stops being read (ADR-0012, ADR-0014).
	Alert bool
	// Alerts are the tile's Dependabot alert rows, drawn before everything else (FR-1.16 AC5).
	Alerts      []tileItemView
	PRs, Issues []tileItemView
	// AlertsOff and AlertsUnavailable name the repositories whose alerts the tile cannot vouch
	// for, joined for the page: "arc42-template, zorgscope". Empty when there are none.
	AlertsOff, AlertsUnavailable string
	// Href and Label are the "all →" link, set only when the tile cut something (FR-1.8 AC3).
	// The link narrows the list to everything marked, security items included, because the tier
	// axis is a floor.
	Href, Label string
}

// newTierTileView renders the Security tile.
func newTierTileView(tile domain.TierTile, now time.Time) tierTileView {
	v := tierTileView{
		ID:                "tile-security-title",
		Clear:             tile.Clear(),
		Alert:             tile.Security > 0,
		Alerts:            tileItems(tile.Alerts, now),
		PRs:               tileItems(tile.PRs, now),
		Issues:            tileItems(tile.Issues, now),
		AlertsOff:         repoNames(tile.AlertsOff),
		AlertsUnavailable: repoNames(tile.AlertsUnavailable),
	}
	switch {
	case tile.Security+tile.Dependency > 0:
		v.Band = "security"
	case tile.Serious > 0:
		v.Band = "alert"
	}
	var terms []string
	if tile.AlertTotal > 0 {
		terms = append(terms, alertsLine(tile.AlertTotal, tile.Serious))
	}
	if tile.Security > 0 {
		terms = append(terms, strconv.Itoa(tile.Security)+" security")
	}
	if tile.Dependency > 0 {
		terms = append(terms, strconv.Itoa(tile.Dependency)+" dependency")
	}
	v.CountLine = strings.Join(terms, " · ")
	if tile.More {
		v.Href = "/?" + url.Values{"tier": {domain.TierDependency.String()}}.Encode()
		v.Label = "all " + kindsLine(tile.PRTotal, tile.IssueTotal)
		if tile.AlertTotal > 0 {
			v.Label = "all " + quantity(tile.AlertTotal, "alert") + " · " + kindsLine(tile.PRTotal, tile.IssueTotal)
		}
	}
	return v
}

// alertsLine counts the alerts by how serious they are: "4 high, 2 low alerts" when some are
// serious and some not, "2 high alerts" or "2 alerts" when they are all one or the other. The
// serious count is taken before the cut, so it is exact; the rest is the difference.
func alertsLine(total, serious int) string {
	noun := "alerts"
	if total == 1 {
		noun = "alert"
	}
	switch serious {
	case 0:
		return strconv.Itoa(total) + " low " + noun
	case total:
		return strconv.Itoa(total) + " high " + noun
	default:
		return strconv.Itoa(serious) + " high, " + strconv.Itoa(total-serious) + " low " + noun
	}
}

// repoNames joins repositories by name, without their owner, for a line of the Security tile.
func repoNames(repos []string) string {
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		names = append(names, r[strings.IndexByte(r, '/')+1:])
	}
	return strings.Join(names, ", ")
}

// tileView is one tile. Like every view type here, every string it prints is computed in Go.
type tileView struct {
	// ID names the tile's heading, which the section points at with aria-labelledby.
	ID string
	// Name is the site's name, and URL its address; URL is empty for Other, whose heading is not a
	// link.
	Name, URL string
	// Hue is a palette key the stylesheet defines, rendered as the hue-<key> class.
	Hue string
	// Tag tells apart two sites sharing a hue; empty for most.
	Tag string
	// CountLine is the tile's totals before the cut: "4 PRs · 11 issues".
	CountLine   string
	PRs, Issues []tileItemView
	// Links are the tile's "all →" links, empty unless the tile cut something.
	Links []tileLinkView
	// AlertLine says how many Dependabot alerts the site has open — "1 high alert", "2 alerts (1
	// high)" — and AlertHref leads to them; both empty when there are none (FR-1.16).
	AlertLine, AlertHref string
}

// tileItemView is one row of a tile: less than a list row, because a tile is for a glance.
type tileItemView struct {
	Number int
	Title  string
	URL    string
	// Repo names the item's repository. It is drawn only on the Security tile, which spans every
	// site and so is the one tile whose heading does not already say where a row comes from.
	Repo string
	// Tier is the same tier the list and search draw: a tile must not be the one place a
	// Security or Dependency item goes unmarked.
	Tier    tierView
	Updated timeView
	// Raised says the row is an alert, whose time is when GitHub raised it rather than when it was
	// last updated (FR-1.16).
	Raised bool
}

// tileLinkView is one "all →" link to the list filtered to a repository.
type tileLinkView struct {
	// Href is the filtered list's address, its query escaped by url.Values.
	Href string
	// Label says what the list holds: "all 4 PRs · 11 issues".
	Label string
	// Repo names the repository on the Other tile; empty on a site's own tile, whose heading
	// already names the site.
	Repo string
	// Site is the tile's name, added for screen readers, which read a link out of its tile.
	Site string
}

// newTileView renders tile number n.
func newTileView(n int, tile domain.SiteTile, now time.Time) tileView {
	v := tileView{
		ID:        "tile-" + strconv.Itoa(n) + "-title",
		Name:      tile.Spec.Name,
		URL:       tile.Spec.URL,
		Hue:       tile.Spec.Hue,
		Tag:       tile.Spec.Tag,
		CountLine: kindsLine(tile.PRTotal, tile.IssueTotal),
		PRs:       tileItems(tile.PRs, now),
		Issues:    tileItems(tile.Issues, now),
	}
	if tile.Alerts > 0 {
		v.AlertLine = siteAlertLine(tile.Alerts, tile.Serious)
		q := url.Values{"kind": {string(domain.KindAlert)}}
		if len(tile.Spec.Repos) == 1 {
			q.Set("repo", tile.Spec.Repos[0])
		}
		v.AlertHref = "/?" + q.Encode()
	}
	if !tile.More {
		return v
	}
	for _, count := range tile.Counts {
		if count.PRs+count.Issues == 0 {
			continue // a repository with nothing open has no list to link to
		}
		link := tileLinkView{
			Href:  "/?" + url.Values{"repo": {count.Repo}}.Encode(),
			Label: "all " + kindsLine(count.PRs, count.Issues),
			Site:  tile.Spec.Name,
		}
		if tile.Spec.Name == otherTileName {
			link.Repo = count.Repo
		}
		v.Links = append(v.Links, link)
	}
	return v
}

// siteAlertLine is a site tile's alert count: "1 high alert", "3 alerts" when none is serious,
// "3 alerts (1 high)" when some are.
func siteAlertLine(total, serious int) string {
	switch serious {
	case 0:
		return quantity(total, "alert")
	case total:
		if total == 1 {
			return "1 high alert"
		}
		return strconv.Itoa(total) + " high alerts"
	default:
		return quantity(total, "alert") + " (" + strconv.Itoa(serious) + " high)"
	}
}

// kindsLine is a count of both kinds: "1 PR · 11 issues".
func kindsLine(prs, issues int) string {
	return quantity(prs, "PR") + " · " + quantity(issues, "issue")
}

// tileItems renders a tile's rows.
func tileItems(items []domain.Item, now time.Time) []tileItemView {
	out := make([]tileItemView, 0, len(items))
	for _, it := range items {
		out = append(out, tileItemView{
			Number:  it.Number,
			Title:   it.Title,
			URL:     it.URL,
			Repo:    it.Repo,
			Tier:    newTierView(it),
			Updated: newTimeView(it.UpdatedAt, now),
			Raised:  it.Kind == domain.KindAlert,
		})
	}
	return out
}
