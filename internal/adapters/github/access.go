package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gernotstarke/zorgscope/internal/ports"
)

// AccessChecker answers FR-8.3's one question — may this visitor see the dashboard? — by asking
// GitHub what the visitor may do to the configured repository. It is a ports.AccessChecker.
type AccessChecker struct {
	apiBase string // "" means https://api.github.com
	repo    string // owner/name
	hc      *http.Client
}

// NewAccessChecker builds the checker for one repository. apiBase is the REST root, which is what
// GITHUB_BASE_URL points at the fixture server; empty means the real GitHub.
func NewAccessChecker(apiBase, repo string, hc *http.Client) *AccessChecker {
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	return &AccessChecker{apiBase: strings.TrimRight(apiBase, "/"), repo: repo, hc: hc}
}

// accessBodyLimit bounds what is read from GitHub's answer. The repository object is a few
// kilobytes; a megabyte is far more than it can be and far less than a hostile endpoint could
// otherwise make this process allocate on an unauthenticated route.
const accessBodyLimit = 1 << 20

// HasPushAccess makes one request with the visitor's token and reads the permissions block GitHub
// returns for the authenticated user. Push or admin admits; a missing block, any other status
// and any transport error refuse — the check fails closed. The token is never part of an error.
func (a *AccessChecker) HasPushAccess(ctx context.Context, token string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.apiBase+"/repos/"+a.repo, nil)
	if err != nil {
		return false, fmt.Errorf("access check: building the request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := a.hc.Do(req)
	if err != nil {
		// The transport error is deliberately dropped rather than wrapped: it quotes the request,
		// and the request carries the Authorization header in some transports' wording (QS-4.3).
		return false, errors.New("access check: the request to GitHub failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// The status and nothing else: GitHub's body for a 401 is harmless, but the habit of
		// echoing an upstream body into an error is how a credential eventually reaches a log.
		return false, fmt.Errorf("access check: GitHub answered %d", resp.StatusCode)
	}
	var body struct {
		Permissions *struct{ Admin, Push bool } `json:"permissions"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, accessBodyLimit)).Decode(&body); err != nil {
		return false, fmt.Errorf("access check: reading the answer: %w", err)
	}
	// A repository the visitor can see but has no permissions block for is refused, and is said
	// out loud: "GitHub did not tell us" is a different thing from "GitHub said no", and only one
	// of the two is fixed by asking for a scope (design 2026-09-14 §8).
	if body.Permissions == nil {
		return false, ports.ErrNoPermissionsBlock
	}
	// maintain and triage are not read: GitHub sets push true for maintain, and triage does not
	// grant push. Reading the two booleans the rule is actually about keeps it one sentence long.
	return body.Permissions.Push || body.Permissions.Admin, nil
}
