package github_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gernotstarke/zorgscope/internal/adapters/github"
	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/fakesources"
)

// graphQLBudget is QS-3.5's ceiling: at most 2 requests per repository and at most 15 repositories,
// so at most 30 per fetch. A fetch happens only on a stale page view (ADR-0011), at most every 5
// minutes: 12 × 30 = 360 of GitHub's 5,000 an hour, about 7 %, and 30 requests in flight at once.
const graphQLBudget = 30

// representativeRepos is the repository half of the representative configuration QS-2.3 and
// QS-2.5 both name: 10 repositories.
func representativeRepos() []string { return reposN(10) }

// reposN is n repositories. org/repo is the fixture-backed one; the others are unconfigured in
// internal/fakesources and are served as empty connections, which costs the same one request per
// connection as a real single-page repository — the requests are what QS-3.5 counts, not the nodes
// they return.
func reposN(n int) []string {
	repos := []string{"org/repo"}
	for i := 2; i <= n; i++ {
		repos = append(repos, fmt.Sprintf("org/repo-%d", i))
	}
	return repos
}

// TestGraphQLRequestBudget is QS-3.5, whose measure is "≤ 30 GraphQL point-equivalents per run,
// asserted by counting requests against the fake server", at the representative configuration.
//
// The metric is stated in point-equivalents but asserted in requests: this counts each GraphQL
// request as one point-equivalent — the reading the requirement's own assertion clause prescribes.
// GitHub's real cost model is node-based (a query's points depend on the connections and page sizes
// it asks for), and a fake server cannot compute that; counting requests is the proxy the
// requirement chose.
//
// Issues and pull requests paginate independently and cannot share a query, so a repository costs
// two requests; the test asserts the exact count as well as the ceiling, so that a third query per
// repository is noticed even while the total stays under it.
func TestGraphQLRequestBudget(t *testing.T) {
	assertBudget(t, representativeRepos())
}

// QS-3.5 at the largest configuration config.Load admits: 15 repositories, 30 requests, no headroom.
func TestGraphQLRequestBudgetAtFifteen(t *testing.T) {
	assertBudget(t, reposN(config.MaxRepos))
}

func assertBudget(t *testing.T, repos []string) {
	t.Helper()
	var mu sync.Mutex
	graphQLRequests := 0

	fake := fakesources.NewServer()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			mu.Lock()
			graphQLRequests++
			mu.Unlock()
		}
		fake.ServeHTTP(w, r)
	}))
	defer srv.Close()

	f := github.NewIssueFetcher(github.Config{
		Token: "x", BaseURL: srv.URL + "/graphql", Repos: repos,
	}, srv.Client())

	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	mu.Lock()
	got := graphQLRequests
	mu.Unlock()

	if got > graphQLBudget {
		t.Errorf("one fetch made %d GraphQL requests over %d repositories, which exceeds "+
			"QS-3.5's budget of %d point-equivalents per run", got, len(repos), graphQLBudget)
	}
	if want := 2 * len(repos); got != want {
		t.Errorf("GraphQL requests = %d, want %d (one issues query and one pull requests query "+
			"per repository) — the cost per repository changed", got, want)
	}
}
