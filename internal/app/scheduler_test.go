package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/clock"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/ports/memstore"
)

type fakeFetcher struct {
	id    string
	items []domain.Item
	err   error
	calls atomic.Int32
	block chan struct{} // if non-nil, Fetch waits until closed
}

func (f *fakeFetcher) ID() string   { return f.id }
func (f *fakeFetcher) Kind() string { return ports.KindGitHubRepo }
func (f *fakeFetcher) Fetch(ctx context.Context) ([]domain.Item, error) {
	f.calls.Add(1)
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.items, f.err
}

var t0 = time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

func newSched(t *testing.T) (*Scheduler, *memstore.Store, *clock.Fake) {
	t.Helper()
	st := memstore.New()
	clk := clock.NewFake(t0)
	return NewScheduler(st, st, clk, slog.Default(), 30*time.Second), st, clk
}

func TestFetchNowSuccessStoresItemsAndStatus(t *testing.T) {
	s, st, clk := newSched(t)
	f := &fakeFetcher{id: "src", items: []domain.Item{{ID: domain.ItemID{SourceID: "src", ExternalID: "a"}, Kind: domain.KindIssue, CreatedAt: t0}}}
	s.Add(f, 10*time.Minute)
	if err := s.FetchNow(context.Background(), "src"); err != nil {
		t.Fatal(err)
	}
	items, _ := st.Items(context.Background(), "src")
	if len(items) != 1 || !items[0].FirstSeen.Equal(clk.Now()) {
		t.Fatalf("items = %+v", items)
	}
	status, _ := st.Status(context.Background(), "src")
	if status == nil || !status.LastSuccess.Equal(t0) || status.ItemCount != 1 || status.InFlight || !status.NextRun.Equal(t0.Add(10*time.Minute)) || status.Kind != ports.KindGitHubRepo {
		t.Fatalf("status = %+v", status)
	}
	if err := s.FetchNow(context.Background(), "nope"); !errors.Is(err, ErrUnknownSource) {
		t.Fatalf("unknown source: %v", err)
	}
}

func TestFetchErrorsBackOffAndRecover(t *testing.T) {
	s, st, clk := newSched(t)
	f := &fakeFetcher{id: "src", err: fmt.Errorf("boom: %w", ports.ErrTransient)}
	s.Add(f, 10*time.Minute)
	ctx := context.Background()
	for i, want := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute} {
		if err := s.FetchNow(ctx, "src"); err == nil {
			t.Fatal("expected error")
		}
		status, _ := st.Status(ctx, "src")
		if got := status.NextRun.Sub(clk.Now()); got != want {
			t.Fatalf("attempt %d: backoff %v want %v", i, got, want)
		}
		if status.Healthy() || status.ErrorMsg == "" || status.AuthFailed {
			t.Fatalf("status after error: %+v", status)
		}
	}
	f.err = nil
	if err := s.FetchNow(ctx, "src"); err != nil {
		t.Fatal(err)
	}
	status, _ := st.Status(ctx, "src")
	if !status.Healthy() || status.NextRun.Sub(clk.Now()) != 10*time.Minute {
		t.Fatalf("recovery must reset backoff: %+v", status)
	}
	f.err = ports.ErrTransient
	_ = s.FetchNow(ctx, "src")
	status, _ = st.Status(ctx, "src")
	if status.NextRun.Sub(clk.Now()) != time.Minute {
		t.Fatal("backoff must restart at 1m after a success")
	}
}

func TestBackoffIsCapped(t *testing.T) {
	s, st, clk := newSched(t)
	f := &fakeFetcher{id: "src", err: ports.ErrTransient}
	s.Add(f, time.Minute)
	for i := 0; i < 10; i++ {
		_ = s.FetchNow(context.Background(), "src")
	}
	status, _ := st.Status(context.Background(), "src")
	if status.NextRun.Sub(clk.Now()) != 30*time.Minute {
		t.Fatalf("cap = %v", status.NextRun.Sub(clk.Now()))
	}
}

func TestAuthAndRateLimitErrors(t *testing.T) {
	s, st, clk := newSched(t)
	auth := &fakeFetcher{id: "auth", err: fmt.Errorf("401: %w", ports.ErrAuth)}
	rl := &fakeFetcher{id: "rl", err: &ports.RateLimitedError{ResetAt: t0.Add(17 * time.Minute)}}
	s.Add(auth, time.Minute)
	s.Add(rl, time.Minute)
	_ = s.FetchNow(context.Background(), "auth")
	_ = s.FetchNow(context.Background(), "rl")
	a, _ := st.Status(context.Background(), "auth")
	if !a.AuthFailed {
		t.Fatalf("auth failed flag: %+v", a)
	}
	r, _ := st.Status(context.Background(), "rl")
	if !r.NextRun.Equal(clk.Now().Add(18 * time.Minute)) { // reset + 1 min safety
		t.Fatalf("rate limit next run = %v", r.NextRun)
	}
}

func TestSingleFlight(t *testing.T) {
	s, _, _ := newSched(t)
	f := &fakeFetcher{id: "src", block: make(chan struct{})}
	s.Add(f, time.Minute)
	done := make(chan error, 1)
	go func() { done <- s.FetchNow(context.Background(), "src") }()
	for s.InFlight() == 0 {
		time.Sleep(time.Millisecond)
	}
	if err := s.FetchNow(context.Background(), "src"); !errors.Is(err, ErrInFlight) {
		t.Fatalf("second concurrent fetch: %v", err)
	}
	if n := s.TriggerAll(); n != 0 {
		t.Fatalf("TriggerAll must skip in-flight sources, got %d", n)
	}
	close(f.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("calls = %d", f.calls.Load())
	}
}

func TestTriggerAllRespectsMinGap(t *testing.T) {
	s, _, clk := newSched(t)
	s.Add(&fakeFetcher{id: "a"}, time.Minute)
	s.Add(&fakeFetcher{id: "b"}, time.Minute)
	if n := s.TriggerAll(); n != 2 {
		t.Fatalf("fresh sources: %d", n)
	}
	// drain wake signals so the next TriggerAll can enqueue again
	for _, sc := range s.sources {
		<-sc.wake
	}
	_ = s.FetchNow(context.Background(), "a")
	if n := s.TriggerAll(); n != 1 {
		t.Fatalf("a fetched just now → only b: %d", n)
	}
	for _, sc := range s.sources {
		select {
		case <-sc.wake:
		default:
		}
	}
	clk.Advance(31 * time.Second)
	if n := s.TriggerAll(); n != 2 {
		t.Fatalf("after min gap: %d", n)
	}
}

func TestRunLoopPollsRepeatedly(t *testing.T) {
	s, _, _ := newSched(t)
	f := &fakeFetcher{id: "src"}
	s.Add(f, 20*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	s.Run(ctx)
	if f.calls.Load() < 3 {
		t.Fatalf("expected ≥3 polls, got %d", f.calls.Load())
	}
}
