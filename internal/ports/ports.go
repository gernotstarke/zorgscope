// Package ports defines the interfaces between zorgscope's application core and the outside world
// (arc42 §5). Adapters implement them; the app consumes them.
package ports

import (
	"context"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// Source kinds (config section names / registry keys).
const (
	KindGitHubRepo       = "github-repo"
	KindGitHubMentions   = "github-mentions"
	KindPlausibleSite    = "plausible-site"
	KindTodoist          = "todoist"
	KindFeed             = "feed"
	KindWatchCredentials = "watch-credentials"
	KindWatchURL         = "watch-url"
)

// SourceFetcher retrieves the current items of one source.
type SourceFetcher interface {
	ID() string   // e.g. "github:arc42/arc42-template"
	Kind() string // one of the Kind* constants
	Fetch(ctx context.Context) ([]domain.Item, error)
}

// ItemStore caches the latest items per source.
type ItemStore interface {
	// ReplaceItems atomically replaces the items of a source, preserving FirstSeen of known ids
	// and setting FirstSeen=now for new ones.
	ReplaceItems(ctx context.Context, sourceID string, items []domain.Item, now time.Time) error
	// Items returns a source's items, newest (CreatedAt) first.
	Items(ctx context.Context, sourceID string) ([]domain.Item, error)
	AllItems(ctx context.Context) ([]domain.Item, error)
	ExternalIDs(ctx context.Context, sourceID string) ([]string, error)
}

// SnapshotStore persists daily id snapshots.
type SnapshotStore interface {
	PutSnapshot(ctx context.Context, s domain.Snapshot) error
	// LatestSnapshot returns nil, nil when the source has no snapshot.
	LatestSnapshot(ctx context.Context, sourceID string) (*domain.Snapshot, error)
	// SnapshotBefore returns the latest snapshot with Date < date, or nil, nil.
	SnapshotBefore(ctx context.Context, sourceID, date string) (*domain.Snapshot, error)
	// PruneSnapshots deletes snapshots with Date < beforeDate.
	PruneSnapshots(ctx context.Context, beforeDate string) error
}

// DismissalStore persists user dismissals.
type DismissalStore interface {
	PutDismissal(ctx context.Context, d domain.Dismissal) error
	Dismissal(ctx context.Context, id domain.ItemID) (*domain.Dismissal, error)
	Dismissals(ctx context.Context) (map[domain.ItemID]domain.Dismissal, error)
}

// StatusStore persists per-source fetch health.
type StatusStore interface {
	RecordStatus(ctx context.Context, s domain.FetchStatus) error
	Status(ctx context.Context, sourceID string) (*domain.FetchStatus, error)
	Statuses(ctx context.Context) ([]domain.FetchStatus, error) // sorted by SourceID
}

// Store is everything the app persists.
type Store interface {
	ItemStore
	SnapshotStore
	DismissalStore
	StatusStore
}

// Clock abstracts time.Now for testability.
type Clock interface{ Now() time.Time }

// CredentialSink receives credential expiries adapters detect (e.g. GitHub token header, FR-11.2).
type CredentialSink interface {
	ReportCredential(name string, expires *time.Time, usedBy string)
}

// Notifier is reserved for later push channels (Slack); unused in v1.
type Notifier interface {
	Notify(ctx context.Context, title, body string) error
}
