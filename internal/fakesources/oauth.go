package fakesources

import (
	"encoding/json"
	"net/http"
	"net/url"
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

// handleAuthorize serves GET /login/oauth/authorize, GitHub's first sign-in redirect. The real
// endpoint sends the browser on to redirect_uri with a code the caller exchanges for a token;
// this fake does the same, but the backend's own request (spec §2) carries no redirect_uri, so it
// falls back to whatever POST /_control/oauth-callback last configured. Neither present is a
// caller error worth a clear 400, not a silent redirect to nowhere.
func (s *server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	callback := q.Get("redirect_uri")
	if callback == "" {
		s.mu.Lock()
		callback = s.oauthCallback
		s.mu.Unlock()
	}
	if callback == "" {
		http.Error(w, "no callback: POST /_control/oauth-callback?url=… first, or pass redirect_uri", http.StatusBadRequest)
		return
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
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

// handleAccessToken serves POST /login/oauth/access_token, the code-for-token exchange. GitHub
// accepts this either as a form body or as JSON depending on the caller's Accept header; the fake
// only needs to read the code back out, so it tries JSON when the caller says so and otherwise
// falls back to a form body.
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
// set "absent", a permissions block. It coexists with GET /repos/{owner}/{repo}/actions/runs
// (github_rest.go) because net/http's mux always prefers the more specific pattern.
func (s *server) handleRepository(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+fakeToken {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	s.mu.Lock()
	perms := permissionSets[s.oauthPermission]
	s.mu.Unlock()
	body := map[string]any{"full_name": r.PathValue("owner") + "/" + r.PathValue("repo")}
	if perms != nil {
		body["permissions"] = perms
	}
	writeJSON(w, http.StatusOK, body)
}

// handleControlOAuthUser serves POST /_control/oauth-user?permission=…, steering the permissions
// block the next GET /repos/{owner}/{repo} responses carry, so a test can exercise every access
// level the backend must recognise without a second fixture mechanism.
func (s *server) handleControlOAuthUser(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("permission")
	if _, ok := permissionSets[p]; !ok {
		http.Error(w, "permission must be one of admin, maintain, push, pull, none, absent", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.oauthPermission = p
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// handleControlOAuthCallback serves POST /_control/oauth-callback?url=…, setting where
// /login/oauth/authorize redirects to when the request itself carries no redirect_uri — the case
// the backend's real request is in (spec §2).
func (s *server) handleControlOAuthCallback(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.oauthCallback = r.URL.Query().Get("url")
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}
