package web

import (
	"net/url"
	"strings"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// parseFilter reads the four filter parameters. A value it cannot read is ignored rather than
// refused: a stale bookmark with a typo in it should show the unfiltered page, not an error.
func parseFilter(q url.Values, loc *time.Location) domain.Filter {
	f := domain.Filter{
		Repo: strings.TrimSpace(q.Get("repo")),
		Text: strings.TrimSpace(q.Get("q")),
	}
	switch q.Get("kind") {
	case "issue":
		f.Kind = domain.KindIssue
	case "pr":
		f.Kind = domain.KindPR
	}
	switch q.Get("tier") {
	case "security":
		f.MinTier = domain.TierSecurity
	case "dependency":
		f.MinTier = domain.TierDependency
	}
	if since := q.Get("since"); since != "" {
		// Parsed in the configured timezone rather than in UTC, because the visitor typed a date
		// into a date field and meant their own day: "since today" a couple of hours after
		// midnight in Berlin would otherwise still be yesterday and let yesterday's items through.
		if t, err := time.ParseInLocation(dateLayout, since, loc); err == nil {
			f.CreatedSince = t
		}
	}
	return f
}

// dateLayout is the form the date input submits and the form echoes back (RFC 3339's date half,
// which is what <input type="date"> uses).
const dateLayout = "2006-01-02"

// queryString renders f as the canonical query string the page's own URL carries — "?kind=pr&q=x"
// — or "" when nothing is set. It is what the "items" fragment carries as its own query, so
// GET /items answers with the same filter the page was drawn with rather than the unfiltered one.
func queryString(f domain.Filter) string {
	q := url.Values{}
	if f.Repo != "" {
		q.Set("repo", f.Repo)
	}
	if f.Kind != "" {
		q.Set("kind", string(f.Kind))
	}
	if !f.CreatedSince.IsZero() {
		q.Set("since", f.CreatedSince.Format(dateLayout))
	}
	if text := strings.TrimSpace(f.Text); text != "" {
		q.Set("q", text)
	}
	if f.MinTier != domain.TierNone {
		q.Set("tier", f.MinTier.String())
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}
