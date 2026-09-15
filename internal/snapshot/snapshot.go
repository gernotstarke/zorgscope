// Package snapshot keeps the most recently fetched item list in memory and refetches it when it
// is older than a TTL. It is the whole of zorgscope's state: a process that restarts, or a Fly
// Machine that wakes from zero, starts empty and pays one fetch on its first page view.
package snapshot

import (
	"context"
	"sync"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// Snapshot is what a page renders from.
//
// Items and FetchedAt describe the last fetch that produced items — possibly with an error
// alongside, when some repositories failed and others did not. Err and ErrAt describe the last
// fetch that failed, and are cleared by the next fetch that does not. So a page can say both
// "this list is from 11:50" and "GitHub has been failing since 12:04" at once.
type Snapshot struct {
	Items     []domain.Item
	FetchedAt time.Time // zero until a fetch has returned items
	Err       error     // the most recent fetch error, nil once a fetch succeeds cleanly
	ErrAt     time.Time // when Err was recorded
}

// Cache is safe for concurrent use. A fetch runs under the mutex, so concurrent callers wait for
// the one fetch in flight rather than start their own: with one visitor and a fetch of a few
// seconds that is simpler than single-flight and equally correct.
type Cache struct {
	src   ports.Source
	ttl   time.Duration
	clock ports.Clock

	mu    sync.Mutex
	cur   Snapshot
	stale bool // set by Invalidate; cleared by the next fetch
}

// New returns an empty cache. ttl <= 0 means every Get fetches.
func New(src ports.Source, ttl time.Duration, clock ports.Clock) *Cache {
	return &Cache{src: src, ttl: ttl, clock: clock, stale: true}
}

// Get returns the current snapshot, fetching first if it is missing, invalidated, or older than
// the TTL. A failed fetch never discards the previous items.
func (c *Cache) Get(ctx context.Context) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.clock.Now()
	if !c.stale && !c.cur.FetchedAt.IsZero() && now.Sub(c.cur.FetchedAt) <= c.ttl {
		return c.cur
	}
	// A failing source is retried at most once per TTL as well, so a broken upstream does not
	// turn every page view into a fetch.
	if !c.stale && !c.cur.ErrAt.IsZero() && now.Sub(c.cur.ErrAt) <= c.ttl {
		return c.cur
	}

	items, err := c.src.Fetch(ctx)
	c.stale = false
	if items != nil || err == nil {
		c.cur.Items = items
		c.cur.FetchedAt = now
	}
	if err != nil {
		c.cur.Err = err
		c.cur.ErrAt = now
	} else {
		c.cur.Err = nil
		c.cur.ErrAt = time.Time{}
	}
	return c.cur
}

// Invalidate makes the next Get fetch regardless of age.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	c.stale = true
	c.mu.Unlock()
}
