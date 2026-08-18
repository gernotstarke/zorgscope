package web

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// minPollSeconds floors the htmx poll interval (FR-1.6 AC1). The interval comes from
// configuration, and a misconfigured tiny value would turn an open tab into a request loop
// against a machine that is billed for being awake.
const minPollSeconds = 30

// ---------------------------------------------------------------- handlers

// handleDashboard renders the whole page: everything the store holds, assembled by the domain and
// rendered from stored data alone. It contacts no upstream service (FR-1.1 AC2) — the only
// dependency it reaches for is the store — and it is not a visit: only POST /seen moves the
// last-visit watermark, so leaving the tab open and letting it poll never clears a badge
// (FR-1.6 AC2).
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	d, err := s.dashboard(r.Context())
	if err != nil {
		s.fail(w, r, "assembling the dashboard", err)
		return
	}
	view := s.dashboardView(d)
	s.render(w, r, http.StatusOK, "dashboard.html", pageData{
		// The dashboard is the site, so the tab reads "zorgscope" rather than
		// "Dashboard · zorgscope". NewCount supplies FR-1.2 AC3's prefix.
		Title:     "",
		NewCount:  d.NewTotal,
		Dashboard: &view,
	})
}

// handleTile renders one tile on its own, without the layout, so htmx can swap a single tile
// (FR-1.6 AC1) and so a template change never has to be made twice: the fragment a poll returns
// is the very same "tile" template the page composed itself from.
//
// The wildcard is whatever the caller put in the path, so it is matched against the tiles the
// domain actually assembled rather than interpolated into a template name.
func (s *Server) handleTile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("source")
	d, err := s.dashboard(r.Context())
	if err != nil {
		s.fail(w, r, "assembling a tile", err)
		return
	}
	for _, tile := range d.Tiles {
		if tile.Name != name {
			continue
		}
		var buf bytes.Buffer
		td := s.tileData(tile, d.GeneratedAt, d.LastVisitAt)
		if err := s.tiles.ExecuteTemplate(&buf, "tile", td); err != nil {
			s.fail(w, r, "rendering a tile", err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(buf.Bytes())
		return
	}
	http.NotFound(w, r)
}

// handleSeen marks everything seen (FR-1.3 AC1) and sends the browser back to the dashboard.
//
// It answers 303 rather than swapping a fragment because the control is a plain form: the whole
// page has to be re-rendered for the badges to go, and a form post that ends in a redirect is
// exactly what works with JavaScript disabled (FR-1.3 AC3). htmx is enhancement on this page, not
// the transport.
func (s *Server) handleSeen(w http.ResponseWriter, r *http.Request) {
	if err := s.store.SetLastVisit(r.Context(), s.clock.Now()); err != nil {
		s.fail(w, r, "marking everything seen", err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ---------------------------------------------------------------- assembly

// dashboard reads everything the page needs from the store and hands it to the domain. Every read
// is a plain query against stored data; nothing here talks to an upstream service (FR-1.1 AC2).
func (s *Server) dashboard(ctx context.Context) (domain.Dashboard, error) {
	items, err := s.store.Items(ctx)
	if err != nil {
		return domain.Dashboard{}, fmt.Errorf("reading items: %w", err)
	}
	builds, err := s.store.Builds(ctx)
	if err != nil {
		return domain.Dashboard{}, fmt.Errorf("reading builds: %w", err)
	}
	metrics, err := s.store.Metrics(ctx)
	if err != nil {
		return domain.Dashboard{}, fmt.Errorf("reading metrics: %w", err)
	}
	states, err := s.store.SourceStates(ctx)
	if err != nil {
		return domain.Dashboard{}, fmt.Errorf("reading source states: %w", err)
	}
	lastRun, err := s.store.LastRun(ctx)
	if err != nil {
		return domain.Dashboard{}, fmt.Errorf("reading the last refresh run: %w", err)
	}
	lastVisit, err := s.store.LastVisit(ctx)
	if err != nil {
		return domain.Dashboard{}, fmt.Errorf("reading the last visit: %w", err)
	}

	return domain.BuildDashboard(domain.DashboardInput{
		Now:         s.clock.Now(),
		LastVisitAt: lastVisit,
		LastRun:     lastRun,
		StaleAfter:  s.cfg.Refresh.StaleAfter,
		Items:       items,
		Builds:      builds,
		Metrics:     metrics,
		States:      states,
		Disabled:    s.disabledSources(),
	}), nil
}

// disabledSources names the fetchers with no credential, in the source vocabulary the domain's
// States and Disabled use (FR-8.2 AC2). GitHub contributes two fetcher names because issues and
// builds are two fetchers over one credential: without the token both tiles are dark, and naming
// only "github" would leave the builds tile claiming to be merely stale.
func (s *Server) disabledSources() []string {
	var out []string
	if !s.cfg.Enabled("github") {
		out = append(out, "github", "github-builds")
	}
	if !s.cfg.Enabled("plausible") {
		out = append(out, "plausible")
	}
	if !s.cfg.Enabled("todoist") {
		out = append(out, "todoist")
	}
	return out
}

// pollSeconds is the htmx poll interval for every tile (FR-1.6 AC1): the configured refresh
// interval, because a tile cannot change between refreshes and polling faster would only wake a
// machine that is meant to scale to zero.
func (s *Server) pollSeconds() int {
	secs := int(s.cfg.Refresh.Interval / time.Second)
	if secs < minPollSeconds {
		return minPollSeconds
	}
	return secs
}

// ---------------------------------------------------------------- view types

// The view types below exist so that the templates can stay free of logic. Every string a
// template prints is computed here, in Go, where it is testable and where html/template's
// contextual escaping is the only thing left between upstream text and the page. In particular
// nothing is ever a template.HTML: issue titles, repository names, Todoist content and upstream
// error text are all attacker-influenceable in principle (QS-4.3, QS-4.4).

// dashboardView is the whole page.
type dashboardView struct {
	NewTotal  int
	LastRun   timeView
	LastVisit timeView
	Tiles     []tileData
}

// tileData is one tile, and is what both the page and the /tile/{source} fragment execute the
// "tile" template with — the fragment is therefore identical to the tile the page drew.
type tileData struct {
	Name     string
	Title    string
	NewCount int
	Stale    bool
	Disabled bool
	// Error is upstream error text, already scrubbed of every configured secret (QS-4.3).
	Error    string
	LastOK   timeView
	PollSecs int
	Items    []itemView
	Builds   []buildView
	Sites    []siteView
}

// timeView is one timestamp rendered twice: an absolute stamp for the <time> element's machine
// attribute, and the relative phrase a person reads. Known is false for a zero time, which is the
// difference between "never" and "at the epoch".
type timeView struct {
	Known    bool
	Absolute string
	Relative string
}

// itemView is one row of the GitHub or the tasks tile.
type itemView struct {
	Title    string
	URL      string
	Repo     string
	Number   int
	Kind     string
	Author   string
	New      bool
	Created  timeView
	Updated  timeView
	HasDue   bool
	Overdue  bool
	Due      string
	DueAt    string
	Priority string
}

// buildView is one repository's build state.
type buildView struct {
	Repo     string
	Workflow string
	RunURL   string
	// Running is a run in progress; Conclusion then holds the previous run's outcome, which is
	// shown next to it (FR-2.3 AC2).
	Running    bool
	Conclusion string
	Label      string
	Class      string
	Finished   timeView
}

// siteView is one site's pair of Plausible windows.
type siteView struct {
	Site  string
	Week  windowView
	Month windowView
}

// windowView is one Plausible window. Known is false when the window never arrived — a partial
// Plausible failure can leave a site holding only one of its two — which must read as missing
// rather than as a row of zeroes.
type windowView struct {
	Known          bool
	Days           int
	Visitors       int
	Pageviews      int
	VisitorChange  changeView
	PageviewChange changeView
}

// changeView is a figure's change against the preceding period (FR-3.1 AC2). Text always carries
// the direction as a sign or a word, so colour is never the only carrier of the meaning
// (FR-1.5 AC2).
type changeView struct {
	Known     bool
	Direction string
	Text      string
}

// ---------------------------------------------------------------- view construction

// dashboardView turns the assembled domain dashboard into the page's presentation data.
func (s *Server) dashboardView(d domain.Dashboard) dashboardView {
	v := dashboardView{
		NewTotal:  d.NewTotal,
		LastRun:   newTimeView(d.LastRunAt, d.GeneratedAt),
		LastVisit: newTimeView(d.LastVisitAt, d.GeneratedAt),
		Tiles:     make([]tileData, 0, len(d.Tiles)),
	}
	for _, tile := range d.Tiles {
		v.Tiles = append(v.Tiles, s.tileData(tile, d.GeneratedAt, d.LastVisitAt))
	}
	return v
}

// tileData turns one domain tile into its presentation data. It preserves the order the domain
// produced: BuildDashboard already sorts GitHub items new-first-then-recently-updated and the
// tasks tile by due date, and re-sorting here would silently put a task overdue by a week below
// one due this evening (FR-4.1 AC2).
func (s *Server) tileData(t domain.Tile, now, lastVisit time.Time) tileData {
	td := tileData{
		Name:     t.Name,
		Title:    t.Title,
		NewCount: t.NewCount,
		Stale:    t.Stale,
		Disabled: t.Disabled,
		// The error came from an upstream library and may quote a URL, a header or a token.
		// Nothing reaches the page without going through Redact (QS-4.3).
		Error:    Redact(s.cfg.Secrets, t.Error),
		LastOK:   newTimeView(t.LastOKAt, now),
		PollSecs: s.pollSeconds(),
	}
	for _, it := range t.Items {
		td.Items = append(td.Items, newItemView(it, now, it.IsNew(lastVisit)))
	}
	for _, b := range t.Builds {
		td.Builds = append(td.Builds, newBuildView(b, now))
	}
	for _, site := range t.Sites {
		td.Sites = append(td.Sites, siteView{
			Site:  site.Site,
			Week:  newWindowView(site.Week, 7),
			Month: newWindowView(site.Month, 30),
		})
	}
	return td
}

func newTimeView(t, now time.Time) timeView {
	if t.IsZero() {
		return timeView{}
	}
	return timeView{
		Known:    true,
		Absolute: t.UTC().Format(time.RFC3339),
		Relative: humanise(now.Sub(t)) + " ago",
	}
}

// newItemView renders one item. isNew is passed in rather than recomputed because the last-visit
// watermark belongs to the dashboard, not to the item.
func newItemView(it domain.Item, now time.Time, isNew bool) itemView {
	v := itemView{
		Title:    it.Title,
		URL:      it.URL,
		Repo:     it.Repo,
		Number:   it.Number,
		Kind:     kindLabel(it.Kind),
		Author:   it.Author,
		New:      isNew,
		Created:  newTimeView(it.CreatedAt, now),
		Updated:  newTimeView(it.UpdatedAt, now),
		Priority: priorityLabel(it.Priority),
	}
	if !it.DueAt.IsZero() {
		v.HasDue = true
		v.DueAt = it.DueAt.UTC().Format(time.RFC3339)
		if it.DueAt.Before(now) {
			v.Overdue = true
			v.Due = "overdue by " + humanise(now.Sub(it.DueAt))
		} else {
			v.Due = "due in " + humanise(it.DueAt.Sub(now))
		}
	}
	return v
}

func newBuildView(b domain.Build, now time.Time) buildView {
	v := buildView{
		Repo:       b.Repo,
		Workflow:   b.Workflow,
		RunURL:     b.RunURL,
		Conclusion: spaced(b.Conclusion),
		Finished:   newTimeView(b.FinishedAt, now),
	}
	// Status is the newest run's; Conclusion is the newest *completed* run's. A newest run that
	// has not completed is therefore a run in progress sitting on top of an older outcome, which
	// is exactly what FR-2.3 AC2 asks to be shown side by side.
	if b.Status != "" && b.Status != "completed" {
		v.Running = true
		v.Label = "running"
		v.Class = "running"
		return v
	}
	v.Label = v.Conclusion
	v.Class = conclusionClass(b.Conclusion)
	if v.Label == "" {
		v.Label = "no completed run"
	}
	return v
}

// newWindowView renders one Plausible window. A window that never arrived is the zero Metric,
// which the domain marks by an empty Site — the one thing that distinguishes it from a real
// result of zero visitors.
func newWindowView(m domain.Metric, days int) windowView {
	if m.Site == "" {
		return windowView{Days: days}
	}
	visitorPct, visitorKnown := m.VisitorChange()
	pageviewPct, pageviewKnown := m.PageviewChange()
	return windowView{
		Known:          true,
		Days:           days,
		Visitors:       m.Visitors,
		Pageviews:      m.Pageviews,
		VisitorChange:  newChangeView(visitorPct, visitorKnown),
		PageviewChange: newChangeView(pageviewPct, pageviewKnown),
	}
}

// newChangeView formats a percentage change (FR-3.1 AC2). The direction is a word rather than a
// sign or an arrow, for two reasons: colour is then never the only carrier of the meaning
// (FR-1.5 AC2), and a screen reader says "up twenty per cent" instead of spelling a glyph. An
// unknown change — the preceding period was zero, so a jump from nothing is not a percentage —
// says so instead of rendering as 0%.
func newChangeView(percent float64, known bool) changeView {
	if !known {
		return changeView{Direction: "unknown", Text: "no baseline"}
	}
	switch {
	case percent > 0:
		return changeView{Known: true, Direction: "up", Text: fmt.Sprintf("up %.1f%%", percent)}
	case percent < 0:
		return changeView{Known: true, Direction: "down", Text: fmt.Sprintf("down %.1f%%", -percent)}
	default:
		return changeView{Known: true, Direction: "flat", Text: "no change"}
	}
}

// ---------------------------------------------------------------- formatting

// humanise renders a duration as the coarse phrase the page shows, without a direction: callers
// append " ago", "due in " or "overdue by ". It is deliberately imprecise. The dashboard answers
// "does anything need me right now", and "3 hours" answers that better than "2h51m17s".
func humanise(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	const day = 24 * time.Hour
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return quantity(int(d/time.Minute), "minute")
	case d < day:
		return quantity(int(d/time.Hour), "hour")
	case d < 30*day:
		return quantity(int(d/day), "day")
	case d < 365*day:
		return quantity(int(d/(30*day)), "month")
	default:
		return quantity(int(d/(365*day)), "year")
	}
}

func quantity(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

func kindLabel(k domain.Kind) string {
	switch k {
	case domain.KindIssue:
		return "issue"
	case domain.KindPR:
		return "pull request"
	case domain.KindTask:
		return "task"
	default:
		return string(k)
	}
}

// priorityLabel maps Todoist's API priority — where 1 is natural and 4 is urgent — onto the p1..p4
// labels the Todoist interface itself shows, in which p1 is the urgent one. Anything outside the
// documented range is rendered as nothing rather than as a wrong label.
func priorityLabel(p int) string {
	if p < 1 || p > 4 {
		return ""
	}
	return "P" + strconv.Itoa(5-p)
}

// conclusionClass groups a GitHub Actions conclusion into the four states the stylesheet knows
// about. The text is always shown as well, so the class only ever adds colour to a meaning that
// is already in words (FR-1.5 AC2).
func conclusionClass(conclusion string) string {
	switch conclusion {
	case "success":
		return "ok"
	case "failure", "timed_out", "startup_failure":
		return "fail"
	case "":
		return "unknown"
	default:
		return "other"
	}
}

// spaced turns GitHub's snake_case enumerations into readable words.
func spaced(s string) string {
	out := make([]byte, len(s))
	for i := range len(s) {
		if s[i] == '_' {
			out[i] = ' '
			continue
		}
		out[i] = s[i]
	}
	return string(out)
}
