package github_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

	if !res.OwnsBuilds {
		t.Error("the builds fetcher does not claim the builds table, so nothing it returns is " +
			"stored at all (FR-2.3)")
	}

	b := res.Builds[0]
	if b.Repo != "org/repo" {
		t.Errorf("Repo = %q, want org/repo", b.Repo)
	}
	if b.Workflow != "CI" {
		t.Errorf("Workflow = %q, want CI", b.Workflow)
	}
	// The display name and the file are both carried, because only the file can address the
	// workflow from outside — a badge takes the file name (FR-2.3 AC5).
	if b.WorkflowPath != ".github/workflows/ci.yml" {
		t.Errorf("WorkflowPath = %q, want .github/workflows/ci.yml", b.WorkflowPath)
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
	// FetchedAt must stay zero here: Store.UpsertBuilds stamps it with the run's `now`, and a
	// fetcher that filled it in would be calling time.Now outside ports.SystemClock for a value
	// the store overwrites anyway.
	if !b.FetchedAt.IsZero() {
		t.Errorf("FetchedAt = %v, want the zero time — the store owns it", b.FetchedAt)
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
	// And the empty result still claims the builds table. This is the case the claim exists for:
	// without it the runner cannot tell "every watched repository has lost its build" from "this
	// source has nothing to say about builds", so it stores nothing and the rows of repositories
	// that no longer build stay on the tile forever.
	if !res.OwnsBuilds {
		t.Error("a builds fetch that found none does not claim the builds table, so the stale " +
			"rows of repositories that no longer build are never cleared (FR-2.3)")
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

// badgeServer serves a badge for every request and counts what it was asked for.
type badgeServer struct {
	mu       sync.Mutex
	paths    []string
	status   int
	ctype    string
	body     []byte
	delay    time.Duration
	authSeen bool
}

func (b *badgeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	b.paths = append(b.paths, r.URL.Path)
	if r.Header.Get("Authorization") != "" {
		b.authSeen = true
	}
	delay, status, ctype, body := b.delay, b.status, b.ctype, b.body
	b.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("Content-Type", ctype)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (b *badgeServer) asked() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.paths...)
}

func newBadgeServer() *badgeServer {
	return &badgeServer{
		status: http.StatusOK,
		ctype:  "image/svg+xml; charset=utf-8",
		body:   []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="88" height="20"></svg>`),
	}
}

// FR-2.3 AC5: the badge is fetched with the build state it illustrates and travels with it, so
// that the page showing it makes no request of its own.
func TestBuildsCarryTheirBadge(t *testing.T) {
	fake := httptest.NewServer(fakesources.NewServer())
	defer fake.Close()
	badges := newBadgeServer()
	bs := httptest.NewServer(badges)
	defer bs.Close()

	f := github.NewBuildFetcher(github.Config{
		Token: "x", RESTBaseURL: fake.URL, BadgeBaseURL: bs.URL, Repos: []string{"org/repo"},
	}, fake.Client())

	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Builds) != 1 {
		t.Fatalf("len(builds) = %d, want 1", len(res.Builds))
	}
	if len(res.Builds[0].Badge) == 0 {
		t.Fatal("the build carries no badge")
	}
	// Addressed by the workflow *file*, which is the only thing a badge service understands.
	if got := badges.asked(); len(got) != 1 || got[0] != "/org/repo/ci.yml" {
		t.Errorf("the badge was asked for at %v, want [/org/repo/ci.yml]", got)
	}
	// The badge service is not GitHub and has no business holding this installation's token
	// (QS-4.3).
	if badges.authSeen {
		t.Error("the badge request carried a credential")
	}
}

// A badge is decoration. Nothing about it may fail the fetch it belongs to, or take the build
// state down with it — the same rule a failed announcement follows (FR-6.1 AC3).
func TestABadgeThatDoesNotArriveNeverFailsTheFetch(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(*badgeServer)
	}{
		{"the service is rate limiting", func(b *badgeServer) { b.status = http.StatusTooManyRequests }},
		{"the service is broken", func(b *badgeServer) { b.status = http.StatusInternalServerError }},
		{"something answered with a web page", func(b *badgeServer) { b.ctype = "text/html"; b.body = []byte("<html>nope") }},
		{"the answer is empty", func(b *badgeServer) { b.body = nil }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := httptest.NewServer(fakesources.NewServer())
			defer fake.Close()
			badges := newBadgeServer()
			c.prepare(badges)
			bs := httptest.NewServer(badges)
			defer bs.Close()

			f := github.NewBuildFetcher(github.Config{
				Token: "x", RESTBaseURL: fake.URL, BadgeBaseURL: bs.URL,
				Repos: []string{"org/repo"},
			}, fake.Client())

			res, err := f.Fetch(context.Background())
			if err != nil {
				t.Fatalf("Fetch: %v — a badge must never fail the fetch", err)
			}
			if len(res.Builds) != 1 {
				t.Fatalf("len(builds) = %d, want 1 — the build state was lost with the badge", len(res.Builds))
			}
			if res.Builds[0].Conclusion != "success" {
				t.Errorf("Conclusion = %q, want success", res.Builds[0].Conclusion)
			}
			if len(res.Builds[0].Badge) != 0 {
				t.Error("a badge was stored from a response that should not have produced one")
			}
		})
	}
}

// The same, for a badge service that cannot be reached at all — a timeout takes this code path
// too, and this one costs the test suite nothing to exercise.
func TestABadgeServiceThatCannotBeReachedNeverFailsTheFetch(t *testing.T) {
	fake := httptest.NewServer(fakesources.NewServer())
	defer fake.Close()
	bs := httptest.NewServer(newBadgeServer())
	closed := bs.URL
	bs.Close() // nothing is listening there now

	f := github.NewBuildFetcher(github.Config{
		Token: "x", RESTBaseURL: fake.URL, BadgeBaseURL: closed, Repos: []string{"org/repo"},
	}, fake.Client())

	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v — an unreachable badge service must never fail the fetch", err)
	}
	if len(res.Builds) != 1 || res.Builds[0].Conclusion != "success" {
		t.Fatalf("builds = %+v, want the build state intact", res.Builds)
	}
	if len(res.Builds[0].Badge) != 0 {
		t.Error("a badge appeared from a service that is not running")
	}
}

// A run with no workflow file behind it — GitHub's built-in Pages deployment — is not asked about
// at all. There is nothing to address, and asking would spend a request to be told so.
func TestNoBadgeIsRequestedForARunWithNoWorkflowFile(t *testing.T) {
	fake := httptest.NewServer(fakesources.NewServer())
	defer fake.Close()
	badges := newBadgeServer()
	bs := httptest.NewServer(badges)
	defer bs.Close()

	// org/build-none has no runs at all, so there is no build and therefore nothing to illustrate.
	f := github.NewBuildFetcher(github.Config{
		Token: "x", RESTBaseURL: fake.URL, BadgeBaseURL: bs.URL,
		Repos: []string{"org/build-none"},
	}, fake.Client())

	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := badges.asked(); len(got) != 0 {
		t.Errorf("badges were requested for a repository with no runs: %v", got)
	}
}
