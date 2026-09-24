package web

import (
	"net/http"
	"net/url"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// handleContributors renders the Contributors page (FR-12.2): the people who opened the open
// items, busiest first. It reads only the snapshot, through answeredWaiting like every page.
func (s *Server) handleContributors(w http.ResponseWriter, r *http.Request) {
	snap, waiting := s.answeredWaiting(w, r)
	if waiting {
		return
	}
	now := s.clock.Now()
	people := domain.BuildContributors(snap.Items, s.cfg.GitHub.Repos)
	view := contributorsView{
		headerView: s.headerView(snap),
		CountLine:  contributorsCountLine(len(people)),
		Rows:       make([]contributorView, 0, len(people)),
	}
	for _, c := range people {
		row := contributorView{
			Login: c.Login, Unknown: c.Login == "",
			PRs: c.PRs, Issues: c.Issues, Repos: c.Repos,
			LastActive: newTimeView(c.LastActive, now),
		}
		if row.Unknown {
			row.Login = "unknown"
		} else {
			row.ProfileURL = "https://github.com/" + url.PathEscape(c.Login)
			row.ItemsURL = "/search?" + url.Values{"q": {c.Login}}.Encode()
		}
		view.Rows = append(view.Rows, row)
	}
	s.execute(w, r, http.StatusOK, "contributors.html", pageData{
		Title: "Contributors", Chrome: chromeFor(r, snap, s.cfg.GitHub.Owner, s.clock.Now()), Contributors: &view,
	})
}

// contributorsView is the Contributors page.
type contributorsView struct {
	headerView
	// CountLine: "12 contributors" or "No contributors yet."
	CountLine string
	Rows      []contributorView
}

// contributorView is one row of the table.
type contributorView struct {
	// Login is the login, or "unknown" for the author GitHub no longer knows; Unknown says which.
	Login   string
	Unknown bool
	// ProfileURL is the GitHub profile and ItemsURL the search for the login; both empty when
	// Unknown, so the template draws no link.
	ProfileURL, ItemsURL string
	PRs, Issues          int
	Repos                []string
	LastActive           timeView
}

// contributorsCountLine is the line above the table.
func contributorsCountLine(n int) string {
	if n == 0 {
		return "No contributors yet."
	}
	return quantity(n, "contributor")
}
