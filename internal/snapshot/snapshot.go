// Package snapshot keeps the most recently fetched item list in memory and refetches it when it
// is older than a TTL. It is the whole of zorgscope's state: a process that restarts, or a Fly
// Machine that wakes from zero, starts empty and fetches on its first page view.
//
// Get never waits for that fetch (QS-2.6). When one is due it starts it in a goroutine of its own
// and returns what is known so far, marked Fetching; the page shows a wait page and asks again.
// The goroutine is the one piece of work this process ever does outside a request, it exists only
// because a request asked, and it lives at most fetchBudget (ADR-0011). There is no ticker.
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
//
// Fetching reports that a fetch is in flight: everything else in the snapshot is what was known
// before it started. A page that would rather wait than show that (FR-1.9) reads this field.
type Snapshot struct {
	Items     []domain.Item
	FetchedAt time.Time // zero until a fetch has returned items
	Err       error     // the most recent fetch error, nil once a fetch succeeds cleanly
	ErrAt     time.Time // when Err was recorded
	Fetching  bool
}

// fetchBudget bounds one fetch. A fetch is shared by every caller that finds it in flight, so it
// runs detached from the context of the caller that happened to trigger it — a visitor closing the
// tab must not cancel it and leave everyone else an error for a whole TTL. Detached, nothing would
// stop a hung upstream from holding the in-flight mark, and every page view with it, indefinitely;
// the budget is that stop, and is generous for a fetch that runs its repositories side by side.
const fetchBudget = 60 * time.Second

// Cache is safe for concurrent use. At most one fetch runs at a time: a Get that finds one in
// flight reports it rather than starting another.
type Cache struct {
	src   ports.Source
	ttl   time.Duration
	clock ports.Clock

	mu       sync.Mutex
	cur      Snapshot // cur.Fetching is always false; Get sets it on the copy it returns
	stale    bool     // set by Invalidate; cleared when the next fetch lands
	inflight bool     // a fetch goroutine is running
}

// New returns an empty cache. ttl is how long both a fetched list and a failure are reused before
// the next Get fetches again, measured against clock; config.Load only ever supplies a positive
// one.
func New(src ports.Source, ttl time.Duration, clock ports.Clock) *Cache {
	return &Cache{src: src, ttl: ttl, clock: clock, stale: true}
}

// Get returns the current snapshot at once. When a fetch is due — nothing fetched yet,
// invalidated, the list older than the TTL, or the last failure older than the TTL — and none is
// in flight, it starts one and returns with Fetching set; while one is in flight, it returns the
// previous snapshot with Fetching set. ctx's cancellation does not reach the fetch — see
// fetchBudget.
func (c *Cache) Get(ctx context.Context) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.inflight {
		return c.fetching()
	}
	now := c.clock.Now()
	if !c.due(now) {
		return c.cur
	}

	c.inflight = true
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchBudget)
	go func() {
		defer cancel()
		items, err := c.src.Fetch(fetchCtx)
		c.land(now, items, err)
	}()
	return c.fetching()
}

// fetching is the snapshot Get returns while a fetch runs: what is known, marked. Called with
// the mutex held.
func (c *Cache) fetching() Snapshot {
	s := c.cur
	s.Fetching = true
	return s
}

// due reports whether a fetch should start now. A failing source is retried at most once per TTL
// as well, so a broken upstream does not turn every page view into a fetch. Called with the mutex
// held.
func (c *Cache) due(now time.Time) bool {
	if c.stale {
		return true
	}
	if !c.cur.FetchedAt.IsZero() && now.Sub(c.cur.FetchedAt) <= c.ttl {
		return false
	}
	if !c.cur.ErrAt.IsZero() && now.Sub(c.cur.ErrAt) <= c.ttl {
		return false
	}
	return true
}

// land records the result of a fetch that began at began. A failed fetch never discards the
// previous items.
func (c *Cache) land(began time.Time, items []domain.Item, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.inflight = false
	c.stale = false
	switch {
	case err == nil:
		c.cur.Items, c.cur.FetchedAt = items, began
	case items == nil:
		// Nothing fetched at all: the previous snapshot stands as it was.
	case c.cur.FetchedAt.IsZero():
		// A partial first fetch has no older list to fill in from.
		c.cur.Items, c.cur.FetchedAt = items, began
	default:
		// A partial fetch: fill in the repositories that failed, and leave FetchedAt alone.
		c.cur.Items = mergePartial(c.cur.Items, items)
	}
	if err != nil {
		c.cur.Err = err
		c.cur.ErrAt = began
	} else {
		c.cur.Err = nil
		c.cur.ErrAt = time.Time{}
	}
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

// Invalidate makes the next Get fetch regardless of age. Called while a fetch is in flight it
// changes nothing: that fetch is the freshest list there can be, and its landing clears the mark.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	c.stale = true
	c.mu.Unlock()
}
