// Package storetest holds the behavioural contract test for ports.Store implementations.
package storetest

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

var t0 = time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

func item(src, ext string, created time.Time) domain.Item {
	return domain.Item{ID: domain.ItemID{SourceID: src, ExternalID: ext}, Kind: domain.KindIssue, Title: "T " + ext,
		URL: "https://x/" + ext, Author: "alice", CreatedAt: created, UpdatedAt: created, Labels: []string{"bug"},
		Payload: domain.MustPayload(domain.IssuePayload{Comments: 1})}
}

// Run executes the contract against a fresh store per subtest.
func Run(t *testing.T, open func(t *testing.T) ports.Store) {
	t.Helper()
	ctx := context.Background()

	t.Run("items replace keeps first_seen and removes absent", func(t *testing.T) {
		s := open(t)
		a := item("s1", "a", t0.Add(-time.Hour))
		b := item("s1", "b", t0)
		if err := s.ReplaceItems(ctx, "s1", []domain.Item{a, b}, t0); err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceItems(ctx, "s2", []domain.Item{item("s2", "z", t0)}, t0); err != nil {
			t.Fatal(err)
		}
		got, err := s.Items(ctx, "s1")
		if err != nil || len(got) != 2 {
			t.Fatalf("Items: %v %d", err, len(got))
		}
		if got[0].ID.ExternalID != "b" {
			t.Fatalf("newest first, got %s", got[0].ID.ExternalID)
		}
		if !got[0].FirstSeen.Equal(t0) || got[1].Title != "T a" || got[1].Labels[0] != "bug" || len(got[1].Payload) == 0 {
			t.Fatalf("round trip lost data: %+v", got[1])
		}
		// second replace: b updated, a gone, c new
		b2 := b
		b2.UpdatedAt = t0.Add(time.Minute)
		if err := s.ReplaceItems(ctx, "s1", []domain.Item{b2, item("s1", "c", t0)}, t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		got, _ = s.Items(ctx, "s1")
		if len(got) != 2 {
			t.Fatalf("want 2 items, got %d", len(got))
		}
		for _, it := range got {
			switch it.ID.ExternalID {
			case "b":
				if !it.FirstSeen.Equal(t0) || !it.UpdatedAt.Equal(t0.Add(time.Minute)) {
					t.Fatalf("b must keep first_seen and take new updated_at: %+v", it)
				}
			case "c":
				if !it.FirstSeen.Equal(t0.Add(time.Hour)) {
					t.Fatalf("c first_seen = %v", it.FirstSeen)
				}
			default:
				t.Fatalf("unexpected %s", it.ID.ExternalID)
			}
		}
		ids, _ := s.ExternalIDs(ctx, "s1")
		if !reflect.DeepEqual(ids, []string{"b", "c"}) {
			t.Fatalf("ExternalIDs = %v", ids)
		}
		all, _ := s.AllItems(ctx)
		if len(all) != 3 {
			t.Fatalf("AllItems = %d", len(all))
		}
		if err := s.ReplaceItems(ctx, "s2", nil, t0); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Items(ctx, "s2"); len(got) != 0 {
			t.Fatal("empty replace clears source")
		}
	})

	t.Run("snapshots", func(t *testing.T) {
		s := open(t)
		if got, err := s.LatestSnapshot(ctx, "s1"); err != nil || got != nil {
			t.Fatalf("no snapshot yet: %v %v", got, err)
		}
		for _, d := range []string{"2026-08-14", "2026-08-16", "2026-08-15"} {
			if err := s.PutSnapshot(ctx, domain.NewSnapshot("s1", d, t0, []string{"x-" + d})); err != nil {
				t.Fatal(err)
			}
		}
		_ = s.PutSnapshot(ctx, domain.NewSnapshot("s2", "2026-08-16", t0, []string{"other"}))
		latest, _ := s.LatestSnapshot(ctx, "s1")
		if latest == nil || latest.Date != "2026-08-16" || !latest.Contains("x-2026-08-16") {
			t.Fatalf("latest = %+v", latest)
		}
		before, _ := s.SnapshotBefore(ctx, "s1", "2026-08-16")
		if before == nil || before.Date != "2026-08-15" {
			t.Fatalf("before = %+v", before)
		}
		if got, _ := s.SnapshotBefore(ctx, "s1", "2026-08-14"); got != nil {
			t.Fatal("nothing before the first")
		}
		// upsert same date replaces ids
		_ = s.PutSnapshot(ctx, domain.NewSnapshot("s1", "2026-08-16", t0.Add(time.Hour), []string{"y"}))
		latest, _ = s.LatestSnapshot(ctx, "s1")
		if !latest.Contains("y") || latest.Contains("x-2026-08-16") {
			t.Fatal("upsert must replace ids")
		}
		if err := s.PruneSnapshots(ctx, "2026-08-16"); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.SnapshotBefore(ctx, "s1", "2026-08-16"); got != nil {
			t.Fatal("pruned snapshots must be gone")
		}
		if got, _ := s.LatestSnapshot(ctx, "s2"); got == nil {
			t.Fatal("prune must not touch dates >= cutoff")
		}
	})

	t.Run("dismissals", func(t *testing.T) {
		s := open(t)
		id := domain.ItemID{SourceID: "s1", ExternalID: "issues/1"}
		if got, err := s.Dismissal(ctx, id); err != nil || got != nil {
			t.Fatalf("none yet: %v %v", got, err)
		}
		d := domain.Dismissal{ID: id, UpdatedAt: t0, DismissedAt: t0.Add(time.Minute)}
		if err := s.PutDismissal(ctx, d); err != nil {
			t.Fatal(err)
		}
		got, _ := s.Dismissal(ctx, id)
		if got == nil || !got.UpdatedAt.Equal(t0) {
			t.Fatalf("got %+v", got)
		}
		d.UpdatedAt = t0.Add(time.Hour)
		_ = s.PutDismissal(ctx, d) // upsert
		all, _ := s.Dismissals(ctx)
		if len(all) != 1 || !all[id].UpdatedAt.Equal(t0.Add(time.Hour)) {
			t.Fatalf("Dismissals = %+v", all)
		}
	})

	t.Run("statuses", func(t *testing.T) {
		s := open(t)
		if got, err := s.Status(ctx, "s1"); err != nil || got != nil {
			t.Fatalf("none yet: %v %v", got, err)
		}
		st := domain.FetchStatus{SourceID: "s1", Kind: "github-repo", LastSuccess: t0, ItemCount: 3, Duration: 2 * time.Second, NextRun: t0.Add(10 * time.Minute)}
		if err := s.RecordStatus(ctx, st); err != nil {
			t.Fatal(err)
		}
		st2 := st
		st2.SourceID = "s0"
		st2.LastError = t0
		st2.ErrorMsg = "boom"
		st2.AuthFailed = true
		_ = s.RecordStatus(ctx, st2)
		got, _ := s.Status(ctx, "s1")
		if got == nil || got.ItemCount != 3 || !got.LastSuccess.Equal(t0) || got.Duration != 2*time.Second {
			t.Fatalf("got %+v", got)
		}
		all, _ := s.Statuses(ctx)
		if len(all) != 2 || all[0].SourceID != "s0" || !all[0].AuthFailed || all[0].ErrorMsg != "boom" {
			t.Fatalf("Statuses = %+v", all)
		}
	})
}
