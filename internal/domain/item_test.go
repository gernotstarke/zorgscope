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
	visit := at("2026-08-17T10:00:00Z")
	tests := []struct {
		name      string
		firstSeen time.Time
		want      bool
	}{
		{"seen after the visit is new", at("2026-08-17T10:00:01Z"), true},
		{"seen before the visit is not new", at("2026-08-17T09:59:59Z"), false},
		{"seen exactly at the visit is not new", visit, false},
		{"never seen is not new", time.Time{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			it := domain.Item{FirstSeenAt: tc.firstSeen}
			if got := it.IsNew(visit); got != tc.want {
				t.Errorf("IsNew() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSortItemsPutsNewFirstThenNewestUpdate(t *testing.T) {
	visit := at("2026-08-17T10:00:00Z")
	items := []domain.Item{
		{ExternalID: "old-recent", FirstSeenAt: at("2026-08-01T00:00:00Z"), UpdatedAt: at("2026-08-17T12:00:00Z")},
		{ExternalID: "new-stale", FirstSeenAt: at("2026-08-17T11:00:00Z"), UpdatedAt: at("2026-08-02T00:00:00Z")},
		{ExternalID: "new-recent", FirstSeenAt: at("2026-08-17T11:00:00Z"), UpdatedAt: at("2026-08-17T13:00:00Z")},
	}

	domain.SortItems(items, visit)

	want := []string{"new-recent", "new-stale", "old-recent"}
	for i, id := range want {
		if items[i].ExternalID != id {
			t.Fatalf("position %d = %q, want %q (order: %v)", i, items[i].ExternalID, id, ids(items))
		}
	}
}

func TestSortItemsIsStableForEqualKeys(t *testing.T) {
	visit := at("2026-08-17T10:00:00Z")
	same := at("2026-08-17T12:00:00Z")
	items := []domain.Item{
		{ExternalID: "a", UpdatedAt: same}, {ExternalID: "b", UpdatedAt: same}, {ExternalID: "c", UpdatedAt: same},
	}

	domain.SortItems(items, visit)

	for i, id := range []string{"a", "b", "c"} {
		if items[i].ExternalID != id {
			t.Fatalf("sort is not stable: got %v", ids(items))
		}
	}
}

func TestCountNew(t *testing.T) {
	visit := at("2026-08-17T10:00:00Z")
	items := []domain.Item{
		{FirstSeenAt: at("2026-08-17T11:00:00Z")},
		{FirstSeenAt: at("2026-08-17T09:00:00Z")},
		{FirstSeenAt: at("2026-08-17T12:00:00Z")},
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

func ids(items []domain.Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ExternalID
	}
	return out
}
