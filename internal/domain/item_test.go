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

// The threshold is a parameter because it is a preference (FR-1.12 AC4), not a property of an
// item. Zero means nothing is ever quiet, which is what the "never" setting asks for — and it has
// to be the zero value's meaning, because a caller that forgets to pass one must not silently
// mark everything quiet.
func TestIsQuietHonoursTheThreshold(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	item := func(daysAgo int) domain.Item {
		return domain.Item{UpdatedAt: now.AddDate(0, 0, -daysAgo)}
	}
	day := 24 * time.Hour

	for _, tc := range []struct {
		name      string
		daysAgo   int
		after     time.Duration
		wantQuiet bool
	}{
		{"60 days against 30 is quiet", 60, 30 * day, true},
		{"60 days against 90 is not", 60, 90 * day, false},
		{"exactly at the threshold is quiet", 90, 90 * day, true},
		{"a day short of it is not", 89, 90 * day, false},
		{"180 days against 180 is quiet", 180, 180 * day, true},
		{"never: a year old is still not quiet", 365, 0, false},
		{"never: even a decade", 3650, 0, false},
	} {
		if got := item(tc.daysAgo).IsQuiet(now, tc.after); got != tc.wantQuiet {
			t.Errorf("%s: IsQuiet = %v, want %v", tc.name, got, tc.wantQuiet)
		}
	}

	// An unknown update time is never quiet, whatever the threshold: unknown is not idle.
	if (domain.Item{}).IsQuiet(now, 30*day) {
		t.Error("an item with no update time was called quiet")
	}
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
			if got := (domain.Item{UpdatedAt: c.updated}).IsQuiet(now, domain.QuietAfter); got != c.want {
				t.Errorf("IsQuiet = %v, want %v", got, c.want)
			}
		})
	}
}
