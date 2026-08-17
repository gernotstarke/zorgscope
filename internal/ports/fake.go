package ports

import (
	"context"
	"sync"
	"time"
)

// FixedClock is a test Clock that reports a fixed time until advanced.
type FixedClock struct{ T time.Time }

// Now returns the clock's current time.
func (c *FixedClock) Now() time.Time { return c.T }

// Advance moves the clock forward by d.
func (c *FixedClock) Advance(d time.Duration) { c.T = c.T.Add(d) }

// FakeFetcher is a test SourceFetcher that returns a fixed result, or an error, and counts its
// calls.
type FakeFetcher struct {
	SourceName string
	Result     FetchResult
	Err        error
	// Calls counts the number of times Fetch has been called. It is safe to read directly only
	// after all fetches have finished; while a fetch may still be in flight, use CallCount
	// instead.
	Calls int
	// Block, when non-nil, makes Fetch wait until it is closed before returning. Fetch also
	// honours context cancellation while waiting.
	Block chan struct{}

	mu sync.Mutex
}

// Name returns the fetcher's configured source name.
func (f *FakeFetcher) Name() string { return f.SourceName }

// Fetch first checks ctx.Err(): an already-cancelled context is refused immediately with
// FetchResult{}, ctx.Err(), and Calls is not incremented, matching a real fetcher (every net/http
// call fails immediately on a cancelled context) and what Task 15 expects for a request rejected
// before fetching. Otherwise it increments Calls, then waits on Block if it is non-nil, returning
// ctx.Err() promptly if the context is cancelled while waiting. Otherwise it returns the
// configured Result and Err.
func (f *FakeFetcher) Fetch(ctx context.Context) (FetchResult, error) {
	if err := ctx.Err(); err != nil {
		return FetchResult{}, err
	}

	f.mu.Lock()
	f.Calls++
	f.mu.Unlock()

	if f.Block != nil {
		select {
		case <-f.Block:
		case <-ctx.Done():
			return FetchResult{}, ctx.Err()
		}
	}

	return f.Result, f.Err
}

// CallCount returns the number of times Fetch has been called so far. Unlike reading Calls
// directly, it is safe to call while a fetch may still be in flight.
func (f *FakeFetcher) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Calls
}
