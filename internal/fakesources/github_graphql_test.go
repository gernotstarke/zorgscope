package fakesources_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestFailControlWithRepoOnlyFailsThatRepo(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	post(t, srv.URL+"/_control/fail?source=github&repo=org/bad&status=500")

	resp, err := http.Post(srv.URL+"/graphql", "application/json",
		bytes.NewReader(mustJSON(t, map[string]any{"variables": map[string]string{"owner": "org", "name": "bad"}})))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("org/bad status = %d, want 500", resp.StatusCode)
	}

	body := graphqlQuery(t, srv.URL, "org", "repo", "")
	if len(body.Data.Repository.Issues.Nodes) == 0 {
		t.Fatal("org/repo must keep working when only org/bad is failing")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestControlAddIssueAppearsInTheFixture(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	before := graphqlQuery(t, srv.URL, "org", "repo", "")
	beforeCount := len(before.Data.Repository.Issues.Nodes)

	resp, err := http.Post(srv.URL+"/_control/add-issue", "application/json",
		bytes.NewReader(mustJSON(t, map[string]string{"title": "Freshly added"})))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	after := graphqlQuery(t, srv.URL, "org", "repo", "")
	if len(after.Data.Repository.Issues.Nodes) != beforeCount+1 {
		t.Fatalf("issues after add = %d, want %d", len(after.Data.Repository.Issues.Nodes), beforeCount+1)
	}

	found := false
	for _, n := range after.Data.Repository.Issues.Nodes {
		if n.Title == "Freshly added" {
			found = true
			if n.CreatedAt == "" {
				t.Error("added issue has no createdAt")
			}
		}
	}
	if !found {
		t.Fatal("added issue not found in the fixture")
	}
}

func TestControlResetRestoresPristineState(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	before := graphqlQuery(t, srv.URL, "org", "repo", "")
	beforeCount := len(before.Data.Repository.Issues.Nodes)

	post(t, srv.URL+"/_control/add-issue")
	post(t, srv.URL+"/_control/fail?source=github&status=500")
	post(t, srv.URL+"/_control/reset")

	after := graphqlQuery(t, srv.URL, "org", "repo", "")
	if len(after.Data.Repository.Issues.Nodes) != beforeCount {
		t.Fatalf("issues after reset = %d, want %d (added issue must be gone)", len(after.Data.Repository.Issues.Nodes), beforeCount)
	}
	if after.Data.Repository.Issues.PageInfo.EndCursor != "" && after.Data.Repository.Issues.PageInfo.HasNextPage {
		t.Fatal("unexpected pagination state after reset")
	}
}
