package web

import (
	"bytes"
	"html/template"
	"net/http"
	"slices"
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

// topbarSearchID is the id of the top bar's search form (layout.html). htmx sends it as the
// HX-Trigger header of every search typed there, and that form replaces the whole <main>, so
// during a fetch the request is answered like a navigation rather than like a fragment: see
// answeredWaiting (FR-1.9 AC5, FR-12.1 AC4).
const topbarSearchID = "topbar-search"

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
// true. While a fetch is in flight and the snapshot is empty it also answers the request itself
// and reports so:
//
//   - an ordinary page view gets the wait page, and so does a search typed in the top bar: that
//     form replaces the whole <main>, and htmx then selects the wait page's own <main>, whose
//     poll asks for /search?q=… and swaps the results in when the fetch ends. Answering it from
//     the snapshot instead would swap the poll away and strand the visitor on "Nothing matches";
//   - the wait page's own poll gets 204, so htmx swaps nothing and the animation keeps running;
//   - any other htmx request — the filter form — is not answered here at all, and the caller
//     renders it from the current snapshot (AC5), since a wait page selected for #items would
//     empty the list.
//
// So what a request comes down to is which element issued it, which htmx names in HX-Trigger:
// every htmx caller that swaps a whole page has to be named here. Adding hx-boost to the body
// would make ordinary navigations carry HX-Request: true under the id of whatever link was
// clicked, and they would be answered from the snapshot rather than with the wait page,
// defeating FR-1.9 for every boosted link.
//
// When no fetch is in flight it answers nothing and the caller renders as it always has.
func (s *Server) answeredWaiting(w http.ResponseWriter, r *http.Request) (snapshot.Snapshot, bool) {
	snap := s.cache.Get(r.Context())
	// A fetch in flight is not by itself a reason to withhold the page. What is, is having
	// nothing to put on it: an empty snapshot can only draw an empty list, and "nothing is open"
	// is a wrong answer rather than a stale one — which is the whole reason the wait page exists.
	// With items in hand the page is drawn from them and says that a fetch is running (FR-1.9
	// AC1, AC6), and the poll inside the list swaps the fresh one in when it lands (ADR-0013).
	//
	// This is deliberately blind to why the snapshot is stale. POST /refresh invalidates and
	// redirects, so the redirected GET sees exactly what an aged-out TTL leaves behind, and
	// telling them apart would need a field in Snapshot that nothing else wants.
	if !snap.Fetching || len(snap.Items) > 0 {
		return snap, false
	}
	trigger := r.Header.Get("HX-Trigger")
	if r.Header.Get("HX-Request") != "true" || trigger == topbarSearchID {
		// requireSession already sets no-store on every session route (see its own comment); this
		// Set restates, for a reader of this branch, that the guarantee covers the wait page too —
		// it must never come back out of a browser cache, since it is only ever right now.
		w.Header().Set("Cache-Control", "no-store")
		s.execute(w, r, http.StatusOK, "waiting.html", pageData{
			Waiting: &waitingView{Repos: len(s.cfg.GitHub.Repos), Path: r.URL.RequestURI()},
			Chrome:  chromeFor(r, snap, s.cfg.GitHub.Owner, s.clock.Now()),
		})
		return snap, true
	}
	if trigger == waitingPollID {
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
	// The visitor's landing view (FR-1.12 AC3). "/" is where the list lives, so only the other
	// views redirect; landingList falling through is what keeps "/" from bouncing to itself.
	//
	// The cache is asked before redirecting, and that is the whole reason this is three lines
	// rather than one. A fetch is started by whichever handler asks the cache, so a redirect that
	// only redirected would push the first ask into the *next* request — costing a cold Machine
	// exactly the round trip ADR-0013 exists to save. Asking here starts it just as early as
	// rendering would, and the answer is deliberately discarded: the page the visitor lands on
	// asks again and will find either the fetch in flight or its result.
	//
	// A request that carries a query string is left alone, whatever the landing preference says.
	// A query is asking for a specific list — a Sites tile's "N more" link (FR-1.8 AC3), a
	// bookmark, or the filter form's own GET — and a landing preference is about where a visitor
	// *arrives*, not a veto on where they just asked to go. Redirecting one away would make every
	// filtered link and every bookmark of a filtered list unreachable for anyone whose landing
	// view is not the list.
	if target := settingsOf(r).Landing.path(); target != "/" && r.URL.RawQuery == "" {
		_ = s.cache.Get(r.Context())
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}

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
// and the filter the request's query carries, and executes tmpl with it. It contacts no upstream
// service itself (FR-1.1) and does not read the cache on its own: for a page, answeredWaiting has
// already dealt with a fetch in flight before render runs, and snap is the very snapshot it decided
// that with; the /items fragment is deliberately served from the current snapshot whatever the
// cache is doing (FR-1.9 AC5).
func (s *Server) render(w http.ResponseWriter, r *http.Request, tmpl string, snap snapshot.Snapshot) {
	now := s.clock.Now()
	quiet := settingsOf(r).Quiet

	d := domain.BuildDashboard(domain.DashboardInput{
		Now: now, Items: snap.Items,
		Repos: s.cfg.GitHub.Repos, Filter: parseFilter(r.URL.Query(), s.loc),
	})
	view := s.dashboardView(d, snap, now, quiet)

	if tmpl == fragmentItemsTemplate {
		s.writeFragment(w, r, view.Items)
		return
	}
	s.execute(w, r, http.StatusOK, tmpl, pageData{Dashboard: &view, Chrome: chromeFor(r, snap, s.cfg.GitHub.Owner, s.clock.Now())})
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
	// The band lives above the filter, outside #items, so the fragment carries it out of band:
	// the refresh poll swaps in fresh items and a fresh band together (FR-1.14 AC4).
	if err := s.fragments.ExecuteTemplate(&buf, "needs-oob", items); err != nil {
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

// headerView is what the header every signed-in page shares needs
// (templates/fragments/header.html).
type headerView struct {
	// FetchedAt is when the snapshot's items were fetched, in the configured timezone, as
	// "15:04" — or "never" before the first fetch has returned anything.
	FetchedAt string
	// FetchedAgo is the same moment as an age — "4 minutes ago" — because a clock time alone does
	// not say at a glance whether the list is fresh. Empty before the first fetch.
	FetchedAgo string
	// Error is the notice shown when the most recent fetch failed: what went wrong, scrubbed of
	// every configured secret (QS-4.3), and when — empty when the last fetch succeeded. A failed
	// fetch never discards the previous items (see internal/snapshot), so the page below it is
	// still whatever was fetched last.
	Error string
}

// chromeView is what layout.html draws between the brand and the appearance control.
type chromeView struct {
	// View is "list", "sites", "radar", "contributors" or "search"; the switch marks all but search.
	View string
	// Query is the search box's text: the query on the results page, empty everywhere else —
	// the list's own filter also uses q, and its text is not a search.
	Query string
	// Return is the page the Refresh form comes back to, path and query (FR-1.8 AC4). It is what
	// the browser asked for, so handleRefresh only ever uses it through safeReturn.
	Return string
	// Issues and PRs count what is open across every configured repository — the snapshot, not
	// the page, so a filter or a search never changes them. Counted is false until a fetch has
	// returned: before one there is nothing to count, and "0 issues" would be a wrong answer.
	Issues, PRs int
	Counted     bool
	// Needs is how many items need the owner (FR-1.14), counted the way the band counts them. It
	// leads the top bar, because it is the one number the page is opened for; the totals follow,
	// quieter.
	Needs int
}

// NeedsLine is the top bar's lead: "3 need you", "1 needs you" or "Nothing needs you".
func (c chromeView) NeedsLine() string {
	switch c.Needs {
	case 0:
		return "Nothing needs you"
	case 1:
		return "1 needs you"
	default:
		return strconv.Itoa(c.Needs) + " need you"
	}
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
	// Security is how many security items are open, counted over everything whatever the filter
	// (FR-1.13 AC4). Zero draws nothing at all: "0 security" is a sentence nobody needs. It is
	// drawn beside ShownLine rather than inside it because it is a link to the list narrowed to
	// those items, and a link cannot be built out of a sentence.
	Security int
	// Fetching says a fetch is in flight, so the list draws the poll that will replace it and
	// states that it is not current (FR-1.9 AC2, AC6). It lives on the items view rather than on
	// the page, because GET /items renders this struct alone: putting it on the page would mean
	// the swapped-in fragment could not carry the poll, and the list would stop updating after
	// the first swap.
	Fetching bool
	// Repos is how many repositories the running fetch is asking, for the status line. The wait
	// page names the same number, from the same configuration.
	Repos int
	// Needs is the Needs-you band (FR-1.14), nil only when nothing is open at all.
	Needs *needsView
}

// groupView is one repository's block of the list. CountLine is taken before the filter is
// applied, so narrowing the list never makes the page understate what is out there (FR-2.1 AC3).
type groupView struct {
	Repo string
	// Hue is the site's colour key, drawn as the group's stripe (FR-1.10 AC2).
	Hue       string
	CountLine string // "3 of 7" when filtered, "7" otherwise
	Items     []itemView
}

// filterView is the filter as the form renders it: every field a string, because that is what an
// input carries, and Since in the form <input type="date"> submits.
type filterView struct {
	Repo, Text, Since string
	Kind              string // "", "issue" or "pr"
	Tier              string // "", "dependency" or "security" (FR-1.13)
}

// Active is how many of the filter's axes are in force, for the disclosure's summary: "· 2 active".
func (f filterView) Active() int {
	n := 0
	for _, v := range []string{f.Repo, f.Kind, f.Text, f.Since, f.Tier} {
		if v != "" {
			n++
		}
	}
	return n
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
	// Kind is "PR" or "Issue", and KindClass its class suffix: the Needs-you band's and the top
	// bar's code, drawn in the same colours.
	Kind, KindClass string
	Author          string
	Created         timeView
	Updated         timeView
	// Labels are the item's chips, in GitHub's order (FR-1.10 AC3).
	Labels []labelView
	// Quiet marks an item nothing has touched for the visitor's chosen threshold, domain.QuietAfter
	// by default (FR-1.10 AC4, FR-1.12 AC4).
	Quiet bool
	// Tier is how loudly the row is marked (FR-1.13), shared with the search results' rows.
	Tier tierView
	// Needed says the item is in the Needs-you band (FR-1.14). The list draws such a row at full
	// weight and every other row a step quieter, so the two tiers read at a glance while the list
	// stays complete (QG-1).
	Needed bool
}

// labelView is one chip: the name as GitHub spells it, and the key that picks its colour class.
type labelView struct {
	Name, Key string
}

// labelKeys are the label names that get a colour of their own, as the keys their CSS classes and
// tokens use: label-<key> and --label-<key>. They are the names the arc42 repositories actually
// use, normalised as labelKey normalises them. Any other label is labelOther (FR-1.10 AC3).
var labelKeys = []string{"bug", "enhancement", "documentation", "question", "help-wanted", "in-progress"}

// labelOther is the key of every label outside labelKeys: a neutral chip whose name does the work.
const labelOther = "other"

// labelKey normalises a label name — lowercase, trimmed, runs of whitespace as one hyphen — and
// returns it when it is one of labelKeys, labelOther otherwise. "Help Wanted", "help wanted" and
// "help-wanted" all land on the same chip, which is the point: the same label is spelled three
// ways across the arc42 repositories.
func labelKey(name string) string {
	key := strings.Join(strings.Fields(strings.ToLower(name)), "-")
	if slices.Contains(labelKeys, key) {
		return key
	}
	return labelOther
}

// labelViews turns an item's labels into chips, keeping GitHub's order; nil for none.
//
// The label that produced the item's mark is left out: a Dependency row said "Dependency" in a
// chip and "dependencies" in a label right beside it, and a Security row did the same. The tier
// chip says it louder and says why, so the label is furniture. Only that one label goes — a
// Security item labelled "dependencies" keeps it, because that label is not what marked it.
func labelViews(it domain.Item) []labelView {
	if len(it.Labels) == 0 {
		return nil
	}
	var evidence string
	switch it.Tier() {
	case domain.TierSecurity:
		evidence = "security"
	case domain.TierDependency:
		evidence = "dependencies"
	}
	out := make([]labelView, 0, len(it.Labels))
	for _, name := range it.Labels {
		if evidence != "" && strings.EqualFold(name, evidence) {
			continue
		}
		out = append(out, labelView{Name: name, Key: labelKey(name)})
	}
	return out
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

// headerView builds the shared header from the snapshot on screen.
func (s *Server) headerView(snap snapshot.Snapshot) headerView {
	v := headerView{
		FetchedAt: clockLabel(snap.FetchedAt, s.loc),
		Error:     errorNotice(s.cfg.Secrets, snap, s.loc),
	}
	if !snap.FetchedAt.IsZero() {
		v.FetchedAgo = humanise(s.clock.Now().Sub(snap.FetchedAt)) + " ago"
	}
	return v
}

// views maps a page's path to the name the switch marks.
var views = map[string]string{"/": "list", "/sites": "sites", "/contributors": "contributors", "/radar": "radar", "/search": "search"}

// chromeFor is the top bar for the signed-in page answering r, counting what is open in snap and
// what of it needs owner.
func chromeFor(r *http.Request, snap snapshot.Snapshot, owner string, now time.Time) *chromeView {
	c := &chromeView{View: views[r.URL.Path], Return: r.URL.RequestURI(), Counted: !snap.FetchedAt.IsZero()}
	if r.URL.Path == "/search" {
		c.Query = strings.TrimSpace(r.URL.Query().Get("q"))
	}
	for _, it := range snap.Items {
		if domain.NeedsNow(it, owner, now) {
			c.Needs++
		}
	}
	for _, it := range snap.Items {
		switch it.Kind {
		case domain.KindIssue:
			c.Issues++
		case domain.KindPR:
			c.PRs++
		default:
			// A third kind, should one arrive, is neither.
		}
	}
	return c
}

// dashboardView turns the assembled domain dashboard and the snapshot it was built from into the
// page's presentation data.
func (s *Server) dashboardView(d domain.Dashboard, snap snapshot.Snapshot, now time.Time, quiet time.Duration) dashboardView {
	return dashboardView{
		headerView: s.headerView(snap),
		Total:      d.Total,
		Shown:      d.Shown,
		Filter:     newFilterView(d.Filter),
		Repos:      filterRepos(s.cfg.GitHub.Repos, d.Filter.Repo),
		Items:      s.itemsView(d, snap, now, quiet),
	}
}

// itemsView turns the assembled dashboard into the list section. It preserves the order the
// domain produced — BuildDashboard groups in configuration order and sorts each group most
// recently updated first — because re-sorting here would silently disagree with the counts the
// same pass produced.
func (s *Server) itemsView(d domain.Dashboard, snap snapshot.Snapshot, now time.Time, quiet time.Duration) itemsView {
	v := itemsView{
		Query:     template.URL(queryString(d.Filter)), // #nosec G203 -- see the field's comment
		Groups:    make([]groupView, 0, len(d.Groups)),
		Filtered:  !d.Filter.Empty(),
		ShownLine: shownLine(d),
		Security:  d.Security,
		Fetching:  snap.Fetching,
		Repos:     len(s.cfg.GitHub.Repos),
	}
	// With nothing open at all, "Nothing open." already says everything the band would. A filter
	// does not hide the band: it answers "what needs me", whatever the list is narrowed to, and a
	// band that vanished would move the filter out from under the pointer that just changed it.
	if d.Total > 0 {
		v.Needs = newNeedsView(snap.Items, s.cfg.GitHub, now)
	}
	for _, g := range d.Groups {
		gv := groupView{
			Repo:      g.Repo,
			Hue:       hueForRepo(s.cfg.GitHub, g.Repo),
			CountLine: countLine(len(g.Items), g.Total, v.Filtered),
			Items:     make([]itemView, 0, len(g.Items)),
		}
		for _, it := range g.Items {
			iv := newItemView(it, now, quiet)
			iv.Needed = domain.NeedsNow(it, s.cfg.GitHub.Owner, now)
			gv.Items = append(gv.Items, iv)
		}
		v.Groups = append(v.Groups, gv)
	}
	return v
}

// shownLine is the count above the list. It names the total whenever a filter is in force,
// because "12 open" and "12 of 150 open" are different news and only one of them is true. The
// security count is drawn beside it, by the template, as a link (FR-1.13 AC4).
func shownLine(d domain.Dashboard) string {
	if !d.Filter.Empty() {
		return strconv.Itoa(d.Shown) + " of " + strconv.Itoa(d.Total) + " open"
	}
	return strconv.Itoa(d.Total) + " open"
}

// countLine is the same figure per repository, and is drawn from the group's unfiltered total for
// the reason FR-2.1 AC3 gives: the filter says what is being looked at, never what is out there.
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
	v := filterView{Repo: f.Repo, Kind: string(f.Kind), Text: f.Text, Tier: f.MinTier.String()}
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

// newItemView renders one item.
func newItemView(it domain.Item, now time.Time, quiet time.Duration) itemView {
	return itemView{
		Title:     it.Title,
		Summary:   summaryLine(it.Summary),
		URL:       it.URL,
		Number:    it.Number,
		Kind:      kindShort(it.Kind),
		KindClass: string(it.Kind),
		Author:    it.Author,
		Created:   newTimeView(it.CreatedAt, now),
		Updated:   newTimeView(it.UpdatedAt, now),
		Labels:    labelViews(it),
		Quiet:     it.ShowsQuiet(now, quiet),
		Tier:      newTierView(it),
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

// kindShort is the kind as the list and the band abbreviate it.
func kindShort(k domain.Kind) string {
	switch k {
	case domain.KindPR:
		return "PR"
	case domain.KindIssue:
		return "Issue"
	default:
		return string(k)
	}
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
