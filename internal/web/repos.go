package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// orgReposTTL is how long the organisation's repository list is reused before a member's page view
// refetches it. It is public data that changes a few times a year; an hour keeps the page off
// GitHub's rate limit for the people who open it ten times while deciding.
const orgReposTTL = time.Hour

// orgRepos is the one list, shared by every member: public data fetched with whichever member's
// token asked first after it went stale (spec 2026-10-12 §7).
type orgRepos struct {
	mu        sync.Mutex
	list      []ports.Repo
	fetchedAt time.Time
}

// orgRepoList returns the organisation's repositories, fetching them with token when the held
// list is older than orgReposTTL. A failed fetch returns the error and keeps whatever was held:
// a stale list is better than none for a page that only ticks boxes.
func (s *Server) orgRepoList(ctx context.Context, token string) ([]ports.Repo, error) {
	s.orgRepos.mu.Lock()
	defer s.orgRepos.mu.Unlock()
	now := s.clock.Now()
	if s.orgRepos.list != nil && now.Sub(s.orgRepos.fetchedAt) <= orgReposTTL {
		return s.orgRepos.list, nil
	}
	list, err := s.repos.PublicRepos(ctx, token, s.cfg.GitHub.MembersOrg)
	if err != nil {
		return s.orgRepos.list, scrubbedError(err, token)
	}
	s.orgRepos.list, s.orgRepos.fetchedAt = list, now
	return list, nil
}

// reposView is the repository page (FR-8.5).
type reposView struct {
	Login, Org  string
	Max, Chosen int
	Rows        []repoRow
	// Error is why the page could not do what was asked: the list could not be fetched, or
	// too many were chosen. The list is still drawn from whatever is known.
	Error string
	Saved bool
}

// repoRow is one checkbox.
type repoRow struct {
	FullName, Name, Description string
	Checked                     bool
}

// reposPage builds the view for sess over list, marking chosen; listErr, when set, is shown.
func reposPage(sess session, org string, list []ports.Repo, chosen []string, listErr error) reposView {
	picked := make(map[string]bool, len(chosen))
	for _, repo := range chosen {
		picked[repo] = true
	}
	v := reposView{Login: sess.Login, Org: org, Max: config.MaxMemberRepos, Chosen: len(chosen)}
	known := make(map[string]bool, len(list))
	for _, repo := range list {
		known[repo.FullName] = true
		v.Rows = append(v.Rows, repoRow{
			FullName: repo.FullName, Name: repo.FullName[strings.IndexByte(repo.FullName, '/')+1:],
			Description: repo.Description, Checked: picked[repo.FullName],
		})
	}
	// A chosen repository the list does not hold — the list failed, or the repository went
	// private or archived since — is still drawn, checked, so the member can unchoose it.
	for _, repo := range chosen {
		if !known[repo] {
			v.Rows = append(v.Rows, repoRow{FullName: repo, Name: repo[strings.IndexByte(repo, '/')+1:], Checked: true})
		}
	}
	if listErr != nil {
		v.Error = "The organisation's repositories could not be fetched: " + listErr.Error()
	}
	return v
}

// handleRepos shows the page (FR-8.5). The owner has no selection and is sent to the dashboard.
func (s *Server) handleRepos(w http.ResponseWriter, r *http.Request) {
	sess := sessionOf(r)
	if !sess.member() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	list, err := s.orgRepoList(r.Context(), sess.Token)
	view := reposPage(sess, s.cfg.GitHub.MembersOrg, list, sess.Repos, err)
	view.Saved = r.URL.Query().Get("saved") == "1"
	s.execute(w, r, http.StatusOK, "repos.html", pageData{Title: "Repositories", Repos: &view})
}

// handleReposSave records the selection in the cookie (FR-8.5). Only repositories the
// organisation list holds are kept, each once, in the list's order; more than
// config.MaxMemberRepos is refused with the page redrawn and nothing saved; a list that cannot be
// fetched refuses the same way, because nothing can be validated against it.
func (s *Server) handleReposSave(w http.ResponseWriter, r *http.Request) {
	sess := sessionOf(r)
	if !sess.member() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, "reading the form", err)
		return
	}
	asked := make(map[string]bool, len(r.PostForm["repo"]))
	for _, repo := range r.PostForm["repo"] {
		asked[repo] = true
	}
	list, err := s.orgRepoList(r.Context(), sess.Token)
	if err != nil {
		view := reposPage(sess, s.cfg.GitHub.MembersOrg, list, sess.Repos, err)
		s.execute(w, r, http.StatusOK, "repos.html", pageData{Title: "Repositories", Repos: &view})
		return
	}
	var chosen []string
	for _, repo := range list {
		if asked[repo.FullName] {
			chosen = append(chosen, repo.FullName)
		}
	}
	if len(chosen) > config.MaxMemberRepos {
		view := reposPage(sess, s.cfg.GitHub.MembersOrg, list, chosen, nil)
		view.Error = "Choose at most " + strconv.Itoa(config.MaxMemberRepos) + " repositories — " +
			strconv.Itoa(len(chosen)) + " are ticked."
		s.execute(w, r, http.StatusOK, "repos.html", pageData{Title: "Repositories", Repos: &view})
		return
	}
	sess.Repos = chosen
	s.setSession(w, sess)
	if len(chosen) == 0 {
		http.Redirect(w, r, "/repos?saved=1", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, settingsOf(r).Landing.path(), http.StatusSeeOther)
}
