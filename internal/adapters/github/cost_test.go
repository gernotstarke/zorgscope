package github_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gernotstarke/zorgscope/internal/adapters/github"
	"github.com/gernotstarke/zorgscope/internal/fakesources"
)

// graphQLBudget is QS-3.5's ceiling: ≤ 20 GraphQL point-equivalents per refresh run, so that a
// 15-minute interval stays under 2 % of GitHub's hourly limit.
const graphQLBudget = 20

// representativeRepos is the repository half of the representative configuration QS-2.3, QS-2.5
// and QS-3.2 all name: 10 repositories. org/repo is the fixture-backed one; the other nine are
// unconfigured in internal/fakesources and are served as empty connections, which costs the same
// one request per connection as a real single-page repository — the requests are what QS-3.5
// counts, not the nodes they return.
func representativeRepos() []string {
	repos := []string{"org/repo"}
	for i := 2; i <= 10; i++ {
		repos = append(repos, fmt.Sprintf("org/repo-%d", i))
	}
	return repos
}

// TestGraphQLRequestBudget is QS-3.5, whose measure is "≤ 20 GraphQL point-equivalents per run,
// asserted by counting requests against the fake server".
//
// The metric is stated in point-equivalents but asserted in requests, so this test reads one
// GraphQL request as one point-equivalent — the reading the requirement's own assertion clause
// prescribes. GitHub's real cost model is node-based (a query's points depend on the connections
// and page sizes it asks for), and a fake server cannot compute that; counting requests is the
// proxy the requirement chose.
//
// At the representative configuration this comes to exactly 20 — 10 repositories × 2 queries,
// since issues and pull requests paginate independently and cannot share a query. The budget is
// met with no headroom: one more query per repository, or one repository whose issues spill onto
// a second page, puts the run over. That is worth knowing, and it is why this asserts the exact
// count as well as the ceiling.
func TestGraphQLRequestBudget(t *testing.T) {
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

	repos := representativeRepos()
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
		t.Errorf("one refresh run made %d GraphQL requests over %d repositories, which exceeds "+
			"QS-3.5's budget of %d point-equivalents per run", got, len(repos), graphQLBudget)
	}
	if want := 2 * len(repos); got != want {
		t.Errorf("GraphQL requests = %d, want %d (one issues query and one pull requests query "+
			"per repository) — the cost per repository changed", got, want)
	}
}
