package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// componentSnapshotter names this component in structured logs (arc42 §8.8).
const componentSnapshotter = "snapshotter"

// Snapshotter records one id snapshot per source and snapshot day (FR-7.1, ADR-0008).
type Snapshotter struct {
	items     ports.ItemStore
	snaps     ports.SnapshotStore
	status    ports.StatusStore
	sources   func() []string
	hour, min int
	loc       *time.Location
	retention int
	clock     ports.Clock
	log       *slog.Logger
}

// NewSnapshotter wires a snapshotter. sources returns the ids to snapshot (usually Scheduler.SourceIDs).
// hour and minute are the configured daily snapshot time in loc; retentionDays is how many days of
// snapshots to keep.
func NewSnapshotter(items ports.ItemStore, snaps ports.SnapshotStore, status ports.StatusStore, sources func() []string,
	hour, minute int, loc *time.Location, retentionDays int, clock ports.Clock, log *slog.Logger) *Snapshotter {
	return &Snapshotter{items: items, snaps: snaps, status: status, sources: sources, hour: hour, min: minute,
		loc: loc, retention: retentionDays, clock: clock, log: log}
}

// RunDue takes snapshots for every source that has none for the current snapshot day and prunes
// snapshots older than the retention window. Sources without a successful fetch are skipped (an
// empty snapshot would be noise, not truth). A source with a snapshot for a day at or after the
// current snapshot day is left alone, which is what makes catch-up bounded: no matter how long the
// process was down, at most one snapshot is taken per source per call, dated the current snapshot
// day. A store error for one source is logged and does not stop the remaining sources; all such
// errors are joined and returned so the caller (Run) can still surface the failure.
func (s *Snapshotter) RunDue(ctx context.Context) (int, error) {
	now := s.clock.Now()
	day := domain.SnapshotDay(now, s.hour, s.min, s.loc)
	taken := 0
	var errs []error
	for _, src := range s.sources() {
		if err := s.snapshotOne(ctx, src, day, now); err != nil {
			if err != errSkip {
				errs = append(errs, err)
				s.log.Error("snapshot failed", "component", componentSnapshotter, "source_id", src, "err", err, "status", "error")
			}
			continue
		}
		taken++
	}
	if err := s.prune(ctx, day); err != nil {
		errs = append(errs, err)
	}
	return taken, errors.Join(errs...)
}

// errSkip is a sentinel used internally by snapshotOne to signal "nothing to do", as opposed to a
// real error. It is never returned from RunDue.
var errSkip = errors.New("snapshot skipped")

// snapshotOne takes a snapshot for one source if it is due, or returns errSkip if the source
// already has a snapshot for day or has no successful fetch yet.
func (s *Snapshotter) snapshotOne(ctx context.Context, src, day string, now time.Time) error {
	latest, err := s.snaps.LatestSnapshot(ctx, src)
	if err != nil {
		return err
	}
	if latest != nil && latest.Date >= day {
		return errSkip
	}
	st, err := s.status.Status(ctx, src)
	if err != nil {
		return err
	}
	if st == nil || st.LastSuccess.IsZero() {
		return errSkip
	}
	ids, err := s.items.ExternalIDs(ctx, src)
	if err != nil {
		return err
	}
	if err := s.snaps.PutSnapshot(ctx, domain.NewSnapshot(src, day, now, ids)); err != nil {
		return err
	}
	s.log.Info("snapshot taken", "component", componentSnapshotter, "source_id", src, "date", day, "item_count", len(ids), "status", "ok")
	return nil
}

// prune deletes snapshots older than the retention window, computing the cutoff by subtracting
// retention days from the civil date of day in loc (domain.SnapshotDay's calendar, DST-safe)
// rather than a 24h × N duration, which would drift across DST transitions.
func (s *Snapshotter) prune(ctx context.Context, day string) error {
	dayT, err := time.ParseInLocation("2006-01-02", day, s.loc)
	if err != nil {
		return err
	}
	cutoff := domain.DateOf(dayT.AddDate(0, 0, -s.retention), s.loc)
	if err := s.snaps.PruneSnapshots(ctx, cutoff); err != nil {
		s.log.Error("snapshot prune failed", "component", componentSnapshotter, "err", err, "status", "error")
		return err
	}
	return nil
}

// Run calls RunDue immediately and then every `every` until ctx is cancelled. It runs entirely in
// the calling goroutine and spawns none of its own, so cancelling ctx leaves nothing behind.
func (s *Snapshotter) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		if _, err := s.RunDue(ctx); err != nil {
			s.log.Error("snapshotter run failed", "component", componentSnapshotter, "err", err, "status", "error")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
