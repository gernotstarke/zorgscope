// Package github implements ports.Source for GitHub: open issues and pull requests over the
// GraphQL API v4. It is the one package allowed to import github.com/shurcooL/githubv4 (QS-5.2,
// enforced by depguard).
package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// defaultGraphQLURL is used when Config.BaseURL is empty.
const defaultGraphQLURL = "https://api.github.com/graphql"

// maxPages bounds how many pages a single connection's pagination loop will follow. 100 pages at
// 100 nodes per page is 10,000 items — far beyond anything real — so this is a backstop against
// a misbehaving upstream, not a limit anything legitimate should ever hit. Without it, an
// upstream that returns hasNextPage: true forever (with a stuck or empty endCursor) would loop
// without bound: on a scale-to-zero Fly Machine that holds the machine awake for as long as the
// process runs (QS-2.5). The caller's http.Client timeout (cmd/zorgscope/main.go's
// upstreamTimeout) bounds each individual request, but not a sequence of individually-prompt
// requests that never stops asking for another page — this cap is what bounds that.
const maxPages = 100

// Config configures access to GitHub for IssueFetcher.
type Config struct {
	Token   string
	BaseURL string   // "" -> https://api.github.com/graphql (GraphQL endpoint)
	Repos   []string // "owner/name"
}

// IssueFetcher fetches open issues and open pull requests for the repositories in Config, over
// the GitHub GraphQL API (FR-1.1).
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

// Fetch retrieves open issues and open pull requests for every repository in f.repos, so that
// *IssueFetcher satisfies ports.Source. A failure fetching one repository does not lose items
// already fetched from the others (QS-1.4): every per-repository error is collected, joined with
// errors.Join, and returned alongside every item successfully fetched — never returned early on
// the first failure.
//
// The repositories are fetched side by side (QS-2.7): one goroutine per repository and
// connection, all started at once. There is no separate concurrency limit, because QS-3.5 caps the
// configuration at ten repositories and therefore at twenty requests in flight. Each goroutine
// writes into its own slot, and the slots are read out in order once every goroutine has finished,
// so the result is the one the sequential loop produced — repositories in configuration order, a
// repository's issues before its pull requests — whatever order the upstream answered in.
func (f *IssueFetcher) Fetch(ctx context.Context) ([]domain.Item, error) {
	type slot struct {
		items []domain.Item
		err   error
	}
	// Two slots per repository: issues at 2i, pull requests at 2i+1.
	slots := make([]slot, 2*len(f.repos))

	var wg sync.WaitGroup
	for i, repo := range f.repos {
		owner, name, ok := splitRepo(repo)
		if !ok {
			slots[2*i].err = fmt.Errorf("github: %q is not owner/name", repo)
			continue
		}
		wg.Add(2)
		go func(s *slot) {
			defer wg.Done()
			s.items, s.err = f.fetchIssues(ctx, owner, name)
			if s.err != nil {
				s.err = fmt.Errorf("github: fetching issues for %s: %w", repo, s.err)
			}
		}(&slots[2*i])
		go func(s *slot) {
			defer wg.Done()
			s.items, s.err = f.fetchPullRequests(ctx, owner, name)
			if s.err != nil {
				s.err = fmt.Errorf("github: fetching pull requests for %s: %w", repo, s.err)
			}
		}(&slots[2*i+1])
	}
	wg.Wait()

	var items []domain.Item
	var errs []error
	for _, s := range slots {
		items = append(items, s.items...)
		if s.err != nil {
			errs = append(errs, s.err)
		}
	}
	return items, errors.Join(errs...)
}

// ghIssueConnection is one page of the issues connection: a page of nodes plus pageInfo.
type ghIssueConnection struct {
	Nodes    []ghIssueNode
	PageInfo ghPageInfo
}

// ghPRConnection is one page of the pullRequests connection. It is a separate type from
// ghIssueConnection only because its nodes are: see ghPRNode.
type ghPRConnection struct {
	Nodes    []ghPRNode
	PageInfo ghPageInfo
}

// issuesQuery fetches one page of a repository's open issues. Pull requests are fetched by a
// separate query (pullRequestsQuery) with their own cursor variable: on GitHub, issues and pull
// requests paginate independently, so one query sharing a single cursor between the two
// connections would be wrong, not just inconvenient.
type issuesQuery struct {
	Repository struct {
		Issues ghIssueConnection `graphql:"issues(states: OPEN, first: 100, after: $after, orderBy: {field: CREATED_AT, direction: ASC})"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// pullRequestsQuery fetches one page of a repository's open pull requests. See issuesQuery.
type pullRequestsQuery struct {
	Repository struct {
		PullRequests ghPRConnection `graphql:"pullRequests(states: OPEN, first: 100, after: $after, orderBy: {field: CREATED_AT, direction: ASC})"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// ghIssueNode is one issue node.
type ghIssueNode struct {
	Number githubv4.Int
	Title  githubv4.String
	// BodyText is the item's description with GitHub's Markdown already stripped, which is the
	// field to ask for rather than body: the dashboard shows a line of prose in small type, and
	// rendering raw Markdown there would put backticks, link syntax and image tags on the page.
	// It arrives whole and is cut down to maxSummaryLen before it is stored — asking for a
	// prefix is not something GraphQL offers, and keeping the whole of every issue body of eight
	// repositories in the database would grow it for text no page ever shows.
	BodyText  githubv4.String
	URL       githubv4.URI
	Author    struct{ Login githubv4.String }
	CreatedAt githubv4.DateTime
	UpdatedAt githubv4.DateTime
	State     githubv4.String
	Labels    ghLabelConnection `graphql:"labels(first: 5)"`
}

// ghPRNode is one pull-request node: every field of ghIssueNode plus isDraft (FR-2.1 AC1).
//
// The seven shared fields are spelled out again rather than embedded. shurcooL/graphql builds the
// query text by reflecting over this struct, and it reads an embedded struct as a GraphQL
// fragment, not as a set of inlined fields — so embedding would change the query, not just the
// Go type. Duplicating seven field declarations is the cheaper of the two mistakes.
//
// A separate type is required in the first place because isDraft exists on GitHub's PullRequest
// and not on its Issue. One shared node type would put isDraft into the issues query too, which
// real GitHub rejects with "Field 'isDraft' doesn't exist on type 'Issue'" — a failure the fake
// server would never show, since it does not validate queries against a schema.
type ghPRNode struct {
	Number    githubv4.Int
	Title     githubv4.String
	BodyText  githubv4.String
	URL       githubv4.URI
	Author    struct{ Login githubv4.String }
	CreatedAt githubv4.DateTime
	UpdatedAt githubv4.DateTime
	State     githubv4.String
	Labels    ghLabelConnection `graphql:"labels(first: 5)"`
	IsDraft   githubv4.Boolean
}

// ghPageInfo mirrors a GraphQL connection's pageInfo.
type ghPageInfo struct {
	HasNextPage githubv4.Boolean
	EndCursor   githubv4.String
}

// ghLabelNode is one label of an issue or pull request: its name is all the page needs.
type ghLabelNode struct {
	Name githubv4.String
}

// ghLabelConnection is the labels connection of one node. first: 5 is the whole of any arc42
// item's labels today (the most-labelled item has three), and it is a field on a node the query
// already fetches — it costs points, not requests, so QS-3.5's count of 20 does not move
// (FR-1.10 AC3).
type ghLabelConnection struct {
	Nodes []ghLabelNode
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
			items = append(items, toPRItem(owner, name, n))
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
// looping: an unmoving cursor with hasNextPage true is the one shape of misbehaviour maxPages
// above does not catch on its own, since each request it makes looks individually well-formed
// (QS-2.5).
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

// toItem maps one GraphQL issue node to a domain.Item.
func toItem(owner, name string, kind domain.Kind, n ghIssueNode) domain.Item {
	repo := owner + "/" + name
	number := int(n.Number)
	return domain.Item{
		Kind:       kind,
		Repo:       repo,
		Number:     number,
		Title:      string(n.Title),
		Summary:    summarise(string(n.BodyText)),
		Advisories: advisoryIDs(string(n.Title), string(n.BodyText)),
		URL:        n.URL.String(),
		Author:     string(n.Author.Login),
		Labels:     labelNames(n.Labels),
		State:      string(n.State),
		CreatedAt:  n.CreatedAt.UTC(),
		UpdatedAt:  n.UpdatedAt.UTC(),
	}
}

// draftState is the value Item.State carries for a draft pull request (FR-2.1 AC1).
const draftState = "DRAFT"

// toPRItem maps one GraphQL pull-request node to a domain.Item, carrying the draft flag in State.
//
// State is the honest place for it, not a workaround for the absence of a dedicated field.
// pullRequestsQuery asks for states: OPEN, so every node reaching here has state OPEN and the
// field carries no information at all today — it is a constant. Replacing that constant with
// "DRAFT" for a draft therefore loses nothing, and it is the vocabulary GitHub itself uses:
// `gh pr list` prints DRAFT in the same column that otherwise prints OPEN, and the web UI labels
// the pull request "Draft" where it would say "Open". A reader of the stored row sees a value
// that means what it says.
//
// The alternative — a bool on domain.Item — would need a column to survive the store, and so a
// second migration, for one bit that an existing column already has room for. Nothing branches on
// State today (nothing else reads it), so nothing is broken by the value changing; a renderer that
// wants a "draft" badge tests State == "DRAFT".
func toPRItem(owner, name string, n ghPRNode) domain.Item {
	it := toItem(owner, name, domain.KindPR, ghIssueNode{
		Number:    n.Number,
		Title:     n.Title,
		BodyText:  n.BodyText,
		URL:       n.URL,
		Author:    n.Author,
		CreatedAt: n.CreatedAt,
		UpdatedAt: n.UpdatedAt,
		State:     n.State,
		Labels:    n.Labels,
	})
	if bool(n.IsDraft) {
		it.State = draftState
	}
	return it
}

// labelNames is the names of a label connection, in GitHub's order; nil for none, so an item
// without labels carries nil rather than an empty slice.
func labelNames(c ghLabelConnection) []string {
	if len(c.Nodes) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.Nodes))
	for _, l := range c.Nodes {
		out = append(out, string(l.Name))
	}
	return out
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

// maxAdvisories bounds how many advisory identifiers an item keeps. One is enough to explain why
// a row is red, and three keeps the chip's title readable; a Dependabot group update can cite a
// dozen.
const maxAdvisories = 3

// advisoryPattern matches the two identifier schemes GitHub's own advisories use: CVE, with at
// least four digits after the year as the scheme requires, and GHSA, three groups of four.
var advisoryPattern = regexp.MustCompile(`(?i)\b(CVE-\d{4}-\d{4,}|GHSA(?:-[0-9a-z]{4}){3})\b`)

// advisoryIDs returns the CVE and GHSA identifiers the texts cite, in the order first seen,
// without duplicates, at most maxAdvisories (FR-1.13 AC1).
//
// It exists because of where Dependabot puts them: in the release notes it quotes, far past the
// 300 bytes zorgscope keeps as a summary. So it has to run on the whole body, before summarise
// cuts it — which costs nothing, because the body already arrives whole in the query that runs
// today (QS-3.5). The identifiers are normalised so that the same advisory spelled in two cases is
// one advisory: CVE upper-case, as the scheme writes it, and GHSA with a lower-case body, as GitHub
// writes it.
func advisoryIDs(texts ...string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, text := range texts {
		for _, m := range advisoryPattern.FindAllString(text, -1) {
			id := strings.ToUpper(m)
			if strings.HasPrefix(id, "GHSA-") {
				id = "GHSA-" + strings.ToLower(id[len("GHSA-"):])
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
			if len(out) == maxAdvisories {
				return out
			}
		}
	}
	return out
}

// maxSummaryLen bounds what is stored of an item's body text. The dashboard renders roughly eighty
// characters of it, so this is generous enough that a longer display can be tried without another
// refresh, and small enough that eight repositories of issue bodies stay a rounding error in a
// database that is measured in kilobytes.
const maxSummaryLen = 300

// summarise reduces an item's body text to what is worth storing: whitespace collapsed to single
// spaces and the result cut to maxSummaryLen.
//
// The whitespace matters more than the length. A GitHub issue body is written as Markdown, so it
// arrives full of newlines, indentation and blank lines even after GitHub has stripped the markup
// for bodyText; stored as it is, it would be a paragraph of ragged text sitting in one HTML
// element, where every one of those newlines collapses to a single space anyway. Doing it here
// means what is stored is what is meant, and the eighty characters the page shows are eighty
// characters of prose rather than eighty characters of indentation.
func summarise(body string) string {
	collapsed := strings.Join(strings.Fields(body), " ")
	if len(collapsed) <= maxSummaryLen {
		return collapsed
	}
	// Cut on a rune boundary: a body is arbitrary text and slicing mid-rune would store invalid
	// UTF-8, which the template would then render as a replacement character.
	cut := maxSummaryLen
	for cut > 0 && !utf8.RuneStart(collapsed[cut]) {
		cut--
	}
	return collapsed[:cut]
}
