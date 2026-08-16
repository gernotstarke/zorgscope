package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

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
func NewSnapshotter(items ports.ItemStore, snaps ports.SnapshotStore, status ports.StatusStore, sources func() []string,
	hour, minute int, loc *time.Location, retentionDays int, clock ports.Clock, log *slog.Logger) *Snapshotter {
	return &Snapshotter{items: items, snaps: snaps, status: status, sources: sources, hour: hour, min: minute,
		loc: loc, retention: retentionDays, clock: clock, log: log}
}

// RunDue takes snapshots for every source that has none for the current snapshot day and prunes old
// ones. Sources without a successful fetch are skipped (an empty snapshot would be noise, not truth).
func (s *Snapshotter) RunDue(ctx context.Context) (int, error) {
	now := s.clock.Now()
	day := domain.SnapshotDay(now, s.hour, s.min, s.loc)
	taken := 0
	for _, src := range s.sources() {
		latest, err := s.snaps.LatestSnapshot(ctx, src)
		if err != nil {
			return taken, err
		}
		if latest != nil && latest.Date >= day {
			continue
		}
		st, err := s.status.Status(ctx, src)
		if err != nil {
			return taken, err
		}
		if st == nil || st.LastSuccess.IsZero() {
			continue
		}
		ids, err := s.items.ExternalIDs(ctx, src)
		if err != nil {
			return taken, err
		}
		if err := s.snaps.PutSnapshot(ctx, domain.NewSnapshot(src, day, now, ids)); err != nil {
			return taken, err
		}
		s.log.Info("snapshot taken", "source", src, "date", day, "ids", len(ids))
		taken++
	}
	dayT, err := time.ParseInLocation("2006-01-02", day, s.loc)
	if err != nil {
		return taken, err
	}
	cutoff := dayT.AddDate(0, 0, -s.retention).Format("2006-01-02")
	return taken, s.snaps.PruneSnapshots(ctx, cutoff)
}

// Run calls RunDue immediately and then every `every` until ctx is cancelled.
func (s *Snapshotter) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		if _, err := s.RunDue(ctx); err != nil {
			s.log.Error("snapshot run failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
