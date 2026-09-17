package web

import (
	"bytes"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/snapshot"
)

// fragmentItemsTemplate is the tmpl value handleItems passes to render, which is what tells render
// to answer with the bare "items" fragment rather than a whole page.
const fragmentItemsTemplate = "fragments/items.html"

// waitingPollID is the id of the wait page's polling element. htmx sends it as the HX-Trigger
// header of every poll, which is how a poll is told from the filter form's own htmx request:
// the poll is answered 204 while the fetch runs, the filter is answered from the snapshot.
const waitingPollID = "waiting"

// waitingView is what waiting.html renders: how many repositories are being asked, and the
// page's own path and query, which the poll asks for again.
type waitingView struct {
	Repos int
	Path  string
}

// answeredWaiting is the first thing a page handler does (FR-1.9). It asks the cache once — which
// starts a fetch when one is due and never waits for it (QS-2.6) — and hands the snapshot back to
// the caller either way, so a page never has to ask the cache a second time and risk that second
// call crossing the TTL a moment after the first, and rendering the ordinary page with Fetching
// true. While a fetch is in flight it also answers the request itself and reports so:
//
//   - an ordinary page view gets the wait page;
//   - the wait page's own poll gets 204, so htmx swaps nothing and the animation keeps running;
//   - any other htmx request — the filter form — is not answered here at all, and the caller
//     renders it from the current snapshot (AC5), since a wait page selected for #items would
//     empty the list.
//
// This rule assumes htmx is used only for fragments: it is what a request either being, or not
// being, the wait page's own poll comes down to. Adding hx-boost to the body would make ordinary
// navigations carry HX-Request: true as well and be answered from the snapshot rather than with
// the wait page, defeating FR-1.9 for every boosted link.
//
// When no fetch is in flight it answers nothing and the caller renders as it always has.
func (s *Server) answeredWaiting(w http.ResponseWriter, r *http.Request) (snapshot.Snapshot, bool) {
	snap := s.cache.Get(r.Context())
	if !snap.Fetching {
		return snap, false
	}
	if r.Header.Get("HX-Request") != "true" {
		// requireSession already sets no-store on every session route (see its own comment); this
		// Set restates, for a reader of this branch, that the guarantee covers the wait page too —
		// it must never come back out of a browser cache, since it is only ever right now.
		w.Header().Set("Cache-Control", "no-store")
		s.execute(w, r, http.StatusOK, "waiting.html", pageData{
			Waiting: &waitingView{Repos: len(s.cfg.GitHub.Repos), Path: r.URL.RequestURI()},
		})
		return snap, true
	}
	if r.Header.Get("HX-Trigger") == waitingPollID {
		w.WriteHeader(http.StatusNoContent)
		return snap, true
	}
	return snap, false
}

// ---------------------------------------------------------------- handlers

// handleDashboard renders the whole page; handleItems renders only the #items fragment for the
// htmx filter form. Both read the same session; handleDashboard's snapshot comes from
// answeredWaiting, and handleItems reads its own, since GET /items answers on its own rather than
// as part of a page view.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	snap, waiting := s.answeredWaiting(w, r)
	if waiting {
		return
	}
	s.render(w, r, "dashboard.html", snap)
}
func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, fragmentItemsTemplate, s.cache.Get(r.Context()))
}

// render assembles the dashboard from snap — the snapshot its caller already read from the cache —
// and the visitor's session, and executes tmpl with it. It contacts no upstream service itself
// (FR-1.1) and does not read the cache on its own: for a page, answeredWaiting has already dealt
// with a fetch in flight before render runs, and snap is the very snapshot it decided that with;
// the /items fragment is deliberately served from the current snapshot whatever the cache is
// doing (FR-1.9 AC5).
//
// It is not a visit: only POST /seen moves the seen-mark, so rendering the page never clears a
// badge on its own (FR-1.2 AC4).
func (s *Server) render(w http.ResponseWriter, r *http.Request, tmpl string, snap snapshot.Snapshot) {
	sess, _ := s.session(r) // requireSession already admitted the request
	now := s.clock.Now()

	d := domain.BuildDashboard(domain.DashboardInput{
		Now: now, LastVisitAt: sess.Seen, Items: snap.Items,
		Repos: s.cfg.GitHub.Repos, Filter: parseFilter(r.URL.Query(), s.loc),
	})
	view := s.dashboardView(d, snap, now, r)

	if tmpl == fragmentItemsTemplate {
		s.writeFragment(w, r, view.Items)
		return
	}
	s.execute(w, r, http.StatusOK, tmpl, pageData{
		NewCount:  view.NewTotal,
		Dashboard: &view,
	})
}

// handleSeen re-mints the cookie with seen = the fetched-at of the snapshot the visitor was shown
// and goes back to the page (FR-1.2).
//
// It is a plain form post and a redirect, so the badges clear whether or not JavaScript is running
// (FR-1.2 AC4).
//
// The mark only ever moves forwards. A tab left open on an older fetch still carries that fetch's
// seen_at, and pressing its button after a newer one was acknowledged elsewhere would otherwise turn
// items already seen back into NEW ones. Keeping the later of the two is still never later than the
// click, since the session's own mark was itself validated when it was set.
func (s *Server) handleSeen(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.session(r)
	if at := s.seenAt(r); at.After(sess.Seen) {
		sess.Seen = at
	}
	s.setSession(w, sess)
	// safeReturn is the sanitiser between the form field and the Location header; see handleTheme
	// for why the annotation is a trailing comment.
	http.Redirect(w, r, safeReturn(r.FormValue("return")), http.StatusSeeOther) // #nosec G710 -- sanitised by safeReturn
}

// seenAt is the moment "Mark all seen" stamps as seen: the "seen_at" field the form carries,
// which the header template (templates/fragments/header.html) fills in with the fetched-at of
// the snapshot that was actually on the page (see headerView.FetchedAtUnix) — not the moment
// of the click.
//
// The two differ by up to the cache TTL: a visitor can click the button seconds after an item was
// fetched that they never had the chance to see, or minutes after one that arrived in a fetch they
// never asked for. Stamping seen at the click would mark such an item seen sight unseen, and it
// would never show as NEW at all. Stamping it at the fetch instead means only what was actually on
// the page counts as acknowledged.
//
// A missing, unparsable, non-positive or future value falls back to now — a visitor must never end
// up with a seen-mark later than the moment they clicked, and a field this handler did not itself
// produce is not trusted blindly. This is safe to get wrong: seen_at only ever narrows what counts
// as NEW for this one visitor's own next view, and it travels inside the signed cookie only after
// this function has validated it, so a tampered form field can do no more than move the visitor's
// own watermark within these bounds.
func (s *Server) seenAt(r *http.Request) time.Time {
	now := s.clock.Now()
	raw := r.FormValue("seen_at")
	if raw == "" {
		return now
	}
	unix, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || unix <= 0 {
		return now
	}
	at := time.Unix(unix, 0)
	if at.After(now) {
		return now
	}
	return at
}

// handleRefresh throws the snapshot away so the redirected GET fetches (FR-1.3), and goes back to
// the page it was pressed on (FR-1.8 AC4).
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	s.cache.Invalidate()
	http.Redirect(w, r, safeReturn(r.FormValue("return")), http.StatusSeeOther) // #nosec G710 -- sanitised by safeReturn
}

// handleLogout clears the session cookie. It is this product's only other sign-out beyond
// rotating the OAuth client secret (FR-8.3 AC4), and the one a visitor can reach for themselves.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// writeFragment renders the "items" template on its own, without the layout, so htmx can swap it
// and so a template change never has to be made twice: the fragment GET /items returns is the very
// same template the page composed itself from.
func (s *Server) writeFragment(w http.ResponseWriter, r *http.Request, items itemsView) {
	var buf bytes.Buffer
	if err := s.fragments.ExecuteTemplate(&buf, "items", items); err != nil {
		s.fail(w, r, "rendering the list", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

// ---------------------------------------------------------------- view types

// The view types below exist so that the templates can stay free of logic. Every string a
// template prints is computed here, in Go, where it is testable and where html/template's
// contextual escaping is the only thing left between upstream text and the page. In particular
// nothing is ever a template.HTML: issue titles, repository names, summaries and upstream error
// text are all attacker-influenceable in principle (QS-4.3, QS-4.4).

// headerView is what the header both pages share needs (templates/fragments/header.html).
type headerView struct {
	// FetchedAt is when the snapshot's items were fetched, in the configured timezone, as
	// "15:04" — or "never" before the first fetch has returned anything.
	FetchedAt string
	// FetchedAtUnix is the same moment as FetchedAt, in Unix seconds, and zero before the first
	// fetch has returned anything. The "Mark all seen" form carries it as a hidden field so
	// POST /seen can stamp seen at the fetch the visitor actually saw rather than at the click —
	// see handleSeen's seenAt. The template omits the field entirely when it is zero.
	FetchedAtUnix int64
	// Error is the notice shown when the most recent fetch failed: what went wrong, scrubbed of
	// every configured secret (QS-4.3), and when — empty when the last fetch succeeded. A failed
	// fetch never discards the previous items (see internal/snapshot), so the page below it is
	// still whatever was fetched last.
	Error string
	// NewTotal is counted over every item regardless of any filter. A filter that also moved the
	// NEW total would make the page lie about what is new the moment somebody typed into the
	// search box.
	NewTotal int
	// Return is the page the header sits on, as a path with its query — "/?kind=pr", "/sites" —
	// which the "Mark all seen" and "Refresh" forms carry so each action comes back to where it was
	// pressed (FR-1.8 AC4). It is what the browser asked for, so the handlers only ever use it
	// through safeReturn.
	Return string
}

// dashboardView is the whole list page.
type dashboardView struct {
	headerView
	// Total and Shown are counted at two different points: Total over every item regardless of
	// the filter, Shown over what the filter actually let through.
	Total, Shown int
	// Filter is the filter that was applied, echoed back so the page can render it as the
	// visitor left it, and Repos is what its repository list offers — configuration order,
	// followed by the applied filter's own repository when configuration no longer names it.
	Filter filterView
	Repos  []string
	// Items is the list itself, and is what both this page and GET /items execute the "items"
	// template with, so the two can never draw different markup.
	Items itemsView
}

// itemsView is the whole list section: one group per repository.
type itemsView struct {
	// Query is the filter as a query string — "?kind=pr&q=header", or "" — kept so a fragment
	// carries the filter it was drawn with rather than silently widening to everything. It is a
	// template.URL because it is a URL fragment composed here rather than borrowed text;
	// url.Values.Encode escapes every value in it.
	Query  template.URL
	Groups []groupView
	// Filtered says whether a filter is in force, which is the difference between "nothing
	// matches this filter" and "nothing is open".
	Filtered  bool
	ShownLine string // "12 of 40 open" when filtered, "40 open" otherwise
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

// ---------------------------------------------------------------- view construction

// clockLayout is how FetchedAt and the error notice's timestamps are shown: a plain clock time in
// the configured timezone, because the header is asking "how current is this", not "how long ago".
const clockLayout = "15:04"

// clockLabel renders t in loc as "15:04", or "never" for the zero time.
func clockLabel(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return "never"
	}
	return t.In(loc).Format(clockLayout)
}

// errorNotice is the sentence shown under the header when the last fetch failed: when GitHub went
// unreachable, the scrubbed upstream text, and — when there is one — when the list being shown was
// last actually fetched, so the visitor knows both how wrong the picture might be and how old it is
// (design §5). It is "" when the last fetch succeeded.
func errorNotice(secrets config.Secrets, snap snapshot.Snapshot, loc *time.Location) string {
	if snap.Err == nil {
		return ""
	}
	sentence := "GitHub unreachable since " + clockLabel(snap.ErrAt, loc) +
		": " + Redact(secrets, snap.Err.Error())
	if !snap.FetchedAt.IsZero() {
		sentence += " — showing the list from " + clockLabel(snap.FetchedAt, loc)
	}
	return sentence
}

// headerView builds the shared header from the snapshot on screen, the visitor's NEW total and the
// request the page answers.
func (s *Server) headerView(snap snapshot.Snapshot, newTotal int, r *http.Request) headerView {
	var fetchedAtUnix int64
	if !snap.FetchedAt.IsZero() {
		fetchedAtUnix = snap.FetchedAt.Unix()
	}
	return headerView{
		FetchedAt:     clockLabel(snap.FetchedAt, s.loc),
		FetchedAtUnix: fetchedAtUnix,
		Error:         errorNotice(s.cfg.Secrets, snap, s.loc),
		NewTotal:      newTotal,
		Return:        r.URL.RequestURI(),
	}
}

// dashboardView turns the assembled domain dashboard and the snapshot it was built from into the
// page's presentation data.
func (s *Server) dashboardView(d domain.Dashboard, snap snapshot.Snapshot, now time.Time, r *http.Request) dashboardView {
	return dashboardView{
		headerView: s.headerView(snap, d.NewTotal, r),
		Total:      d.Total,
		Shown:      d.Shown,
		Filter:     newFilterView(d.Filter),
		Repos:      filterRepos(s.cfg.GitHub.Repos, d.Filter.Repo),
		Items:      s.itemsView(d, now),
	}
}

// itemsView turns the assembled dashboard into the list section. It preserves the order the
// domain produced — BuildDashboard groups in configuration order and sorts each group new first,
// then most recently updated — because re-sorting here would silently disagree with the counts the
// same pass produced.
func (s *Server) itemsView(d domain.Dashboard, now time.Time) itemsView {
	v := itemsView{
		Query:     template.URL(queryString(d.Filter)), // #nosec G203 -- see the field's comment
		Groups:    make([]groupView, 0, len(d.Groups)),
		Filtered:  !d.Filter.Empty(),
		ShownLine: shownLine(d),
	}
	for _, g := range d.Groups {
		gv := groupView{
			Repo:      g.Repo,
			NewCount:  g.NewCount,
			CountLine: countLine(len(g.Items), g.Total, v.Filtered),
			Items:     make([]itemView, 0, len(g.Items)),
		}
		for _, it := range g.Items {
			gv.Items = append(gv.Items, newItemView(it, now, it.IsNew(d.LastVisitAt)))
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

// newItemView renders one item. isNew is passed in rather than recomputed because the seen-mark
// belongs to the dashboard, not to the item.
func newItemView(it domain.Item, now time.Time, isNew bool) itemView {
	return itemView{
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
// append " ago". It is deliberately imprecise. The dashboard answers "does anything need me right
// now", and "3 hours" answers that better than "2h51m17s".
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
