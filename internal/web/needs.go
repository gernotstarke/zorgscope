package web

import (
	"strings"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
)

// needsShown is how many rows of the band are drawn open. The rest wait behind a disclosure, so a
// morning with forty Dependabot bumps still leaves the list itself on the first screen.
const needsShown = 10

// needsView is the Needs-you band above the list (FR-1.14): what needs the owner, loudest reason
// first. It is drawn over every item whatever the filter, like the top bar's counts: the filter
// narrows the list below it, not the answer above it.
type needsView struct {
	// Shown are the first needsShown rows that need the owner now; More are the rest of those,
	// drawn inside a disclosure. Older are the rows that have gone stale — six months without
	// activity (FR-1.14 AC10) — behind a disclosure of their own, so they are one click away
	// rather than on top.
	Shown, More, Older []needView
	// Security says a Security item is among them, which frames the whole band in red: the one
	// row that must not be missed should not have to be found first.
	Security bool
}

// Count is how many items need the owner now, for the heading. The older ones are not counted: the
// count is the number the page is opened for, and a six-month-old pull request is not in it.
func (v needsView) Count() int { return len(v.Shown) + len(v.More) }

// needView is one row of the band: one line, less than a list row, because the list below still
// carries every item in full.
type needView struct {
	// Reason is the class suffix — "security", "dependency", "review", "contribution" or "own" — and
	// ReasonLabel the word the row carries. Colour is never the only signal.
	Reason, ReasonLabel string
	// Tier is set for a marked item, so the band draws the very chip the list draws.
	Tier tierView
	// Kind is "PR" or "Issue", drawn in the top bar's colours.
	Kind, KindClass string
	Number          int
	Title, URL      string
	Author          string
	// Repo is the repository's name without its owner, and Hue its site's colour key.
	Repo, Hue string
	Updated   timeView
}

// newNeedsView builds the band from every item the snapshot holds — not the filtered ones: the band
// answers "what needs me", whatever the list below is narrowed to.
func newNeedsView(items []domain.Item, gh config.GitHub, now time.Time) *needsView {
	needed := domain.BuildNeedsYou(items, gh.Owner)
	v := &needsView{}
	for _, n := range needed {
		row := newNeedView(n, gh, now)
		switch {
		case n.Need == domain.NeedSecurity:
			v.Security = true
		case n.Stale(now):
			v.Older = append(v.Older, row)
			continue
		default:
		}
		if len(v.Shown) < needsShown {
			v.Shown = append(v.Shown, row)
		} else {
			v.More = append(v.More, row)
		}
	}
	return v
}

func newNeedView(n domain.Needed, gh config.GitHub, now time.Time) needView {
	it := n.Item
	v := needView{
		Reason:  n.Need.String(),
		Number:  it.Number,
		Title:   it.Title,
		URL:     it.URL,
		Author:  it.Author,
		Repo:    it.Repo[strings.IndexByte(it.Repo, '/')+1:],
		Hue:     hueForRepo(gh, it.Repo),
		Updated: newTimeView(it.UpdatedAt, now),
	}
	switch n.Need {
	case domain.NeedSecurity, domain.NeedDependency:
		v.Tier = newTierView(it)
	case domain.NeedReview:
		v.ReasonLabel = "Review requested"
	case domain.NeedContribution:
		v.ReasonLabel = "Contribution"
	case domain.NeedOwn:
		v.ReasonLabel = "Your PR"
	default:
	}
	v.Kind, v.KindClass = kindShort(it.Kind), string(it.Kind)
	return v
}
