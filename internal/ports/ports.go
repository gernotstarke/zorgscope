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
	// Fetch retrieves the source's current state. On a partial failure (e.g. one repository of
	// several erroring) it may return the items it did successfully fetch together with a
	// non-nil error, rather than discarding them. A caller must not store such a partial
	// result: Store.ReplaceItems deletes rows absent from the incoming set, so storing a
	// partial GitHub result would delete every item belonging to the repository that failed —
	// and when that repository recovered, its items would come back with a fresh
	// FirstSeenAt and be shown as NEW. A transient upstream error would then manufacture a
	// screen of false new items, exactly what the first-seen invariant exists to prevent. A
	// non-nil error therefore means: do not call ReplaceItems with this result.
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
	// UpsertBuilds replaces the full set of builds with builds, keyed by repository alone: one
	// row per repository. A repository running several workflows is represented by a single row
	// (the one build this package's fetchers select per FR-2.3 AC2), not one row per workflow.
	// Like ReplaceItems it also deletes: a repository absent from builds loses its row, so a
	// repository dropped from the configuration does not linger on the tile forever. Passing an
	// empty slice therefore clears the table.
	UpsertBuilds(ctx context.Context, builds []domain.Build, now time.Time) error
	// UpsertMetrics inserts or updates metrics, keyed by (site, window_days) — one row per site
	// and window, not one per site. A caller that treats the key as the site alone leaves one of
	// the two windows blank on every write.
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
	// fetched. It does not clear the recorded last error: the dashboard shows both, so that a
	// source that has recovered still says what went wrong last time (FR-1.4 AC2).
	RecordSourceOK(ctx context.Context, source string, at time.Time, count int) error
	// RecordSourceError records a failed fetch from source at time at, with message msg. The
	// mirror of RecordSourceOK: it leaves the last success and the item count alone, so the
	// dashboard can still say how old the last good data is (FR-1.4 AC2).
	RecordSourceError(ctx context.Context, source string, at time.Time, msg string) error

	// LastVisit returns the time the dashboard was last viewed, or the zero time — with a nil
	// error — if it never was. A store that has never been visited is not an error condition;
	// with the zero time, nothing is new (QS-1.3).
	LastVisit(ctx context.Context) (time.Time, error)
	// SetLastVisit records t as the time the dashboard was last viewed.
	SetLastVisit(ctx context.Context, t time.Time) error

	// AcquireRefreshLease attempts to take the refresh lease for holder, valid until ttl after
	// now. It returns whether the lease was acquired. Acquiring succeeds when the lease is free,
	// expired, or already held by the same holder. holder must not contain '|', which separates
	// holder from expiry in the stored value; one that does is rejected with an error rather
	// than stored.
	AcquireRefreshLease(ctx context.Context, holder string, now time.Time, ttl time.Duration) (bool, error)
	// ReleaseRefreshLease releases the refresh lease if holder is the one holding it, and does
	// nothing otherwise — a holder whose lease has expired and been taken over must not free the
	// new holder's. Releasing a lease one does not hold is not an error.
	ReleaseRefreshLease(ctx context.Context, holder string) error

	// StartRun records the start of a refresh run triggered by trigger at time at, and returns
	// its ID.
	StartRun(ctx context.Context, trigger string, at time.Time) (int64, error)
	// FinishRun records the end of the refresh run identified by id at time at, with outcome ok
	// and detail message detail.
	FinishRun(ctx context.Context, id int64, at time.Time, ok bool, detail string) error
	// LastRun returns the most recently started refresh run, or the zero RefreshRun — with a nil
	// error — when no run has ever been recorded. "Never run" is a state to render, not a
	// failure, so it is not reported as one.
	LastRun(ctx context.Context) (domain.RefreshRun, error)

	// MarkNotified records that the items identified by keys have been notified about, as of
	// time at. A key already recorded keeps its original time: an item is announced once, and the
	// first announcement is the one that counts (FR-6.1 AC2).
	MarkNotified(ctx context.Context, keys []string, at time.Time) error
	// UnnotifiedKeys filters keys down to those that have not yet been marked notified, in the
	// order given.
	UnnotifiedKeys(ctx context.Context, keys []string) ([]string, error)

	// Close releases the store's underlying resources.
	Close() error
}

// Notifier sends a notification about new items.
type Notifier interface {
	// Notify sends a notification about items — one message per item, in order, stopping at the
	// first failure. A non-nil error therefore means an unknown prefix of items may already have
	// been delivered, which is why a caller that must not announce anything twice passes one item
	// at a time and records each success as it happens.
	Notify(ctx context.Context, items []domain.Item) error
}

// Clock reports the current time, so that callers needing time.Now can be tested with a fixed
// or fake clock instead.
type Clock interface{ Now() time.Time }

// SystemClock is the production Clock: it reports the real current time, in UTC.
type SystemClock struct{}

// Now returns the current time in UTC.
func (SystemClock) Now() time.Time { return time.Now().UTC() }
