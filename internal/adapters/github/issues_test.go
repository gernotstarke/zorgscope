package github_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/github"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/fakesources"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

func TestFetchReturnsIssuesAndPRs(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	f := github.NewIssueFetcher(github.Config{
		Token: "x", BaseURL: srv.URL + "/graphql", Repos: []string{"org/repo"},
	}, srv.Client())

	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Items) == 0 {
		t.Fatal("want items")
	}
	for _, it := range res.Items {
		if it.Source != "github" {
			t.Errorf("Source = %q, want github", it.Source)
		}
		if it.Kind != domain.KindIssue && it.Kind != domain.KindPR {
			t.Errorf("Kind = %q, want issue or pr", it.Kind)
		}
		if it.ExternalID == "" || it.URL == "" || it.Title == "" {
			t.Errorf("incomplete item: %+v", it)
		}
		if it.CreatedAt.IsZero() || it.UpdatedAt.IsZero() {
			t.Errorf("item %s has no timestamps; FR-2.2 needs them", it.ExternalID)
		}
	}
}

// QS-1.5
func TestFetchFollowsPagination(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	f := github.NewIssueFetcher(github.Config{Token: "x", BaseURL: srv.URL + "/graphql", Repos: []string{"org/paged"}}, srv.Client())
	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Items) != 150 {
		t.Fatalf("len(items) = %d, want 150 — pagination was not followed (QS-1.5)", len(res.Items))
	}
}

// QS-1.4: one bad repository must not lose the others.
func TestFetchReportsFailureButKeepsGoodRepos(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()
	post(t, srv.URL+"/_control/fail?source=github&repo=org/bad&status=500")

	f := github.NewIssueFetcher(github.Config{Token: "x", BaseURL: srv.URL + "/graphql", Repos: []string{"org/repo", "org/bad"}}, srv.Client())
	res, err := f.Fetch(context.Background())

	if err == nil {
		t.Fatal("want an error naming the failing repository")
	}
	if !strings.Contains(err.Error(), "org/bad") {
		t.Errorf("error %q must name the failing repository", err)
	}
	if len(res.Items) == 0 {
		t.Error("items from the healthy repository must still be returned (QS-1.4)")
	}
}

func TestExternalIDIsStableAcrossFetches(t *testing.T) {
	// two Fetch calls must produce identical ExternalIDs, or every refresh would
	// re-mark everything as new (FR-5.3).
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	f := github.NewIssueFetcher(github.Config{
		Token: "x", BaseURL: srv.URL + "/graphql", Repos: []string{"org/repo", "org/paged"},
	}, srv.Client())

	res1, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch (1st): %v", err)
	}
	res2, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch (2nd): %v", err)
	}

	ids1 := externalIDs(res1.Items)
	ids2 := externalIDs(res2.Items)

	if len(ids1) != len(ids2) {
		t.Fatalf("fetch produced different item counts: %d vs %d", len(ids1), len(ids2))
	}
	for i := range ids1 {
		if ids1[i] != ids2[i] {
			t.Fatalf("ExternalIDs differ across fetches at index %d: %q vs %q — ids must be stable (FR-5.3)", i, ids1[i], ids2[i])
		}
	}
}

// QS-1.5, QS-2.5: an upstream that keeps claiming another page exists but never moves the
// cursor must not loop forever. internal/fakesources never misbehaves this way, so this test
// drives a small handler of its own that always answers hasNextPage: true with the very same
// cursor, and asserts Fetch returns (with an error naming the repository) rather than hangs.
func TestFetchStopsWhenCursorNeverAdvances(t *testing.T) {
	const stuckCursor = "cursor-stuck"

	srv := httptest.NewServer(connectionEchoHandler(t, func(string) (nodes []any, hasNextPage bool, endCursor string) {
		return []any{stuckNode(1)}, true, stuckCursor
	}))
	defer srv.Close()

	f := github.NewIssueFetcher(github.Config{
		Token: "x", BaseURL: srv.URL + "/graphql", Repos: []string{"org/stuck"},
	}, srv.Client())

	res, err := fetchWithTimeout(t, f, 10*time.Second)

	if err == nil {
		t.Fatal("want an error when the pagination cursor never advances")
	}
	if !strings.Contains(err.Error(), "org/stuck") {
		t.Errorf("error %q must name the repository", err)
	}
	// The first page's items were already collected before the stuck cursor was detected; the
	// point of the guard is stopping, not discarding what was already fetched.
	if len(res.Items) == 0 {
		t.Error("want the first page's items even though pagination stopped early")
	}
}

// QS-2.5: a page cap backstops the forward-progress check above — an upstream whose cursor
// genuinely advances every time, but never reports hasNextPage: false, must still stop.
func TestFetchStopsAtPageCap(t *testing.T) {
	var page int64
	var mu sync.Mutex

	srv := httptest.NewServer(connectionEchoHandler(t, func(string) (nodes []any, hasNextPage bool, endCursor string) {
		mu.Lock()
		page++
		cursor := fmt.Sprintf("cursor-%d", page)
		mu.Unlock()
		return []any{}, true, cursor
	}))
	defer srv.Close()

	f := github.NewIssueFetcher(github.Config{
		Token: "x", BaseURL: srv.URL + "/graphql", Repos: []string{"org/endless"},
	}, srv.Client())

	_, err := fetchWithTimeout(t, f, 20*time.Second)

	if err == nil {
		t.Fatal("want an error when pagination never terminates")
	}
	if !strings.Contains(err.Error(), "org/endless") {
		t.Errorf("error %q must name the repository", err)
	}
}

// fetchWithTimeout runs f.Fetch on its own goroutine and fails the test — instead of hanging the
// whole suite — if it has not returned within timeout. A regression that removes the pagination
// guard is caught as a fast, explicit failure rather than an indefinite hang.
func fetchWithTimeout(t *testing.T, f *github.IssueFetcher, timeout time.Duration) (ports.FetchResult, error) {
	t.Helper()

	type result struct {
		res ports.FetchResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := f.Fetch(context.Background())
		done <- result{res: res, err: err}
	}()

	select {
	case r := <-done:
		return r.res, r.err
	case <-time.After(timeout):
		t.Fatalf("Fetch did not return within %s — pagination loop is unbounded (QS-2.5)", timeout)
		return ports.FetchResult{}, nil
	}
}

// connectionEchoHandler builds a minimal GraphQL handler that inspects only the query text (to
// tell whether the caller asked for "issues" or "pullRequests" — the two are queried
// independently, see issues.go) and answers with next, called once per request. It exists so
// pagination-guard tests do not depend on internal/fakesources, which does not misbehave this
// way.
func connectionEchoHandler(t *testing.T, next func(conn string) (nodes []any, hasNextPage bool, endCursor string)) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request body", http.StatusBadRequest)
			return
		}

		conn := "pullRequests"
		if strings.Contains(body.Query, "issues(") {
			conn = "issues"
		}

		nodes, hasNextPage, endCursor := next(conn)
		resp := map[string]any{
			"data": map[string]any{
				"repository": map[string]any{
					conn: map[string]any{
						"nodes": nodes,
						"pageInfo": map[string]any{
							"hasNextPage": hasNextPage,
							"endCursor":   endCursor,
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// stuckNode returns a minimal, well-formed issue/PR node JSON object for the pagination-guard
// tests, which care about looping behaviour, not node content.
func stuckNode(number int) map[string]any {
	return map[string]any{
		"number":    number,
		"title":     "stuck",
		"url":       fmt.Sprintf("https://github.com/org/stuck/issues/%d", number),
		"author":    map[string]any{"login": "someone"},
		"createdAt": "2026-08-01T09:00:00Z",
		"updatedAt": "2026-08-01T09:00:00Z",
		"state":     "OPEN",
	}
}

// externalIDs returns the sorted set of ExternalIDs from items, so two fetches can be compared
// by value rather than by order.
func externalIDs(items []domain.Item) []string {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ExternalID
	}
	sort.Strings(ids)
	return ids
}

// post posts an empty body to url and fails the test on error or a non-2xx status.
func post(t *testing.T, url string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		t.Fatalf("post %s: status = %d, want < 300", url, resp.StatusCode)
	}
}
