package ports_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

func TestFakeFetcherName(t *testing.T) {
	f := &ports.FakeFetcher{SourceName: "github"}
	if got := f.Name(); got != "github" {
		t.Errorf("Name() = %q, want %q", got, "github")
	}
}

func TestFakeFetcherFetchReturnsResultAndCountsCalls(t *testing.T) {
	want := ports.FetchResult{Items: []domain.Item{{ExternalID: "1"}}}
	f := &ports.FakeFetcher{Result: want}

	got, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v, want nil", err)
	}
	if len(got.Items) != 1 || got.Items[0].ExternalID != "1" {
		t.Errorf("Fetch() = %+v, want %+v", got, want)
	}
	if f.CallCount() != 1 {
		t.Errorf("CallCount() = %d, want 1", f.CallCount())
	}

	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("second Fetch() error = %v, want nil", err)
	}
	if f.CallCount() != 2 {
		t.Errorf("CallCount() after second call = %d, want 2", f.CallCount())
	}
}

func TestFakeFetcherFetchReturnsConfiguredError(t *testing.T) {
	wantErr := errors.New("boom")
	f := &ports.FakeFetcher{Err: wantErr}

	_, err := f.Fetch(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Fetch() error = %v, want %v", err, wantErr)
	}
}

func TestFakeFetcherBlocksUntilBlockCloses(t *testing.T) {
	f := &ports.FakeFetcher{Block: make(chan struct{})}

	done := make(chan struct{})
	go func() {
		_, _ = f.Fetch(context.Background())
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("Fetch() returned before Block was closed")
	case <-time.After(50 * time.Millisecond):
	}

	close(f.Block)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Fetch() did not return after Block was closed")
	}
}

func TestFakeFetcherFetchRespectsContextCancellationWhileBlocked(t *testing.T) {
	f := &ports.FakeFetcher{Block: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())

	type result struct {
		res ports.FetchResult
		err error
	}
	resultCh := make(chan result, 1)
	go func() {
		res, err := f.Fetch(ctx)
		resultCh <- result{res, err}
	}()

	cancel()

	select {
	case r := <-resultCh:
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("Fetch() error = %v, want %v", r.err, context.Canceled)
		}
		if len(r.res.Items) != 0 || len(r.res.Builds) != 0 {
			t.Errorf("Fetch() result = %+v, want zero value", r.res)
		}
	case <-time.After(time.Second):
		t.Fatal("Fetch() did not return promptly after context cancellation")
	}
}

func TestFakeFetcherFetchRefusesAlreadyCancelledContextWithoutBlock(t *testing.T) {
	f := &ports.FakeFetcher{Result: ports.FetchResult{Items: []domain.Item{{ExternalID: "1"}}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	type result struct {
		res ports.FetchResult
		err error
	}
	resultCh := make(chan result, 1)
	go func() {
		res, err := f.Fetch(ctx)
		resultCh <- result{res, err}
	}()

	select {
	case r := <-resultCh:
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("Fetch() error = %v, want %v", r.err, context.Canceled)
		}
		if len(r.res.Items) != 0 || len(r.res.Builds) != 0 {
			t.Errorf("Fetch() result = %+v, want zero value", r.res)
		}
	case <-time.After(time.Second):
		t.Fatal("Fetch() did not return promptly for an already-cancelled context")
	}

	if f.CallCount() != 0 {
		t.Errorf("CallCount() = %d, want 0: a rejected fetch must not count as a call", f.CallCount())
	}
}

func TestFixedClockAdvance(t *testing.T) {
	start := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	c := &ports.FixedClock{T: start}

	if got := c.Now(); !got.Equal(start) {
		t.Fatalf("Now() = %v, want %v", got, start)
	}

	c.Advance(time.Hour)

	want := start.Add(time.Hour)
	if got := c.Now(); !got.Equal(want) {
		t.Errorf("Now() after Advance = %v, want %v", got, want)
	}
}
