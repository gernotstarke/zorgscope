package domain

import (
	"testing"
	"time"
)

func ev(ext string, lvl Level, created time.Time) Evaluated {
	return Evaluated{Item: Item{ID: ItemID{"s", ext}, CreatedAt: created}, Eval: Evaluation{Level: lvl}}
}

func TestSortFilterCap(t *testing.T) {
	list := []Evaluated{
		ev("aged", LevelAged, now0),
		ev("unanswered-old", LevelUnanswered, now0.Add(-48*time.Hour)),
		ev("new-old", LevelNew, now0.Add(-2*time.Hour)),
		ev("new-fresh", LevelNew, now0.Add(-time.Hour)),
		ev("auth", LevelAuthFailed, now0.Add(-72*time.Hour)),
		ev("stale", LevelStale, now0.Add(-900*time.Hour)),
	}
	SortByUrgency(list)
	want := []string{"auth", "new-fresh", "new-old", "unanswered-old", "stale", "aged"}
	for i, w := range want {
		if list[i].Item.ID.ExternalID != w {
			t.Fatalf("pos %d: got %s want %s", i, list[i].Item.ID.ExternalID, w)
		}
	}
	att := FilterAttention(list)
	if len(att) != 4 {
		t.Fatalf("FilterAttention: got %d want 4 (auth, 2 new, unanswered)", len(att))
	}
	shown, overflow := Cap(att, 3)
	if len(shown) != 3 || overflow != 1 {
		t.Fatalf("Cap: %d shown, %d overflow", len(shown), overflow)
	}
	shown, overflow = Cap(att, 0)
	if len(shown) != 4 || overflow != 0 {
		t.Fatal("Cap(0) means unlimited")
	}
}
