package fakesources

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// fakeCode and fakeToken are the only values the fake's OAuth endpoints ever hand out or accept.
// A real GitHub app would receive an opaque, single-use code and exchange it for an opaque
// token; the fake has no need of either property, so it uses one fixed string for each and
// treats any other value as wrong.
const (
	fakeCode  = "fake-code"
	fakeToken = "fake-token"
)

// defaultCallback is where GET /login/oauth/authorize redirects to when the request itself
// carries no redirect_uri — the case the backend's own request is always in (design 2026-09-14
// §2: zorgscope never sends redirect_uri, so real GitHub uses the callback registered on the
// App). It matches the callback the local OAuth App used with the fake is registered with
// (spec §7: signing in against the fake while working offline, the backend listening on
// localhost:8080) — a real GitHub App's registered callback is a deployment-time setting with
// nothing to stand in for it here, so the fake answers every such request with this one fixed
// address instead of needing a control route to configure it per test.
const defaultCallback = "http://localhost:8080/auth/callback"

// permissionSets are the permissions blocks GET /repos/{owner}/{repo} can answer with. GitHub
// reports five booleans and higher roles imply the lower ones; "absent" leaves the key out
// entirely, which is the shape the backend must treat as no access at all.
var permissionSets = map[string]map[string]bool{
	"admin":    {"admin": true, "maintain": true, "push": true, "triage": true, "pull": true},
	"maintain": {"admin": false, "maintain": true, "push": true, "triage": true, "pull": true},
	"push":     {"admin": false, "maintain": false, "push": true, "triage": true, "pull": true},
	"pull":     {"admin": false, "maintain": false, "push": false, "triage": false, "pull": true},
	"none":     {"admin": false, "maintain": false, "push": false, "triage": false, "pull": false},
	"absent":   nil,
}

// handleAuthorize serves GET /login/oauth/authorize, GitHub's first sign-in redirect. When the
// request carries a redirect_uri, it is honoured exactly as real GitHub honours one registered
// for a client — this fake does the same for any caller that sends one. The backend's own request
// (spec §2) carries no redirect_uri, so for that request — and for the fake's local
// interactive sign-in (spec §7) — it falls back to defaultCallback, matching the local OAuth App's
// registered callback.
func (s *server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	callback := q.Get("redirect_uri")
	if callback == "" {
		callback = defaultCallback
	}
	u, err := url.Parse(callback)
	if err != nil {
		http.Error(w, "bad callback", http.StatusBadRequest)
		return
	}
	v := u.Query()
	v.Set("code", fakeCode)
	v.Set("state", q.Get("state"))
	u.RawQuery = v.Encode()
	// #nosec G710 -- the destination is either a caller-supplied redirect_uri or the fixed
	// default above; this server is a fixture that only ever listens on localhost, never on the
	// internet.
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

// handleAccessToken serves POST /login/oauth/access_token, the code-for-token exchange. GitHub
// accepts this either as a form body or as JSON depending on the request's own Content-Type; the
// fake only needs to read the code back out, so it decodes JSON when the request says that is
// what it sent and otherwise falls back to a form body.
func (s *server) handleAccessToken(w http.ResponseWriter, r *http.Request) {
	code := ""
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var body struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		code = body.Code
	} else {
		_ = r.ParseForm()
		code = r.PostForm.Get("code")
	}
	if code != fakeCode {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_verification_code"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"access_token": fakeToken, "token_type": "bearer", "scope": ""})
}

// handleRepository serves GET /repos/{owner}/{repo} for the OAuth flow: it needs the fake bearer
// token exchanged above and answers with the repository's full name and, unless the control below
// set "absent", a permissions block.
func (s *server) handleRepository(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+fakeToken {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	s.mu.Lock()
	key := s.oauthPermission
	if override, ok := s.oauthRepoPermission[r.PathValue("owner")+"/"+r.PathValue("repo")]; ok {
		key = override
	}
	perms := permissionSets[key]
	s.mu.Unlock()
	body := map[string]any{"full_name": r.PathValue("owner") + "/" + r.PathValue("repo")}
	if perms != nil {
		body["permissions"] = perms
	}
	writeJSON(w, http.StatusOK, body)
}

// handleControlOAuthUser serves POST /_control/oauth-user?permission=P[&repo=owner/name][&login=L].
// Without repo it sets the default permission GET /repos/{owner}/{repo} answers with and clears
// every per-repository override; with repo it sets that repository's alone, so a test can exercise
// every access level the backend must recognise. login sets what GET /user answers.
func (s *server) handleControlOAuthUser(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := q.Get("permission")
	if _, ok := permissionSets[p]; !ok {
		http.Error(w, "permission must be one of admin, maintain, push, pull, none, absent", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	if repo := q.Get("repo"); repo != "" {
		s.oauthRepoPermission[repo] = p
	} else {
		s.oauthPermission = p
		s.oauthRepoPermission = map[string]string{}
	}
	if login := q.Get("login"); login != "" {
		s.oauthLogin = login
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// handleUser serves GET /user: the login the control set, for the fake's one token.
func (s *server) handleUser(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+fakeToken {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	s.mu.Lock()
	login := s.oauthLogin
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"login": login, "id": 1})
}

// orgRepoPageSize is how many repositories one page of GET /orgs/{org}/repos holds, whatever
// per_page asks: small, so that the adapter's Link-following is exercised by fifteen fixtures.
const orgRepoPageSize = 10

// handleOrgRepos serves GET /orgs/{org}/repos: fourteen live repositories and one archived, in
// name order, paged by orgRepoPageSize with a Link header to the next page.
func (s *server) handleOrgRepos(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+fakeToken {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	org := r.PathValue("org")
	all := make([]map[string]any, 0, 15)
	for i := 1; i <= 14; i++ {
		n := fmt.Sprintf("%02d", i)
		all = append(all, map[string]any{"full_name": org + "/repo-" + n, "description": "Repository " + n, "archived": false})
	}
	all = append(all, map[string]any{"full_name": org + "/old", "description": nil, "archived": true})
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	from := min((page-1)*orgRepoPageSize, len(all))
	to := min(from+orgRepoPageSize, len(all))
	if to < len(all) {
		next := *r.URL
		q := next.Query()
		q.Del("page")
		// page goes last, as GitHub writes it, so that the link ends in page=N>.
		next.RawQuery = strings.TrimPrefix(q.Encode()+"&page="+strconv.Itoa(page+1), "&")
		w.Header().Set("Link", "<http://"+r.Host+next.String()+`>; rel="next"`)
	}
	writeJSON(w, http.StatusOK, all[from:to])
}

// handleRevoke serves DELETE /applications/{client_id}/token: the App revoking one of its tokens.
// The fake knows no client secret, so it checks only that the basic-auth user is the client id in
// the path and that the body names its one token.
func (s *server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	user, _, ok := r.BasicAuth()
	var body struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !ok || user != r.PathValue("client_id") || body.AccessToken != fakeToken {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "not a token of this app"})
		return
	}
	s.mu.Lock()
	s.revoked = append(s.revoked, body.AccessToken)
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// handleControlRevoked serves GET /_control/revoked: every token handleRevoke accepted.
func (s *server) handleControlRevoked(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	out := append([]string(nil), s.revoked...)
	s.mu.Unlock()
	if out == nil {
		out = []string{}
	}
	writeJSON(w, http.StatusOK, out)
}
