// Package domain holds zorgscope's pure core: the types shared across every source, and the one
// real rule of the product — what counts as NEW. It imports nothing but the standard library
// (QS-5.1), so every function that needs the current time takes it as a parameter; nothing in
// here calls time.Now().
package domain

import (
	"sort"
	"time"
)

// Kind distinguishes the two shapes of item zorgscope tracks.
type Kind string

// The set of item kinds zorgscope understands.
const (
	KindIssue Kind = "issue"
	KindPR    Kind = "pr"
)

// Item is a single tracked unit from GitHub: an issue or a pull request.
type Item struct {
	Kind Kind
	// Repo is the repository the item belongs to, "owner/name".
	Repo   string
	Number int
	Title  string
	// Summary is a short prefix of the item's own text — a GitHub issue or pull request body —
	// with its whitespace collapsed, or empty when the source has none. It is stored short
	// because it is displayed short: the dashboard renders it in small type under the title, so
	// that a list of numbers and headlines says what the items are actually about. It is
	// borrowed text like Title, and is escaped, never trusted.
	Summary   string
	URL       string
	Author    string
	State     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// IsNew reports whether the item was created after seen. The zero seen means the visitor has
// never marked the list, and then nothing is new: a first visit that shouts NEW at every item
// says nothing.
func (i Item) IsNew(seen time.Time) bool {
	return !seen.IsZero() && i.CreatedAt.After(seen)
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
