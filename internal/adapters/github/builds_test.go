package github_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gernotstarke/zorgscope/internal/adapters/github"
	"github.com/gernotstarke/zorgscope/internal/fakesources"
)

// FR-2.3 AC1/AC2 (happy path): a repository with a single completed run yields one Build with
// Conclusion, Workflow, RunURL and FinishedAt all set.
func TestFetchLatestCompletedRunPerRepo(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	f := github.NewBuildFetcher(github.Config{
		Token: "x", RESTBaseURL: srv.URL, Repos: []string{"org/repo"},
	}, srv.Client())

	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Builds) != 1 {
		t.Fatalf("len(builds) = %d, want 1", len(res.Builds))
	}

	b := res.Builds[0]
	if b.Repo != "org/repo" {
		t.Errorf("Repo = %q, want org/repo", b.Repo)
	}
	if b.Workflow != "CI" {
		t.Errorf("Workflow = %q, want CI", b.Workflow)
	}
	if b.Conclusion != "success" {
		t.Errorf("Conclusion = %q, want success", b.Conclusion)
	}
	if b.Status != "completed" {
		t.Errorf("Status = %q, want completed", b.Status)
	}
	if b.RunURL == "" {
		t.Error("RunURL is empty")
	}
	if b.FinishedAt.IsZero() {
		t.Error("FinishedAt is zero")
	}
	if b.FetchedAt.IsZero() {
		t.Error("FetchedAt is zero")
	}
	if loc := b.FetchedAt.Location(); loc.String() != "UTC" {
		t.Errorf("FetchedAt location = %q, want UTC", loc)
	}
}

// FR-2.3 AC2: when the newest run overall is in_progress (or queued), Status reflects that run,
// but Conclusion must still carry the last *completed* run's conclusion — an in-progress run must
// never blank out or overwrite the previous result. org/build-running's fixture has an
// in_progress run on top of an older completed/failure run.
func TestInProgressRunKeepsThePreviousConclusion(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	f := github.NewBuildFetcher(github.Config{
		Token: "x", RESTBaseURL: srv.URL, Repos: []string{"org/build-running"},
	}, srv.Client())

	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Builds) != 1 {
		t.Fatalf("len(builds) = %d, want 1", len(res.Builds))
	}

	b := res.Builds[0]
	if b.Status != "in_progress" {
		t.Errorf("Status = %q, want in_progress", b.Status)
	}
	if b.Conclusion != "failure" {
		t.Errorf("Conclusion = %q, want failure (from the older completed run, not blanked by the running one)", b.Conclusion)
	}
}

// FR-2.3 AC3: a repository with no workflow runs at all is a normal state, not a failure — no
// Build, no error.
func TestRepoWithoutWorkflowsYieldsNoBuildAndNoError(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	f := github.NewBuildFetcher(github.Config{
		Token: "x", RESTBaseURL: srv.URL, Repos: []string{"org/build-none"},
	}, srv.Client())

	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("a repo without workflows is not an error: %v", err)
	}
	if len(res.Builds) != 0 {
		t.Fatalf("len(builds) = %d, want 0", len(res.Builds))
	}
}

// QS-1.4: one failing repository must not lose the builds fetched for the others.
func TestBuildsFetchReportsFailureButKeepsGoodRepos(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()
	post(t, srv.URL+"/_control/fail?source=github&repo=org/repo&status=500")

	f := github.NewBuildFetcher(github.Config{
		Token: "x", RESTBaseURL: srv.URL, Repos: []string{"org/repo", "org/build-running"},
	}, srv.Client())

	res, err := f.Fetch(context.Background())

	if err == nil {
		t.Fatal("want an error naming the failing repository")
	}
	if !strings.Contains(err.Error(), "org/repo") {
		t.Errorf("error %q must name the failing repository", err)
	}
	if len(res.Builds) == 0 {
		t.Error("builds from the healthy repository must still be returned (QS-1.4)")
	}
}

// Review fix round 1, Finding 1: a non-empty workflow_runs array whose entries have no usable
// run_started_at must not be indistinguishable from AC3's legitimate empty-array case. It must
// surface as an error naming the repository, not read as a silent, healthy-looking empty tile.
// internal/fakesources' shared fixtures cannot produce this shape (Ruling in task-6: don't
// reshape them), so this test drives a small local handler instead.
func TestUnparseableRunTimestampsIsAnErrorNotAnEmptyTile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total_count": 2,
			"workflow_runs": [
				{"id": 1, "name": "CI", "status": "completed", "conclusion": "success",
				 "html_url": "https://example.invalid/1", "run_started_at": "", "updated_at": ""},
				{"id": 2, "name": "CI", "status": "in_progress", "conclusion": null,
				 "html_url": "https://example.invalid/2", "run_started_at": "not-a-timestamp", "updated_at": ""}
			]
		}`))
	}))
	defer srv.Close()

	f := github.NewBuildFetcher(github.Config{
		Token: "x", RESTBaseURL: srv.URL, Repos: []string{"org/unparseable"},
	}, srv.Client())

	res, err := f.Fetch(context.Background())

	if err == nil {
		t.Fatal("want an error when no run has a usable run_started_at")
	}
	if !strings.Contains(err.Error(), "org/unparseable") {
		t.Errorf("error %q must name the repository", err)
	}
	if len(res.Builds) != 0 {
		t.Errorf("len(builds) = %d, want 0 — an unreadable response must not silently yield a build", len(res.Builds))
	}
}
