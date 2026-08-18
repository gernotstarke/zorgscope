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
		// The total count is not inside any tile — it is the tab title (FR-1.2 AC3) and the
		// summary line — so a swap that carried only the tile would leave both showing the figure
		// the page was loaded with, and the page would contradict itself from the first poll on.
		// They travel back with the fragment as htmx out-of-band swaps, addressed by id, which
		// keeps this a property of the markup: no inline script, which the CSP forbids anyway
		// (QS-4.4), and nothing to run for a visitor who has JavaScript switched off, for whom a
		// full page load is the only thing that ever happens and is correct on its own
		// (FR-1.3 AC3).
		for _, oob := range []struct {
			name string
			data any
		}{
			{"tab-title", titleView{NewCount: d.NewTotal}},
			{"dash-summary", newSummaryView(d, true)},
		} {
			if err := s.tiles.ExecuteTemplate(&buf, oob.name, oob.data); err != nil {
				s.fail(w, r, "rendering a tile", err)
				return
			}
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

// noAuthNotice is FR-8.2 AC2's case: the source's secret is not in the environment. The
// identifier avoids the word the sentence itself uses, because gosec's G101 reads any constant
// named after a credential as being one.
const noAuthNotice = "no credential is configured for this source"

// tileCredential maps a tile to the configured source whose credential and watch list decide
// whether it is dark. Both GitHub tiles hang off the one entry, because issues and builds are two
// fetchers over a single token.
var tileCredential = map[string]string{
	"github": "github",
	"builds": "github",
	"sites":  "plausible",
	"tasks":  "todoist",
}

// disabledReason says why a tile is dark, in words that point at the thing that is actually
// missing.
//
// Config.Enabled answers one question with two meanings: it is false when the credential is absent
// and also when there is nothing configured to watch — no repositories, no sites, no filter. The
// tile used to render both as "no credential is configured", so an operator who had commented out
// the repos: list while testing was sent hunting a Fly secrets problem that did not exist, with a
// correct token in place. Enabled is where the two are conflated and it may not be changed here,
// so the distinction is drawn where the sentence is written: the credential is checked first, and
// only when it is present does the empty watch list get named.
func (s *Server) disabledReason(tile string) string {
	switch tileCredential[tile] {
	case "github":
		if s.cfg.Secrets.GitHubToken == "" {
			return noAuthNotice
		}
		return "no repositories are configured to watch"
	case "plausible":
		if s.cfg.Secrets.PlausibleKey == "" {
			return noAuthNotice
		}
		return "no sites are configured to watch"
	case "todoist":
		if s.cfg.Secrets.TodoistToken == "" {
			return noAuthNotice
		}
		return "no task filter is configured"
	}
	return noAuthNotice
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
	NewTotal int
	// LastRun is when the run the header describes reached the state LastRunState names, and
	// LastRunState is which of "never", "running", "succeeded" and "failed" that is. A time on
	// its own cannot carry it: the same zero means "never" and "still running", and the same
	// stamp means "succeeded then" and "failed then" (FR-1.1 AC3).
	LastRun      timeView
	LastRunState string
	// LastRunDetail is the run record's per-source outcome, already scrubbed of every configured
	// secret (QS-4.3). It is quiet on the page and is the only place a blocked Slack webhook
	// shows up at all.
	LastRunDetail string
	LastVisit     timeView
	Tiles         []tileData
}

// Summary is the summary line's own data, so that the line the page draws and the one a poll
// swaps back in are the same markup executed twice rather than two copies that can drift.
func (v dashboardView) Summary() summaryView {
	return summaryView{NewTotal: v.NewTotal, LastVisit: v.LastVisit}
}

// titleView is the tab title (FR-1.2 AC3), and summaryView the "N new since your last visit" line.
// Both are outside every tile and both state the *total*, so a poll that swapped a tile without
// them would leave the page disagreeing with itself. The fragment therefore carries both — the
// title as a plain <title>, which htmx lifts out of any response and applies to the document, the
// summary as an out-of-band swap, which is what OOB marks. See templates/tiles/counts.html.
type titleView struct {
	Title    string
	NewCount int
}

type summaryView struct {
	OOB       bool
	NewTotal  int
	LastVisit timeView
}

// TitleView lets the layout draw its <title> from the same block the polled fragment sends back.
func (d pageData) TitleView() titleView {
	return titleView{Title: d.Title, NewCount: d.NewCount}
}

func newSummaryView(d domain.Dashboard, oob bool) summaryView {
	return summaryView{
		OOB:       oob,
		NewTotal:  d.NewTotal,
		LastVisit: newTimeView(d.LastVisitAt, d.GeneratedAt),
	}
}

// tileData is one tile, and is what both the page and the /tile/{source} fragment execute the
// "tile" template with — the fragment is therefore identical to the tile the page drew.
type tileData struct {
	Name     string
	Title    string
	NewCount int
	Stale    bool
	Disabled bool
	// DisabledReason is why the tile is dark, set only when Disabled is. It names the credential
	// or the empty watch list, whichever is actually missing — never both, and never the wrong
	// one (FR-8.2 AC2).
	DisabledReason string
	// Error is upstream error text, already scrubbed of every configured secret (QS-4.3).
	Error    string
	LastOK   timeView
	PollSecs int
	Items    []itemView
	Builds   []buildView
	Sites    []siteView
	// HasContent says whether this tile has anything stored to show, whatever its health. A
	// disabled source holding items is the case that needs it: the tile must say that what is
	// below is the last stored data rather than pretend nothing was ever fetched.
	HasContent bool
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
		NewTotal:     d.NewTotal,
		LastRun:      newTimeView(d.LastRunAt, d.GeneratedAt),
		LastRunState: string(d.LastRun),
		// The detail is assembled from upstream error text — a source's failure message and the
		// reason an announcement did not go out — and it is rendered, so it goes through Redact
		// like every other borrowed string on this page (QS-4.3).
		LastRunDetail: Redact(s.cfg.Secrets, d.LastRunDetail),
		LastVisit:     newTimeView(d.LastVisitAt, d.GeneratedAt),
		Tiles:         make([]tileData, 0, len(d.Tiles)),
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
	if t.Disabled {
		td.DisabledReason = s.disabledReason(t.Name)
	}
	for _, it := range t.Items {
		td.Items = append(td.Items, newItemView(it, now, it.IsNew(lastVisit)))
	}
	for _, b := range t.Builds {
		td.Builds = append(td.Builds, newBuildView(b, now))
	}
	if t.Name == "sites" {
		td.Sites = s.orderedSites(t.Sites)
	}
	td.HasContent = len(td.Items) > 0 || len(td.Builds) > 0 || len(td.Sites) > 0
	return td
}

// orderedSites lists the sites the sites tile shows in the order configuration names them
// (FR-3.1 AC3), rather than in whatever order the store happened to return their metrics in — an
// ORDER BY added to the query, or a driver returning rows differently, would otherwise silently
// re-order the tile.
//
// A configured site that has no metrics at all still gets a row, with both windows reading as
// missing. Dropping it would be the wrong answer to "I configured this site and cannot see it":
// the tile would look complete while a site was quietly absent. A site with metrics that
// configuration does not name — a site removed from the YAML whose rows are still stored — is
// listed after the configured ones rather than hidden.
func (s *Server) orderedSites(sites []domain.SiteMetrics) []siteView {
	byName := make(map[string]domain.SiteMetrics, len(sites))
	for _, sm := range sites {
		byName[sm.Site] = sm
	}

	out := make([]siteView, 0, len(sites)+len(s.cfg.Plausible.Sites))
	done := make(map[string]bool, len(out))
	for _, name := range s.cfg.Plausible.Sites {
		if done[name] {
			continue
		}
		done[name] = true
		out = append(out, newSiteView(name, byName[name]))
	}
	for _, sm := range sites {
		if done[sm.Site] {
			continue
		}
		done[sm.Site] = true
		out = append(out, newSiteView(sm.Site, sm))
	}
	return out
}

func newSiteView(name string, sm domain.SiteMetrics) siteView {
	return siteView{
		Site:  name,
		Week:  newWindowView(sm.Week, 7),
		Month: newWindowView(sm.Month, 30),
	}
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
