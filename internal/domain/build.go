package domain

import "time"

// Build is the most recent CI run for a repository's workflow.
type Build struct {
	Repo, Workflow, Conclusion, Status, RunURL string
	FinishedAt, FetchedAt                      time.Time
}
