package githubfake

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func gql(t *testing.T, srv *httptest.Server, token string, vars map[string]any) (*http.Response, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"query": "query { repository(owner:$owner,name:$name) {...} }", "variables": vars})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/graphql", bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	_ = resp.Body.Close()
	return resp, out
}

func TestGraphQLIssuesAndPagination(t *testing.T) {
	s := New()
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	Seed(s, now)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, _ := gql(t, srv, "", map[string]any{"owner": "arc42", "name": "arc42-template", "n": 100, "withIssues": true, "withPRs": true})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token → 401, got %d", resp.StatusCode)
	}
	resp, out := gql(t, srv, "tok", map[string]any{"owner": "arc42", "name": "arc42-template", "n": 2, "withIssues": true, "withPRs": true})
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	repo := out["data"].(map[string]any)["repository"].(map[string]any)
	issues := repo["issues"].(map[string]any)
	if len(issues["nodes"].([]any)) != 2 || issues["pageInfo"].(map[string]any)["hasNextPage"] != true {
		t.Fatalf("page 1 = %v", issues)
	}
	cursor := issues["pageInfo"].(map[string]any)["endCursor"].(string)
	_, out = gql(t, srv, "tok", map[string]any{"owner": "arc42", "name": "arc42-template", "n": 2, "withIssues": true, "withPRs": false, "issuesAfter": cursor})
	issues = out["data"].(map[string]any)["repository"].(map[string]any)["issues"].(map[string]any)
	if len(issues["nodes"].([]any)) != 1 || issues["pageInfo"].(map[string]any)["hasNextPage"] != false {
		t.Fatalf("page 2 = %v", issues)
	}
	if _, ok := out["data"].(map[string]any)["repository"].(map[string]any)["pullRequests"]; ok {
		t.Fatal("withPRs=false must omit pullRequests")
	}
	_, out = gql(t, srv, "tok", map[string]any{"owner": "nobody", "name": "nothing", "n": 2, "withIssues": true, "withPRs": true})
	if out["data"].(map[string]any)["repository"] != nil {
		t.Fatal("unknown repo → repository null")
	}
	if s.Requests() < 4 {
		t.Fatalf("requests = %d", s.Requests())
	}
}

func TestRunsNotificationsAndControl(t *testing.T) {
	s := New()
	Seed(s, time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC))
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	get := func(path string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer tok")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		_ = resp.Body.Close()
		return resp, b.String()
	}
	resp, body := get("/repos/arc42/arc42.org-site/actions/runs?branch=main&per_page=1")
	if resp.StatusCode != 200 || !strings.Contains(body, `"conclusion":"failure"`) {
		t.Fatalf("runs: %d %s", resp.StatusCode, body)
	}
	resp, body = get("/notifications?participating=true")
	if resp.StatusCode != 200 || !strings.Contains(body, `"reason":"mention"`) {
		t.Fatalf("notifications: %d %s", resp.StatusCode, body)
	}
	// control: inject an issue, then it shows up
	payload := `{"repo":"arc42/arc42-template","issue":{"Number":999,"Title":"Injected","Author":"tester","CreatedAt":"2026-08-16T11:00:00Z","UpdatedAt":"2026-08-16T11:00:00Z"}}`
	resp, err := http.Post(srv.URL+"/__control/issues", "application/json", strings.NewReader(payload))
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("control issues: %v %d", err, resp.StatusCode)
	}
	_, out := gql(t, srv, "tok", map[string]any{"owner": "arc42", "name": "arc42-template", "n": 100, "withIssues": true, "withPRs": false})
	if !strings.Contains(mustJSON(out), "Injected") {
		t.Fatal("injected issue missing")
	}
	// fail next
	resp, _ = http.Post(srv.URL+"/__control/fail", "application/json", strings.NewReader(`{"status":403,"rate_limited":true}`))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatal("control fail")
	}
	resp, _ = get("/notifications")
	if resp.StatusCode != 403 || resp.Header.Get("X-RateLimit-Remaining") != "0" || resp.Header.Get("X-RateLimit-Reset") == "" {
		t.Fatalf("forced failure: %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = get("/notifications")
	if resp.StatusCode != 200 {
		t.Fatal("failure is one-shot")
	}
	s.SetTokenExpiry(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	resp, _ = get("/notifications")
	if resp.Header.Get("GitHub-Authentication-Token-Expiration") != "2026-09-01 00:00:00 UTC" {
		t.Fatalf("expiry header = %q", resp.Header.Get("GitHub-Authentication-Token-Expiration"))
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
