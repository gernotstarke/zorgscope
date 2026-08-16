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
	// statusWriteTimeout bounds the terminal status write so a detached write can never hang
	// shutdown indefinitely (it no longer inherits the fetch ctx's cancellation, see fetch).
	statusWriteTimeout = 5 * time.Second
)

// componentScheduler is the "component" field value on every log line this file emits (arc42 §8.8).
const componentScheduler = "scheduler"

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

// Add registers a source with its poll interval. Sources added after Run has started are
// registered and reachable via SourceIDs and FetchNow, but Run snapshots the source set at entry,
// so a late addition gets no polling loop until the next Run.
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
	prev, readErr := s.status.Status(ctx, id)
	if readErr != nil {
		s.log.Warn("status read failed", "component", componentScheduler, "source_id", id, "status", "read_failed", "err", readErr)
	} else if prev != nil {
		st = *prev
	}
	st.InFlight = true
	if writeErr := s.status.RecordStatus(ctx, st); writeErr != nil {
		s.log.Warn("status write failed", "component", componentScheduler, "source_id", id, "status", "write_failed", "err", writeErr)
	}

	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	items, err := sc.fetcher.Fetch(fctx)
	cancel()
	if err == nil {
		err = s.items.ReplaceItems(ctx, id, items, s.clock.Now())
	}
	done := s.clock.Now()
	st.InFlight = false
	st.Duration = done.Sub(start)
	durationMs := st.Duration.Milliseconds()

	s.mu.Lock()
	if err == nil {
		st.LastSuccess, st.ItemCount, st.ErrorMsg, st.AuthFailed = done, len(items), "", false
		sc.backoff = 0
		st.NextRun = done.Add(sc.interval)
		s.log.Info("fetch complete", "component", componentScheduler, "source_id", id, "status", "ok", "items", len(items), "duration_ms", durationMs)
	} else {
		st.LastError, st.ErrorMsg = done, err.Error()
		st.AuthFailed = errors.Is(err, ports.ErrAuth)
		if rl, ok := ports.AsRateLimited(err); ok && rl.ResetAt.After(done) {
			st.NextRun = rl.ResetAt.Add(time.Minute)
		} else {
			// Deliberate M1 choice (review ruling): ErrPermanent gets the same capped
			// exponential backoff as ErrTransient rather than stopping retries forever. An
			// adapter's "permanent" classification, derived from an upstream status code, can
			// be wrong (a 404 for a repo that gets un-archived, a 403 whose scope later gets
			// fixed) — for a personal dashboard, retrying every 30 minutes forever beats a
			// source that silently stops polling with no way back. Do not "fix" this into a
			// permanent stop without revisiting that call.
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
		s.log.Warn("fetch failed", "component", componentScheduler, "source_id", id, "status", "error", "err", err, "next_run", st.NextRun, "duration_ms", durationMs)
	}
	sc.lastRun, sc.nextRun = done, st.NextRun
	s.mu.Unlock()

	// The terminal write must survive cancellation of ctx: Run(ctx) is cancelled during graceful
	// shutdown while a fetch is in flight, and a manual-refresh caller's request context can
	// expire during the up-to-60s fetch window (Task 14). Either way, the write that clears
	// InFlight and records the outcome must still land, or the dashboard would show a source
	// stuck "refreshing…" until the next successful poll overwrites it — so it runs on a context
	// detached from ctx's cancellation, bounded by its own short timeout.
	wctx, wcancel := context.WithTimeout(context.WithoutCancel(ctx), statusWriteTimeout)
	if writeErr := s.status.RecordStatus(wctx, st); writeErr != nil {
		s.log.Warn("status write failed", "component", componentScheduler, "source_id", id, "status", "write_failed", "err", writeErr)
	}
	wcancel()

	return err
}
