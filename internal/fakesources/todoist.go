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
