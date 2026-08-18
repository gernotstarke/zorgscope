package github_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// FR-2.1 AC1: a pull request's draft flag is fetched and reaches the item. It is carried in
// State — "DRAFT" for a draft, "OPEN" for a ready one — so a draft is distinguishable from a
// ready pull request by the time the item reaches the store, with no new column. The fixture's
// org/repo has PR #11 marked isDraft and PR #10 not.
//
// The issues in the same fixture are asserted too: isDraft exists on GitHub's PullRequest and not
// on its Issue, so an issue must never come back as DRAFT.
func TestPullRequestDraftFlagIsFetched(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	f := github.NewIssueFetcher(github.Config{
		Token: "x", BaseURL: srv.URL + "/graphql", Repos: []string{"org/repo"},
	}, srv.Client())

	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	byID := map[string]domain.Item{}
	for _, it := range res.Items {
		byID[it.ExternalID] = it
	}

	draft, ok := byID["pr:org/repo#11"]
	if !ok {
		t.Fatalf("draft pull request pr:org/repo#11 missing from %d items", len(res.Items))
	}
	if draft.State != "DRAFT" {
		t.Errorf("draft PR State = %q, want DRAFT — the draft flag is not carried (FR-2.1 AC1)", draft.State)
	}

	ready, ok := byID["pr:org/repo#10"]
	if !ok {
		t.Fatal("ready pull request pr:org/repo#10 missing")
	}
	if ready.State != "OPEN" {
		t.Errorf("ready PR State = %q, want OPEN — a ready PR must not read as a draft", ready.State)
	}

	for _, it := range res.Items {
		if it.Kind == domain.KindIssue && it.State != "OPEN" {
			t.Errorf("issue %s State = %q, want OPEN", it.ExternalID, it.State)
		}
	}
}

// FR-2.1 AC1, guarding the half internal/fakesources cannot: isDraft exists on GitHub's
// PullRequest type and not on its Issue type, so asking for it in the issues query is rejected by
// real GitHub with "Field 'isDraft' doesn't exist on type 'Issue'". The fake server does not
// validate queries against a schema, so it would answer such a query happily and every other test
// in this file would still pass. This one reads the query text the client actually sends.
func TestOnlyThePullRequestQueryAsksForIsDraft(t *testing.T) {
	var mu sync.Mutex
	queries := map[string]string{}

	echo := connectionEchoHandler(t, func(string) (nodes []any, hasNextPage bool, endCursor string) {
		return []any{}, false, ""
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			http.Error(w, "unreadable body", http.StatusBadRequest)
			return
		}
		var q struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(body, &q); err != nil {
			t.Errorf("decode request body: %v", err)
			http.Error(w, "bad request body", http.StatusBadRequest)
			return
		}
		conn := "pullRequests"
		if strings.Contains(q.Query, "issues(") {
			conn = "issues"
		}
		mu.Lock()
		queries[conn] = q.Query
		mu.Unlock()

		r.Body = io.NopCloser(bytes.NewReader(body)) // the echo handler decodes it again
		echo(w, r)
	}))
	defer srv.Close()

	f := github.NewIssueFetcher(github.Config{
		Token: "x", BaseURL: srv.URL + "/graphql", Repos: []string{"org/repo"},
	}, srv.Client())
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if queries["issues"] == "" || queries["pullRequests"] == "" {
		t.Fatalf("both connections must have been queried, got %d: %v", len(queries), queries)
	}
	if strings.Contains(queries["issues"], "isDraft") {
		t.Errorf("the issues query asks for isDraft, which real GitHub rejects on type Issue:\n%s", queries["issues"])
	}
	if !strings.Contains(queries["pullRequests"], "isDraft") {
		t.Errorf("the pull request query must ask for isDraft (FR-2.1 AC1):\n%s", queries["pullRequests"])
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
