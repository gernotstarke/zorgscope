package fakesources

import (
	"encoding/json"
	"net/http"
)

// pageSize is the fixed page size the GraphQL handler serves — 100 nodes per connection per
// page, matching the "first: 100" Task 7's query is expected to use.
const pageSize = 100

// nextCursor is the only pagination cursor this fake ever hands out. A real GitHub cursor is an
// opaque blob; here it names the page instead, which is enough to drive a two-page fixture
// (org/paged) and is easy to assert against in a test.
const nextCursor = "cursor-1"

// graphqlRequest is the shape Task 7's client sends. The query text itself is ignored — this
// fake decides what to return purely from variables, honouring exactly the two names Task 7 is
// told to send (owner, name) plus the pagination cursor (after).
type graphqlRequest struct {
	Query     string `json:"query"`
	Variables struct {
		Owner string `json:"owner"`
		Name  string `json:"name"`
		After string `json:"after"`
	} `json:"variables"`
}

// graphqlResponse is shaped exactly as shurcooL/githubv4 unmarshals it:
// data.repository.issues.nodes[] and data.repository.pullRequests.nodes[], each connection
// carrying pageInfo.hasNextPage and pageInfo.endCursor. This shape is the contract with Task 7.
type graphqlResponse struct {
	Data struct {
		Repository struct {
			Issues       ghConnection `json:"issues"`
			PullRequests ghConnection `json:"pullRequests"`
		} `json:"repository"`
	} `json:"data"`
}

// ghConnection is one paginated GraphQL connection: a page of nodes plus pageInfo.
type ghConnection struct {
	Nodes    []ghIssue  `json:"nodes"`
	PageInfo ghPageInfo `json:"pageInfo"`
}

// ghPageInfo is a GraphQL connection's pageInfo sub-object.
type ghPageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

// handleGraphQL serves POST /graphql. It ignores the query text and decides what to return from
// variables.owner and variables.name — when either is absent (the brief's own test posts
// {"query":"{}"} with no variables at all), it serves the org/repo fixture. variables.after
// selects the page: absent or empty means page 1, "cursor-1" means page 2.
func (s *server) handleGraphQL(w http.ResponseWriter, r *http.Request) {
	var req graphqlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}

	repo := req.Variables.Owner + "/" + req.Variables.Name
	if req.Variables.Owner == "" || req.Variables.Name == "" {
		repo = "org/repo"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if status, fail := s.shouldFailLocked("github", repo); fail {
		w.WriteHeader(status)
		return
	}

	fx := s.githubRepos[repo] // nil (zero value) for an unconfigured repo: served as empty.

	var resp graphqlResponse
	if fx != nil {
		resp.Data.Repository.Issues = paginate(fx.Issues, req.Variables.After)
		resp.Data.Repository.PullRequests = paginate(fx.PullRequests, req.Variables.After)
	} else {
		resp.Data.Repository.Issues = ghConnection{Nodes: []ghIssue{}}
		resp.Data.Repository.PullRequests = ghConnection{Nodes: []ghIssue{}}
	}

	writeJSON(w, http.StatusOK, resp)
}

// paginate slices nodes into the page named by after: "" (or any value other than nextCursor)
// serves page 1, the first pageSize nodes; nextCursor serves the remainder. This only ever
// produces two pages, which is all the fixtures need — org/paged is the one repository with more
// than pageSize nodes.
func paginate(nodes []ghIssue, after string) ghConnection {
	start := 0
	if after == nextCursor {
		start = pageSize
	}
	if start > len(nodes) {
		start = len(nodes)
	}
	end := start + pageSize
	if end > len(nodes) {
		end = len(nodes)
	}

	page := nodes[start:end]
	if page == nil {
		page = []ghIssue{}
	}

	hasNext := end < len(nodes)
	cursor := ""
	if hasNext {
		cursor = nextCursor
	}

	return ghConnection{Nodes: page, PageInfo: ghPageInfo{HasNextPage: hasNext, EndCursor: cursor}}
}
