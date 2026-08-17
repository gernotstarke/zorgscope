// Package ports declares the interfaces that separate zorgscope's domain from the outside
// world: fetching from upstream sources, persisting state, notifying about new items, and
// reading the current time. It imports only the standard library and internal/domain (QS-5.1);
// every adapter that implements one of these interfaces lives elsewhere.
package ports

import (
	"context"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// FetchResult is everything a single SourceFetcher.Fetch call can return: the items, builds and
// metrics it found. A fetcher populates only the fields relevant to its source; the rest stay
// nil.
type FetchResult struct {
	Items   []domain.Item
	Builds  []domain.Build
	Metrics []domain.Metric
}

// SourceFetcher retrieves the current state of one upstream source — GitHub, Plausible or
// Todoist.
type SourceFetcher interface {
	// Name identifies the source, e.g. for logging and for recording source health.
	Name() string
	// Fetch retrieves the source's current state. It returns an error rather than a partial
	// FetchResult on failure.
	Fetch(ctx context.Context) (FetchResult, error)
}

// Store persists everything zorgscope tracks: items, builds, metrics, source health, the last
// visit time, the refresh lease, refresh run history, and notification bookkeeping. It is
// implemented once, by the libSQL adapter.
type Store interface {
	// Migrate brings the store's schema up to date. It must be safe to call on every startup.
	Migrate(ctx context.Context) error

	// ReplaceItems replaces the full set of items for source with items, preserving FirstSeenAt
	// for items that already existed. It returns the number of items stored for source —
	// len(items) — not the number that are new.
	ReplaceItems(ctx context.Context, source string, items []domain.Item, now time.Time) (int, error)
	// UpsertBuilds inserts or updates builds, keyed by repository and workflow.
	UpsertBuilds(ctx context.Context, builds []domain.Build, now time.Time) error
	// UpsertMetrics inserts or updates metrics, keyed by site.
	UpsertMetrics(ctx context.Context, metrics []domain.Metric, now time.Time) error

	// Items returns every stored item.
	Items(ctx context.Context) ([]domain.Item, error)
	// Builds returns every stored build.
	Builds(ctx context.Context) ([]domain.Build, error)
	// Metrics returns every stored metric.
	Metrics(ctx context.Context) ([]domain.Metric, error)
	// SourceStates returns the last known health of every source, keyed by source name.
	SourceStates(ctx context.Context) (map[string]domain.SourceState, error)

	// RecordSourceOK records a successful fetch from source at time at, with count items
	// fetched.
	RecordSourceOK(ctx context.Context, source string, at time.Time, count int) error
	// RecordSourceError records a failed fetch from source at time at, with message msg.
	RecordSourceError(ctx context.Context, source string, at time.Time, msg string) error

	// LastVisit returns the time the dashboard was last viewed.
	LastVisit(ctx context.Context) (time.Time, error)
	// SetLastVisit records t as the time the dashboard was last viewed.
	SetLastVisit(ctx context.Context, t time.Time) error

	// AcquireRefreshLease attempts to take the refresh lease for holder, valid until ttl after
	// now. It returns whether the lease was acquired.
	AcquireRefreshLease(ctx context.Context, holder string, now time.Time, ttl time.Duration) (bool, error)
	// ReleaseRefreshLease releases the refresh lease held by holder.
	ReleaseRefreshLease(ctx context.Context, holder string) error

	// StartRun records the start of a refresh run triggered by trigger at time at, and returns
	// its ID.
	StartRun(ctx context.Context, trigger string, at time.Time) (int64, error)
	// FinishRun records the end of the refresh run identified by id at time at, with outcome ok
	// and detail message detail.
	FinishRun(ctx context.Context, id int64, at time.Time, ok bool, detail string) error
	// LastRun returns the most recently started refresh run.
	LastRun(ctx context.Context) (domain.RefreshRun, error)

	// MarkNotified records that the items identified by keys have been notified about, as of
	// time at.
	MarkNotified(ctx context.Context, keys []string, at time.Time) error
	// UnnotifiedKeys filters keys down to those that have not yet been marked notified.
	UnnotifiedKeys(ctx context.Context, keys []string) ([]string, error)

	// Close releases the store's underlying resources.
	Close() error
}

// Notifier sends a notification about new items.
type Notifier interface {
	// Notify sends a notification about items.
	Notify(ctx context.Context, items []domain.Item) error
}

// Clock reports the current time, so that callers needing time.Now can be tested with a fixed
// or fake clock instead.
type Clock interface{ Now() time.Time }

// SystemClock is the production Clock: it reports the real current time, in UTC.
type SystemClock struct{}

// Now returns the current time in UTC.
func (SystemClock) Now() time.Time { return time.Now().UTC() }
