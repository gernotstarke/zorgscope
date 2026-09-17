package fakesources_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/fakesources"
)

type ghNode struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	State     string `json:"state"`
}

type ghConnection struct {
	Nodes    []ghNode `json:"nodes"`
	PageInfo struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
}

type ghBody struct {
	Data struct {
		Repository struct {
			Issues       ghConnection `json:"issues"`
			PullRequests ghConnection `json:"pullRequests"`
		} `json:"repository"`
	} `json:"data"`
}

// graphqlQuery sends a query mentioning both issues and pullRequests — the shape Task 7's initial,
// non-continuation query uses — so callers get both connections back. Tests exercising the
// query-text dispatch itself live in github_graphql_dispatch_test.go, with their own query text.
func graphqlQuery(t *testing.T, base, owner, name, after string) ghBody {
	t.Helper()
	payload := map[string]any{
		"query": "query { repository(owner: $owner, name: $name) { " +
			"issues { nodes { number } } pullRequests { nodes { number } } } }",
		"variables": map[string]string{
			"owner": owner,
			"name":  name,
			"after": after,
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(base+"/graphql", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body ghBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func TestGraphQLOrgRepoHasWellFormedIssuesAndPRs(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	body := graphqlQuery(t, srv.URL, "org", "repo", "")

	if len(body.Data.Repository.Issues.Nodes) == 0 {
		t.Fatal("want at least one issue")
	}
	if len(body.Data.Repository.PullRequests.Nodes) == 0 {
		t.Fatal("want at least one pull request")
	}
	for _, n := range append(body.Data.Repository.Issues.Nodes, body.Data.Repository.PullRequests.Nodes...) {
		if n.Number == 0 || n.URL == "" || n.Title == "" || n.Author.Login == "" {
			t.Errorf("incomplete node: %+v", n)
		}
		if _, err := time.Parse(time.RFC3339, n.CreatedAt); err != nil {
			t.Errorf("createdAt %q not RFC3339: %v", n.CreatedAt, err)
		}
		if _, err := time.Parse(time.RFC3339, n.UpdatedAt); err != nil {
			t.Errorf("updatedAt %q not RFC3339: %v", n.UpdatedAt, err)
		}
	}
	if body.Data.Repository.Issues.PageInfo.HasNextPage {
		t.Error("org/repo has few issues; hasNextPage should be false")
	}
}

func TestGraphQLPaginationSplits150IssuesAcrossTwoPages(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	page1 := graphqlQuery(t, srv.URL, "org", "paged", "")
	if len(page1.Data.Repository.Issues.Nodes) != 100 {
		t.Fatalf("page1 len = %d, want 100", len(page1.Data.Repository.Issues.Nodes))
	}
	if !page1.Data.Repository.Issues.PageInfo.HasNextPage {
		t.Fatal("page1 hasNextPage = false, want true")
	}
	cursor := page1.Data.Repository.Issues.PageInfo.EndCursor
	if cursor != "cursor-1" {
		t.Fatalf("endCursor = %q, want cursor-1", cursor)
	}
	if len(page1.Data.Repository.PullRequests.Nodes) != 0 {
		t.Fatalf("org/paged must have no pull requests, got %d", len(page1.Data.Repository.PullRequests.Nodes))
	}

	page2 := graphqlQuery(t, srv.URL, "org", "paged", cursor)
	if len(page2.Data.Repository.Issues.Nodes) != 50 {
		t.Fatalf("page2 len = %d, want 50", len(page2.Data.Repository.Issues.Nodes))
	}
	if page2.Data.Repository.Issues.PageInfo.HasNextPage {
		t.Fatal("page2 hasNextPage = true, want false")
	}

	total := len(page1.Data.Repository.Issues.Nodes) + len(page2.Data.Repository.Issues.Nodes)
	if total != 150 {
		t.Fatalf("total issues = %d, want 150", total)
	}
}

func TestGraphQLNoVariablesServesOrgRepo(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/graphql", "application/json", bytes.NewReader([]byte(`{"query":"{}"}`)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body ghBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data.Repository.Issues.Nodes) == 0 {
		t.Fatal("want the org/repo fixture when variables are absent")
	}
}

// graphQL posts body to the fake's /graphql and returns the response body.
func graphQL(t *testing.T, base, body string) []byte {
	t.Helper()
	resp, err := http.Post(base+"/graphql", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /graphql: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /graphql = %d: %s", resp.StatusCode, out)
	}
	return out
}

// The fixture's labels round-trip through the GraphQL handler in the shape GitHub uses:
// labels.nodes[].name, in fixture order.
func TestGraphQLServesLabelsInFixtureOrder(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	body := graphQL(t, srv.URL, `{"query":"{ repository(owner: $owner, name: $name) { issues { nodes { number labels { nodes { name } } } } } }","variables":{"owner":"org","name":"repo"}}`)
	var resp struct {
		Data struct {
			Repository struct {
				Issues struct {
					Nodes []struct {
						Number int `json:"number"`
						Labels struct {
							Nodes []struct {
								Name string `json:"name"`
							} `json:"nodes"`
						} `json:"labels"`
					} `json:"nodes"`
				} `json:"issues"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decoding: %v\n%s", err, body)
	}
	got := map[int][]string{}
	for _, n := range resp.Data.Repository.Issues.Nodes {
		var names []string
		for _, l := range n.Labels.Nodes {
			names = append(names, l.Name)
		}
		got[n.Number] = names
	}
	if strings.Join(got[1], "|") != "bug|Help Wanted" || strings.Join(got[3], "|") != "documentation|needs-triage" {
		t.Errorf("labels = %v, want #1 [bug Help Wanted] and #3 [documentation needs-triage]", got)
	}
}

// A node without labels serialises an empty connection, never a missing key or a null list:
// shurcooL's decoder is strict about the keys the query asked for.
func TestANodeWithoutLabelsServesAnEmptyConnection(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	body := graphQL(t, srv.URL, `{"query":"{ repository(owner: $owner, name: $name) { pullRequests { nodes { number labels { nodes { name } } } } } }","variables":{"owner":"org","name":"bad"}}`)
	if !strings.Contains(string(body), `"labels":{"nodes":[]}`) {
		t.Errorf("a node without labels must serialise \"labels\":{\"nodes\":[]}; got %s", body)
	}
}
