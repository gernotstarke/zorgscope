package web

import (
	"context"
	"net/http"
	"strings"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/snapshot"
)

// scope is what a signed-in page renders from (spec 2026-10-12 §5): the repositories, the sites
// drawn as tiles and sectors, the login "needs you" means, and the cache the items come from. The
// owner's scope is the configuration and the one cache main builds; a member's is their selection,
// one generated site per repository, themselves as owner, and a cache of their own. Every handler
// reads these four from the scope and never from the configuration directly, which is what keeps
// the owner's pages and a member's pages one set of code.
type scope struct {
	GH     config.GitHub
	Cache  *snapshot.Cache
	Member bool
	Login  string
}

// sessionKey carries the decoded session from requireSession to the handler behind it.
type sessionKey struct{}

// sessionOf is the session requireSession admitted the request on. On a public route it is the
// zero session, which is nobody's.
func sessionOf(r *http.Request) session {
	s, _ := r.Context().Value(sessionKey{}).(session)
	return s
}

func withSession(r *http.Request, sess session) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), sessionKey{}, sess))
}

// scopeFor is the scope sess sees.
func (s *Server) scopeFor(sess session) scope {
	if !sess.member() {
		return scope{GH: s.cfg.GitHub, Cache: s.cache, Login: sess.Login}
	}
	return scope{GH: memberGitHub(s.cfg.GitHub, sess), Cache: s.members.cacheFor(sess), Member: true, Login: sess.Login}
}

// scopeOf resolves the request's scope. A member who has chosen nothing yet has no scope to
// render: the request is answered here — a page is sent to /repos, an htmx fragment gets 204 with
// HX-Redirect — and false says so. Nothing is fetched for a selection of nothing.
//
// A request requireSession did not admit carries the zero session, which is nobody's. scopeFor
// would read it as the owner's, since it is not a member's, so it is refused here with 401: a
// handler wired to the wrong route kind then fails closed instead of showing the owner's view.
func (s *Server) scopeOf(w http.ResponseWriter, r *http.Request) (scope, bool) {
	sess := sessionOf(r)
	if sess.Role != roleOwner && sess.Role != roleMember {
		s.refuseAnonymous(w, r)
		return scope{}, false
	}
	if sess.member() && len(sess.Repos) == 0 {
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", "/repos")
			w.WriteHeader(http.StatusNoContent)
			return scope{}, false
		}
		http.Redirect(w, r, "/repos", http.StatusSeeOther)
		return scope{}, false
	}
	return s.scopeFor(sess), true
}

// memberGroup is the family a member's sites belong to. None of the fixed radars (ADR-0018) names
// it, so no radar is offered for a member: every site is a sector and no switch is drawn.
const memberGroup = "member"

// memberGitHub is a member's configuration: the deployment's settings with the repositories,
// owner and sites replaced by the member's. One site per repository, named after it, in the plain
// hue of its position (config.MemberHues has exactly MaxMemberRepos entries, D11), alerts off, in
// the member family — so the radar has no switch and the tiles no Other.
func memberGitHub(base config.GitHub, sess session) config.GitHub {
	gh := base
	gh.Repos, gh.Owner = sess.Repos, sess.Login
	gh.Sites = make([]config.Site, 0, len(sess.Repos))
	for i, repo := range sess.Repos {
		gh.Sites = append(gh.Sites, config.Site{
			Name:     repo[strings.IndexByte(repo, '/')+1:],
			URL:      "https://github.com/" + repo,
			Repos:    []string{repo},
			Hue:      config.MemberHues[i%len(config.MemberHues)],
			Group:    memberGroup,
			NoAlerts: true,
		})
	}
	return gh
}
