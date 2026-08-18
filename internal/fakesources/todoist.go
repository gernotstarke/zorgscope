package fakesources

import "net/http"

// handleTodoistTasks serves GET /rest/v2/tasks, returning a plain JSON array of tasks — Todoist's
// REST v2 shape. The filter query parameter is accepted but not applied: Task 10 filters again in
// Go against its own clock, so the fixture only needs to hold tasks with distinguishable due
// dates, not implement Todoist's filter language.
func (s *server) handleTodoistTasks(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if status, fail := s.shouldFailLocked("todoist", ""); fail {
		w.WriteHeader(status)
		return
	}

	writeJSON(w, http.StatusOK, s.todoist)
}

// todoistProjectsTarget is the /_control/fail target that scopes a failure to GET
// /rest/v2/projects alone, leaving GET /rest/v2/tasks healthy. An adapter that resolves a task's
// project_id to a project name makes two calls where it used to make one, and the second must not
// be able to destroy the first: this target is how a test proves it.
const todoistProjectsTarget = "projects"

// handleTodoistProjects serves GET /rest/v2/projects, returning a plain JSON array of projects —
// Todoist's REST v2 shape. It is the lookup table for a task's project_id.
func (s *server) handleTodoistProjects(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if status, fail := s.shouldFailLocked("todoist", todoistProjectsTarget); fail {
		w.WriteHeader(status)
		return
	}

	writeJSON(w, http.StatusOK, s.todoistProjects)
}
