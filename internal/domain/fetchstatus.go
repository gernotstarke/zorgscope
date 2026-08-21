package domain

import "time"

// FetchStatus is the health record of one source (arc42 §8.1, FR-10.1).
type FetchStatus struct {
	SourceID    string
	Kind        string // source kind, e.g. "github-repo"
	LastSuccess time.Time
	LastError   time.Time
	ErrorMsg    string
	NextRun     time.Time
	ItemCount   int
	Duration    time.Duration
	InFlight    bool
	AuthFailed  bool // last error was an authentication failure (FR-11.3)
}

// Healthy is true when the last fetch succeeded (or nothing failed yet). A success in the same second
// as an earlier error counts as healthy.
func (s FetchStatus) Healthy() bool {
	return s.LastError.IsZero() || !s.LastSuccess.Before(s.LastError)
}

// DataAge is how old the last good data is; zero when never fetched.
func (s FetchStatus) DataAge(now time.Time) time.Duration {
	if s.LastSuccess.IsZero() {
		return 0
	}
	return now.Sub(s.LastSuccess)
}
