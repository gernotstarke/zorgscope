package github_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

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
