package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	f := parseFilter(r.URL.Query(), s.loc)
	d, err := s.dashboard(r.Context(), f)
	if err != nil {
		s.fail(w, r, "assembling the dashboard", err)
		return
	}
	view := s.dashboardView(d)
	s.render(w, r, http.StatusOK, "dashboard.html", pageData{
		// The dashboard is the site, so the tab reads "zorgscope" rather than
		// "Dashboard · zorgscope". NewCount supplies FR-1.2 AC3's prefix, and it is the total
		// rather than what the filter let through: a search box that moved the badge would make
		// the tab lie about what is new.
		Title:     "",
		NewCount:  d.NewTotal,
		Dashboard: &view,
	})
}

// handleItems renders the list on its own, without the layout, so htmx can swap it (FR-1.6 AC1)
// and so a template change never has to be made twice: the fragment a poll returns is the very
// same "items" template the page composed itself from.
//
// It reads the filter from its own query string, which is the one the list was drawn with — the
// poll carries it back — so a tab left open on a narrowed list keeps polling that list rather
// than quietly widening to everything.
func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	f := parseFilter(r.URL.Query(), s.loc)
	d, err := s.dashboard(r.Context(), f)
	if err != nil {
		s.fail(w, r, "assembling the list", err)
		return
	}

	var buf bytes.Buffer
	if err := s.fragments.ExecuteTemplate(&buf, "items", s.itemsView(d)); err != nil {
		s.fail(w, r, "rendering the list", err)
		return
	}
	// None of the four blocks below is inside the list — they are the tab title (FR-1.2 AC3), the
	// summary line, the warning box and the build indicator — so a swap that carried only the list
	// would leave all four showing the figures the page was loaded with, and the page would
	// contradict itself from the first poll on. They travel back with the fragment as htmx
	// out-of-band swaps, addressed by id, which keeps this a property of the markup: no inline
	// script, which the CSP forbids anyway (QS-4.4), and nothing to run for a visitor who has
	// JavaScript switched off, for whom a full page load is the only thing that ever happens and is
	// correct on its own (FR-1.3 AC3).
	for _, oob := range []struct {
		name string
		data any
	}{
		{"tab-title", titleView{NewCount: d.NewTotal}},
		{"dash-summary", newSummaryView(d, true)},
		// FR-1.4: a source that starts failing while a tab sits open has to raise the box then,
		// not at the next page load — which on a dashboard meant to be left open may be tomorrow.
		{"dash-alert", newAlertView(d, true)},
		// The build indicator is outside the list too, and a build that breaks while the tab sits
		// open has to turn it red then rather than at the next page load.
		{"build-status", s.buildStatusView(d, true)},
	} {
		if err := s.fragments.ExecuteTemplate(&buf, oob.name, oob.data); err != nil {
			s.fail(w, r, "rendering the list", err)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
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

// handleProblems renders the details page behind the dashboard's warning box (FR-1.4): every
// external interface, what state it is in, and — for the ones that are failing — the upstream
// text, scrubbed of every configured secret (QS-4.3).
//
// It reads the same assembled dashboard the main page does, so the two can never disagree about
// what is wrong: the box that sent the visitor here and the list that greets them are two
// renderings of one Dashboard, not two independent judgements of health.
func (s *Server) handleProblems(w http.ResponseWriter, r *http.Request) {
	// Unfiltered: this page is about the interfaces, not about the items, and a filter carried
	// over from the front page would narrow nothing here while implying it had.
	d, err := s.dashboard(r.Context(), domain.Filter{})
	if err != nil {
		s.fail(w, r, "assembling the problem details", err)
		return
	}
	view := s.dashboardView(d)
	s.render(w, r, http.StatusOK, "problems.html", pageData{
		Title:     "Warnings and errors",
		NewCount:  d.NewTotal,
		Dashboard: &view,
	})
}

// handleBuilds renders the page behind the front page's build indicator (FR-2.3): one row per
// watched repository, worst first, with the workflow, the outcome and a link to the run.
//
// It exists because the front page does not. The dashboard answers "does anything need me right
// now", and for builds that answer is one colour — a list of one row per repository is a report,
// and a report on the front page pushes the issues and pull requests that do need handling below
// the fold.
func (s *Server) handleBuilds(w http.ResponseWriter, r *http.Request) {
	// Unfiltered, for the same reason /problems is: the rows here are repositories, not items.
	d, err := s.dashboard(r.Context(), domain.Filter{})
	if err != nil {
		s.fail(w, r, "assembling the build details", err)
		return
	}
	view := s.dashboardView(d)
	s.render(w, r, http.StatusOK, "builds.html", pageData{
		Title:     "Build status",
		NewCount:  d.NewTotal,
		Dashboard: &view,
	})
}

// ---------------------------------------------------------------- assembly

// dashboard reads everything the page needs from the store and hands it to the domain, narrowed by
// f. Every read is a plain query against stored data; nothing here talks to an upstream service
// (FR-1.1 AC2).
//
// The filter is handed to the domain rather than applied here, because the counts it must not
// change — the badge, the tab title, each repository's total — are counted in the same pass
// (FR-1.2).
func (s *Server) dashboard(ctx context.Context, f domain.Filter) (domain.Dashboard, error) {
	items, err := s.store.Items(ctx)
	if err != nil {
		return domain.Dashboard{}, fmt.Errorf("reading items: %w", err)
	}
	builds, err := s.store.Builds(ctx)
	if err != nil {
		return domain.Dashboard{}, fmt.Errorf("reading builds: %w", err)
	}
	states, err := s.store.SourceStates(ctx)
	if err != nil {
		return domain.Dashboard{}, fmt.Errorf("reading source states: %w", err)
	}
	lastRun, err := s.store.LastRun(ctx)
	if err != nil {
		return domain.Dashboard{}, fmt.Errorf("reading the last refresh run: %w", err)
	}
	// A second read rather than a filter over the first: LastRun is the most recently started run,
	// so when it failed or is still open the last run that actually worked is not in it at all —
	// and that time is what FR-1.1 AC3 puts in the header.
	lastOK, err := s.store.LastSuccessfulRun(ctx)
	if err != nil {
		return domain.Dashboard{}, fmt.Errorf("reading the last successful refresh run: %w", err)
	}
	lastVisit, err := s.store.LastVisit(ctx)
	if err != nil {
		return domain.Dashboard{}, fmt.Errorf("reading the last visit: %w", err)
	}

	return domain.BuildDashboard(domain.DashboardInput{
		Now:               s.clock.Now(),
		LastVisitAt:       lastVisit,
		LastRun:           lastRun,
		LastSuccessfulRun: lastOK,
		StaleAfter:        s.cfg.Refresh.StaleAfter,
		Items:             items,
		Builds:            builds,
		States:            states,
		Disabled:          s.disabledSources(),
		// Configuration order, so the list groups the way the YAML reads and the details page can
		// report a repository that has never produced a workflow run — the store holds only
		// repositories that have.
		Repos:  s.cfg.GitHub.Repos,
		Filter: f,
	}), nil
}

// disabledSources names the fetchers with no credential, in the source vocabulary the domain's
// States and Disabled use (FR-8.2 AC2). GitHub contributes two fetcher names because issues and
// builds are two fetchers over one credential: without the token both go dark, and naming only
// "github" would leave the build indicator claiming to be merely stale.
func (s *Server) disabledSources() []string {
	var out []string
	if !s.cfg.Enabled("github") {
		out = append(out, "github", "github-builds")
	}
	// Slack is not a fetcher and so is not a case Config.Enabled knows about, but the problems
	// page lists it as an external interface all the same — it is the one service whose failures
	// never fail a run (FR-6.1 AC3) and therefore the one most worth naming. The two conditions
	// are the same ones buildNotifier in cmd/zorgscope applies when it decides whether to build a
	// notifier at all, so the page says "not configured" exactly when nothing is being sent.
	if !s.cfg.Notifications.Slack.Enabled || s.cfg.Secrets.SlackWebhook == "" {
		out = append(out, "slack")
	}
	return out
}

// noAuthNotice is FR-8.2 AC2's case: the source's secret is not in the environment. The
// identifier avoids the word the sentence itself uses, because gosec's G101 reads any constant
// named after a credential as being one.
const noAuthNotice = "no credential is configured for this source"

// githubDisabledReason says why there is nothing to show, in words that point at the thing that is
// actually missing.
//
// Config.Enabled answers one question with two meanings: it is false when the credential is absent
// and also when there is nothing configured to watch. The page used to render both as "no
// credential is configured", so an operator who had commented out the repos: list while testing
// was sent hunting a Fly secrets problem that did not exist, with a correct token in place.
// Enabled is where the two are conflated and it may not be changed here, so the distinction is
// drawn where the sentence is written: the credential is checked first, and only when it is
// present does the empty watch list get named.
func (s *Server) githubDisabledReason() string {
	if s.cfg.Secrets.GitHubToken == "" {
		return noAuthNotice
	}
	return "no repositories are configured to watch"
}

// pollSeconds is the htmx poll interval for the list (FR-1.6 AC1): the configured refresh
// interval, because the list cannot change between refreshes and polling faster would only wake a
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
	// LastSuccess is when the last refresh that actually worked finished, and is unknown until
	// one has. The templates print it beside a running or a failed state — the two states in
	// which LastRun belongs to some other run than the one that put the data on the page — so
	// that FR-1.1 AC3's time is on the header whenever it exists, without being said twice when
	// the latest run is itself the successful one.
	LastSuccess timeView
	// LastRunDetail is the run record's per-source outcome, already scrubbed of every configured
	// secret (QS-4.3). It is quiet on the page and is the only place a blocked Slack webhook
	// shows up at all.
	LastRunDetail string
	LastVisit     timeView
	// Filter is the filter as the visitor left it, echoed back so the form renders in the state
	// the page was asked for, and Repos is what its repository list offers — configuration order,
	// because that is the order the groups below it appear in, followed by the applied filter's
	// own repository when configuration no longer names it. See filterRepos.
	Filter filterView
	Repos  []string
	// Items is the list itself, and is what both this page and GET /items execute the "items"
	// template with, so the two can never draw different markup (FR-1.6 AC1).
	Items itemsView
	// Problems is every external interface's health, worst first, and ProblemCount how many of
	// them are actually wrong. The count is what the dashboard's warning box is drawn from: a
	// deployment with a source switched off is not a deployment with a problem, and a box that
	// appeared for it would be dismissed by the second day (FR-1.4).
	Problems     []problemView
	ProblemCount int
	// Alert is the warning box the dashboard draws, empty when nothing is wrong.
	Alert alertView
	// Builds is the front page's build indicator and, on the details page, its rows.
	Builds buildStatusView
}

// alertView is the warning box (FR-1.4). Like summaryView it carries an OOB flag, because the
// page draws it and a polled tile sends the same block back: trouble that starts between two page
// loads has to reach a tab that is sitting open, which is the only kind of tab this dashboard
// really has.
type alertView struct {
	OOB bool
	// Count is how many interfaces are wrong; zero renders an empty slot rather than a box.
	Count int
	// Severity is the worst of them, which is what the box is coloured by.
	Severity string
	// Summary is the sentence, already in the right grammatical number.
	Summary string
}

// newAlertView reduces the assembled dashboard to what the box needs. Problems arrive worst first,
// so the first entry that is wrong is the worst one.
func newAlertView(d domain.Dashboard, oob bool) alertView {
	v := alertView{OOB: oob, Count: d.ProblemCount}
	if v.Count == 0 {
		return v
	}
	for _, p := range d.Problems {
		if p.Wrong() {
			v.Severity = string(p.Severity)
			break
		}
	}
	v.Summary = problemSentence(d.ProblemCount)
	return v
}

// problemSentence says how many services are affected rather than naming them: the box is a
// signpost, and the names, the times and the upstream text are all one click away on /problems.
func problemSentence(n int) string {
	if n == 1 {
		return "1 external service needs attention"
	}
	return strconv.Itoa(n) + " external services need attention"
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

// itemsView is the whole list section: the source's health above it, and one group per
// repository. It is what both the page and the GET /items fragment execute the "items" template
// with, so the fragment is identical to the list the page drew.
type itemsView struct {
	// Query is the filter as a query string — "?kind=pr&q=header", or "" — and is what the poll
	// re-fetches itself with, so an open tab keeps polling the list it is showing rather than
	// widening to everything at the first tick. It is a template.URL because it is a URL fragment
	// composed here rather than borrowed text; url.Values.Encode escapes every value in it.
	Query          template.URL
	PollSecs       int
	Source         sourceView
	DisabledReason string
	Groups         []groupView
	// Filtered says whether a filter is in force, which is the difference between "nothing
	// matches this filter" and "nothing is open".
	Filtered  bool
	ShownLine string // "12 of 40 open" when filtered, "40 open" otherwise
}

// sourceView is the GitHub source's own health, said above the list rather than on a tile: there
// is one source, so its state is the state of everything below it.
type sourceView struct {
	Disabled, Stale bool
	// Error is upstream error text, already scrubbed of every configured secret (QS-4.3).
	Error  string
	LastOK timeView
}

// groupView is one repository's block of the list. NewCount and CountLine are taken before the
// filter is applied, so narrowing the list never makes the page understate what is out there
// (FR-1.2).
type groupView struct {
	Repo      string
	NewCount  int
	CountLine string // "3 of 7" when filtered, "7" otherwise
	Items     []itemView
}

// filterView is the filter as the form renders it: every field a string, because that is what an
// input carries, and Since in the form <input type="date"> submits.
type filterView struct {
	Repo, Text, Since string
	Kind              string // "", "issue" or "pr"
}

// problemView is one external interface on the warning box and the details page. Severity is the
// machine word the stylesheet keys off; Label is the same state in a word a person reads, so
// colour is never the only carrier of the meaning (FR-1.5 AC2). Detail is upstream text that has
// already been through Redact (QS-4.3).
type problemView struct {
	Source   string
	Title    string
	Severity string
	Label    string
	Summary  string
	Detail   string
	At       timeView
	LastOK   timeView
	// Wrong marks the entries the warning box counts: errors and warnings, never an interface
	// that is simply switched off.
	Wrong bool
}

// timeView is one timestamp rendered twice: an absolute stamp for the <time> element's machine
// attribute, and the relative phrase a person reads. Known is false for a zero time, which is the
// difference between "never" and "at the epoch".
type timeView struct {
	Known    bool
	Absolute string
	Relative string
}

// itemView is one row of the list.
type itemView struct {
	Title string
	// Summary is the first line or so of the item's own description, already cut to the length
	// the page shows it at. It is borrowed text and is escaped like every other borrowed string
	// here — never a template.HTML.
	Summary string
	URL     string
	Number  int
	Kind    string
	Author  string
	New     bool
	Created timeView
	Updated timeView
}

// buildView is one row of the build details page.
type buildView struct {
	Repo     string
	Workflow string
	RunURL   string
	// Health is the machine word the stylesheet keys off — "broken", "warning" or "ok" — and
	// Label the same state in words, so colour is never the only carrier of the meaning
	// (FR-1.5 AC2).
	Health string
	// Running is a run in progress; Conclusion then holds the previous run's outcome, which is
	// shown next to it (FR-2.3 AC2).
	Running    bool
	Conclusion string
	Label      string
	Finished   timeView
	// Badge is the badge image this page draws for the repository: a data URI over the bytes the
	// refresh run stored, empty when there is no badge. See badgeDataURI.
	Badge template.URL
}

// ---------------------------------------------------------------- view construction

// dashboardView turns the assembled domain dashboard into the page's presentation data.
func (s *Server) dashboardView(d domain.Dashboard) dashboardView {
	v := dashboardView{
		NewTotal:     d.NewTotal,
		LastRun:      newTimeView(d.LastRunAt, d.GeneratedAt),
		LastRunState: string(d.LastRun),
		LastSuccess:  newTimeView(d.LastSuccessAt, d.GeneratedAt),
		// The detail is assembled from upstream error text — a source's failure message and the
		// reason an announcement did not go out — and it is rendered, so it goes through Redact
		// like every other borrowed string on this page (QS-4.3).
		LastRunDetail: Redact(s.cfg.Secrets, d.LastRunDetail),
		LastVisit:     newTimeView(d.LastVisitAt, d.GeneratedAt),
		Filter:        newFilterView(d.Filter),
		Repos:         filterRepos(s.cfg.GitHub.Repos, d.Filter.Repo),
		Items:         s.itemsView(d),
		ProblemCount:  d.ProblemCount,
		Problems:      make([]problemView, 0, len(d.Problems)),
		Alert:         newAlertView(d, false),
		Builds:        s.buildStatusView(d, false),
	}
	for _, p := range d.Problems {
		v.Problems = append(v.Problems, s.problemView(p, d.GeneratedAt))
	}
	return v
}

// problemView renders one interface's health. The upstream error goes through Redact for the same
// reason every other borrowed string on these pages does: it was written by a library talking to
// a service this process authenticates against, and it may quote the credential it used (QS-4.3).
func (s *Server) problemView(p domain.Problem, now time.Time) problemView {
	return problemView{
		Source:   p.Source,
		Title:    p.Title,
		Severity: string(p.Severity),
		Label:    severityLabel(p.Severity),
		Summary:  p.Summary,
		Detail:   Redact(s.cfg.Secrets, p.Detail),
		At:       newTimeView(p.At, now),
		LastOK:   newTimeView(p.LastOKAt, now),
		Wrong:    p.Wrong(),
	}
}

// severityLabel is the word shown beside the state's colour. Every severity has one, including
// the healthy state: the details page lists healthy interfaces too, so that "nothing is wrong"
// is something the page says rather than something the reader has to infer from an empty list.
func severityLabel(sev domain.Severity) string {
	switch sev {
	case domain.SeverityError:
		return "Error"
	case domain.SeverityWarning:
		return "Warning"
	case domain.SeverityOff:
		return "Not configured"
	case domain.SeverityOK:
		return "OK"
	default:
		return string(sev)
	}
}

// itemsView turns the assembled dashboard into the list section. It preserves the order the
// domain produced — BuildDashboard groups in configuration order and sorts each group new first,
// then most recently updated (FR-2.2 AC2) — because re-sorting here would silently disagree with
// the counts the same pass produced.
func (s *Server) itemsView(d domain.Dashboard) itemsView {
	v := itemsView{
		Query:    template.URL(queryString(d.Filter)), // #nosec G203 -- see the field's comment
		PollSecs: s.pollSeconds(),
		Source: sourceView{
			Disabled: d.Source.Disabled,
			Stale:    d.Source.Stale,
			// The error came from an upstream library and may quote a URL, a header or a token.
			// Nothing reaches the page without going through Redact (QS-4.3).
			Error:  Redact(s.cfg.Secrets, d.Source.Error),
			LastOK: newTimeView(d.Source.LastOKAt, d.GeneratedAt),
		},
		Groups:    make([]groupView, 0, len(d.Groups)),
		Filtered:  !d.Filter.Empty(),
		ShownLine: shownLine(d),
	}
	if d.Source.Disabled {
		v.DisabledReason = s.githubDisabledReason()
	}
	for _, g := range d.Groups {
		gv := groupView{
			Repo:      g.Repo,
			NewCount:  g.NewCount,
			CountLine: countLine(len(g.Items), g.Total, v.Filtered),
			Items:     make([]itemView, 0, len(g.Items)),
		}
		for _, it := range g.Items {
			gv.Items = append(gv.Items, newItemView(it, d.GeneratedAt, it.IsNew(d.LastVisitAt)))
		}
		v.Groups = append(v.Groups, gv)
	}
	return v
}

// shownLine is the count above the list. It names the total whenever a filter is in force,
// because "12 open" and "12 of 150 open" are different news and only one of them is true.
func shownLine(d domain.Dashboard) string {
	if d.Filter.Empty() {
		return strconv.Itoa(d.Total) + " open"
	}
	return strconv.Itoa(d.Shown) + " of " + strconv.Itoa(d.Total) + " open"
}

// countLine is the same figure per repository, and is drawn from the group's unfiltered total for
// the reason FR-1.2 gives: the filter says what is being looked at, never what is out there.
func countLine(shown, total int, filtered bool) string {
	if !filtered {
		return strconv.Itoa(total)
	}
	return strconv.Itoa(shown) + " of " + strconv.Itoa(total)
}

// filterRepos is what the filter's repository list offers: the configured repositories, plus the
// one the applied filter names when configuration no longer does.
//
// A repository dropped from the YAML keeps the items it already had — the domain lists such a
// group after the configured ones rather than hiding it — so a filter naming one is a filter that
// selects something real. Built from configuration alone, the control could not show it: the page
// answered /?repo=org/retired with "all" selected, and then the next touch of any other control
// submitted the form with repo="" and silently threw the filter away. The option is appended
// rather than inserted, because it is not part of the configured order and pretending otherwise
// would move the entries a reader is used to finding in one place.
func filterRepos(configured []string, applied string) []string {
	if applied == "" {
		return configured
	}
	for _, r := range configured {
		if r == applied {
			return configured
		}
	}
	// A fresh slice: appending to the configured one would write into config's own backing array
	// whenever it has spare capacity, so one filtered request could change what every later
	// request is offered.
	out := make([]string, 0, len(configured)+1)
	out = append(out, configured...)
	return append(out, applied)
}

// newFilterView renders the filter back into the strings its form fields carry. An unset date is
// the empty string rather than a zero time formatted, which the date input would refuse anyway.
func newFilterView(f domain.Filter) filterView {
	v := filterView{Repo: f.Repo, Kind: string(f.Kind), Text: f.Text}
	if !f.CreatedSince.IsZero() {
		v.Since = f.CreatedSince.Format(dateLayout)
	}
	return v
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
		Title:   it.Title,
		Summary: summaryLine(it.Summary),
		URL:     it.URL,
		Number:  it.Number,
		Kind:    kindLabel(it.Kind),
		Author:  it.Author,
		New:     isNew,
		Created: newTimeView(it.CreatedAt, now),
		Updated: newTimeView(it.UpdatedAt, now),
	}
	return v
}

func newBuildView(b domain.Build, now time.Time) buildView {
	v := buildView{
		Repo:       b.Repo,
		Workflow:   b.Workflow,
		RunURL:     b.RunURL,
		Health:     string(b.Health()),
		Conclusion: spaced(b.Conclusion),
		Finished:   newTimeView(b.FinishedAt, now),
		// Status is the newest run's; Conclusion is the newest *completed* run's. A newest run
		// that has not completed is a run in progress sitting on top of an older outcome, which
		// is exactly what FR-2.3 AC2 asks to be shown side by side.
		Running: b.Running(),
		Badge:   badgeDataURI(b),
	}

	switch {
	case b.Silent():
		// A configured repository whose workflow has never fired. "No completed run" would
		// suggest there were runs; there were none, and that is the thing worth reading.
		v.Label = "no build information"
	case v.Conclusion != "":
		v.Label = v.Conclusion
	case v.Running:
		// A first run, still going: there is no earlier outcome to name beside it.
		v.Label = "running"
	default:
		v.Label = "no completed run"
	}
	return v
}

// badgeDataURI renders a stored badge as an image the page carries itself, or "" when the build
// has no badge.
//
// The badge travels in the page rather than being linked, which is the whole point of storing it
// (FR-2.3 AC5): a linked badge is a third-party request made while the page is being read, and on
// this page there were eight of them — arriving late, rate limited, or blocked outright by
// anything in the browser that filters other people's images, all of which look identical to the
// reader. Carried in the page there is no request at all and nothing to wait for.
//
// It is an <img> and never inline markup. The bytes come from outside and are only ever handed to
// the browser as an image, which draws SVG in a sandbox: no script, no network, nothing that
// reaches the document around it. Inlining the same bytes into the DOM would put someone else's
// markup inside this page, which the Content-Security-Policy would mostly contain and which there
// is no reason to lean on it for.
//
// The result is template.URL because html/template refuses a data: URI otherwise, replacing it
// with #ZgotmplZ. That is a sanitiser being bypassed, so what replaces it has to be safe by
// construction: the scheme and the media type are literals here, and everything after them is
// base64, whose alphabet cannot close the attribute or leave the URL.
func badgeDataURI(b domain.Build) template.URL {
	if len(b.Badge) == 0 {
		return ""
	}
	// #nosec G203 -- the scheme and media type are literals and the rest is base64, whose
	// alphabet cannot close the attribute or introduce another scheme; attribute escaping still
	// applies on top, since template.URL suppresses URL sanitising and nothing else.
	return template.URL("data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(b.Badge))
}

// buildStatusView is the front page's indicator and, on the details page, the rows behind it.
// Like the other blocks that live outside a tile it carries an OOB flag, because a poll sends the
// same block back — a build that breaks while the tab sits open has to turn the indicator red
// then, not at whatever hour the page is next loaded.
type buildStatusView struct {
	OOB    bool
	Health string
	Label  string
	// Summary states the counts in a sentence, so the indicator is readable without its colour
	// (FR-1.5 AC2).
	Summary                             string
	Broken, Warning, OK, Running, Total int
	Rows                                []buildView
	Stale                               bool
	Disabled                            bool
	DisabledReason                      string
	// Error is the source's own failure, already scrubbed of every configured secret (QS-4.3).
	Error  string
	LastOK timeView
}

// Wrong reports whether the indicator is showing something that needs handling.
func (v buildStatusView) Wrong() bool {
	return v.Health == string(domain.BuildBroken) || v.Health == string(domain.BuildWarning)
}

func (s *Server) buildStatusView(d domain.Dashboard, oob bool) buildStatusView {
	b := d.Builds
	v := buildStatusView{
		OOB:      oob,
		Health:   string(b.Health),
		Label:    buildHealthLabel(b.Health),
		Broken:   b.Broken,
		Warning:  b.Warning,
		OK:       b.OK,
		Running:  b.Running,
		Total:    b.Total(),
		Stale:    b.Stale,
		Disabled: b.Disabled,
		Error:    Redact(s.cfg.Secrets, b.Error),
		LastOK:   newTimeView(b.LastOKAt, d.GeneratedAt),
		Rows:     make([]buildView, 0, len(b.Rows)),
	}
	if b.Disabled {
		v.DisabledReason = s.githubDisabledReason()
	}
	for _, row := range b.Rows {
		v.Rows = append(v.Rows, newBuildView(row, d.GeneratedAt))
	}
	v.Summary = buildSummary(b)
	return v
}

// buildHealthLabel is the state in a word, beside its colour.
func buildHealthLabel(h domain.BuildHealth) string {
	switch h {
	case domain.BuildBroken:
		return "Broken"
	case domain.BuildWarning:
		return "Warnings"
	case domain.BuildOK:
		return "Green"
	default:
		return "Unknown"
	}
}

// buildSummary is the indicator's sentence. It always names the total, because "2 broken" and
// "2 broken of 3" are different news.
func buildSummary(b domain.BuildStatus) string {
	if b.Disabled {
		return "builds are not being fetched"
	}
	if b.Total() == 0 {
		return "no repositories are being watched"
	}
	switch b.Health {
	case domain.BuildBroken:
		return countOf(b.Broken, b.Total()) + " broken"
	case domain.BuildWarning:
		return countOf(b.Warning, b.Total()) + " need a look"
	default:
		return "all " + strconv.Itoa(b.Total()) + " repositories are green"
	}
}

// countOf renders "1 repository of 8" or "3 repositories of 8".
func countOf(n, total int) string {
	unit := " repositories of "
	if n == 1 {
		unit = " repository of "
	}
	return strconv.Itoa(n) + unit + strconv.Itoa(total)
}

// ---------------------------------------------------------------- formatting

// displaySummaryLen is how much of an item's description the list shows. Eighty characters is
// about one line in the small type it is set in — long enough to say what an issue is about,
// short enough that a column of them still reads as a list rather than as prose.
const displaySummaryLen = 80

// summaryLine cuts a stored summary down to what is displayed, on a word boundary, and marks that
// it was cut.
//
// The boundary is the point. Cutting at exactly eighty characters ends most lines mid-word, and a
// half word followed by an ellipsis reads as a rendering fault rather than as a deliberate
// abbreviation. Text with no space in the first eighty characters — a long identifier, a URL, a
// language that does not space its words — is cut at the limit instead, on a rune boundary, since
// the alternative is showing the whole of it.
func summaryLine(s string) string {
	if len(s) <= displaySummaryLen {
		return s
	}
	cut := displaySummaryLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if i := strings.LastIndexByte(s[:cut], ' '); i > displaySummaryLen/2 {
		cut = i
	}
	return strings.TrimRight(s[:cut], " ") + "…"
}

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
	default:
		return string(k)
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
