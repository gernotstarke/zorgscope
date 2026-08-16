package domain

import (
	"testing"
	"time"
)

func TestDismissalCovers(t *testing.T) {
	id := ItemID{SourceID: "s", ExternalID: "issues/1"}
	upd := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	d := Dismissal{ID: id, UpdatedAt: upd, DismissedAt: upd.Add(time.Hour)}
	if !d.Covers(Item{ID: id, UpdatedAt: upd}) {
		t.Fatal("same updated_at must be covered")
	}
	if !d.Covers(Item{ID: id, UpdatedAt: upd.In(time.FixedZone("x", 3600))}) {
		t.Fatal("comparison must be by instant, not by location")
	}
	if d.Covers(Item{ID: id, UpdatedAt: upd.Add(time.Second)}) {
		t.Fatal("changed item must not be covered (FR-2.7 AC3)")
	}
	if d.Covers(Item{ID: ItemID{SourceID: "s", ExternalID: "issues/2"}, UpdatedAt: upd}) {
		t.Fatal("other item must not be covered")
	}
}
