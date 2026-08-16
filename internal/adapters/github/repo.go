package github

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// repoQuery fetches open issues and PRs with the last comment/review author (verified against the
// live API on 2026-08-16). @include lets us continue paginating one list after the other is done.
const repoQuery = `query($owner:String!,$name:String!,$n:Int!,$issuesAfter:String,$prsAfter:String,$withIssues:Boolean!,$withPRs:Boolean!){
  rateLimit{cost remaining resetAt}
  repository(owner:$owner,name:$name){
    nameWithOwner defaultBranchRef{name}
    issues(states:OPEN,first:$n,after:$issuesAfter,orderBy:{field:CREATED_AT,direction:DESC}) @include(if:$withIssues){
      pageInfo{hasNextPage endCursor}
      nodes{ number title url createdAt updatedAt author{login} labels(first:10){nodes{name}}
             comments(last:1){totalCount nodes{author{login} createdAt}} }
    }
    pullRequests(states:OPEN,first:$n,after:$prsAfter,orderBy:{field:CREATED_AT,direction:DESC}) @include(if:$withPRs){
      pageInfo{hasNextPage endCursor}
      nodes{ number title url createdAt updatedAt isDraft reviewDecision author{login} labels(first:10){nodes{name}}
             comments(last:1){totalCount nodes{author{login} createdAt}}
             reviews(last:1){nodes{author{login} submittedAt}} }
    }
  }
}`

type gqlActor struct {
	Login string `json:"login"`
}

type gqlNode struct {
	Number         int       `json:"number"`
	Title          string    `json:"title"`
	URL            string    `json:"url"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	IsDraft        bool      `json:"isDraft"`
	ReviewDecision string    `json:"reviewDecision"`
	Author         *gqlActor `json:"author"`
	Labels         struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Comments struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Author    *gqlActor `json:"author"`
			CreatedAt time.Time `json:"createdAt"`
		} `json:"nodes"`
	} `json:"comments"`
	Reviews struct {
		Nodes []struct {
			Author      *gqlActor `json:"author"`
			SubmittedAt time.Time `json:"submittedAt"`
		} `json:"nodes"`
	} `json:"reviews"`
}

type gqlPage struct {
	PageInfo struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
	Nodes []gqlNode `json:"nodes"`
}

type repoData struct {
	Repository *struct {
		NameWithOwner    string `json:"nameWithOwner"`
		DefaultBranchRef *struct {
			Name string `json:"name"`
		} `json:"defaultBranchRef"`
		Issues       *gqlPage `json:"issues"`
		PullRequests *gqlPage `json:"pullRequests"`
	} `json:"repository"`
}

type runsResponse struct {
	WorkflowRuns []struct {
		ID           int64     `json:"id"`
		Name         string    `json:"name"`
		HTMLURL      string    `json:"html_url"`
		Status       string    `json:"status"`
		Conclusion   string    `json:"conclusion"`
		HeadBranch   string    `json:"head_branch"`
		CreatedAt    time.Time `json:"created_at"`
		UpdatedAt    time.Time `json:"updated_at"`
		RunStartedAt time.Time `json:"run_started_at"`
	} `json:"workflow_runs"`
}

var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// RepoFetcher fetches open issues, PRs and the latest default-branch workflow run of one repository.
type RepoFetcher struct {
	c        *Client
	owner    string
	name     string
	PageSize int
}

// NewRepoFetcher validates "owner/name" and creates a fetcher.
func NewRepoFetcher(c *Client, fullName string) (*RepoFetcher, error) {
	if !repoNameRe.MatchString(fullName) {
		return nil, fmt.Errorf("github: repository name %q must be owner/name", fullName)
	}
	owner, name := splitRepo(fullName)
	return &RepoFetcher{c: c, owner: owner, name: name, PageSize: 100}, nil
}

func splitRepo(full string) (owner, name string) {
	for i := 0; i < len(full); i++ {
		if full[i] == '/' {
			return full[:i], full[i+1:]
		}
	}
	return full, ""
}

// ID implements ports.SourceFetcher.
func (f *RepoFetcher) ID() string { return "github:" + f.owner + "/" + f.name }

// Kind implements ports.SourceFetcher.
func (f *RepoFetcher) Kind() string { return ports.KindGitHubRepo }

// Fetch implements ports.SourceFetcher.
func (f *RepoFetcher) Fetch(ctx context.Context) ([]domain.Item, error) {
	var items []domain.Item
	var issuesAfter, prsAfter any
	withIssues, withPRs := true, true
	defaultBranch := ""
	for withIssues || withPRs {
		var data repoData
		vars := map[string]any{"owner": f.owner, "name": f.name, "n": f.PageSize, "issuesAfter": issuesAfter, "prsAfter": prsAfter,
			"withIssues": withIssues, "withPRs": withPRs}
		if err := f.c.graphql(ctx, repoQuery, vars, &data); err != nil {
			return nil, err
		}
		if data.Repository == nil {
			return nil, fmt.Errorf("%w: repository %s/%s not found or not accessible", ports.ErrPermanent, f.owner, f.name)
		}
		if data.Repository.DefaultBranchRef != nil {
			defaultBranch = data.Repository.DefaultBranchRef.Name
		}
		if withIssues {
			if data.Repository.Issues == nil {
				withIssues = false
			} else {
				for _, n := range data.Repository.Issues.Nodes {
					items = append(items, f.mapNode(n, false))
				}
				withIssues = data.Repository.Issues.PageInfo.HasNextPage
				issuesAfter = data.Repository.Issues.PageInfo.EndCursor
			}
		}
		if withPRs {
			if data.Repository.PullRequests == nil {
				withPRs = false
			} else {
				for _, n := range data.Repository.PullRequests.Nodes {
					items = append(items, f.mapNode(n, true))
				}
				withPRs = data.Repository.PullRequests.PageInfo.HasNextPage
				prsAfter = data.Repository.PullRequests.PageInfo.EndCursor
			}
		}
	}
	if defaultBranch != "" {
		run, err := f.latestRun(ctx, defaultBranch)
		if err != nil {
			return nil, err
		}
		if run != nil {
			items = append(items, *run)
		}
	}
	return items, nil
}

func login(a *gqlActor) string {
	if a == nil {
		return "ghost"
	}
	return a.Login
}

func (f *RepoFetcher) mapNode(n gqlNode, isPR bool) domain.Item {
	kind, prefix := domain.KindIssue, "issues/"
	if isPR {
		kind, prefix = domain.KindPR, "pulls/"
	}
	it := domain.Item{
		ID:        domain.ItemID{SourceID: f.ID(), ExternalID: fmt.Sprintf("%s%d", prefix, n.Number)},
		Kind:      kind,
		Title:     n.Title,
		URL:       n.URL,
		Author:    login(n.Author),
		CreatedAt: n.CreatedAt,
		UpdatedAt: n.UpdatedAt,
	}
	for _, l := range n.Labels.Nodes {
		it.Labels = append(it.Labels, l.Name)
	}
	if len(n.Comments.Nodes) > 0 {
		c := n.Comments.Nodes[len(n.Comments.Nodes)-1]
		it.LastActivityBy, it.LastActivityAt = login(c.Author), c.CreatedAt
	}
	if len(n.Reviews.Nodes) > 0 {
		r := n.Reviews.Nodes[len(n.Reviews.Nodes)-1]
		if r.SubmittedAt.After(it.LastActivityAt) {
			it.LastActivityBy, it.LastActivityAt = login(r.Author), r.SubmittedAt
		}
	}
	if isPR {
		it.Payload = domain.MustPayload(domain.PRPayload{Draft: n.IsDraft, ReviewDecision: n.ReviewDecision, Comments: n.Comments.TotalCount})
	} else {
		it.Payload = domain.MustPayload(domain.IssuePayload{Comments: n.Comments.TotalCount})
	}
	return it
}

func (f *RepoFetcher) latestRun(ctx context.Context, branch string) (*domain.Item, error) {
	q := url.Values{"branch": {branch}, "per_page": {"1"}, "exclude_pull_requests": {"true"}}
	var resp runsResponse
	path := fmt.Sprintf("/repos/%s/%s/actions/runs?%s", url.PathEscape(f.owner), url.PathEscape(f.name), q.Encode())
	if _, err := f.c.do(ctx, "GET", path, nil, &resp); err != nil {
		return nil, err
	}
	if len(resp.WorkflowRuns) == 0 {
		return nil, nil
	}
	r := resp.WorkflowRuns[0]
	created := r.RunStartedAt
	if created.IsZero() {
		created = r.CreatedAt
	}
	return &domain.Item{
		ID: domain.ItemID{SourceID: f.ID(), ExternalID: fmt.Sprintf("runs/%d", r.ID)}, Kind: domain.KindWorkflowRun,
		Title: r.Name, URL: r.HTMLURL, CreatedAt: created, UpdatedAt: r.UpdatedAt,
		Payload: domain.MustPayload(domain.WorkflowRunPayload{RunID: r.ID, WorkflowName: r.Name, Conclusion: r.Conclusion, Status: r.Status, Branch: r.HeadBranch}),
	}, nil
}
