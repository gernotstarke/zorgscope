package domain

import (
	"reflect"
	"testing"
	"time"
)

func TestSnapshotContainsAndDiff(t *testing.T) {
	prev := NewSnapshot("s", "2026-08-15", time.Time{}, []string{"a", "b", "c"})
	cur := NewSnapshot("s", "2026-08-16", time.Time{}, []string{"b", "c", "d", "e"})
	if !prev.Contains("a") || prev.Contains("d") {
		t.Fatal("Contains wrong")
	}
	added, removed := Diff(prev, cur)
	if !reflect.DeepEqual(added, []string{"d", "e"}) || !reflect.DeepEqual(removed, []string{"a"}) {
		t.Fatalf("Diff = %v %v", added, removed)
	}
	if got := cur.IDList(); !reflect.DeepEqual(got, []string{"b", "c", "d", "e"}) {
		t.Fatalf("IDList = %v", got)
	}
}

func TestSnapshotDay(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	cases := []struct {
		now  string
		want string
	}{
		{"2026-08-16T02:59:00+02:00", "2026-08-15"}, // before 03:00 → still yesterday's snapshot day
		{"2026-08-16T03:00:00+02:00", "2026-08-16"},
		{"2026-08-16T23:30:00+02:00", "2026-08-16"},
		{"2026-08-16T00:30:00Z", "2026-08-15"}, // 02:30 Berlin
	}
	for _, c := range cases {
		now, _ := time.Parse(time.RFC3339, c.now)
		if got := SnapshotDay(now, 3, 0, berlin); got != c.want {
			t.Errorf("SnapshotDay(%s) = %s want %s", c.now, got, c.want)
		}
	}
	if DateOf(time.Date(2026, 8, 16, 23, 30, 0, 0, time.UTC), berlin) != "2026-08-17" {
		t.Fatal("DateOf must use the location")
	}
}
