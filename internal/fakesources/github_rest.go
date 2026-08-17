package fakesources

import "net/http"

// handleActionsRuns serves GET /repos/{owner}/{repo}/actions/runs — the GitHub Actions REST
// endpoint Task 8 reads. per_page and branch are accepted but not honoured: they need not filter
// the fixture, only not break the request. A repository with no fixture is served with an empty
// workflow_runs array, matching FR-2.3 AC3 (no workflows is not an error).
func (s *server) handleActionsRuns(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	repo := r.PathValue("repo")
	full := owner + "/" + repo

	s.mu.Lock()
	defer s.mu.Unlock()

	if status, fail := s.shouldFailLocked("github", full); fail {
		w.WriteHeader(status)
		return
	}

	fx := s.githubRuns[full]
	if fx == nil {
		fx = &runsFixture{WorkflowRuns: []workflowRun{}}
	}

	writeJSON(w, http.StatusOK, fx)
}
