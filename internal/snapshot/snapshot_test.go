package snapshot_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/snapshot"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

// countingSource counts calls and answers with whatever items/err are set at call time.
type countingSource struct {
	mu    sync.Mutex
	calls int
	items []domain.Item
	err   error
	block chan struct{} // when non-nil, Fetch waits on it before returning
}

func (s *countingSource) Fetch(_ context.Context) ([]domain.Item, error) {
	s.mu.Lock()
	s.calls++
	items, err, block := s.items, s.err, s.block
	s.mu.Unlock()
	if block != nil {
		<-block
	}
	return items, err
}

func (s *countingSource) count() int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls }

func (s *countingSource) set(items []domain.Item, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items, s.err = items, err
}

func one(title string) []domain.Item { return []domain.Item{{Repo: "a/b", Number: 1, Title: title}} }

// settled calls Get until no fetch is in flight and returns what landed. Get returns at once, so a
// test that wants the result of the fetch it just triggered waits here; the fakes answer in
// microseconds, so the loop is a formality with a deadline for when something is actually wrong.
func settled(t *testing.T, c *snapshot.Cache) snapshot.Snapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s := c.Get(context.Background())
		if !s.Fetching {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatal("the fetch never landed")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestFirstGetFetchesAndSecondWithinTTLDoesNot(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x")}
	c := snapshot.New(src, 5*time.Minute, clock)

	s1 := settled(t, c)
	s2 := c.Get(context.Background())
	if src.count() != 1 {
		t.Fatalf("fetches = %d, want 1", src.count())
	}
	if len(s1.Items) != 1 || s1.FetchedAt != clock.t || s1.Err != nil {
		t.Fatalf("first snapshot = %+v", s1)
	}
	if s2.FetchedAt != s1.FetchedAt || s2.Fetching {
		t.Fatalf("second Get refetched: %+v", s2)
	}
}

// QS-2.6: Get never waits for the source. The first Get of an empty cache returns before the
// source has answered, says a fetch is in flight, and carries nothing yet.
func TestFirstGetReturnsAtOnceWithNothingWhileFetching(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x"), block: make(chan struct{})}
	c := snapshot.New(src, time.Hour, clock)

	s := c.Get(context.Background()) // would hang here if Get waited for the source
	if !s.Fetching || len(s.Items) != 0 || !s.FetchedAt.IsZero() {
		t.Fatalf("got %+v, want an empty snapshot with Fetching set", s)
	}
	close(src.block)
	if got := settled(t, c); len(got.Items) != 1 || got.FetchedAt != clock.t {
		t.Fatalf("after the fetch landed: %+v", got)
	}
}

// While a refetch is in flight the previous list is what Get returns — marked Fetching, so the page
// can decide to wait rather than show it (design §5.1).
func TestGetDuringARefetchReturnsThePreviousListMarkedFetching(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x")}
	c := snapshot.New(src, time.Minute, clock)
	first := settled(t, c)

	clock.t = clock.t.Add(2 * time.Minute)
	block := make(chan struct{})
	src.mu.Lock()
	src.items, src.block = one("y"), block
	src.mu.Unlock()

	s := c.Get(context.Background())
	if !s.Fetching || len(s.Items) != 1 || s.Items[0].Title != "x" || s.FetchedAt != first.FetchedAt {
		t.Fatalf("during the refetch: %+v, want the previous list marked Fetching", s)
	}
	close(block)
	if got := settled(t, c); got.Items[0].Title != "y" || got.FetchedAt != clock.t {
		t.Fatalf("after the refetch: %+v", got)
	}
}

func TestGetRefetchesOnceTheSnapshotIsOlderThanTTL(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x")}
	c := snapshot.New(src, 5*time.Minute, clock)
	settled(t, c)

	clock.t = clock.t.Add(5*time.Minute + time.Second)
	src.set(one("y"), nil)
	s := settled(t, c)
	if src.count() != 2 || s.Items[0].Title != "y" || s.FetchedAt != clock.t {
		t.Fatalf("after ttl: fetches=%d snapshot=%+v", src.count(), s)
	}
}

func TestAFailedFetchKeepsTheOldItemsAndReportsTheError(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x")}
	c := snapshot.New(src, time.Minute, clock)
	good := settled(t, c)

	clock.t = clock.t.Add(2 * time.Minute)
	src.set(nil, errors.New("boom"))
	s := settled(t, c)
	if s.Err == nil || s.ErrAt != clock.t {
		t.Fatalf("error not reported: %+v", s)
	}
	if len(s.Items) != 1 || s.FetchedAt != good.FetchedAt {
		t.Fatalf("old items not kept: %+v", s)
	}
	// A failure is not retried within the TTL: the next Get serves the failure, not a fetch.
	if again := c.Get(context.Background()); again.Fetching || src.count() != 2 {
		t.Fatalf("a failure was retried at once: fetches=%d %+v", src.count(), again)
	}
}

func TestAFailedFirstFetchYieldsAnEmptyListWithTheError(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{err: errors.New("boom")}
	c := snapshot.New(src, time.Minute, clock)
	s := settled(t, c)
	if s.Err == nil || len(s.Items) != 0 || !s.FetchedAt.IsZero() {
		t.Fatalf("got %+v", s)
	}
}

func TestPartialResultIsKeptTogetherWithItsError(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("partial"), err: errors.New("one repo failed")}
	c := snapshot.New(src, time.Minute, clock)
	s := settled(t, c)
	if s.Err == nil || len(s.Items) != 1 || s.FetchedAt != clock.t {
		t.Fatalf("partial result mishandled: %+v", s)
	}
}

func TestInvalidateForcesTheNextGetToFetch(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x")}
	c := snapshot.New(src, time.Hour, clock)
	settled(t, c)
	c.Invalidate()
	settled(t, c)
	if src.count() != 2 {
		t.Fatalf("fetches = %d, want 2", src.count())
	}
}

// Refresh pressed while a fetch is already running changes nothing: that fetch is the freshest
// list there can be, and its landing clears the mark Invalidate set (design §4).
func TestInvalidateDuringAFetchDoesNotStartAnother(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x"), block: make(chan struct{})}
	c := snapshot.New(src, time.Hour, clock)

	c.Get(context.Background()) // starts the fetch
	c.Invalidate()
	if s := c.Get(context.Background()); !s.Fetching {
		t.Fatalf("got %+v, want the running fetch reported", s)
	}
	close(src.block)
	settled(t, c)
	c.Get(context.Background())
	if src.count() != 1 {
		t.Fatalf("fetches = %d, want 1: Invalidate during a fetch must not queue a second one", src.count())
	}
}

func TestManyCallersShareOneFetch(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x"), block: make(chan struct{})}
	c := snapshot.New(src, time.Hour, clock)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s := c.Get(context.Background()); !s.Fetching {
				t.Errorf("a caller during the fetch got %+v, want Fetching", s)
			}
		}()
	}
	wg.Wait() // every caller returned while the source is still blocked
	close(src.block)
	settled(t, c)
	if src.count() != 1 {
		t.Fatalf("fetches = %d, want 1", src.count())
	}
}

// ctxSource starts a fetch, then waits for either its release channel or its context. It checks
// its context once more after being released, so a fetch whose context was cancelled while it
// waited fails deterministically rather than on whichever select case the runtime happens to pick.
type ctxSource struct {
	started, release chan struct{}
	items            []domain.Item
}

func (s *ctxSource) Fetch(ctx context.Context) ([]domain.Item, error) {
	close(s.started)
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.items, nil
}

// The fetch is detached from the request that started it: the visitor who happened to trigger it
// must not be able to cancel it by going away, or the error would be served to everyone for a TTL.
func TestACallerThatGoesAwayDoesNotCancelTheFetch(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &ctxSource{started: make(chan struct{}), release: make(chan struct{}), items: one("x")}
	c := snapshot.New(src, time.Minute, clock)

	ctx, cancel := context.WithCancel(context.Background())
	c.Get(ctx)
	<-src.started
	cancel() // the visitor closes the tab mid-fetch
	close(src.release)

	s := settled(t, c)
	if s.Err != nil || len(s.Items) != 1 || s.FetchedAt != clock.t {
		t.Fatalf("a cancelled caller poisoned the fetch: %+v", s)
	}
}

// FR-1.4 AC1 and AC4: when one repository fails and another does not, the page keeps the failing
// repository's previous items, and FetchedAt stays at the last fetch whose items are all current —
// it is what "Mark all seen" acknowledges, and the failing repository's list is not current.
func TestAPartialFetchKeepsTheFailingRepositorysItemsAndTheOldFetchedAt(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: []domain.Item{
		{Repo: "a/b", Number: 1, Title: "a/b before"},
		{Repo: "c/d", Number: 2, Title: "c/d two"},
		{Repo: "c/d", Number: 3, Title: "c/d three"},
	}}
	c := snapshot.New(src, time.Minute, clock)
	good := settled(t, c)

	clock.t = clock.t.Add(2 * time.Minute)
	src.set([]domain.Item{{Repo: "a/b", Number: 1, Title: "a/b after"}}, errors.New("c/d: unexpected status 502"))
	s := settled(t, c)

	if s.Err == nil || s.ErrAt != clock.t {
		t.Errorf("error not reported: %+v", s)
	}
	if s.FetchedAt != good.FetchedAt {
		t.Errorf("FetchedAt = %v, want the good fetch's %v: the c/d list is not current", s.FetchedAt, good.FetchedAt)
	}
	got := map[string]bool{}
	for _, it := range s.Items {
		got[it.Title] = true
	}
	want := map[string]bool{"a/b after": true, "c/d two": true, "c/d three": true}
	if len(s.Items) != len(want) {
		t.Errorf("items = %+v, want exactly %v", s.Items, want)
	}
	for title := range want {
		if !got[title] {
			t.Errorf("items lack %q: %+v", title, s.Items)
		}
	}
	// The previous slice is shared with renders that may still be running; it must be untouched.
	if good.Items[0].Title != "a/b before" || len(good.Items) != 3 {
		t.Errorf("the previous snapshot's slice was mutated: %+v", good.Items)
	}
}

var _ ports.Source = (*countingSource)(nil)
