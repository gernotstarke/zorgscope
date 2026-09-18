package domain_test

import (
	"reflect"
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

func TestSortItemsPutsTheNewestUpdateFirst(t *testing.T) {
	now := at("2026-08-17T10:00:00Z")
	items := []domain.Item{
		{Number: 1, UpdatedAt: now.Add(-3 * time.Hour)},
		{Number: 2, UpdatedAt: now.Add(-time.Hour)},
		{Number: 3, UpdatedAt: now.Add(-2 * time.Hour)},
	}
	domain.SortItems(items)
	if got := []int{items[0].Number, items[1].Number, items[2].Number}; !reflect.DeepEqual(got, []int{2, 3, 1}) {
		t.Errorf("order = %v, want [2 3 1] (most recently updated first)", got)
	}
}

func TestSortItemsIsStableForEqualKeys(t *testing.T) {
	same := at("2026-08-17T12:00:00Z")
	items := []domain.Item{
		{Title: "a", UpdatedAt: same}, {Title: "b", UpdatedAt: same}, {Title: "c", UpdatedAt: same},
	}

	domain.SortItems(items)

	for i, title := range []string{"a", "b", "c"} {
		if items[i].Title != title {
			t.Fatalf("sort is not stable: got %v", titles(items))
		}
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
