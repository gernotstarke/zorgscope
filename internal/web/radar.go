package web

import (
	"hash/crc32"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

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
	radarWidth  = 1640
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
	radarPanelW = 380
	radarPanelH = 880
	// radarTitleWidth and radarSummaryWidth are how many characters a line of the data block holds.
	radarTitleWidth   = 30
	radarSummaryWidth = 36
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
	Sectors                      []radarSector
	Rings                        []radarRing
	// Sweep is the beam's far end and Glow the afterglow wedge behind it, both drawn pointing
	// north; app.css turns the group they are in.
	Sweep radarPoint
	Glow  string
	// Blips are drawn in this order: calm first, loud last, so a security item is never under a
	// quieter one.
	Blips                                          []radarBlip
	Total, PRs, Issues, Needs, Security, Dependency int
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
	R     float64
	Label string
}

// radarBlip is one item.
type radarBlip struct {
	X, Y float64
	// Shape is a diamond's path for a pull request, empty for an issue, which is a circle of
	// radius R.
	Shape string
	R     float64
	// Ring is the radius of the mark's ring, when it has one.
	Ring float64
	// Mark is "security", "dependency", "needs" or "": fixed text, never anything an upstream said.
	Mark string
	// Hue, Bearing and Age are class names: the site's colour, the sweep delay, the fade.
	Hue, Bearing, Age string
	URL               string
	// Label is what a screen reader says for the link: the card is hidden until hover.
	Label string
	// Tag is the short text beside a loud blip: "⚠ #12" or "#12".
	Tag  string
	Card radarCard
}

// radarCard is the data block a hovered or focused blip shows in the panel.
type radarCard struct {
	Head        string
	Title       []string
	Reason      string
	ReasonClass string
	Facts       []radarFact
	Summary     []string
}

type radarFact struct{ Key, Value string }

// handleRadar renders the Radar view (FR-1.15). Like every page it reads only the snapshot.
func (s *Server) handleRadar(w http.ResponseWriter, r *http.Request) {
	snap, waiting := s.answeredWaiting(w, r)
	if waiting {
		return
	}
	view := buildRadar(snap.Items, s.cfg.GitHub, s.clock.Now())
	view.headerView = s.headerView(snap)
	s.execute(w, r, http.StatusOK, "radar.html", pageData{
		Title:  "Radar",
		Radar:  &view,
		Chrome: chromeFor(r, snap, s.cfg.GitHub.Owner, s.clock.Now()),
	})
}

// buildRadar places every item on the scope. It is a pure function of its arguments.
func buildRadar(items []domain.Item, gh config.GitHub, now time.Time) radarView {
	specs := siteSpecs(gh)
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
		Panel: radarRect{radarPanelX, radarPanelY, radarPanelW, radarPanelH},
		Sweep: polar(radarR, 0),
	}
	glow := polar(radarR, -40)
	v.Glow = "M" + coord(radarCX, radarCY) + " L" + coord(glow.X, glow.Y) +
		" A" + strconv.Itoa(radarR) + "," + strconv.Itoa(radarR) + " 0 0 1 " + coord(v.Sweep.X, v.Sweep.Y) + " Z"

	width := 360.0 / float64(max(len(specs), 1))
	for i, spec := range specs {
		v.Sectors = append(v.Sectors, newRadarSector(spec, float64(i)*width, float64(i+1)*width))
	}
	for _, ring := range radarRingDays {
		v.Rings = append(v.Rings, radarRing{R: round1(radarRadius(ring.days)), Label: ring.label})
	}

	for _, it := range items {
		sector, ok := sectorOf[it.Repo]
		if !ok {
			sector = other
		}
		b := newRadarBlip(it, gh, now, float64(sector)*width, width, specs[sector].Hue)
		switch b.Mark {
		case "security":
			v.Security++
		case "dependency":
			v.Dependency++
		case "needs":
			v.Needs++
		}
		if it.Kind == domain.KindPR {
			v.PRs++
		} else {
			v.Issues++
		}
		v.Blips = append(v.Blips, b)
	}
	v.Total = len(v.Blips)
	sort.SliceStable(v.Blips, func(i, j int) bool {
		return markRank[v.Blips[i].Mark] < markRank[v.Blips[j].Mark]
	})
	return v
}

// markRank is the draw order: the loudest last, on top.
var markRank = map[string]int{"": 0, "needs": 1, "dependency": 2, "security": 3}

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
	bearing := a0 + margin + h*(width-2*margin)

	days := float64(radarMaxDays)
	if !it.UpdatedAt.IsZero() {
		days = max(0, now.Sub(it.UpdatedAt).Hours()/24)
	}
	p := polar(radarRadius(days), bearing)

	b := radarBlip{
		X: p.X, Y: p.Y, Hue: "hue-" + hue, URL: it.URL,
		Bearing: "bearing-" + strconv.Itoa(int(bearing/10)%radarBearings),
	}
	need := domain.NeedFor(it, gh.Owner)
	switch it.Tier() {
	case domain.TierSecurity:
		b.Mark, b.Ring, b.Tag = "security", 15, "⚠ #"+strconv.Itoa(it.Number)
	case domain.TierDependency:
		b.Mark, b.Ring = "dependency", 13
	default:
		if domain.NeedsNow(it, gh.Owner, now) {
			b.Mark, b.Ring, b.Tag = "needs", 12, "#"+strconv.Itoa(it.Number)
		}
	}

	k := 9.0
	if b.Mark == "security" {
		k = 11
	}
	b.R = k - 2
	if it.Kind == domain.KindPR {
		b.Shape = "M" + coord(b.X, b.Y-k) + " l" + num(k) + "," + num(k) + " l-" + num(k) + "," + num(k) +
			" l-" + num(k) + ",-" + num(k) + "Z"
	}

	b.Age = "age-0"
	if b.Mark != "security" && b.Mark != "dependency" {
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
	b.Label = kindShort(it.Kind) + " " + it.Repo + " #" + strconv.Itoa(it.Number) + ": " + it.Title
	if b.Card.Reason != "" {
		b.Label += " — " + b.Card.Reason
	}
	return b
}

func newRadarCard(it domain.Item, mark string, need domain.Need, now time.Time) radarCard {
	kind := "ISSUE"
	if it.Kind == domain.KindPR {
		kind = "PULL REQUEST"
	}
	c := radarCard{
		Head:  kind + " · " + it.Repo + " #" + strconv.Itoa(it.Number),
		Title: wrapLines(it.Title, radarTitleWidth, 3),
	}
	switch mark {
	case "security":
		c.Reason, c.ReasonClass = "Security: "+newTierView(it).Evidence, "security"
	case "dependency":
		c.Reason, c.ReasonClass = "Dependency update", "dependency"
	case "needs":
		c.Reason, c.ReasonClass = "Needs you: contribution waiting", "needs"
		if need == domain.NeedReview {
			c.Reason = "Needs you: review requested"
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
	c.Facts = []radarFact{
		{"author", author},
		{"opened", relative(it.CreatedAt, now)},
		{"activity", relative(it.UpdatedAt, now)},
		{"labels", labels},
	}
	c.Summary = wrapLines(it.Summary, radarSummaryWidth, 2)
	return c
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

// polar is the point at radius r and compass bearing deg (north 0, clockwise), rounded to a tenth.
func polar(r, deg float64) radarPoint {
	a := (deg - 90) * math.Pi / 180
	return radarPoint{X: round1(radarCX + r*math.Cos(a)), Y: round1(radarCY + r*math.Sin(a))}
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

func num(f float64) string { return strconv.FormatFloat(round1(f), 'f', -1, 64) }

func coord(x, y float64) string { return num(x) + "," + num(y) }
