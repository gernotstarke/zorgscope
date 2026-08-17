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
	KindWatchCredentials = "watch-credentials"
	KindWatchURL         = "watch-url"
)

// SourceFetcher retrieves the current items of one source.
type SourceFetcher interface {
	// ID returns the source's stable identifier, e.g. "github:arc42/arc42-template".
	ID() string
	// Kind returns one of the Kind* constants.
	Kind() string
	// Fetch retrieves the source's current items.
	Fetch(ctx context.Context) ([]domain.Item, error)
}

// ItemStore caches the latest items per source.
type ItemStore interface {
	// ReplaceItems atomically replaces the items of a source, preserving FirstSeen of known ids
	// and setting FirstSeen=now for new ones.
	ReplaceItems(ctx context.Context, sourceID string, items []domain.Item, now time.Time) error
	// Items returns a source's items, newest (CreatedAt) first.
	Items(ctx context.Context, sourceID string) ([]domain.Item, error)
	// AllItems returns every item across all sources.
	AllItems(ctx context.Context) ([]domain.Item, error)
	// ExternalIDs returns the external ids currently stored for a source.
	ExternalIDs(ctx context.Context, sourceID string) ([]string, error)
}

// SnapshotStore persists daily id snapshots.
type SnapshotStore interface {
	// PutSnapshot upserts a snapshot by (source, date).
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
	// PutDismissal upserts a dismissal by item id.
	PutDismissal(ctx context.Context, d domain.Dismissal) error
	// Dismissal returns nil, nil when the item has no dismissal.
	Dismissal(ctx context.Context, id domain.ItemID) (*domain.Dismissal, error)
	// Dismissals returns every dismissal, keyed by item id.
	Dismissals(ctx context.Context) (map[domain.ItemID]domain.Dismissal, error)
}

// StatusStore persists per-source fetch health.
type StatusStore interface {
	// RecordStatus upserts a source's fetch status.
	RecordStatus(ctx context.Context, s domain.FetchStatus) error
	// Status returns nil, nil when the source has no recorded status.
	Status(ctx context.Context, sourceID string) (*domain.FetchStatus, error)
	// Statuses returns every recorded status, sorted by SourceID.
	Statuses(ctx context.Context) ([]domain.FetchStatus, error)
}

// Store is everything the app persists.
type Store interface {
	ItemStore
	SnapshotStore
	DismissalStore
	StatusStore
}

// Clock abstracts time.Now for testability.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
}

// CredentialSink receives credential expiries adapters detect (e.g. GitHub token header, FR-11.2).
type CredentialSink interface {
	// ReportCredential records that a named credential, used by usedBy, expires at the given time.
	ReportCredential(name string, expires *time.Time, usedBy string)
}

// Notifier is reserved for later push channels (Slack); unused in v1.
type Notifier interface {
	// Notify sends a message with the given title and body.
	Notify(ctx context.Context, title, body string) error
}
