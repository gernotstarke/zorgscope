// Package domain holds zorgscope's pure core: the types shared across every source, and the one
// real rule of the product — what counts as NEW. It imports nothing but the standard library
// (QS-5.1), so every function that needs the current time takes it as a parameter; nothing in
// here calls time.Now().
package domain

import (
	"sort"
	"time"
)

// Kind distinguishes the three shapes of item zorgscope tracks.
type Kind string

// The set of item kinds zorgscope understands.
const (
	KindIssue Kind = "issue"
	KindPR    Kind = "pr"
	KindTask  Kind = "task"
)

// Item is a single tracked unit from a source: a GitHub issue or pull request, or a Todoist task.
type Item struct {
	Source     string
	ExternalID string
	Kind       Kind
	// Repo is the item's container, which is not the same thing in every source: for a GitHub
	// issue or pull request it is the repository, "owner/name"; for a Todoist task it is the
	// project name the task sits in, which is what FR-4.1 AC1 asks to be shown beside it. The
	// field is one column, one template variable and one adapter mapping in either case — only
	// the name says "repository".
	//
	// Renaming it to something source-neutral (Container, Group) is the real fix and was
	// deliberately deferred: it reaches the domain, the schema, every adapter and the templates,
	// which is a migration and a broad edit for a naming defect with no behavioural consequence.
	// It was decided, not missed.
	Repo   string
	Number int
	Title  string
	// Summary is a short prefix of the item's own text — a GitHub issue or pull request body —
	// with its whitespace collapsed, or empty when the source has none. It is stored short
	// because it is displayed short: the dashboard renders it in small type under the title, so
	// that a list of numbers and headlines says what the items are actually about. It is
	// borrowed text like Title, and is escaped, never trusted.
	Summary     string
	URL         string
	Author      string
	State       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DueAt       time.Time // zero when the item has no due date
	Priority    int
	FirstSeenAt time.Time
}

// IsNew reports whether the item first appeared after lastVisit. An item never seen
// (FirstSeenAt zero) is not new, and an item first seen exactly at lastVisit is not new either —
// only a first sighting strictly after the visit counts.
func (i Item) IsNew(lastVisit time.Time) bool {
	return !i.FirstSeenAt.IsZero() && i.FirstSeenAt.After(lastVisit)
}

// SortItems orders items new-first, then by most recently updated within each group. The sort is
// stable, so items with equal keys keep their original relative order.
func SortItems(items []Item, lastVisit time.Time) {
	sort.SliceStable(items, func(i, j int) bool {
		iNew, jNew := items[i].IsNew(lastVisit), items[j].IsNew(lastVisit)
		if iNew != jNew {
			return iNew
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
}

// CountNew reports how many items are new as of lastVisit.
func CountNew(items []Item, lastVisit time.Time) int {
	n := 0
	for _, it := range items {
		if it.IsNew(lastVisit) {
			n++
		}
	}
	return n
}

// AgeBucket classifies how long ago something happened, relative to a display cutoff.
type AgeBucket string

// The set of age buckets a timestamp can fall into.
const (
	BucketDay   AgeBucket = "day"
	BucketWeek  AgeBucket = "week"
	BucketMonth AgeBucket = "month"
	BucketOlder AgeBucket = "older"
)

// Age classifies t relative to now into a bucket, using the boundaries 24 hours, 7 days and
// 30 days.
func Age(t, now time.Time) AgeBucket {
	age := now.Sub(t)
	switch {
	case age <= 24*time.Hour:
		return BucketDay
	case age <= 7*24*time.Hour:
		return BucketWeek
	case age <= 30*24*time.Hour:
		return BucketMonth
	default:
		return BucketOlder
	}
}
