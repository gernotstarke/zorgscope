package domain_test

import (
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestIsNew(t *testing.T) {
	seen := at("2026-08-17T10:00:00Z")
	tests := []struct {
		name      string
		seen      time.Time
		createdAt time.Time
		want      bool
	}{
		{"created after seen is new", seen, at("2026-08-17T10:00:01Z"), true},
		{"created before seen is not new", seen, at("2026-08-17T09:59:59Z"), false},
		{"created exactly at seen is not new", seen, seen, false},
		{"zero seen is not new, however recently created", time.Time{}, at("2026-08-17T10:00:01Z"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			it := domain.Item{CreatedAt: tc.createdAt}
			if got := it.IsNew(tc.seen); got != tc.want {
				t.Errorf("IsNew() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSortItemsPutsNewFirstThenNewestUpdate(t *testing.T) {
	visit := at("2026-08-17T10:00:00Z")
	items := []domain.Item{
		{Title: "old-recent", CreatedAt: at("2026-08-01T00:00:00Z"), UpdatedAt: at("2026-08-17T12:00:00Z")},
		{Title: "new-stale", CreatedAt: at("2026-08-17T11:00:00Z"), UpdatedAt: at("2026-08-02T00:00:00Z")},
		{Title: "new-recent", CreatedAt: at("2026-08-17T11:00:00Z"), UpdatedAt: at("2026-08-17T13:00:00Z")},
	}

	domain.SortItems(items, visit)

	want := []string{"new-recent", "new-stale", "old-recent"}
	for i, title := range want {
		if items[i].Title != title {
			t.Fatalf("position %d = %q, want %q (order: %v)", i, items[i].Title, title, titles(items))
		}
	}
}

func TestSortItemsIsStableForEqualKeys(t *testing.T) {
	visit := at("2026-08-17T10:00:00Z")
	same := at("2026-08-17T12:00:00Z")
	items := []domain.Item{
		{Title: "a", UpdatedAt: same}, {Title: "b", UpdatedAt: same}, {Title: "c", UpdatedAt: same},
	}

	domain.SortItems(items, visit)

	for i, title := range []string{"a", "b", "c"} {
		if items[i].Title != title {
			t.Fatalf("sort is not stable: got %v", titles(items))
		}
	}
}

func TestCountNew(t *testing.T) {
	visit := at("2026-08-17T10:00:00Z")
	items := []domain.Item{
		{CreatedAt: at("2026-08-17T11:00:00Z")},
		{CreatedAt: at("2026-08-17T09:00:00Z")},
		{CreatedAt: at("2026-08-17T12:00:00Z")},
	}
	if got := domain.CountNew(items, visit); got != 2 {
		t.Errorf("CountNew() = %d, want 2", got)
	}
}

func TestAge(t *testing.T) {
	now := at("2026-08-17T12:00:00Z")
	tests := []struct {
		in   string
		want domain.AgeBucket
	}{
		{"2026-08-17T11:00:00Z", domain.BucketDay},
		{"2026-08-16T11:00:00Z", domain.BucketWeek},
		{"2026-08-05T12:00:00Z", domain.BucketMonth},
		{"2026-06-01T12:00:00Z", domain.BucketOlder},
	}
	for _, tc := range tests {
		if got := domain.Age(at(tc.in), now); got != tc.want {
			t.Errorf("Age(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func titles(items []domain.Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Title
	}
	return out
}

// FR-1.10 AC4: quiet is 90 days without an update, measured to the second; an item whose update
// time is unknown is never quiet — unknown is not idle.
func TestIsQuietAtTheBoundary(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		updated time.Time
		want    bool
	}{
		{"just under 90 days", now.Add(-domain.QuietAfter + time.Second), false},
		{"exactly 90 days", now.Add(-domain.QuietAfter), true},
		{"a year", now.AddDate(-1, 0, 0), true},
		{"unknown", time.Time{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (domain.Item{UpdatedAt: c.updated}).IsQuiet(now); got != c.want {
				t.Errorf("IsQuiet = %v, want %v", got, c.want)
			}
		})
	}
}
