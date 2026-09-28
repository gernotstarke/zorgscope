package domain

import (
	"sort"
	"strings"
	"time"
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
	NeedAlert
	NeedSecurity
	NeedDependency
	NeedReview
	NeedContribution
	NeedOwn
)

// String is the reason as the page names it in a class, and so is fixed text.
func (n Need) String() string {
	switch n {
	case NeedAlert:
		return "alert"
	case NeedSecurity:
		return "security"
	case NeedDependency:
		return "dependency"
	case NeedReview:
		return "review"
	case NeedContribution:
		return "contribution"
	case NeedOwn:
		return "own"
	default:
		return ""
	}
}

// NeedFor classifies the item for owner, the one GitHub login zorgscope works for. An item with
// several reasons gets the loudest: a Dependabot pull request citing a CVE is a security item, not
// a contribution, and it is listed once.
//
//   - Alert: a Dependabot alert of High or Critical severity (FR-1.16 AC2). A Low or Medium alert
//     needs nobody: it is on the list, the tiles and the radar, but counted here it would sit in
//     the band for as long as nobody bumps a build-time dependency, and a mark that is always on
//     stops being read (ADR-0012).
//   - Security or Dependency: the item is marked (FR-1.13), whoever opened it.
//   - Review: a pull request that asks owner for a review.
//   - Contribution: a pull request someone else opened, draft or ready — somebody is waiting for
//     the owner to look at it.
//   - Own: a pull request owner opened — it is open, so it is waiting for the owner to merge it.
//
// Every open pull request therefore needs the owner; what goes stale after six months is decided
// by Needed.Stale, not here. An empty owner matches nobody, so without one every pull request is a
// contribution.
func NeedFor(it Item, owner string) Need {
	if it.Kind == KindAlert {
		if it.Alert != nil && !it.Alert.Severity.Serious() {
			return NeedNone
		}
		return NeedAlert
	}
	switch it.Tier() {
	case TierSecurity:
		return NeedSecurity
	case TierDependency:
		return NeedDependency
	default:
	}
	if it.Kind != KindPR {
		return NeedNone
	}
	if owner == "" {
		return NeedContribution
	}
	for _, login := range it.ReviewRequested {
		if strings.EqualFold(login, owner) {
			return NeedReview
		}
	}
	if strings.EqualFold(it.Author, owner) {
		return NeedOwn
	}
	return NeedContribution
}

// Needed is one item of the band, with the reason it is there.
type Needed struct {
	Item Item
	Need Need
	// FixReady is the number of the open pull request that fixes this alert, when that pull
	// request is on the list too; 0 otherwise. The band then says "fix ready: #n" on the alert's
	// row and does not list the pull request a second time (FR-1.16 AC2).
	FixReady int
}

// NeedsWindow is how long an item may go without activity and still need the owner now: six
// months. A pull request nobody has touched for longer is not "right now"; the band keeps it one
// click away rather than on top (FR-1.14 AC10).
const NeedsWindow = 183 * 24 * time.Hour

// Stale reports whether the item has had no activity for longer than NeedsWindow. A Security item
// or an alert is never stale — an old, unfixed vulnerability is exactly the item that must stay in
// sight, as it is never quiet (FR-1.13 AC3, FR-1.16 AC2) — and neither is an item whose update
// time is unknown: unknown is not idle.
func (n Needed) Stale(now time.Time) bool {
	return n.Need != NeedSecurity && n.Need != NeedAlert &&
		!n.Item.UpdatedAt.IsZero() && now.Sub(n.Item.UpdatedAt) > NeedsWindow
}

// NeedsNow reports whether it needs owner and has not gone stale: what the band lists on top, the
// top bar counts and the list draws at full weight.
func NeedsNow(it Item, owner string, now time.Time) bool {
	n := Needed{Item: it, Need: NeedFor(it, owner)}
	return n.Need != NeedNone && !n.Stale(now)
}

// BuildNeedsYou is the band (FR-1.14): every item that needs owner, loudest reason first and,
// within a reason, most recently updated first. It never mutates items. The list below the band
// still shows every item (QG-1); the band only says which of them to look at first.
//
// An alert whose fix pull request is open is one problem, not two: the alert carries FixReady and
// the pull request is left out of the band (FR-1.16 AC2). The list still shows both.
func BuildNeedsYou(items []Item, owner string) []Needed {
	open, fixes := fixPRs(items, owner)
	var out []Needed
	for _, it := range items {
		if it.Kind == KindPR && fixes[fixKey{it.Repo, it.Number}] {
			continue
		}
		if n := NeedFor(it, owner); n != NeedNone {
			nd := Needed{Item: it, Need: n}
			if n == NeedAlert && it.Alert != nil && open[fixKey{it.Repo, it.Alert.FixPR}] {
				nd.FixReady = it.Alert.FixPR
			}
			out = append(out, nd)
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

// fixKey names one pull request: its repository and number.
type fixKey struct {
	repo   string
	number int
}

// fixPRs returns every open pull request on the list, and those of them that fix an alert the
// band lists. Only an alert that needs the owner absorbs its fix: the fix of a Low alert stays in
// the band on its own merits, or the problem would leave the band altogether.
func fixPRs(items []Item, owner string) (open, fixes map[fixKey]bool) {
	open = make(map[fixKey]bool)
	for _, it := range items {
		if it.Kind == KindPR {
			open[fixKey{it.Repo, it.Number}] = true
		}
	}
	fixes = make(map[fixKey]bool)
	for _, it := range items {
		if it.Kind != KindAlert || it.Alert == nil || it.Alert.FixPR == 0 || NeedFor(it, owner) != NeedAlert {
			continue
		}
		if k := (fixKey{it.Repo, it.Alert.FixPR}); open[k] {
			fixes[k] = true
		}
	}
	return open, fixes
}
