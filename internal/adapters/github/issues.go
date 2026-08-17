// Package github implements ports.SourceFetcher for GitHub: open issues and pull requests over
// the GraphQL API v4 (this file), and — from Task 8 — build/workflow status over the REST API.
// It is the one package allowed to import github.com/shurcooL/githubv4 (QS-5.2, enforced by
// depguard).
package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// defaultGraphQLURL is used when Config.BaseURL is empty.
const defaultGraphQLURL = "https://api.github.com/graphql"

// maxPages bounds how many pages a single connection's pagination loop will follow. 100 pages at
// 100 nodes per page is 10,000 items — far beyond anything real — so this is a backstop against
// a misbehaving upstream, not a limit anything legitimate should ever hit. Without it, an
// upstream that returns hasNextPage: true forever (with a stuck or empty endCursor) would loop
// without bound: on a scale-to-zero Fly Machine that holds the machine awake and burns the
// refresh budget (QS-2.5) for as long as the process runs, since nothing today imposes a
// deadline on Fetch (Task 15 adds one later).
const maxPages = 100

// Config configures access to GitHub for every fetcher in this package. BaseURL and RESTBaseURL
// are deliberately two separate fields, not one repurposed field: BaseURL is the GraphQL
// endpoint IssueFetcher talks to (an httptest URL in tests already ends in "/graphql"), while
// RESTBaseURL is the API root BuildFetcher (Task 8) builds
// "{RESTBaseURL}/repos/{owner}/{repo}/actions/runs" on top of. Task 12 wires both from
// config.GitHub.BaseURL — but they name different things on GitHub's real API surface
// (api.github.com/graphql vs. api.github.com), so collapsing them into one field would force one
// of the two fetchers to mangle it back into shape.
type Config struct {
	Token       string
	BaseURL     string   // "" -> https://api.github.com/graphql (GraphQL endpoint, IssueFetcher)
	RESTBaseURL string   // "" -> https://api.github.com (REST API root, BuildFetcher)
	Repos       []string // "owner/name"
}

// IssueFetcher fetches open issues and open pull requests for the repositories in Config, over
// the GitHub GraphQL API (FR-2.1, FR-2.2).
type IssueFetcher struct {
	client *githubv4.Client
	repos  []string
}

// NewIssueFetcher builds an IssueFetcher from cfg. hc supplies the transport and any test-only
// settings (as in this package's tests, which pass an httptest server's client); the token from
// cfg.Token is added by composing an oauth2.Transport onto hc's existing transport rather than
// replacing it, so hc's own settings are preserved. When cfg.BaseURL is non-empty, requests go
// to that URL (used to point at a fake or an Enterprise instance); otherwise they go to GitHub's
// public GraphQL endpoint.
func NewIssueFetcher(cfg Config, hc *http.Client) *IssueFetcher {
	authed := *hc
	authed.Transport = &oauth2.Transport{
		Source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: cfg.Token}),
		Base:   hc.Transport,
	}

	url := cfg.BaseURL
	if url == "" {
		url = defaultGraphQLURL
	}

	return &IssueFetcher{
		client: githubv4.NewEnterpriseClient(url, &authed),
		repos:  cfg.Repos,
	}
}

// Name identifies this fetcher's source (FR-2.1).
func (f *IssueFetcher) Name() string { return "github" }

// Fetch retrieves open issues and open pull requests for every repository in f.repos. A failure
// fetching one repository does not lose items already fetched from the others (QS-1.4): every
// per-repository error is collected, joined with errors.Join, and returned alongside every item
// successfully fetched — never returned early on the first failure.
func (f *IssueFetcher) Fetch(ctx context.Context) (ports.FetchResult, error) {
	var items []domain.Item
	var errs []error

	for _, repo := range f.repos {
		owner, name, ok := splitRepo(repo)
		if !ok {
			errs = append(errs, fmt.Errorf("github: %q is not owner/name", repo))
			continue
		}

		issues, err := f.fetchIssues(ctx, owner, name)
		if err != nil {
			errs = append(errs, fmt.Errorf("github: fetching issues for %s: %w", repo, err))
		}
		items = append(items, issues...)

		prs, err := f.fetchPullRequests(ctx, owner, name)
		if err != nil {
			errs = append(errs, fmt.Errorf("github: fetching pull requests for %s: %w", repo, err))
		}
		items = append(items, prs...)
	}

	return ports.FetchResult{Items: items}, errors.Join(errs...)
}

// ghConnection is one page of a GraphQL connection: a page of nodes plus pageInfo. Issues and
// pull requests share this shape.
type ghConnection struct {
	Nodes    []ghIssueNode
	PageInfo ghPageInfo
}

// issuesQuery fetches one page of a repository's open issues. Pull requests are fetched by a
// separate query (pullRequestsQuery) with their own cursor variable: on GitHub, issues and pull
// requests paginate independently, so one query sharing a single cursor between the two
// connections would be wrong, not just inconvenient.
type issuesQuery struct {
	Repository struct {
		Issues ghConnection `graphql:"issues(states: OPEN, first: 100, after: $after, orderBy: {field: CREATED_AT, direction: ASC})"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// pullRequestsQuery fetches one page of a repository's open pull requests. See issuesQuery.
type pullRequestsQuery struct {
	Repository struct {
		PullRequests ghConnection `graphql:"pullRequests(states: OPEN, first: 100, after: $after, orderBy: {field: CREATED_AT, direction: ASC})"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// ghIssueNode is one issue or pull-request node; both connections share this shape.
type ghIssueNode struct {
	Number    githubv4.Int
	Title     githubv4.String
	URL       githubv4.URI
	Author    struct{ Login githubv4.String }
	CreatedAt githubv4.DateTime
	UpdatedAt githubv4.DateTime
	State     githubv4.String
}

// ghPageInfo mirrors a GraphQL connection's pageInfo.
type ghPageInfo struct {
	HasNextPage githubv4.Boolean
	EndCursor   githubv4.String
}

// fetchIssues fetches every open issue for owner/name, following pageInfo.hasNextPage with the
// after cursor until exhausted, bounded by nextAfter's forward-progress check and maxPages.
func (f *IssueFetcher) fetchIssues(ctx context.Context, owner, name string) ([]domain.Item, error) {
	var items []domain.Item
	var after *githubv4.String
	repo := owner + "/" + name

	for page := 0; ; page++ {
		if page >= maxPages {
			return items, fmt.Errorf("github: %s: issues pagination did not stop after %d pages", repo, maxPages)
		}

		var q issuesQuery
		vars := map[string]interface{}{
			"owner": githubv4.String(owner),
			"name":  githubv4.String(name),
			"after": after,
		}
		if err := f.client.Query(ctx, &q, vars); err != nil {
			return items, err
		}

		for _, n := range q.Repository.Issues.Nodes {
			items = append(items, toItem(owner, name, domain.KindIssue, n))
		}

		next, done, err := nextAfter(after, q.Repository.Issues.PageInfo, repo)
		if err != nil {
			return items, err
		}
		if done {
			return items, nil
		}
		after = next
	}
}

// fetchPullRequests fetches every open pull request for owner/name, following
// pageInfo.hasNextPage with the after cursor until exhausted, bounded by nextAfter's
// forward-progress check and maxPages.
func (f *IssueFetcher) fetchPullRequests(ctx context.Context, owner, name string) ([]domain.Item, error) {
	var items []domain.Item
	var after *githubv4.String
	repo := owner + "/" + name

	for page := 0; ; page++ {
		if page >= maxPages {
			return items, fmt.Errorf("github: %s: pull request pagination did not stop after %d pages", repo, maxPages)
		}

		var q pullRequestsQuery
		vars := map[string]interface{}{
			"owner": githubv4.String(owner),
			"name":  githubv4.String(name),
			"after": after,
		}
		if err := f.client.Query(ctx, &q, vars); err != nil {
			return items, err
		}

		for _, n := range q.Repository.PullRequests.Nodes {
			items = append(items, toItem(owner, name, domain.KindPR, n))
		}

		next, done, err := nextAfter(after, q.Repository.PullRequests.PageInfo, repo)
		if err != nil {
			return items, err
		}
		if done {
			return items, nil
		}
		after = next
	}
}

// nextAfter computes the cursor for the next page from pageInfo, given the cursor prev that was
// just sent to fetch the page pageInfo came from. It reports done = true when there is no next
// page. When the upstream claims another page exists but the cursor has not actually moved —
// empty, or identical to what was just sent — it returns an error naming repo instead of
// looping: an unmoving cursor with hasNextPage true is the one shape of misbehaviour that would
// otherwise loop forever, since nothing today imposes a deadline on Fetch (QS-2.5; Task 15 adds
// one later).
func nextAfter(prev *githubv4.String, pageInfo ghPageInfo, repo string) (after *githubv4.String, done bool, err error) {
	if !bool(pageInfo.HasNextPage) {
		return nil, true, nil
	}
	cursor := pageInfo.EndCursor
	if cursor == "" || (prev != nil && cursor == *prev) {
		return nil, false, fmt.Errorf("github: %s: pagination cursor did not advance (hasNextPage true, endCursor %q)", repo, string(cursor))
	}
	return &cursor, false, nil
}

// externalIDPrefix returns the ExternalID prefix for kind: "issue" or "pr". Issue and
// pull-request numbers share one namespace per GitHub repository, so without a kind prefix an
// issue and a PR could collide on the same ExternalID and silently overwrite each other in the
// store's primary key (source, external_id).
func externalIDPrefix(kind domain.Kind) string {
	if kind == domain.KindPR {
		return "pr"
	}
	return "issue"
}

// toItem maps one GraphQL issue or pull-request node to a domain.Item. DueAt and Priority are
// left zero — GitHub items have neither. FirstSeenAt is left zero too: the store owns it, and an
// adapter writing it would break the one invariant the product depends on (FR-5.3).
func toItem(owner, name string, kind domain.Kind, n ghIssueNode) domain.Item {
	repo := owner + "/" + name
	number := int(n.Number)
	return domain.Item{
		Source:     "github",
		ExternalID: fmt.Sprintf("%s:%s#%d", externalIDPrefix(kind), repo, number),
		Kind:       kind,
		Repo:       repo,
		Number:     number,
		Title:      string(n.Title),
		URL:        n.URL.String(),
		Author:     string(n.Author.Login),
		State:      string(n.State),
		CreatedAt:  n.CreatedAt.UTC(),
		UpdatedAt:  n.UpdatedAt.UTC(),
	}
}

// splitRepo splits "owner/name" into its two parts. It reports ok = false for anything else,
// including a missing owner or name.
func splitRepo(repo string) (owner, name string, ok bool) {
	i := strings.IndexByte(repo, '/')
	if i <= 0 || i == len(repo)-1 {
		return "", "", false
	}
	return repo[:i], repo[i+1:], true
}
