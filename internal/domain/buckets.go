package domain

import "time"

// Bucket is the coarse age class of an item (FR-2.4).
type Bucket int

// Age buckets, youngest first.
const (
	BucketLT24h Bucket = iota
	BucketLT7d
	BucketLT30d
	BucketGE30d
)

var bucketNames = [...]string{"lt24h", "lt7d", "lt30d", "ge30d"}
var bucketLabels = [...]string{"< 24 h", "< 7 d", "< 30 d", "≥ 30 d"}

// String is the CSS-friendly name.
func (b Bucket) String() string { return bucketNames[b] }

// Label is the human-readable name.
func (b Bucket) Label() string { return bucketLabels[b] }

// BucketOf classifies the age of t relative to now. Future timestamps count as youngest.
func BucketOf(t, now time.Time) Bucket {
	age := now.Sub(t)
	switch {
	case age < 24*time.Hour:
		return BucketLT24h
	case age < 7*24*time.Hour:
		return BucketLT7d
	case age < 30*24*time.Hour:
		return BucketLT30d
	default:
		return BucketGE30d
	}
}
