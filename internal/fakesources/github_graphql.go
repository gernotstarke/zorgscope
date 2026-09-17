package fakesources

import (
	"encoding/json"
	"net/http"
	"strings"
)

// pageSize is the fixed page size the GraphQL handler serves — 100 nodes per connection per
// page, matching the "first: 100" Task 7's query is expected to use.
const pageSize = 100

// nextCursor is the only pagination cursor this fake ever hands out. A real GitHub cursor is an
// opaque blob; here it names the page instead, which is enough to drive a two-page fixture
// (org/paged) and is easy to assert against in a test.
const nextCursor = "cursor-1"

// graphqlRequest is the shape Task 7's client sends. variables.owner and variables.name (plus the
// pagination cursor, variables.after) decide which repository and page to serve. The query text
// itself is otherwise not interpreted — it is only inspected for the two connection names, to
// decide which of them to include in the response (see handleGraphQL).
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
//
// Issues and PullRequests are pointers so that omitempty can drop a connection entirely from the
// JSON when the query did not ask for it — shurcooL/graphql's decoder is strict, and a response
// key with no corresponding struct field is a decode error, not something ignored. A zero-value
// (non-pointer) ghConnection is never "empty" to encoding/json, so a value field would always be
// serialised even if the caller never populated it.
type graphqlResponse struct {
	Data struct {
		Repository struct {
			Issues       *ghConnection `json:"issues,omitempty"`
			PullRequests *ghConnection `json:"pullRequests,omitempty"`
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

// handleGraphQL serves POST /graphql. variables.owner and variables.name decide the repository —
// when either is absent (the brief's own test posts {"query":"{}"} with no variables at all), it
// serves the org/repo fixture. variables.after selects the page: absent or empty means page 1,
// "cursor-1" means page 2.
//
// Which connections the response carries is decided by inspecting the query text for the
// substrings "issues" and "pullRequests" — not by parsing GraphQL, which a fake has no need to
// do. Querying only "issues" gets a response with no "pullRequests" key at all, and the mirror
// for "pullRequests" alone; querying both, or neither (as the brief's own test does), returns
// both. This matters because shurcooL/graphql's decoder is strict about unexpected fields: if
// this fake always returned both connections, an adapter query that asked for only one of them
// would fail to decode, forcing every query to declare a connection it never reads.
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

	fx := s.githubRepos[repo] // nil (zero value) for an unconfigured repo: served as empty.

	wantIssues, wantPRs := connectionsRequested(req.Query)

	var resp graphqlResponse
	if wantIssues {
		c := paginateOrEmpty(fx, req.Variables.After, true)
		resp.Data.Repository.Issues = &c
	}
	if wantPRs {
		c := paginateOrEmpty(fx, req.Variables.After, false)
		resp.Data.Repository.PullRequests = &c
	}

	writeJSON(w, http.StatusOK, resp)
}

// connectionsRequested reports which of the two connections a query text asks for, by looking
// for the substrings "issues" and "pullRequests" ("issues" never occurs inside "pullRequests",
// so a plain Contains is unambiguous here). Mentioning only one connection serves only that one;
// mentioning both, or neither — the brief's own {"query":"{}"} test mentions neither — serves
// both, which is the safe default for a query this fake did not recognise.
func connectionsRequested(query string) (wantIssues, wantPRs bool) {
	mentionsIssues := strings.Contains(query, "issues")
	mentionsPRs := strings.Contains(query, "pullRequests")

	switch {
	case mentionsIssues && !mentionsPRs:
		return true, false
	case mentionsPRs && !mentionsIssues:
		return false, true
	default: // both mentioned, or neither
		return true, true
	}
}

// paginateOrEmpty pages fx's issues (issues=true) or pull requests (issues=false) after cursor
// after. A nil fx (an unconfigured repository) is served as an empty connection rather than an
// error.
func paginateOrEmpty(fx *ghRepoFixture, after string, issues bool) ghConnection {
	if fx == nil {
		return ghConnection{Nodes: []ghIssue{}}
	}
	if issues {
		return paginate(fx.Issues, after)
	}
	return paginate(fx.PullRequests, after)
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

	// A copy, so that filling in an empty label list never writes into the fixture. shurcooL's
	// decoder wants every key it asked for, so a node without labels is served with
	// labels: {nodes: []} rather than null.
	page := make([]ghIssue, 0, end-start)
	for _, n := range nodes[start:end] {
		if n.Labels.Nodes == nil {
			n.Labels.Nodes = []ghLabel{}
		}
		page = append(page, n)
	}

	hasNext := end < len(nodes)
	cursor := ""
	if hasNext {
		cursor = nextCursor
	}

	return ghConnection{Nodes: page, PageInfo: ghPageInfo{HasNextPage: hasNext, EndCursor: cursor}}
}
