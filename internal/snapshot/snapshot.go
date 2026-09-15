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
// Items is the newest list of every repository: the items of the last fetch that produced any,
// plus — when that fetch failed for some repositories — the previous items of each repository it
// returned nothing for. FetchedAt is the last fetch whose items are all current, and does not move
// on a partial fetch: it is what the page states as "fetched at" and what "Mark all seen"
// acknowledges, and neither may claim a repository that did not fetch (FR-1.2, FR-1.4). The one
// exception is a partial first fetch, which has nothing older to fall back on and stamps now.
//
// Err and ErrAt describe the last fetch that failed, and are cleared by the next fetch that does
// not. So a page can say both "this list is from 11:50" and "GitHub has been failing since 12:04"
// at once.
type Snapshot struct {
	Items     []domain.Item
	FetchedAt time.Time // zero until a fetch has returned items
	Err       error     // the most recent fetch error, nil once a fetch succeeds cleanly
	ErrAt     time.Time // when Err was recorded
}

// fetchBudget bounds one fetch. A fetch is shared by every caller waiting on the mutex, so it runs
// detached from the context of the caller that happened to trigger it — a visitor closing the tab
// must not cancel it and leave everyone else an error for a whole TTL. Detached, nothing would stop
// a hung upstream from holding the mutex, and every page view with it, indefinitely; the budget is
// that stop, and is generous enough for a representative configuration's sequential requests.
const fetchBudget = 60 * time.Second

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

// New returns an empty cache. ttl is how long both a fetched list and a failure are reused before
// the next Get fetches again, measured against clock; config.Load only ever supplies a positive
// one.
func New(src ports.Source, ttl time.Duration, clock ports.Clock) *Cache {
	return &Cache{src: src, ttl: ttl, clock: clock, stale: true}
}

// Get returns the current snapshot, fetching first if it is missing, invalidated, or older than
// the TTL. A failed fetch never discards the previous items, and ctx's cancellation does not reach
// the fetch — see fetchBudget.
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

	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchBudget)
	defer cancel()
	items, err := c.src.Fetch(fetchCtx)
	c.stale = false
	switch {
	case err == nil:
		c.cur.Items, c.cur.FetchedAt = items, now
	case items == nil:
		// Nothing fetched at all: the previous snapshot stands as it was.
	case c.cur.FetchedAt.IsZero():
		// A partial first fetch has no older list to fill in from.
		c.cur.Items, c.cur.FetchedAt = items, now
	default:
		// A partial fetch: fill in the repositories that failed, and leave FetchedAt alone.
		c.cur.Items = mergePartial(c.cur.Items, items)
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

// mergePartial is the list after a fetch that failed for some repositories: every fresh item, plus
// the previous items of each repository the fresh result holds nothing for.
//
// Repositories are told apart by Item.Repo alone, because the source reports a partial failure as
// one joined error rather than per repository. A repository that fetched cleanly but now has no
// open items is therefore indistinguishable from one that failed, and keeps its old items until the
// next clean fetch — the cheaper mistake, since the alternative blanks a repository on a failure.
//
// It builds a new slice and writes into neither argument: prev is the slice earlier snapshots
// handed to renders that may still be reading it.
func mergePartial(prev, fresh []domain.Item) []domain.Item {
	fetched := make(map[string]bool, len(fresh))
	for _, it := range fresh {
		fetched[it.Repo] = true
	}
	out := make([]domain.Item, 0, len(fresh)+len(prev))
	out = append(out, fresh...)
	for _, it := range prev {
		if !fetched[it.Repo] {
			out = append(out, it)
		}
	}
	return out
}

// Invalidate makes the next Get fetch regardless of age.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	c.stale = true
	c.mu.Unlock()
}
