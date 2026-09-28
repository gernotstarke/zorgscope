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
//
// The alert fields follow the same rule (FR-1.16): present only when the query names
// vulnerabilityAlerts. Errors carries GitHub's refusal of them, which arrives beside whatever data
// GitHub did answer — or with no data at all.
type graphqlResponse struct {
	Data   *graphqlData   `json:"data"`
	Errors []graphqlError `json:"errors,omitempty"`
}

// graphqlData is the data object: the one repository asked about.
type graphqlData struct {
	Repository struct {
		Issues                        *ghConnection      `json:"issues,omitempty"`
		PullRequests                  *ghConnection      `json:"pullRequests,omitempty"`
		HasVulnerabilityAlertsEnabled *bool              `json:"hasVulnerabilityAlertsEnabled,omitempty"`
		VulnerabilityAlerts           *ghAlertConnection `json:"vulnerabilityAlerts,omitempty"`
	} `json:"repository"`
}

// ghAlertConnection is the vulnerabilityAlerts connection: the adapter reads its nodes only.
type ghAlertConnection struct {
	Nodes []ghAlert `json:"nodes"`
}

// graphqlError is one entry of a GraphQL response's errors array.
type graphqlError struct {
	Message string `json:"message"`
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
	wantAlerts := strings.Contains(req.Query, "vulnerabilityAlerts")

	var resp graphqlResponse
	resp.Data = &graphqlData{}
	if wantIssues {
		c := paginateOrEmpty(fx, req.Variables.After, true)
		resp.Data.Repository.Issues = &c
	}
	if wantPRs {
		c := paginateOrEmpty(fx, req.Variables.After, false)
		resp.Data.Repository.PullRequests = &c
	}
	if wantAlerts {
		var alerts ghAlertsFixture
		if fx != nil {
			alerts = fx.Alerts
		}
		switch alerts.Refuse {
		case "query":
			// A token without the scope: GitHub refuses the whole query and answers no data.
			resp.Data = nil
			resp.Errors = []graphqlError{{Message: "Your token has not been granted the required scopes to execute this query."}}
		case "field":
			// A token whose owner may not see the alerts: the rest is answered, the alert fields
			// are not.
			resp.Errors = []graphqlError{{Message: "Resource not accessible by integration"}}
		default:
			enabled := alerts.Enabled
			nodes := alerts.Nodes
			if !enabled || nodes == nil || alerts.Refuse == "silent" {
				nodes = []ghAlert{}
			}
			resp.Data.Repository.HasVulnerabilityAlertsEnabled = &enabled
			resp.Data.Repository.VulnerabilityAlerts = &ghAlertConnection{Nodes: nodes}
		}
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
	conn := paginate(fx.PullRequests, after)
	// Like labels, a pull request without review requests is served with an empty connection
	// rather than null, because the decoder wants every key it asked for.
	for i := range conn.Nodes {
		if rr := conn.Nodes[i].ReviewRequests; rr == nil || rr.Nodes == nil {
			conn.Nodes[i].ReviewRequests = &ghReviewRequests{Nodes: []ghReviewRequest{}}
		}
	}
	return conn
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

// handleDependabotAlerts serves GET /repos/{owner}/{repo}/dependabot/alerts, the REST list the
// adapter asks when GraphQL answered with an empty one (FR-1.16). A token GitHub would refuse —
// every refusal shape of the fixture — gets 403, as does a repository with alerts switched off;
// otherwise it gets 200 and the open alerts' numbers, which is all the adapter looks at: the status.
func (s *server) handleDependabotAlerts(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	fx := s.githubRepos[r.PathValue("owner")+"/"+r.PathValue("repo")]
	s.mu.Unlock()
	if fx == nil || !fx.Alerts.Enabled || fx.Alerts.Refuse != "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "Resource not accessible by personal access token"})
		return
	}
	out := make([]map[string]int, 0, len(fx.Alerts.Nodes))
	for _, n := range fx.Alerts.Nodes {
		out = append(out, map[string]int{"number": n.Number})
	}
	writeJSON(w, http.StatusOK, out)
}
