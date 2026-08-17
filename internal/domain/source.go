package domain

import "time"

// SourceState is the last known health of one upstream source (GitHub, Plausible or Todoist).
type SourceState struct {
	Source        string
	LastSuccessAt time.Time
	LastError     string
	LastErrorAt   time.Time
	ItemCount     int
}

// Stale reports whether the source's last success is older than after. A source that has never
// succeeded (LastSuccessAt zero) is always stale.
func (s SourceState) Stale(now time.Time, after time.Duration) bool {
	return now.Sub(s.LastSuccessAt) > after
}

// Failing reports whether the most recent event for this source was an error, i.e. the error is
// non-empty and happened after the last success.
func (s SourceState) Failing() bool {
	return s.LastError != "" && s.LastErrorAt.After(s.LastSuccessAt)
}

// RefreshRun records the outcome of one refresh cycle across all sources.
type RefreshRun struct {
	ID                    int64
	StartedAt, FinishedAt time.Time
	Trigger               string
	OK                    bool
	Detail                string
}
