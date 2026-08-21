package domain

import (
	"sort"
	"time"
)

// Snapshot is the set of external ids a source had on a given snapshot day (FR-7.1).
type Snapshot struct {
	SourceID string
	Date     string // YYYY-MM-DD, the snapshot day (see SnapshotDay)
	TakenAt  time.Time
	IDs      map[string]struct{}
}

// NewSnapshot builds a snapshot from a list of external ids.
func NewSnapshot(sourceID, date string, takenAt time.Time, ids []string) Snapshot {
	s := Snapshot{SourceID: sourceID, Date: date, TakenAt: takenAt, IDs: make(map[string]struct{}, len(ids))}
	for _, id := range ids {
		s.IDs[id] = struct{}{}
	}
	return s
}

// Contains reports whether the external id was present.
func (s Snapshot) Contains(externalID string) bool {
	_, ok := s.IDs[externalID]
	return ok
}

// IDList returns the ids sorted, for persistence and tests.
func (s Snapshot) IDList() []string {
	out := make([]string, 0, len(s.IDs))
	for id := range s.IDs {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Diff returns ids present in cur but not prev (added) and vice versa (removed), sorted.
func Diff(prev, cur Snapshot) (added, removed []string) {
	for id := range cur.IDs {
		if !prev.Contains(id) {
			added = append(added, id)
		}
	}
	for id := range prev.IDs {
		if !cur.Contains(id) {
			removed = append(removed, id)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// DateOf formats t as YYYY-MM-DD in loc.
func DateOf(t time.Time, loc *time.Location) string { return t.In(loc).Format("2006-01-02") }

// SnapshotDay is the date of the most recent scheduled snapshot time (hour:minute in loc) at or before now.
// Before today's snapshot time it is yesterday's date. New-detection compares against the snapshot
// *before* this day, which guarantees every item is highlighted for at least 24 h (ADR-0008).
func SnapshotDay(now time.Time, hour, minute int, loc *time.Location) string {
	t := now.In(loc)
	boundary := time.Date(t.Year(), t.Month(), t.Day(), hour, minute, 0, 0, loc)
	if t.Before(boundary) {
		boundary = boundary.AddDate(0, 0, -1)
	}
	return boundary.Format("2006-01-02")
}
