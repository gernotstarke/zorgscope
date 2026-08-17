package fakesources_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gernotstarke/zorgscope/internal/fakesources"
)

// graphqlKeys posts query (with variables selecting org/repo) and reports whether the response's
// data.repository object has an "issues" key and a "pullRequests" key. Decoding into a generic
// map, rather than a fixed struct, is deliberate: a struct field simply stays at its zero value
// whether or not the key was present in the JSON, which cannot distinguish "absent" from "present
// but empty" — exactly the distinction this fix is about.
func graphqlKeys(t *testing.T, base, query string) (hasIssues, hasPullRequests bool) {
	t.Helper()
	payload := map[string]any{
		"query":     query,
		"variables": map[string]string{"owner": "org", "name": "repo"},
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

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode top level: %v", err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(raw["data"], &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	var repository map[string]json.RawMessage
	if err := json.Unmarshal(data["repository"], &repository); err != nil {
		t.Fatalf("decode repository: %v", err)
	}

	_, hasIssues = repository["issues"]
	_, hasPullRequests = repository["pullRequests"]
	return hasIssues, hasPullRequests
}

// These four tests pin the dispatch rule verbatim: a query naming only one connection gets only
// that connection back — no sibling key at all — because shurcooL/graphql's decoder errors on an
// unexpected field. A query naming both, or neither, gets both, unchanged from before this fix.

func TestGraphQLDispatchIssuesOnlyOmitsPullRequests(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	hasIssues, hasPRs := graphqlKeys(t, srv.URL,
		"query { repository(owner: $owner, name: $name) { issues(first: 100) { nodes { number } } } }")

	if !hasIssues {
		t.Error("want an issues key")
	}
	if hasPRs {
		t.Error("query named only issues; pullRequests key must be absent entirely")
	}
}

func TestGraphQLDispatchPullRequestsOnlyOmitsIssues(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	hasIssues, hasPRs := graphqlKeys(t, srv.URL,
		"query { repository(owner: $owner, name: $name) { pullRequests(first: 100) { nodes { number } } } }")

	if hasIssues {
		t.Error("query named only pullRequests; issues key must be absent entirely")
	}
	if !hasPRs {
		t.Error("want a pullRequests key")
	}
}

func TestGraphQLDispatchBothMentionedReturnsBoth(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	hasIssues, hasPRs := graphqlKeys(t, srv.URL,
		"query { repository(owner: $owner, name: $name) { "+
			"issues(first: 100) { nodes { number } } pullRequests(first: 100) { nodes { number } } } }")

	if !hasIssues || !hasPRs {
		t.Errorf("query named both; want both keys present, got issues=%v pullRequests=%v", hasIssues, hasPRs)
	}
}

// TestGraphQLDispatchNeitherMentionedReturnsBoth is the brief's own case: {"query":"{}"} names
// neither connection and must keep returning both, unchanged.
func TestGraphQLDispatchNeitherMentionedReturnsBoth(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	hasIssues, hasPRs := graphqlKeys(t, srv.URL, "{}")

	if !hasIssues || !hasPRs {
		t.Errorf("query named neither; want both keys present, got issues=%v pullRequests=%v", hasIssues, hasPRs)
	}
}
