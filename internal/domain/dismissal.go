package domain

import "time"

// Dismissal is the user's "seen" for one item at one state (FR-2.7). It stops covering the item as
// soon as the item's UpdatedAt changes.
type Dismissal struct {
	ID          ItemID
	UpdatedAt   time.Time // the item's UpdatedAt at dismissal time
	DismissedAt time.Time
}

// Covers reports whether d suppresses highlights for it.
func (d Dismissal) Covers(it Item) bool {
	return d.ID == it.ID && d.UpdatedAt.Unix() == it.UpdatedAt.Unix()
}
