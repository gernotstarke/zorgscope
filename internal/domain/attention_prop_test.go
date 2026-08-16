package domain

import (
	"testing"
	"time"

	"pgregory.net/rapid"
)

// QS-1.2: for any two snapshots, IsNew is true exactly for ids absent from the previous snapshot,
// and Diff agrees with IsNew.
func TestPropNewMatchesSnapshotDiff(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		idGen := rapid.SliceOfDistinct(rapid.StringMatching(`issues/[0-9]{1,3}`), func(s string) string { return s })
		prevIDs := idGen.Draw(t, "prev")
		curIDs := idGen.Draw(t, "cur")
		prev := NewSnapshot("s", "2026-08-15", now0, prevIDs)
		cur := NewSnapshot("s", "2026-08-16", now0, curIDs)
		added, _ := Diff(prev, cur)
		addedSet := map[string]bool{}
		for _, a := range added {
			addedSet[a] = true
		}
		r := DefaultRules()
		for _, id := range curIDs {
			it := Item{ID: ItemID{SourceID: "s", ExternalID: id}, Kind: KindIssue, CreatedAt: now0.Add(-100 * 24 * time.Hour)}
			if r.IsNew(it, &prev, now0) != addedSet[id] {
				t.Fatalf("IsNew(%s)=%v but Diff says %v", id, !addedSet[id], addedSet[id])
			}
		}
	})
}

// FR-2.7 AC3 / QS-1.6: a dismissal never covers an item whose UpdatedAt differs.
func TestPropDismissalNeverHidesChange(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		base := now0.Add(time.Duration(rapid.IntRange(-1_000_000, 1_000_000).Draw(t, "upd")) * time.Second)
		delta := time.Duration(rapid.IntRange(1, 1_000_000).Draw(t, "delta")) * time.Second
		id := ItemID{SourceID: "s", ExternalID: "x"}
		d := Dismissal{ID: id, UpdatedAt: base}
		if d.Covers(Item{ID: id, UpdatedAt: base.Add(delta)}) || d.Covers(Item{ID: id, UpdatedAt: base.Add(-delta)}) {
			t.Fatal("dismissal covered a changed item")
		}
		if !d.Covers(Item{ID: id, UpdatedAt: base}) {
			t.Fatal("dismissal must cover the unchanged item")
		}
	})
}
