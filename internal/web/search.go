package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/snapshot"
)

// handleSearch renders the results page (FR-12.1). Like the list, it reads only the snapshot;
// answeredWaiting handles a fetch in flight exactly as it does for the list (FR-1.9).
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	snap, waiting := s.answeredWaiting(w, r)
	if waiting {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	view := s.searchView(snap, q, s.clock.Now())
	s.execute(w, r, http.StatusOK, "search.html", pageData{
		Title:  "Search",
		Chrome: chromeFor(r),
		Search: &view,
	})
}

// searchView is the results page.
type searchView struct {
	headerView
	// Query is the text as typed, trimmed; empty shows the hint instead of results.
	Query string
	// CountLine says what was found — computed here, never in the template.
	CountLine string
	Hits      []hitView
}

// hitView is one result row.
type hitView struct {
	Kind, Repo string
	// Hue is the repository's site colour, as the list's groups carry it.
	Hue    string
	Number int
	// Title is the title cut into runs, each marked or plain, so the template can wrap the
	// matched runs in <mark> without ever holding HTML.
	Title   []titleRun
	URL     string
	Labels  []labelView
	Author  string
	Updated timeView
	Quiet   bool
	// Matched is the evidence line, "matched: title, label", or "" when only the kind matched.
	Matched string
}

// titleRun is a run of the title: marked when the query matched it.
type titleRun struct {
	Text string
	Mark bool
}

// searchView ranks the snapshot against q and renders the hits.
func (s *Server) searchView(snap snapshot.Snapshot, q string, now time.Time) searchView {
	hits := domain.Search(snap.Items, domain.ParseQuery(q))
	v := searchView{
		headerView: s.headerView(snap),
		Query:      q,
		CountLine:  searchCountLine(q, len(hits)),
		Hits:       make([]hitView, 0, len(hits)),
	}
	for _, h := range hits {
		v.Hits = append(v.Hits, hitView{
			Kind:    kindLabel(h.Item.Kind),
			Repo:    h.Item.Repo,
			Hue:     hueForRepo(s.cfg.GitHub, h.Item.Repo),
			Number:  h.Item.Number,
			Title:   titleRuns(h.Item.Title, h.TitleSpans),
			URL:     h.Item.URL,
			Labels:  labelViews(h.Item.Labels),
			Author:  h.Item.Author,
			Updated: newTimeView(h.Item.UpdatedAt, now),
			Quiet:   h.Item.IsQuiet(now, domain.QuietAfter),
			Matched: matchedLine(h.Matched),
		})
	}
	return v
}

// searchCountLine is the line above the results: how many, for what.
func searchCountLine(q string, n int) string {
	if n == 0 {
		return "Nothing matches “" + q + "”."
	}
	return quantity(n, "result") + " for “" + q + "”"
}

// matchedLine spells out which fields matched, or "" for none.
func matchedLine(fields []string) string {
	if len(fields) == 0 {
		return ""
	}
	return "matched: " + strings.Join(fields, ", ")
}

// titleRuns cuts title at the spans, which are sorted, merged and inside the title (the domain
// guarantees all three); a title with no spans is one plain run.
func titleRuns(title string, spans [][2]int) []titleRun {
	var runs []titleRun
	at := 0
	for _, sp := range spans {
		if sp[0] > at {
			runs = append(runs, titleRun{Text: title[at:sp[0]]})
		}
		runs = append(runs, titleRun{Text: title[sp[0]:sp[1]], Mark: true})
		at = sp[1]
	}
	if at < len(title) || len(runs) == 0 {
		runs = append(runs, titleRun{Text: title[at:]})
	}
	return runs
}
