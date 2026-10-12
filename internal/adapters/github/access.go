package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/gernotstarke/zorgscope/internal/ports"
)

// Access answers the sign-in questions with the visitor's own token — may this person push to a
// repository, who are they, which public repositories may they choose — and revokes a token when
// they sign out. It is a ports.AccessChecker, ports.IdentityReader, ports.RepoLister and
// ports.TokenRevoker (FR-8.3, FR-8.4, FR-8.5).
type Access struct {
	apiBase                string // "" means https://api.github.com
	clientID, clientSecret string // the OAuth App, for revocation
	hc                     *http.Client
}

// NewAccess builds the adapter. apiBase is the REST root, which is what GITHUB_BASE_URL points at
// the fixture server; empty means the real GitHub.
func NewAccess(apiBase, clientID, clientSecret string, hc *http.Client) *Access {
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	return &Access{apiBase: strings.TrimRight(apiBase, "/"), clientID: clientID, clientSecret: clientSecret, hc: hc}
}

// accessBodyLimit bounds what is read from GitHub's answer. A repository object or a page of a
// hundred of them is tens of kilobytes; a megabyte is far more than it can be and far less than a
// hostile endpoint could otherwise make this process allocate on an unauthenticated route.
const accessBodyLimit = 1 << 20

// do sends a request with token and answers the response, or an error that names the status and
// never the token (QS-4.3). The transport error is deliberately dropped rather than wrapped: it
// quotes the request, and the request carries the Authorization header in some transports' wording.
func (a *Access) do(ctx context.Context, method, url, token string, body io.Reader, what string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("%s: building the request: %w", what, err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := a.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: the request to GitHub failed", what)
	}
	return resp, nil
}

// HasPushAccess makes one request with the visitor's token and reads the permissions block GitHub
// returns for the authenticated user on repo. Push or admin admits; a missing block, any other
// status and any transport error refuse — the check fails closed.
func (a *Access) HasPushAccess(ctx context.Context, token, repo string) (bool, error) {
	resp, err := a.do(ctx, http.MethodGet, a.apiBase+"/repos/"+repo, token, nil, "access check")
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// The status and nothing else: echoing an upstream body into an error is how a credential
		// eventually reaches a log.
		return false, fmt.Errorf("access check: GitHub answered %d", resp.StatusCode)
	}
	var body struct {
		Permissions *struct{ Admin, Push bool } `json:"permissions"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, accessBodyLimit)).Decode(&body); err != nil {
		return false, fmt.Errorf("access check: reading the answer: %w", err)
	}
	if body.Permissions == nil {
		return false, ports.ErrNoPermissionsBlock
	}
	return body.Permissions.Push || body.Permissions.Admin, nil
}

// Login asks GET /user who the token belongs to.
func (a *Access) Login(ctx context.Context, token string) (string, error) {
	resp, err := a.do(ctx, http.MethodGet, a.apiBase+"/user", token, nil, "identity")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("identity: GitHub answered %d", resp.StatusCode)
	}
	var body struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, accessBodyLimit)).Decode(&body); err != nil {
		return "", fmt.Errorf("identity: reading the answer: %w", err)
	}
	if body.Login == "" {
		return "", errors.New("identity: GitHub named no login")
	}
	return body.Login, nil
}

// Revoke ends token at GitHub: DELETE /applications/{client_id}/token, authenticated as the App.
// 204 is success. The token travels in the body and nowhere else; an error names the status only.
func (a *Access) Revoke(ctx context.Context, token string) error {
	payload, _ := json.Marshal(map[string]string{"access_token": token})
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, a.apiBase+"/applications/"+a.clientID+"/token", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("revocation: building the request: %w", err)
	}
	req.SetBasicAuth(a.clientID, a.clientSecret)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.hc.Do(req)
	if err != nil {
		return errors.New("revocation: the request to GitHub failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("revocation: GitHub answered %d", resp.StatusCode)
	}
	return nil
}

// nextLink finds the rel="next" URL in a Link header, or "".
var nextLink = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// maxRepoPages bounds the pagination loop, as maxPages does for GraphQL: a hundred pages of a
// hundred repositories is far beyond any organisation this is for.
const maxRepoPages = 100

// PublicRepos lists org's public, non-archived repositories in GitHub's order (by name), following
// Link pagination.
func (a *Access) PublicRepos(ctx context.Context, token, org string) ([]ports.Repo, error) {
	next := a.apiBase + "/orgs/" + org + "/repos?type=public&per_page=100"
	var out []ports.Repo
	for page := 0; next != "" && page < maxRepoPages; page++ {
		resp, err := a.do(ctx, http.MethodGet, next, token, nil, "repository list")
		if err != nil {
			return nil, err
		}
		var rows []struct {
			FullName    string  `json:"full_name"`
			Description *string `json:"description"`
			Archived    bool    `json:"archived"`
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("repository list: GitHub answered %d", resp.StatusCode)
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, accessBodyLimit)).Decode(&rows)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("repository list: reading the answer: %w", err)
		}
		for _, r := range rows {
			if r.Archived {
				continue
			}
			repo := ports.Repo{FullName: r.FullName}
			if r.Description != nil {
				repo.Description = *r.Description
			}
			out = append(out, repo)
		}
		next = ""
		if m := nextLink.FindStringSubmatch(resp.Header.Get("Link")); m != nil {
			next = m[1]
		}
	}
	return out, nil
}
