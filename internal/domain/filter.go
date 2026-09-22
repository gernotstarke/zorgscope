package domain

import (
	"strings"
	"time"
)

// Filter narrows the dashboard's list. Every field is optional; the zero Filter matches every
// item. The fields are ANDed: an item must pass every axis that is set.
type Filter struct {
	Repo         string    // "" means every repository
	Kind         Kind      // "" means issues and pull requests alike
	CreatedSince time.Time // zero means no lower bound; an item created exactly then passes
	Text         string    // "" (after trimming) means no text filter
	// MinTier is the quietest mark an item may carry and still pass (FR-1.13). TierNone, the
	// zero value, lets everything through. The axis is a floor rather than an equality because
	// the tiers are ordered: asking for dependency updates has to show the security ones too,
	// since a bump that fixes a vulnerability is still a bump.
	MinTier Tier
}

// Match reports whether it passes every axis of f. Text is matched case-insensitively against
// the title and the summary, as a substring: the box is for "the thing about the header", not
// for a query language.
func (f Filter) Match(it Item) bool {
	if f.Repo != "" && it.Repo != f.Repo {
		return false
	}
	if f.Kind != "" && it.Kind != f.Kind {
		return false
	}
	if !f.CreatedSince.IsZero() && it.CreatedAt.Before(f.CreatedSince) {
		return false
	}
	if f.MinTier != TierNone && it.Tier() < f.MinTier {
		return false
	}
	if text := strings.TrimSpace(f.Text); text != "" {
		needle := strings.ToLower(text)
		if !strings.Contains(strings.ToLower(it.Title), needle) &&
			!strings.Contains(strings.ToLower(it.Summary), needle) {
			return false
		}
	}
	return true
}

// Empty reports whether f narrows nothing.
func (f Filter) Empty() bool {
	return f.Repo == "" && f.Kind == "" && f.CreatedSince.IsZero() &&
		strings.TrimSpace(f.Text) == "" && f.MinTier == TierNone
}
