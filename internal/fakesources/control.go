package fakesources

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
)

// controlSources is the set of source names /_control/fail accepts.
var controlSources = map[string]bool{"github": true, "plausible": true, "todoist": true}

// handleControlFail serves POST /_control/fail?source={github|plausible|todoist}&status={code}
// with an optional repo={owner}/{name}. Once called, subsequent requests to that source return
// the given status. When repo is present, only that target fails and every other target keeps
// working — Task 7 relies on this to assert that one bad repository does not lose the others.
//
// repo is named for its GitHub use (an "owner/name" repository) because that is the case the
// Task 6 brief specifies verbatim and Task 7's test posts literally (repo=org/bad); it is kept
// as-is rather than renamed. It is really a generic "which target on this source" parameter,
// though: shouldFailLocked keys github's failures by repository, but plausible has no repository
// concept, so a plausible caller passes a site_id in the same repo parameter (e.g.
// /_control/fail?source=plausible&repo=example.com&status=502) to scope the failure to one site.
// todoist has neither concept, so repo is ignored for it and only a source-wide failure applies.
func (s *server) handleControlFail(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	if !controlSources[source] {
		http.Error(w, "unknown source: "+source, http.StatusBadRequest)
		return
	}

	statusParam := r.URL.Query().Get("status")
	status, err := strconv.Atoi(statusParam)
	if err != nil || status < 100 || status > 599 {
		http.Error(w, "status must be a valid HTTP status code", http.StatusBadRequest)
		return
	}

	repo := r.URL.Query().Get("repo") // "" means the whole source fails

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail[source] == nil {
		s.fail[source] = map[string]int{}
	}
	s.fail[source][repo] = status

	w.WriteHeader(http.StatusOK)
}

// addIssueRequest is the optional JSON body /_control/add-issue accepts. Every field is
// optional; a field left zero gets a sensible default.
type addIssueRequest struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	Author string `json:"author"`
	State  string `json:"state"`
}

// handleControlAddIssue serves POST /_control/add-issue. It appends an issue to a repository
// (default org/repo, creating the repository if it does not already have a fixture) with
// createdAt and updatedAt set to now — the fixture a newly-appeared item test needs. The request
// body is optional JSON; every field defaults if omitted. It responds with the added issue.
func (s *server) handleControlAddIssue(w http.ResponseWriter, r *http.Request) {
	var req addIssueRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			http.Error(w, "bad request body", http.StatusBadRequest)
			return
		}
	}

	repo := req.Repo
	if repo == "" {
		repo = "org/repo"
	}
	title := req.Title
	if title == "" {
		title = "Newly added issue"
	}
	author := req.Author
	if author == "" {
		author = "control"
	}
	state := req.State
	if state == "" {
		state = "OPEN"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	fx := s.githubRepos[repo]
	if fx == nil {
		fx = &ghRepoFixture{}
		s.githubRepos[repo] = fx
	}

	number := req.Number
	if number == 0 {
		number = nextIssueNumber(fx)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	issue := ghIssue{
		Number:    number,
		Title:     title,
		URL:       "https://github.com/" + repo + "/issues/" + strconv.Itoa(number),
		Author:    ghAuthor{Login: author},
		CreatedAt: now,
		UpdatedAt: now,
		State:     state,
	}
	fx.Issues = append(fx.Issues, issue)

	writeJSON(w, http.StatusCreated, issue)
}

// nextIssueNumber returns one past the highest issue or pull-request number already present in
// fx, so an auto-numbered added issue never collides with a fixture issue or PR.
func nextIssueNumber(fx *ghRepoFixture) int {
	highest := 0
	for _, n := range fx.Issues {
		if n.Number > highest {
			highest = n.Number
		}
	}
	for _, n := range fx.PullRequests {
		if n.Number > highest {
			highest = n.Number
		}
	}
	return highest + 1
}

// handleControlReset serves POST /_control/reset, restoring every fixture to its pristine,
// embedded state and clearing every injected failure. Tests call this between cases: without it,
// one test's injected failure or added issue leaks into the next, and the suite passes or fails
// depending on test order.
func (s *server) handleControlReset(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.resetLocked(); err != nil {
		http.Error(w, "reset failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
