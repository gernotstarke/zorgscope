// Package app contains zorgscope's use cases: scheduling fetches, taking snapshots, building the
// dashboard view, dismissing items (arc42 §5).
package app

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// Errors returned by the scheduler.
var (
	ErrInFlight      = errors.New("fetch already in flight")
	ErrUnknownSource = errors.New("unknown source")
)

const (
	fetchTimeout   = 60 * time.Second
	initialBackoff = time.Minute
	maxBackoff     = 30 * time.Minute
	staggerStep    = 500 * time.Millisecond
)

type scheduled struct {
	fetcher  ports.SourceFetcher
	interval time.Duration
	running  bool
	backoff  time.Duration
	lastRun  time.Time
	nextRun  time.Time
	wake     chan struct{}
}

// Scheduler polls every registered source on its own interval with jitter, single-flight and
// exponential backoff (arc42 §6.1, ADR-0005).
type Scheduler struct {
	items  ports.ItemStore
	status ports.StatusStore
	clock  ports.Clock
	log    *slog.Logger
	minGap time.Duration

	mu      sync.Mutex
	sources []*scheduled
	byID    map[string]*scheduled
}

// NewScheduler creates a scheduler; minGap is the minimum time between two fetches of one source
// triggered by manual refresh (FR-1.4).
func NewScheduler(items ports.ItemStore, status ports.StatusStore, clock ports.Clock, log *slog.Logger, minGap time.Duration) *Scheduler {
	return &Scheduler{items: items, status: status, clock: clock, log: log, minGap: minGap, byID: map[string]*scheduled{}}
}

// Add registers a source with its poll interval.
func (s *Scheduler) Add(f ports.SourceFetcher, interval time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := &scheduled{fetcher: f, interval: interval, wake: make(chan struct{}, 1)}
	s.sources = append(s.sources, sc)
	s.byID[f.ID()] = sc
}

// SourceIDs lists registered sources in registration order.
func (s *Scheduler) SourceIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.sources))
	for _, sc := range s.sources {
		out = append(out, sc.fetcher.ID())
	}
	return out
}

// InFlight returns the number of sources currently fetching.
func (s *Scheduler) InFlight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, sc := range s.sources {
		if sc.running {
			n++
		}
	}
	return n
}

// FetchNow fetches one source synchronously (used at startup, by tests and by wake-ups).
func (s *Scheduler) FetchNow(ctx context.Context, sourceID string) error {
	s.mu.Lock()
	sc, ok := s.byID[sourceID]
	s.mu.Unlock()
	if !ok {
		return ErrUnknownSource
	}
	return s.fetch(ctx, sc)
}

// TriggerAll wakes every idle source whose last run is older than minGap; returns how many.
func (s *Scheduler) TriggerAll() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	n := 0
	for _, sc := range s.sources {
		if sc.running || (!sc.lastRun.IsZero() && now.Sub(sc.lastRun) < s.minGap) {
			continue
		}
		select {
		case sc.wake <- struct{}{}:
			n++
		default: // already queued
		}
	}
	return n
}

// Run polls all sources until ctx is cancelled. The first fetch of each source is staggered.
func (s *Scheduler) Run(ctx context.Context) {
	s.mu.Lock()
	sources := append([]*scheduled(nil), s.sources...)
	s.mu.Unlock()
	var wg sync.WaitGroup
	for i, sc := range sources {
		wg.Add(1)
		go func(i int, sc *scheduled) {
			defer wg.Done()
			s.loop(ctx, sc, time.Duration(i)*staggerStep)
		}(i, sc)
	}
	wg.Wait()
}

func (s *Scheduler) loop(ctx context.Context, sc *scheduled, delay time.Duration) {
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-sc.wake:
			timer.Stop()
		}
		_ = s.fetch(ctx, sc) // errors are recorded in status
		delay = s.delayUntilNext(sc)
	}
}

func (s *Scheduler) delayUntilNext(sc *scheduled) time.Duration {
	s.mu.Lock()
	next := sc.nextRun
	s.mu.Unlock()
	d := next.Sub(s.clock.Now())
	if d < 10*time.Millisecond { // never busy-loop; real intervals are minutes, tests use tens of ms
		d = 10 * time.Millisecond
	}
	// ±5 % jitter avoids synchronised bursts across sources
	jitter := time.Duration(rand.Int63n(int64(d)/10+1)) - d/20
	return d + jitter
}

func (s *Scheduler) fetch(ctx context.Context, sc *scheduled) error {
	s.mu.Lock()
	if sc.running {
		s.mu.Unlock()
		return ErrInFlight
	}
	sc.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		sc.running = false
		s.mu.Unlock()
	}()

	id, kind := sc.fetcher.ID(), sc.fetcher.Kind()
	start := s.clock.Now()
	st := domain.FetchStatus{SourceID: id, Kind: kind}
	if prev, err := s.status.Status(ctx, id); err == nil && prev != nil {
		st = *prev
	}
	st.InFlight = true
	_ = s.status.RecordStatus(ctx, st)

	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	items, err := sc.fetcher.Fetch(fctx)
	cancel()
	if err == nil {
		err = s.items.ReplaceItems(ctx, id, items, s.clock.Now())
	}
	done := s.clock.Now()
	st.InFlight = false
	st.Duration = done.Sub(start)

	s.mu.Lock()
	if err == nil {
		st.LastSuccess, st.ItemCount, st.ErrorMsg, st.AuthFailed = done, len(items), "", false
		sc.backoff = 0
		st.NextRun = done.Add(sc.interval)
		s.log.Info("fetched", "source", id, "items", len(items), "duration", st.Duration)
	} else {
		st.LastError, st.ErrorMsg = done, err.Error()
		st.AuthFailed = errors.Is(err, ports.ErrAuth)
		if rl, ok := ports.AsRateLimited(err); ok && rl.ResetAt.After(done) {
			st.NextRun = rl.ResetAt.Add(time.Minute)
		} else {
			if sc.backoff == 0 {
				sc.backoff = initialBackoff
			} else {
				sc.backoff *= 2
			}
			if sc.backoff > maxBackoff {
				sc.backoff = maxBackoff
			}
			st.NextRun = done.Add(sc.backoff)
		}
		s.log.Warn("fetch failed", "source", id, "err", err, "next_run", st.NextRun)
	}
	sc.lastRun, sc.nextRun = done, st.NextRun
	s.mu.Unlock()

	_ = s.status.RecordStatus(ctx, st)
	return err
}
