package web

import (
	"strings"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// tierView is how an item's tier is drawn (FR-1.13 AC2). It is built once, here, and embedded in
// both the list's row and the search results' row, because search builds its rows separately from
// the list — and a chip that appeared on one and not the other is exactly the kind of seam a
// separate builder invites.
type tierView struct {
	// Name is the tier as a class suffix — "security", "dependency" or "" — and is fixed text,
	// never anything an upstream said.
	Name string
	// Label is the word the chip carries. Colour is never the only signal.
	Label string
	// Evidence is the chip's title: why the row is marked. For a Security item it names the
	// advisories it cites, or says it is labelled security; for a Dependency item it is empty.
	Evidence string
}

func newTierView(it domain.Item) tierView {
	switch it.Tier() {
	case domain.TierSecurity:
		evidence := "labelled security"
		if len(it.Advisories) > 0 {
			evidence = "cites " + strings.Join(it.Advisories, ", ")
		}
		return tierView{Name: "security", Label: "Security", Evidence: evidence}
	case domain.TierDependency:
		return tierView{Name: "dependency", Label: "Dependency"}
	default:
		return tierView{}
	}
}
