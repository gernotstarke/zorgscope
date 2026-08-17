package fakesources_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gernotstarke/zorgscope/internal/fakesources"
)

type workflowRun struct {
	Name         string  `json:"name"`
	Status       string  `json:"status"`
	Conclusion   *string `json:"conclusion"`
	HTMLURL      string  `json:"html_url"`
	RunStartedAt string  `json:"run_started_at"`
	UpdatedAt    string  `json:"updated_at"`
}

type runsBody struct {
	WorkflowRuns []workflowRun `json:"workflow_runs"`
}

func fetchRuns(t *testing.T, base, owner, repo string) runsBody {
	t.Helper()
	resp, err := http.Get(base + "/repos/" + owner + "/" + repo + "/actions/runs?per_page=10&branch=main")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body runsBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func TestActionsRunsNewestCompletedHasSuccessConclusion(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	body := fetchRuns(t, srv.URL, "org", "repo")
	if len(body.WorkflowRuns) == 0 {
		t.Fatal("want at least one run")
	}
	newest := body.WorkflowRuns[0]
	if newest.Status != "completed" || newest.Conclusion == nil || *newest.Conclusion != "success" {
		t.Errorf("newest run = %+v, want completed/success", newest)
	}
}

func TestActionsRunsInProgressKeepsPreviousConclusion(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	body := fetchRuns(t, srv.URL, "org", "build-running")
	if len(body.WorkflowRuns) < 2 {
		t.Fatalf("want at least 2 runs, got %d", len(body.WorkflowRuns))
	}
	newest := body.WorkflowRuns[0]
	if newest.Status != "in_progress" {
		t.Errorf("newest run status = %q, want in_progress", newest.Status)
	}
	older := body.WorkflowRuns[1]
	if older.Status != "completed" || older.Conclusion == nil || *older.Conclusion == "" {
		t.Errorf("older run = %+v, want a completed run with a conclusion", older)
	}
}

func TestActionsRunsEmptyIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	body := fetchRuns(t, srv.URL, "org", "build-none")
	if len(body.WorkflowRuns) != 0 {
		t.Fatalf("len(workflow_runs) = %d, want 0", len(body.WorkflowRuns))
	}
}

func TestActionsRunsFailControlScopedToOneRepo(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	post(t, srv.URL+"/_control/fail?source=github&repo=org/repo&status=503")

	resp, err := http.Get(srv.URL + "/repos/org/repo/actions/runs")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}

	// A different repo must keep working.
	body := fetchRuns(t, srv.URL, "org", "build-none")
	if len(body.WorkflowRuns) != 0 {
		t.Fatalf("len(workflow_runs) = %d, want 0", len(body.WorkflowRuns))
	}
}
