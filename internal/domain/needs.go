package domain

import (
	"sort"
	"strings"
)

// StateDraft is the State a draft pull request carries (FR-2.1 AC1): GitHub's own word for it,
// set by the adapter.
const StateDraft = "DRAFT"

// Need is why an item needs the owner (FR-1.14). The zero value is NeedNone. The order of the
// constants is the order the band lists them in: the loudest reason first.
type Need int

// The reasons, from loudest to quietest after NeedNone.
const (
	NeedNone Need = iota
	NeedSecurity
	NeedDependency
	NeedReview
	NeedContribution
)

// String is the reason as the page names it in a class, and so is fixed text.
func (n Need) String() string {
	switch n {
	case NeedSecurity:
		return "security"
	case NeedDependency:
		return "dependency"
	case NeedReview:
		return "review"
	case NeedContribution:
		return "contribution"
	default:
		return ""
	}
}

// NeedFor classifies the item for owner, the one GitHub login zorgscope works for. An item with
// several reasons gets the loudest: a Dependabot pull request citing a CVE is a security item, not
// a contribution, and it is listed once.
//
//   - Security or Dependency: the item is marked (FR-1.13), whoever opened it.
//   - Review: a pull request that asks owner for a review.
//   - Contribution: a pull request someone else opened and is ready — someone is waiting for a
//     merge or a review. A draft is not ready, so it waits on its author, not on owner.
//
// An empty owner matches nobody, so without one only the marked items need anyone.
func NeedFor(it Item, owner string) Need {
	switch it.Tier() {
	case TierSecurity:
		return NeedSecurity
	case TierDependency:
		return NeedDependency
	default:
	}
	if it.Kind != KindPR || owner == "" {
		return NeedNone
	}
	for _, login := range it.ReviewRequested {
		if strings.EqualFold(login, owner) {
			return NeedReview
		}
	}
	if !strings.EqualFold(it.Author, owner) && it.State != StateDraft {
		return NeedContribution
	}
	return NeedNone
}

// Needed is one item of the band, with the reason it is there.
type Needed struct {
	Item Item
	Need Need
}

// BuildNeedsYou is the band (FR-1.14): every item that needs owner, loudest reason first and,
// within a reason, most recently updated first. It never mutates items. The list below the band
// still shows every item (QG-1); the band only says which of them to look at first.
func BuildNeedsYou(items []Item, owner string) []Needed {
	var out []Needed
	for _, it := range items {
		if n := NeedFor(it, owner); n != NeedNone {
			out = append(out, Needed{Item: it, Need: n})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Need != out[j].Need {
			return out[i].Need < out[j].Need
		}
		return out[i].Item.UpdatedAt.After(out[j].Item.UpdatedAt)
	})
	return out
}
