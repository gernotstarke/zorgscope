package web

import (
	"hash/crc32"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
)

// The Radar view (FR-1.15): every open item as a blip on an air-traffic scope. Bearing is the site,
// distance from the centre is how long the item has been idle, and the loud items — security,
// dependency, needs you — wear rings that say so. Everything here is geometry and text computed in
// Go; radar.html only prints it, and app.css carries every colour and every motion, because the
// policy grants no 'unsafe-inline' and so a style attribute would simply not apply (QS-4.4).

// The scope's geometry, in the SVG's own units (viewBox 0 0 radarWidth radarHeight). The scope sits
// on the left with room for the site names around it; the data-block panel on the right.
const (
	radarWidth  = 1700
	radarHeight = 1000
	radarCX     = 660
	radarCY     = 500
	radarR      = 430
	// radarR0 is the inner disc: an item touched a moment ago stands on its edge rather than on
	// top of the centre, where every fresh item would pile up.
	radarR0 = 34
	// radarMaxDays is where the rim is: two years. An item idle longer stands on the rim.
	radarMaxDays = 730
	// radarBearings is how many bearing classes app.css defines (bearing-0 … bearing-35), one per
	// ten degrees: each sets the delay of the flash the sweep sets off as it passes.
	radarBearings = 36
	// radarMargin keeps a blip this many degrees clear of its sector's edges.
	radarMargin = 4
	// The panel the data block is drawn in.
	radarPanelX = 1230
	radarPanelY = 60
	radarPanelW = 440
	radarPanelH = 880
	// radarTitleWidth and radarSummaryWidth are how many characters a line of the data block holds.
	radarTitleWidth   = 30
	radarSummaryWidth = 32
	radarFactWidth    = 23
)

// radarRingDays are the range rings, labelled.
var radarRingDays = []struct {
	days  float64
	label string
}{{1, "1 d"}, {7, "1 wk"}, {30, "1 mo"}, {182, "6 mo"}, {365, "1 yr"}}

// radarView is the whole Radar page.
type radarView struct {
	headerView
	Width, Height, CX, CY, R, R0 int
	Panel                        radarRect
	// Leader is where the dotted line from a hovered blip meets the panel.
	Leader radarPoint
	// CardKeys are the data block's keys, drawn once for every card, and ValueX where the values
	// stand beside them.
	CardKeys []radarLine
	ValueX   int
	Sectors  []radarSector
	// Groups is the group switch, one link per configured group; empty with one group (FR-1.15).
	Groups []radarGroupLink
	Rings  []radarRing
	// Sweep is the beam's far end and Glow the afterglow wedge behind it, both drawn pointing
	// north; app.css turns the group they are in.
	Sweep radarPoint
	Glow  string
	// Blips are drawn in this order: calm first, loud last, so a security item is never under a
	// quieter one.
	Blips                                           []radarBlip
	Total, PRs, Issues, Needs, Security, Dependency int
	// Alerts is how many Dependabot alerts are drawn, and Serious how many of them are High or
	// Critical (FR-1.16).
	Alerts, Serious int
}

// radarGroupLink is one link of the radar's group switch.
type radarGroupLink struct {
	Name, URL string
	Current   bool
}

type radarPoint struct{ X, Y float64 }

type radarRect struct{ X, Y, W, H int }

// radarSector is one site's wedge: its name at the rim, a band of its colour along the rim, and
// two faint dotted lines just inside its edges.
type radarSector struct {
	Name, Hue    string
	EdgeA, EdgeB [2]radarPoint
	Rim          string
	Label        radarPoint
	// Anchor is the label's text-anchor, so a name on the left of the scope ends at the rim
	// instead of starting there.
	Anchor string
}

type radarRing struct {
	// R is the ring's radius and LabelY where its label stands, just above it on the north line.
	R, LabelY float64
	Label     string
}

// radarBlip is one item.
type radarBlip struct {
	X, Y float64
	// Shape is a diamond's path for a pull request and a triangle's for a Dependabot alert, empty
	// for an issue, which is a circle of radius R.
	Shape string
	R     float64
	// Ring is the radius of the mark's ring, when it has one.
	Ring float64
	// Mark is "alert" (a High or Critical Dependabot alert), "alert-low" (a Low or Medium one),
	// "security", "dependency", "needs" or "": fixed text, never anything an upstream said.
	Mark string
	// Hue, Bearing and Age are class names: the site's colour, the sweep delay, the fade.
	Hue, Bearing, Age string
	URL               string
	// Tag is the short text beside a loud blip, "⚠ #12" or "#12", drawn at TagX, TagY.
	Tag        string
	TagX, TagY float64
	Card       radarCard

	// The blip's geometry before it is drawn, kept so that spreadBlips can move it: its distance
	// and bearing, the bearings its sector allows, the half-size of its mark, and its kind, which
	// decides the mark's shape.
	radius, bearing, lo, hi, k float64
	kind                       domain.Kind
}

// radarCard is the data block a hovered or focused blip shows in the panel. Its layout is fixed,
// like an air-traffic data block: every line has its own place whatever the lines before it hold,
// so the keys — author, opened, activity, labels, summary — are drawn once for every card
// (radarView.CardKeys) instead of once per item, which QS-2.3's budget could not afford.
type radarCard struct {
	Head    string
	Title   []radarLine
	Reason  radarLine
	Values  []radarLine
	Summary []radarLine
}

// radarLine is one line of the data block, at its fixed height in the panel.
type radarLine struct {
	Y    int
	Text string
}

// The data block's fixed lines, in panel units: the title's up to three lines, the reason, the
// four facts and the two lines of summary; values stand at radarValueX.
const (
	radarTitleY   = 84
	radarReasonY  = 186
	radarFactY    = 228
	radarSummaryY = 364
	radarLineGap  = 30
	radarValueX   = 130
)

// radarKeys are the fact keys, in the order newRadarCard fills the values.
var radarKeys = []string{"author", "opened", "activity", "labels"}

// handleRadar renders the Radar view (FR-1.15). Like every page it reads only the snapshot.
func (s *Server) handleRadar(w http.ResponseWriter, r *http.Request) {
	snap, waiting := s.answeredWaiting(w, r)
	if waiting {
		return
	}
	view := buildRadar(snap.Items, s.cfg.GitHub, r.URL.Query().Get("group"), s.clock.Now())
	view.headerView = s.headerView(snap)
	s.execute(w, r, http.StatusOK, "radar.html", pageData{
		Title:  "Radar",
		Radar:  &view,
		Chrome: chromeFor(r, snap, s.cfg.GitHub.Owner, s.clock.Now()),
	})
}

// radarGroup is the configured group asked names, ignoring case, else the first group, else "" when
// no site is configured. The asked text itself never reaches the page: only a configured name comes
// back.
func radarGroup(gh config.GitHub, asked string) string {
	groups := gh.Groups()
	for _, g := range groups {
		if strings.EqualFold(g, asked) {
			return g
		}
	}
	if len(groups) == 0 {
		return ""
	}
	return groups[0]
}

// radarSpecs are the radar's sectors for group: one per site of the group, in configuration order,
// then Other for the repositories no site claims (FR-1.15 AC8). The first group's radar stops there
// and names the other groups' repositories in hidden: its items are not drawn, because arc42's
// radar crowded by an iSAQB sector was too much. Any other group's radar puts one sector per other
// group before Other, holding all its sites' repositories and coloured by its first site. With one
// group the sectors are siteSpecs'. It walks gh.Sites rather than siteSpecs, which does not carry
// the group.
func radarSpecs(gh config.GitHub, group string) (specs []domain.SiteSpec, hidden map[string]bool) {
	first := len(gh.Sites) > 0 && gh.Sites[0].GroupName() == group
	hidden = make(map[string]bool)
	var others []domain.SiteSpec
	idx := make(map[string]int) // another group -> its index in others
	claimed := make(map[string]bool, len(gh.Sites))
	for _, site := range gh.Sites {
		claimed[site.Repo] = true
		g := site.GroupName()
		if g == group {
			specs = append(specs, domain.SiteSpec{
				Name: site.Name, URL: site.URL, Hue: tileHue(site.Hue), Tag: site.Tag,
				Repos: []string{site.Repo},
			})
			continue
		}
		if first {
			hidden[site.Repo] = true
			continue
		}
		i, ok := idx[g]
		if !ok {
			i = len(others)
			idx[g] = i
			others = append(others, domain.SiteSpec{Name: g, Hue: tileHue(site.Hue)})
		}
		others[i].Repos = append(others[i].Repos, site.Repo)
	}
	specs = append(specs, others...)
	var unclaimed []string
	for _, repo := range gh.Repos {
		if !claimed[repo] {
			unclaimed = append(unclaimed, repo)
		}
	}
	if len(unclaimed) > 0 {
		specs = append(specs, domain.SiteSpec{Name: otherTileName, Hue: unclaimedHue, Repos: unclaimed})
	}
	return specs, hidden
}

// buildRadar places every item on the scope, the sectors those of group — resolved by radarGroup,
// so "" or an unknown name shows the first group. It is a pure function of its arguments.
func buildRadar(items []domain.Item, gh config.GitHub, group string, now time.Time) radarView {
	group = radarGroup(gh, group)
	specs, hidden := radarSpecs(gh, group)
	// Another group's items are left off the first group's radar (FR-1.15 AC8): dropped here, before
	// they could be counted, placed, or pushed into Other.
	items = slices.DeleteFunc(slices.Clone(items), func(it domain.Item) bool { return hidden[it.Repo] })
	sectorOf := make(map[string]int)
	other := -1
	for i, spec := range specs {
		for _, repo := range spec.Repos {
			sectorOf[repo] = i
		}
		if spec.Name == otherTileName {
			other = i
		}
	}
	// A repository no spec names — left over from an earlier configuration — still has to stand
	// somewhere: nothing the list shows may be missing here (QG-1).
	for _, it := range items {
		if _, ok := sectorOf[it.Repo]; !ok && other < 0 {
			specs = append(specs, domain.SiteSpec{Name: otherTileName, Hue: unclaimedHue})
			other = len(specs) - 1
		}
	}

	v := radarView{
		Width: radarWidth, Height: radarHeight, CX: radarCX, CY: radarCY, R: radarR, R0: radarR0,
		Panel:  radarRect{radarPanelX, radarPanelY, radarPanelW, radarPanelH},
		Sweep:  polar(radarR, 0),
		Leader: radarPoint{X: radarPanelX, Y: radarPanelY + 60},
		ValueX: radarValueX,
	}
	v.CardKeys = append(radarLines(radarKeys, radarFactY), radarLine{Y: radarSummaryY, Text: "summary"})
	glow := polar(radarR, -40)
	v.Glow = "M" + coord(radarCX, radarCY) + " L" + coord(glow.X, glow.Y) +
		" A" + strconv.Itoa(radarR) + "," + strconv.Itoa(radarR) + " 0 0 1 " + coord(v.Sweep.X, v.Sweep.Y) + " Z"

	width := 360.0 / float64(max(len(specs), 1))
	for i, spec := range specs {
		v.Sectors = append(v.Sectors, newRadarSector(spec, float64(i)*width, float64(i+1)*width))
	}
	if groups := gh.Groups(); len(groups) > 1 {
		for _, g := range groups {
			v.Groups = append(v.Groups, radarGroupLink{
				Name: g, URL: "/radar?group=" + url.QueryEscape(g), Current: g == group,
			})
		}
	}
	for _, ring := range radarRingDays {
		r := round1(radarRadius(ring.days))
		v.Rings = append(v.Rings, radarRing{R: r, LabelY: round1(radarCY - r - 4), Label: ring.label})
	}

	for _, it := range items {
		sector, ok := sectorOf[it.Repo]
		if !ok {
			sector = other
		}
		// Inside a condensed sector a blip keeps its own site's colour (FR-1.15 AC8).
		hue := specs[sector].Hue
		if h := hueForRepo(gh, it.Repo); h != unclaimedHue {
			hue = h
		}
		b := newRadarBlip(it, gh, now, float64(sector)*width, width, hue)
		switch b.Mark {
		case "security":
			v.Security++
		case "dependency":
			v.Dependency++
		case "needs":
			v.Needs++
		case "alert":
			v.Serious++
		}
		switch it.Kind {
		case domain.KindPR:
			v.PRs++
		case domain.KindAlert:
			v.Alerts++
		default:
			v.Issues++
		}
		v.Blips = append(v.Blips, b)
	}
	spreadBlips(v.Blips)
	v.Total = len(v.Blips)
	sort.SliceStable(v.Blips, func(i, j int) bool {
		return markRank[v.Blips[i].Mark] < markRank[v.Blips[j].Mark]
	})
	return v
}

// markRank is the draw order: the loudest last, on top.
var markRank = map[string]int{"": 0, "needs": 1, "dependency": 2, "alert-low": 3, "security": 4, "alert": 5}

func newRadarSector(spec domain.SiteSpec, a0, a1 float64) radarSector {
	s := radarSector{Name: spec.Name, Hue: spec.Hue}
	for i, a := range []float64{a0 + 1.2, a1 - 1.2} {
		edge := [2]radarPoint{polar(radarR0+4, a), polar(radarR+2, a)}
		if i == 0 {
			s.EdgeA = edge
		} else {
			s.EdgeB = edge
		}
	}
	from, to := polar(radarR+6, a0+1.2), polar(radarR+6, a1-1.2)
	large := "0"
	if a1-a0 > 180 {
		large = "1"
	}
	s.Rim = "M" + coord(from.X, from.Y) + " A" + strconv.Itoa(radarR+6) + "," + strconv.Itoa(radarR+6) +
		" 0 " + large + " 1 " + coord(to.X, to.Y)

	mid := (a0 + a1) / 2
	s.Label = polar(radarR+30, mid)
	s.Label.Y = round1(s.Label.Y + 5)
	switch east := math.Sin(mid * math.Pi / 180); {
	case math.Abs(east) < 0.3:
		s.Anchor = "middle"
	case east > 0:
		s.Anchor = "start"
	default:
		s.Anchor = "end"
	}
	return s
}

func newRadarBlip(it domain.Item, gh config.GitHub, now time.Time, a0, width float64, hue string) radarBlip {
	h := float64(crc32.ChecksumIEEE([]byte(it.Repo+"#"+strconv.Itoa(it.Number)))&0xffff) / 0xffff
	margin := min(radarMargin, width/4)
	lo, hi := a0+margin, a0+width-margin

	days := float64(radarMaxDays)
	if !it.UpdatedAt.IsZero() {
		days = max(0, now.Sub(it.UpdatedAt).Hours()/24)
	}

	b := radarBlip{
		Hue: "hue-" + hue, URL: it.URL,
		lo: lo, hi: hi, kind: it.Kind,
	}
	need := domain.NeedFor(it, gh.Owner)
	switch it.Tier() {
	case domain.TierAlert:
		// A serious alert is as loud as a Security item, in its own colour and shape; a Low or
		// Medium one is ringed thinly and says nothing more (FR-1.16 AC4).
		if need == domain.NeedAlert {
			b.Mark, b.Ring, b.Tag = "alert", 15, "▲ #"+strconv.Itoa(it.Number)
		} else {
			b.Mark, b.Ring = "alert-low", 12
		}
	case domain.TierSecurity:
		b.Mark, b.Ring, b.Tag = "security", 15, "⚠ #"+strconv.Itoa(it.Number)
	case domain.TierDependency:
		b.Mark, b.Ring = "dependency", 13
	default:
		if domain.NeedsNow(it, gh.Owner, now) {
			b.Mark, b.Ring, b.Tag = "needs", 12, "#"+strconv.Itoa(it.Number)
		}
	}

	b.k = 9.0
	if b.Mark == "security" || b.Mark == "alert" {
		b.k = 11
	}
	b.R = b.k - 2
	b.place(radarRadius(days), lo+h*(hi-lo))

	b.Age = "age-0"
	if b.Mark != "security" && b.Mark != "dependency" && it.Kind != domain.KindAlert {
		switch {
		case days > 182:
			b.Age = "age-3"
		case days > 30:
			b.Age = "age-2"
		case days > 7:
			b.Age = "age-1"
		}
	}

	b.Card = newRadarCard(it, b.Mark, need, now)
	return b
}

// place puts the blip at radius r and bearing deg, and draws everything that depends on where it
// stands: its point, its shape, its tag and the delay of its flash.
func (b *radarBlip) place(r, deg float64) {
	b.radius, b.bearing = r, deg
	p := polar(r, deg)
	b.X, b.Y = p.X, p.Y
	b.Bearing = "bearing-" + strconv.Itoa(int(deg/10)%radarBearings)
	k := b.k
	switch b.kind {
	case domain.KindPR:
		b.Shape = "M" + coord(b.X, b.Y-k) + " l" + num(k) + "," + num(k) + " l-" + num(k) + "," + num(k) +
			" l-" + num(k) + ",-" + num(k) + "Z"
	case domain.KindAlert:
		// A triangle pointing up, its centroid on the blip's place: apex k above, base k/2 below.
		h := round1(k * 0.87)
		b.Shape = "M" + coord(b.X, b.Y-k) + " l" + num(h) + "," + num(round1(k*1.5)) + " l-" + num(round1(2*h)) + ",0Z"
	default:
		b.Shape = ""
	}
	if b.Tag != "" {
		b.TagX, b.TagY = round1(b.X+b.Ring+5), round1(b.Y-b.Ring+2)
	}
}

// radarGap is the clear space, in the SVG's units, spreadBlips keeps between two blips, and
// radarStep how far it moves one at a time.
const (
	radarGap  = 2.0
	radarStep = 4.0
)

// spreadBlips moves blips that would cover each other until none does (FR-1.15 AC4). Without it
// two items touched the same afternoon stand a few units apart, and near the centre a sector is
// barely wider than one ringed blip.
//
// The bearing inside a sector means nothing — it is a hash, there only to spread the items — so a
// blip is first moved sideways, alternately either side of where it stood, within its sector. Only
// when the sector has no room at that distance is it moved outward, a step at a time: a little
// older on the scope than it is, which near the centre, on a logarithmic scale, is a matter of
// hours. The loud blips are placed first and so keep their places; everything here is
// deterministic, so a blip stands in the same place on every render. A blip that finds no room
// even at the rim stays where it was: overlapping is better than missing (QG-1).
func spreadBlips(blips []radarBlip) {
	order := make([]int, len(blips))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := blips[order[i]], blips[order[j]]
		if markRank[a.Mark] != markRank[b.Mark] {
			return markRank[a.Mark] > markRank[b.Mark]
		}
		return a.radius < b.radius
	})

	var placed []*radarBlip
	for _, i := range order {
		b := &blips[i]
		r0, d0 := b.radius, b.bearing
	search:
		for r := r0; r <= radarR; r += radarStep {
			step := radarStep / r * 180 / math.Pi // radarStep along the arc, in degrees
			for n := 0; ; n++ {
				// 0, +1, -1, +2, -2, … steps from where the blip stood.
				off := float64((n+1)/2) * step
				if n%2 == 0 {
					off = -off
				}
				d := d0 + off
				if d0+float64((n+1)/2)*step > b.hi && d0-float64((n+1)/2)*step < b.lo {
					break // both sides of the sector tried at this distance
				}
				if d < b.lo || d > b.hi {
					continue
				}
				b.place(r, d)
				if !collides(b, placed) {
					break search
				}
			}
			b.place(r0, d0)
		}
		placed = append(placed, b)
	}
}

// collides reports whether b covers any of placed: two marks closer than their footprints, two
// tags overlapping, or a tag over a mark.
func collides(b *radarBlip, placed []*radarBlip) bool {
	for _, o := range placed {
		if math.Hypot(b.X-o.X, b.Y-o.Y) < b.footprint()+o.footprint()+radarGap {
			return true
		}
		if b.Tag != "" && (o.Tag != "" && boxesOverlap(b.tagBox(), o.tagBox()) || boxNearPoint(b.tagBox(), o.X, o.Y, o.footprint())) {
			return true
		}
		if o.Tag != "" && boxNearPoint(o.tagBox(), b.X, b.Y, b.footprint()) {
			return true
		}
	}
	return false
}

// footprint is how far from its centre a blip draws: its ring, or its mark.
func (b *radarBlip) footprint() float64 { return max(b.Ring, b.R+2) }

// tagBox is the rectangle the blip's tag covers, x0, y0, x1, y1: 14 px bold type, about nine units
// a character.
func (b *radarBlip) tagBox() [4]float64 {
	return [4]float64{b.TagX, b.TagY - 12, b.TagX + 9*float64(utf8.RuneCountInString(b.Tag)), b.TagY + 3}
}

func boxesOverlap(a, b [4]float64) bool {
	return a[0] < b[2] && b[0] < a[2] && a[1] < b[3] && b[1] < a[3]
}

// boxNearPoint reports whether the box comes within r of the point.
func boxNearPoint(box [4]float64, x, y, r float64) bool {
	dx := max(box[0]-x, 0, x-box[2])
	dy := max(box[1]-y, 0, y-box[3])
	return math.Hypot(dx, dy) < r
}

func newRadarCard(it domain.Item, mark string, need domain.Need, now time.Time) radarCard {
	c := radarCard{
		// "PR", not "PULL REQUEST": a site repository's name is long, and the head is one line.
		Head:   strings.ToUpper(kindShort(it.Kind)) + " · " + it.Repo + " #" + strconv.Itoa(it.Number),
		Title:  radarLines(wrapLines(it.Title, radarTitleWidth, 3), radarTitleY),
		Reason: radarLine{Y: radarReasonY},
	}
	switch mark {
	case "alert", "alert-low":
		c.Reason.Text = newTierView(it).Label + ": " + it.Summary
	case "security":
		c.Reason.Text = "Security: " + newTierView(it).Evidence
	case "dependency":
		c.Reason.Text = "Dependency update"
	case "needs":
		switch need {
		case domain.NeedReview:
			c.Reason.Text = "Needs you: review requested"
		case domain.NeedOwn:
			c.Reason.Text = "Needs you: your PR, not merged"
		default:
			c.Reason.Text = "Needs you: contribution waiting"
		}
	}
	labels := "—"
	if len(it.Labels) > 0 {
		labels = strings.Join(it.Labels, ", ")
	}
	author := it.Author
	if author == "" {
		author = "unknown"
	}
	var values []string
	for _, v := range []string{author, relative(it.CreatedAt, now), relative(it.UpdatedAt, now), labels} {
		values = append(values, wrapLines(v, radarFactWidth, 1)[0])
	}
	c.Values = radarLines(values, radarFactY)
	summary := wrapLines(it.Summary, radarSummaryWidth, 2)
	if len(summary) == 0 {
		summary = []string{"—"}
	}
	c.Summary = radarLines(summary, radarSummaryY+radarLineGap)
	return c
}

// radarLines places lines one radarLineGap apart, the first at y.
func radarLines(lines []string, y int) []radarLine {
	out := make([]radarLine, 0, len(lines))
	for i, l := range lines {
		out = append(out, radarLine{Y: y + i*radarLineGap, Text: l})
	}
	return out
}

// relative is "4 days ago", or "unknown" for a time GitHub did not report.
func relative(t, now time.Time) string {
	if tv := newTimeView(t, now); tv.Known {
		return tv.Relative
	}
	return "unknown"
}

// wrapLines breaks text into at most n lines of at most width characters, at spaces; a word longer
// than a line is cut. What does not fit ends the last line with an ellipsis.
func wrapLines(text string, width, n int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		if r := []rune(word); len(r) > width {
			word = string(r[:width-1]) + "…"
		}
		switch {
		case line == "":
			line = word
		case len([]rune(line))+1+len([]rune(word)) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	if len(lines) > n {
		lines = lines[:n]
		last := []rune(lines[n-1])
		if len(last) >= width {
			last = last[:width-1]
		}
		lines[n-1] = string(last) + "…"
	}
	return lines
}

// radarRadius is where an item idle for days stands: a log scale, so the first week has as much
// room as the year after it.
func radarRadius(days float64) float64 {
	days = min(days, radarMaxDays)
	return radarR0 + (radarR-radarR0-10)*math.Log1p(days)/math.Log1p(radarMaxDays)
}

// polar is the point at radius r and compass bearing deg (north 0, clockwise), rounded to a whole
// unit: a unit is well under a pixel at any width the page is drawn at, and the decimals would cost
// QS-2.3's budget two characters per coordinate, eight coordinates per item.
func polar(r, deg float64) radarPoint {
	a := (deg - 90) * math.Pi / 180
	return radarPoint{X: math.Round(radarCX + r*math.Cos(a)), Y: math.Round(radarCY + r*math.Sin(a))}
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

func num(f float64) string { return strconv.FormatFloat(round1(f), 'f', -1, 64) }

func coord(x, y float64) string { return num(x) + "," + num(y) }
