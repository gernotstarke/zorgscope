package domain

import "time"

// SourceState is the last known health of one upstream fetcher — GitHub's issues or its builds.
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

// Running reports whether this run is still open: it was started and nothing has finished it.
//
// The distinction is not a nicety. A store returns the most recently *started* run, so the run a
// dashboard rendered during a refresh is the open one, and an open run carries a zero FinishedAt —
// the same zero a database holding no runs at all returns. Told apart by FinishedAt alone the two
// are the same value, and the second reading is the one that reaches the page: "no refresh has
// run yet", on a database holding a month of them. It is guaranteed on the 409 page, whose whole
// purpose is to say that a refresh is running.
//
// StartedAt rather than ID is what separates them, because it is the field an open run is
// certain to carry whatever recorded it.
func (r RefreshRun) Running() bool {
	return !r.StartedAt.IsZero() && r.FinishedAt.IsZero()
}
