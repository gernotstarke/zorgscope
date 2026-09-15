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

func (s *countingSource) Fetch(ctx context.Context) ([]domain.Item, error) {
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

func one(title string) []domain.Item { return []domain.Item{{Repo: "a/b", Number: 1, Title: title}} }

func TestFirstGetFetchesAndSecondWithinTTLDoesNot(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x")}
	c := snapshot.New(src, 5*time.Minute, clock)

	s1 := c.Get(context.Background())
	s2 := c.Get(context.Background())
	if src.count() != 1 {
		t.Fatalf("fetches = %d, want 1", src.count())
	}
	if len(s1.Items) != 1 || s1.FetchedAt != clock.t || s1.Err != nil {
		t.Fatalf("first snapshot = %+v", s1)
	}
	if s2.FetchedAt != s1.FetchedAt {
		t.Fatalf("second Get refetched: %v != %v", s2.FetchedAt, s1.FetchedAt)
	}
}

func TestGetRefetchesOnceTheSnapshotIsOlderThanTTL(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x")}
	c := snapshot.New(src, 5*time.Minute, clock)
	c.Get(context.Background())

	clock.t = clock.t.Add(5*time.Minute + time.Second)
	src.items = one("y")
	s := c.Get(context.Background())
	if src.count() != 2 || s.Items[0].Title != "y" || s.FetchedAt != clock.t {
		t.Fatalf("after ttl: fetches=%d snapshot=%+v", src.count(), s)
	}
}

func TestAFailedFetchKeepsTheOldItemsAndReportsTheError(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x")}
	c := snapshot.New(src, time.Minute, clock)
	good := c.Get(context.Background())

	clock.t = clock.t.Add(2 * time.Minute)
	src.err = errors.New("boom")
	src.items = nil
	s := c.Get(context.Background())
	if s.Err == nil || s.ErrAt != clock.t {
		t.Fatalf("error not reported: %+v", s)
	}
	if len(s.Items) != 1 || s.FetchedAt != good.FetchedAt {
		t.Fatalf("old items not kept: %+v", s)
	}
}

func TestAFailedFirstFetchYieldsAnEmptyListWithTheError(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{err: errors.New("boom")}
	c := snapshot.New(src, time.Minute, clock)
	s := c.Get(context.Background())
	if s.Err == nil || len(s.Items) != 0 || !s.FetchedAt.IsZero() {
		t.Fatalf("got %+v", s)
	}
}

func TestPartialResultIsKeptTogetherWithItsError(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("partial"), err: errors.New("one repo failed")}
	c := snapshot.New(src, time.Minute, clock)
	s := c.Get(context.Background())
	if s.Err == nil || len(s.Items) != 1 || s.FetchedAt != clock.t {
		t.Fatalf("partial result mishandled: %+v", s)
	}
}

func TestInvalidateForcesTheNextGetToFetch(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x")}
	c := snapshot.New(src, time.Hour, clock)
	c.Get(context.Background())
	c.Invalidate()
	c.Get(context.Background())
	if src.count() != 2 {
		t.Fatalf("fetches = %d, want 2", src.count())
	}
}

func TestConcurrentGetsShareOneFetch(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	src := &countingSource{items: one("x"), block: make(chan struct{})}
	c := snapshot.New(src, time.Hour, clock)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.Get(context.Background()) }()
	}
	time.Sleep(50 * time.Millisecond) // let every goroutine reach the cache
	close(src.block)
	wg.Wait()
	if src.count() != 1 {
		t.Fatalf("fetches = %d, want 1", src.count())
	}
}

var _ ports.Source = (*countingSource)(nil)
