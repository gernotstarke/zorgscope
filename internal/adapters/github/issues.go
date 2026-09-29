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
	"sync/atomic"
	"unicode/utf8"

	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// defaultGraphQLURL is used when Config.BaseURL is empty, and defaultRESTURL when
// Config.RESTBaseURL is.
const (
	defaultGraphQLURL = "https://api.github.com/graphql"
	defaultRESTURL    = "https://api.github.com"
)

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
	BaseURL string // "" -> https://api.github.com/graphql (GraphQL endpoint)
	// RESTBaseURL is the REST root, "" -> https://api.github.com. It is asked one thing only: whether
	// an empty list of Dependabot alerts may be believed (alertsReadable).
	RESTBaseURL string
	Repos       []string // "owner/name"
	// NoAlerts are the repositories never asked for Dependabot alerts (spec 2026-09-29 §5): their
	// pull request query leaves the alert connection out and no REST probe follows.
	NoAlerts []string
}

// IssueFetcher fetches open issues and open pull requests for the repositories in Config, over
// the GitHub GraphQL API (FR-1.1).
type IssueFetcher struct {
	client *githubv4.Client
	repos  []string
	// noAlerts are the repositories of Config.NoAlerts.
	noAlerts map[string]bool
	// rest and hc are the REST root and the token-carrying client alertsReadable asks with, and
	// readable its answers, per repository, for the life of the process.
	rest     string
	hc       *http.Client
	readable sync.Map // "owner/name" -> bool
	// alertsRefused is set once GitHub has refused the alert fields while answering the same page
	// without them (ADR-0015). From then on this process stops asking: a refused token costs one
	// extra request per repository once, not on every fetch. A restart — on this machine every
	// wake from zero — asks again.
	alertsRefused atomic.Bool
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

	rest := strings.TrimRight(cfg.RESTBaseURL, "/")
	if rest == "" {
		rest = defaultRESTURL
	}

	noAlerts := make(map[string]bool, len(cfg.NoAlerts))
	for _, repo := range cfg.NoAlerts {
		noAlerts[repo] = true
	}

	return &IssueFetcher{
		client:   githubv4.NewEnterpriseClient(url, &authed),
		repos:    cfg.Repos,
		noAlerts: noAlerts,
		rest:     rest,
		hc:       &authed,
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
// configuration at fifteen repositories and therefore at thirty requests in flight. Each goroutine
// writes into its own slot, and the slots are read out in order once every goroutine has finished,
// so the result is the one the sequential loop produced — repositories in configuration order, a
// repository's issues before its pull requests — whatever order the upstream answered in.
func (f *IssueFetcher) Fetch(ctx context.Context) (domain.Fetched, error) {
	type slot struct {
		items    []domain.Item
		coverage domain.Coverage
		err      error
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
			s.items, s.coverage, s.err = f.fetchPullRequests(ctx, owner, name)
			if s.err != nil {
				s.err = fmt.Errorf("github: fetching pull requests for %s: %w", repo, s.err)
			}
		}(&slots[2*i+1])
	}
	wg.Wait()

	got := domain.Fetched{Coverage: make(map[string]domain.Coverage, len(f.repos))}
	var errs []error
	for i, s := range slots {
		got.Items = append(got.Items, s.items...)
		if s.coverage != domain.CoverageUnknown {
			got.Coverage[f.repos[i/2]] = s.coverage
		}
		if s.err != nil {
			errs = append(errs, s.err)
		}
	}
	return got, errors.Join(errs...)
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

// pullRequestsFirstPageQuery is pullRequestsQuery's first page, which also asks for the
// repository's Dependabot alerts (FR-1.16, ADR-0015). Nested in the query already made, they cost
// GitHub points but no request (QS-3.5). It is a type of its own because shurcooL/graphql builds
// the query text from the struct, so a field cannot be left out per call; later pages use
// pullRequestsQuery and do not ask for the alerts again.
type pullRequestsFirstPageQuery struct {
	Repository struct {
		PullRequests                  ghPRConnection `graphql:"pullRequests(states: OPEN, first: 100, after: $after, orderBy: {field: CREATED_AT, direction: ASC})"`
		HasVulnerabilityAlertsEnabled githubv4.Boolean
		VulnerabilityAlerts           ghAlertConnection `graphql:"vulnerabilityAlerts(states: OPEN, first: 100)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// ghAlertConnection is the open Dependabot alerts of one repository. At most 100 are read; no
// arc42 repository has had more than one open at a time, so paginating would be code for a case
// that does not occur.
type ghAlertConnection struct {
	Nodes []ghAlertNode
}

// ghAlertNode is one Dependabot alert, with what the page shows of it.
type ghAlertNode struct {
	Number                 githubv4.Int
	CreatedAt              githubv4.DateTime
	VulnerableManifestPath githubv4.String
	VulnerableRequirements githubv4.String
	SecurityAdvisory       struct {
		Summary     githubv4.String
		Identifiers []struct {
			Type  githubv4.String
			Value githubv4.String
		}
	}
	SecurityVulnerability struct {
		Severity githubv4.String
		Package  struct {
			Name      githubv4.String
			Ecosystem githubv4.String
		}
		// FirstPatchedVersion is null while no fixed release exists.
		FirstPatchedVersion *struct {
			Identifier githubv4.String
		}
	}
	// DependabotUpdate is null unless Dependabot tried to open a fix; its pullRequest is null
	// unless it did.
	DependabotUpdate *struct {
		PullRequest *struct {
			Number githubv4.Int
			State  githubv4.String
		}
	}
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
	// ReviewRequests is who the pull request asks for a review (FR-1.14). Only a User has a login;
	// a Team or a Bot reviewer arrives with an empty one and is skipped. Like labels it is a field
	// on a node the query already fetches, so it costs points, not requests (QS-3.5).
	ReviewRequests ghReviewRequestConnection `graphql:"reviewRequests(first: 10)"`
}

// ghReviewRequestConnection is the reviewRequests connection of one pull request.
type ghReviewRequestConnection struct {
	Nodes []struct {
		RequestedReviewer struct {
			User struct{ Login githubv4.String } `graphql:"... on User"`
		}
	}
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
// forward-progress check and maxPages. Its first page also brings the repository's Dependabot
// alerts, which follow the pull requests in the result, and what could be said about them.
func (f *IssueFetcher) fetchPullRequests(ctx context.Context, owner, name string) ([]domain.Item, domain.Coverage, error) {
	repo := owner + "/" + name
	conn, alerts, coverage, err := f.firstPullRequestPage(ctx, owner, name)
	if err != nil {
		return nil, domain.CoverageUnknown, err
	}

	var items []domain.Item
	var after *githubv4.String
	for page := 1; ; page++ {
		for _, n := range conn.Nodes {
			items = append(items, toPRItem(owner, name, n))
		}

		next, done, err := nextAfter(after, conn.PageInfo, repo)
		if err != nil {
			return append(items, alerts...), coverage, err
		}
		if done {
			return append(items, alerts...), coverage, nil
		}
		if page >= maxPages {
			return append(items, alerts...), coverage,
				fmt.Errorf("github: %s: pull request pagination did not stop after %d pages", repo, maxPages)
		}
		after = next

		var q pullRequestsQuery
		if err := f.client.Query(ctx, &q, prVars(owner, name, after)); err != nil {
			return append(items, alerts...), coverage, err
		}
		conn = q.Repository.PullRequests
	}
}

// firstPullRequestPage fetches the first page of owner/name's open pull requests together with its
// Dependabot alerts, and reports the alerts' coverage (FR-1.16, ADR-0015).
//
// Asking for alerts must never cost the repository its pull requests (QS-1.4). GitHub refuses them
// in two shapes — the whole query with no data when the token lacks the scope, or only the alert
// fields when its owner may not see them — and shurcooL/graphql hands back only the messages, whose
// wording is GitHub's to change. So neither shape is recognised by its text: any error on the
// page that asks for alerts is followed by the same page without them. If that one is answered,
// the alerts were what GitHub refused, and the process stops asking (alertsRefused). If it fails
// too, it is the ordinary failure of the repository it would have been without alerts.
func (f *IssueFetcher) firstPullRequestPage(ctx context.Context, owner, name string) (ghPRConnection, []domain.Item, domain.Coverage, error) {
	vars := prVars(owner, name, nil)
	if f.noAlerts[owner+"/"+name] {
		var q pullRequestsQuery
		if err := f.client.Query(ctx, &q, vars); err != nil {
			return ghPRConnection{}, nil, domain.CoverageUnknown, err
		}
		// Not asked is not refused: alertsRefused stays as it was, and no coverage is claimed.
		return q.Repository.PullRequests, nil, domain.CoverageUnknown, nil
	}
	if !f.alertsRefused.Load() {
		var q pullRequestsFirstPageQuery
		if err := f.client.Query(ctx, &q, vars); err == nil {
			r := q.Repository
			if !bool(r.HasVulnerabilityAlertsEnabled) {
				return r.PullRequests, nil, domain.CoverageOff, nil
			}
			if len(r.VulnerabilityAlerts.Nodes) == 0 {
				if !f.alertsReadable(ctx, owner, name) {
					return r.PullRequests, nil, domain.CoverageUnavailable, nil
				}
				return r.PullRequests, nil, domain.CoverageOn, nil
			}
			alerts := make([]domain.Item, 0, len(r.VulnerabilityAlerts.Nodes))
			for _, n := range r.VulnerabilityAlerts.Nodes {
				alerts = append(alerts, toAlertItem(owner, name, n))
			}
			return r.PullRequests, alerts, domain.CoverageOn, nil
		}
	}

	var q pullRequestsQuery
	if err := f.client.Query(ctx, &q, vars); err != nil {
		return ghPRConnection{}, nil, domain.CoverageUnknown, err
	}
	f.alertsRefused.Store(true)
	return q.Repository.PullRequests, nil, domain.CoverageUnavailable, nil
}

// alertsReadable reports whether this token may read owner/name's Dependabot alerts, so that an
// empty list from GraphQL can be believed (FR-1.16 AC5).
//
// GraphQL does not refuse a token that may not read them — a fine-grained token without the
// Dependabot alerts permission gets an empty list and no error, which reads exactly like a clean
// repository. The REST endpoint does refuse it, with 403. So an empty list is checked there once,
// with one alert asked for, and the answer is kept for the life of the process; a list that is not
// empty needs no check, since it could not have been read otherwise. Only a 200 is believed. Any
// other answer — a refusal, an outage, a timeout — means the repository is not reported clean, and
// only a refusal is remembered, so that a passing outage does not stick.
func (f *IssueFetcher) alertsReadable(ctx context.Context, owner, name string) bool {
	repo := owner + "/" + name
	if v, ok := f.readable.Load(repo); ok {
		return v.(bool)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		f.rest+"/repos/"+repo+"/dependabot/alerts?state=open&per_page=1", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := f.hc.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		f.readable.Store(repo, true)
		return true
	case http.StatusForbidden, http.StatusNotFound, http.StatusUnauthorized:
		f.readable.Store(repo, false)
		return false
	default:
		return false
	}
}

// prVars are the variables of both pull request queries.
func prVars(owner, name string, after *githubv4.String) map[string]interface{} {
	return map[string]interface{}{
		"owner": githubv4.String(owner),
		"name":  githubv4.String(name),
		"after": after,
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
const draftState = domain.StateDraft

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
	for _, r := range n.ReviewRequests.Nodes {
		if login := string(r.RequestedReviewer.User.Login); login != "" {
			it.ReviewRequested = append(it.ReviewRequested, login)
		}
	}
	return it
}

// dependabotLogin is who every alert is by: GitHub's Dependabot raised it, nobody opened it.
const dependabotLogin = "dependabot"

// toAlertItem maps one Dependabot alert to a domain.Item of KindAlert (FR-1.16).
//
// GraphQL offers no URL for an alert, so the link is built from its number, the address GitHub
// itself uses for it. An alert has no update time either: it is raised, and then it is open until
// it is fixed or dismissed, so its creation time stands for both — it is what the list sorts by and
// what the radar measures.
func toAlertItem(owner, name string, n ghAlertNode) domain.Item {
	v := n.SecurityVulnerability
	facts := &domain.AlertFacts{
		Severity:   domain.ParseSeverity(string(v.Severity)),
		Package:    string(v.Package.Name),
		Ecosystem:  string(v.Package.Ecosystem),
		Manifest:   string(n.VulnerableManifestPath),
		Vulnerable: string(n.VulnerableRequirements),
	}
	if v.FirstPatchedVersion != nil {
		facts.PatchedIn = string(v.FirstPatchedVersion.Identifier)
	}
	if u := n.DependabotUpdate; u != nil && u.PullRequest != nil && strings.EqualFold(string(u.PullRequest.State), "OPEN") {
		facts.FixPR = int(u.PullRequest.Number)
	}

	// GHSA first, as GitHub names its own advisories, then CVE; normalised as a cited identifier is.
	var ghsa, cve []string
	for _, id := range n.SecurityAdvisory.Identifiers {
		if strings.EqualFold(string(id.Type), "GHSA") {
			ghsa = append(ghsa, string(id.Value))
		} else {
			cve = append(cve, string(id.Value))
		}
	}

	created := n.CreatedAt.UTC()
	number := int(n.Number)
	return domain.Item{
		Kind:       domain.KindAlert,
		Repo:       owner + "/" + name,
		Number:     number,
		Title:      string(n.SecurityAdvisory.Summary),
		Summary:    summarise(alertSummary(facts)),
		Advisories: advisoryIDs(strings.Join(ghsa, " "), strings.Join(cve, " ")),
		URL:        fmt.Sprintf("https://github.com/%s/%s/security/dependabot/%d", owner, name, number),
		Author:     dependabotLogin,
		Alert:      facts,
		State:      "OPEN",
		CreatedAt:  created,
		UpdatedAt:  created,
	}
}

// alertSummary is the line the page shows under an alert's title: the package, the version the
// manifest pins and the one that fixes it, and the manifest — "rubyzip 2.3.2 → 3.4.0 ·
// Gemfile.lock". A requirement that pins one version ("= 2.3.2") is shown as that version; any
// other is shown as GitHub states it.
func alertSummary(a *domain.AlertFacts) string {
	vulnerable := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(a.Vulnerable), "="))
	line := a.Package
	if vulnerable != "" {
		line += " " + vulnerable
	}
	if a.PatchedIn != "" {
		line += " → " + a.PatchedIn
	} else {
		line += " · no patched version"
	}
	if a.Manifest != "" {
		line += " · " + a.Manifest
	}
	return line
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
