// Package domain holds zorgscope's pure core: the types shared across every source. It imports
// nothing but the standard library (QS-5.1), so every function that needs the current time takes
// it as a parameter; nothing in here calls time.Now().
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
	Summary string
	URL     string
	Author  string
	// Labels are the item's GitHub labels as GitHub spells them, in GitHub's order — at most ten,
	// which covers every arc42 item there is. nil when the item has none. The domain carries the
	// names and nothing else; which of them get a colour is the page's business (FR-1.10 AC3).
	Labels    []string
	State     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SortItems orders items most recently updated first. The sort is stable, so items with equal
// update times keep their original relative order.
func SortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
}

// QuietAfter is how long an item may go without an update before the page calls it quiet
// (FR-1.10 AC4). Three months: long enough that a maintainer's own pause does not trip it, short
// enough that a forgotten issue shows up before the year is out.
const QuietAfter = 90 * 24 * time.Hour

// IsQuiet reports whether nothing has happened to the item for QuietAfter or longer, measured
// from its last update to now. An item whose update time is unknown is never quiet: unknown is
// not idle.
func (i Item) IsQuiet(now time.Time) bool {
	return !i.UpdatedAt.IsZero() && now.Sub(i.UpdatedAt) >= QuietAfter
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
