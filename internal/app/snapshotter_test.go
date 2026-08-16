package app

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/clock"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports/memstore"
)

// TestSnapshotterRunShutsDownOnCancel verifies that Run returns promptly once ctx is cancelled and
// does not leak a goroutine (it must not spawn one of its own).
func TestSnapshotterRunShutsDownOnCancel(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	st := memstore.New()
	clk := clock.NewFake(time.Date(2026, 8, 16, 8, 0, 0, 0, berlin))
	sn := NewSnapshotter(st, st, st, func() []string { return nil }, 3, 0, berlin, 30, clk, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sn.Run(ctx, time.Hour)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after ctx cancellation")
	}
}

func TestSnapshotterTakesOnePerSnapshotDay(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	ctx := context.Background()
	st := memstore.New()
	clk := clock.NewFake(time.Date(2026, 8, 16, 8, 0, 0, 0, berlin)) // 08:00 Berlin, after the 03:00 snapshot time
	_ = st.ReplaceItems(ctx, "s1", []domain.Item{{ID: domain.ItemID{SourceID: "s1", ExternalID: "a"}}}, clk.Now())
	_ = st.RecordStatus(ctx, domain.FetchStatus{SourceID: "s1", LastSuccess: clk.Now()})
	_ = st.ReplaceItems(ctx, "never", nil, clk.Now()) // no successful fetch → skipped
	sn := NewSnapshotter(st, st, st, func() []string { return []string{"s1", "never"} }, 3, 0, berlin, 30, clk, slog.Default())

	n, err := sn.RunDue(ctx)
	if err != nil || n != 1 {
		t.Fatalf("first run: %d %v", n, err)
	}
	latest, _ := st.LatestSnapshot(ctx, "s1")
	if latest == nil || latest.Date != "2026-08-16" || !latest.Contains("a") {
		t.Fatalf("latest = %+v", latest)
	}
	if got, _ := st.LatestSnapshot(ctx, "never"); got != nil {
		t.Fatal("sources without a successful fetch must not be snapshotted")
	}
	if n, _ := sn.RunDue(ctx); n != 0 {
		t.Fatal("second run same day must be a no-op")
	}
	clk.Set(time.Date(2026, 8, 17, 2, 30, 0, 0, berlin)) // before 03:00 → still snapshot day 2026-08-16
	if n, _ := sn.RunDue(ctx); n != 0 {
		t.Fatal("before snapshot time nothing is due")
	}
	clk.Set(time.Date(2026, 8, 17, 3, 0, 0, 0, berlin))
	if n, _ := sn.RunDue(ctx); n != 1 {
		t.Fatal("at snapshot time a new one is due")
	}
	// catch-up: app was down for three days → exactly one snapshot for the current snapshot day
	clk.Set(time.Date(2026, 8, 20, 12, 0, 0, 0, berlin))
	if n, _ := sn.RunDue(ctx); n != 1 {
		t.Fatal("catch-up takes one snapshot")
	}
	latest, _ = st.LatestSnapshot(ctx, "s1")
	if latest.Date != "2026-08-20" {
		t.Fatalf("catch-up date = %s", latest.Date)
	}
	if before, _ := st.SnapshotBefore(ctx, "s1", "2026-08-20"); before == nil || before.Date != "2026-08-17" {
		t.Fatalf("previous snapshot must be the last real one: %+v", before)
	}
}

func TestSnapshotterPrunes(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	ctx := context.Background()
	st := memstore.New()
	clk := clock.NewFake(time.Date(2026, 8, 16, 12, 0, 0, 0, berlin))
	_ = st.RecordStatus(ctx, domain.FetchStatus{SourceID: "s1", LastSuccess: clk.Now()})
	_ = st.PutSnapshot(ctx, domain.NewSnapshot("s1", "2026-07-01", clk.Now(), nil)) // 46 days old
	_ = st.PutSnapshot(ctx, domain.NewSnapshot("s1", "2026-08-01", clk.Now(), nil)) // 15 days old
	sn := NewSnapshotter(st, st, st, func() []string { return []string{"s1"} }, 3, 0, berlin, 30, clk, slog.Default())
	if _, err := sn.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.SnapshotBefore(ctx, "s1", "2026-08-01"); got != nil {
		t.Fatal("snapshot older than retention must be pruned")
	}
	if got, _ := st.SnapshotBefore(ctx, "s1", "2026-08-16"); got == nil || got.Date != "2026-08-01" {
		t.Fatal("snapshot within retention must survive")
	}
}
