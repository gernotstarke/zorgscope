package github_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/github"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/fakesources"
)

func TestFetchReturnsIssuesAndPRs(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	f := github.NewIssueFetcher(github.Config{
		Token: "x", BaseURL: srv.URL + "/graphql", Repos: []string{"org/repo"},
	}, srv.Client())

	items, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("want items")
	}
	for _, it := range items {
		if it.Kind != domain.KindIssue && it.Kind != domain.KindPR {
			t.Errorf("Kind = %q, want issue or pr", it.Kind)
		}
		if it.URL == "" || it.Title == "" {
			t.Errorf("incomplete item: %+v", it)
		}
		if it.CreatedAt.IsZero() || it.UpdatedAt.IsZero() {
			t.Errorf("item %+v has no timestamps; FR-1.1 AC3 needs them", it)
		}
	}
}

// itemKey identifies one item the way the fixtures' issue and pull-request numbers do: a kind
// prefix plus repo#number, since issues and pull requests share one number namespace per
// repository.
func itemKey(it domain.Item) string {
	return string(it.Kind) + ":" + it.Repo + "#" + strconv.Itoa(it.Number)
}

func byKey(items []domain.Item) map[string]domain.Item {
	out := make(map[string]domain.Item, len(items))
	for _, it := range items {
		out[itemKey(it)] = it
	}
	return out
}

// FR-2.1 AC1: a pull request's draft flag is fetched and reaches the item. It is carried in
// State — "DRAFT" for a draft, "OPEN" for a ready one — so a draft is distinguishable from a
// ready pull request by the time the item reaches the page, with no dedicated field. The
// fixture's org/repo has PR #11 marked isDraft and PR #10 not.
//
// The issues in the same fixture are asserted too: isDraft exists on GitHub's PullRequest and not
// on its Issue, so an issue must never come back as DRAFT.
func TestPullRequestDraftFlagIsFetched(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	f := github.NewIssueFetcher(github.Config{
		Token: "x", BaseURL: srv.URL + "/graphql", Repos: []string{"org/repo"},
	}, srv.Client())

	items, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	byID := byKey(items)

	draft, ok := byID["pr:org/repo#11"]
	if !ok {
		t.Fatalf("draft pull request pr:org/repo#11 missing from %d items", len(items))
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

	for _, it := range items {
		if it.Kind == domain.KindIssue && it.State != "OPEN" {
			t.Errorf("issue %s State = %q, want OPEN", itemKey(it), it.State)
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
	items, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(items) != 150 {
		t.Fatalf("len(items) = %d, want 150 — pagination was not followed (QS-1.5)", len(items))
	}
}

// QS-1.4: one bad repository must not lose the others. internal/fakesources no longer offers a
// control route to inject a failure (it served the refresh-pipeline testing the stateless reset
// removed), so the failure is injected here instead, by a small middleware in front of the real
// fake that fails only requests naming org/bad and forwards everything else unchanged.
func TestFetchReportsFailureButKeepsGoodRepos(t *testing.T) {
	fake := fakesources.NewServer()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "unreadable body", http.StatusBadRequest)
			return
		}
		var body struct {
			Variables struct{ Owner, Name string } `json:"variables"`
		}
		_ = json.Unmarshal(raw, &body)
		if body.Variables.Owner+"/"+body.Variables.Name == "org/bad" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		fake.ServeHTTP(w, r)
	}))
	defer srv.Close()

	f := github.NewIssueFetcher(github.Config{Token: "x", BaseURL: srv.URL + "/graphql", Repos: []string{"org/repo", "org/bad"}}, srv.Client())
	items, err := f.Fetch(context.Background())

	if err == nil {
		t.Fatal("want an error naming the failing repository")
	}
	if !strings.Contains(err.Error(), "org/bad") {
		t.Errorf("error %q must name the failing repository", err)
	}
	if len(items) == 0 {
		t.Error("items from the healthy repository must still be returned (QS-1.4)")
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

	items, err := fetchWithTimeout(t, f, 10*time.Second)

	if err == nil {
		t.Fatal("want an error when the pagination cursor never advances")
	}
	if !strings.Contains(err.Error(), "org/stuck") {
		t.Errorf("error %q must name the repository", err)
	}
	// The first page's items were already collected before the stuck cursor was detected; the
	// point of the guard is stopping, not discarding what was already fetched.
	if len(items) == 0 {
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
func fetchWithTimeout(t *testing.T, f *github.IssueFetcher, timeout time.Duration) ([]domain.Item, error) {
	t.Helper()

	type result struct {
		items []domain.Item
		err   error
	}
	done := make(chan result, 1)
	go func() {
		items, err := f.Fetch(context.Background())
		done <- result{items: items, err: err}
	}()

	select {
	case r := <-done:
		return r.items, r.err
	case <-time.After(timeout):
		t.Fatalf("Fetch did not return within %s — pagination loop is unbounded (QS-2.5)", timeout)
		return nil, nil
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

// The item's own description reaches the item, so the dashboard can say what an issue is about
// rather than only what it is called. An issue opened with an empty body carries none — the
// fixture's issue #3 is deliberately written without one — and must arrive with an empty summary
// rather than failing the query, since GitHub omits the key entirely in that case.
func TestIssueBodyTextReachesTheItem(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	f := github.NewIssueFetcher(github.Config{
		Token: "x", BaseURL: srv.URL + "/graphql", Repos: []string{"org/repo"},
	}, srv.Client())

	items, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	byID := byKey(items)

	withBody, ok := byID["issue:org/repo#1"]
	if !ok {
		t.Fatalf("issue:org/repo#1 missing from %d items", len(items))
	}
	if !strings.Contains(withBody.Summary, "liveness probe") {
		t.Errorf("Summary = %q, want the issue's body text", withBody.Summary)
	}
	if strings.ContainsAny(withBody.Summary, "\n\r\t") {
		t.Errorf("Summary = %q, want its whitespace collapsed", withBody.Summary)
	}

	// A pull request carries one too: it goes through a second node type, so it is a second
	// chance to forget the field.
	pr, ok := byID["pr:org/repo#10"]
	if !ok {
		t.Fatal("pr:org/repo#10 missing")
	}
	if pr.Summary == "" {
		t.Error("a pull request's body text is not carried")
	}

	empty, ok := byID["issue:org/repo#3"]
	if !ok {
		t.Fatal("issue:org/repo#3 missing")
	}
	if empty.Summary != "" {
		t.Errorf("Summary = %q for an issue with no body, want empty", empty.Summary)
	}
}

// QS-2.7: the requests of one fetch run side by side. Twenty requests that each take 200 ms would
// take four seconds one after the other; side by side they take about one round trip.
func TestFetchRunsRepositoriesSideBySide(t *testing.T) {
	echo := connectionEchoHandler(t, func(string) (nodes []any, hasNextPage bool, endCursor string) {
		return []any{}, false, ""
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		echo(w, r)
	}))
	defer srv.Close()

	f := github.NewIssueFetcher(github.Config{
		Token: "x", BaseURL: srv.URL, Repos: representativeRepos(),
	}, srv.Client())

	start := time.Now()
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("a fetch of %d repositories took %v; side by side it should take about one "+
			"200 ms round trip (QS-2.7)", len(representativeRepos()), elapsed)
	}
}

// FR-1.4: running side by side changes when items arrive, never where they land. The result is
// the sequential one — repositories in configuration order, a repository's issues before its pull
// requests — however the upstream happens to answer. The handler answers after a random pause so
// that a fetch which merely appended results as they came in would fail here.
func TestFetchKeepsConfigurationOrder(t *testing.T) {
	echo := connectionEchoHandler(t, func(string) (nodes []any, hasNextPage bool, endCursor string) {
		return []any{stuckNode(1)}, false, ""
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Duration(rand.IntN(30)) * time.Millisecond)
		echo(w, r)
	}))
	defer srv.Close()

	repos := []string{"org/one", "org/two", "org/three", "org/four"}
	f := github.NewIssueFetcher(github.Config{Token: "x", BaseURL: srv.URL, Repos: repos}, srv.Client())

	items, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	var got []string
	for _, it := range items {
		got = append(got, string(it.Kind)+":"+it.Repo)
	}
	var want []string
	for _, repo := range repos {
		want = append(want, string(domain.KindIssue)+":"+repo, string(domain.KindPR)+":"+repo)
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("items in order %v, want %v", got, want)
	}
}

// FR-1.4 AC4 across the fan-out: one repository failing, one that is not owner/name, and the
// rest fine. Every error is collected, and the good repositories' items are all returned.
func TestFetchCollectsEveryErrorAndKeepsTheGoodItems(t *testing.T) {
	echo := connectionEchoHandler(t, func(string) (nodes []any, hasNextPage bool, endCursor string) {
		return []any{stuckNode(1)}, false, ""
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"name":"broken"`) {
			http.Error(w, "upstream on fire", http.StatusBadGateway)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		echo(w, r)
	}))
	defer srv.Close()

	repos := []string{"org/good", "org/broken", "not-a-repo", "org/fine"}
	f := github.NewIssueFetcher(github.Config{Token: "x", BaseURL: srv.URL, Repos: repos}, srv.Client())

	items, err := f.Fetch(context.Background())
	if err == nil {
		t.Fatal("want an error naming the broken repository and the malformed name")
	}
	for _, needle := range []string{"org/broken", `"not-a-repo" is not owner/name`} {
		if !strings.Contains(err.Error(), needle) {
			t.Errorf("error %q does not mention %s", err, needle)
		}
	}
	got := map[string]int{}
	for _, it := range items {
		got[it.Repo]++
	}
	if got["org/good"] != 2 || got["org/fine"] != 2 || got["org/broken"] != 0 {
		t.Errorf("items per repository = %v, want org/good:2 org/fine:2 org/broken:0", got)
	}
}

// A cancelled context ends the fetch promptly, and Fetch does not return before every one of its
// goroutines has: nothing of a fetch outlives the call.
func TestFetchStopsWhenCancelled(t *testing.T) {
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // only after the body is read does the server notice a hang-up
		select {
		case <-r.Context().Done(): // the client gave up
		case <-stop:               // the test is over
		}
	}))
	defer srv.Close()
	defer close(stop) // runs before srv.Close: defers are last in, first out

	f := github.NewIssueFetcher(github.Config{
		Token: "x", BaseURL: srv.URL, Repos: representativeRepos(),
	}, srv.Client())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := f.Fetch(ctx)
	if err == nil {
		t.Fatal("want the cancellation reported as an error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Fetch took %v to notice the cancellation", elapsed)
	}
}
