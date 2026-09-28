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
	// Name is the tier as a class suffix — "alert", "security", "dependency" or "" — and is fixed text,
	// never anything an upstream said.
	Name string
	// Label is the word the chip carries. Colour is never the only signal.
	Label string
	// Evidence is the chip's title: why the row is marked. For a Security item it names the
	// advisories it cites, or says it is labelled security; for an alert it names the package, the
	// fix and the advisory; for a Dependency item it is empty.
	Evidence string
	// Sev is an alert's severity as a class suffix — "low", "medium", "high" or "critical" — and
	// empty for every other tier (FR-1.16 AC4). Serious says whether it is High or Critical: those
	// are drawn solid and crosshatched, the others outlined with a plain rule.
	Sev     string
	Serious bool
}

func newTierView(it domain.Item) tierView {
	switch it.Tier() {
	case domain.TierAlert:
		sev := domain.SeverityHigh
		if it.Alert != nil {
			sev = it.Alert.Severity
		}
		evidence := "Dependabot"
		if it.Summary != "" {
			evidence += ": " + it.Summary
		}
		if len(it.Advisories) > 0 {
			evidence += ", " + strings.Join(it.Advisories, ", ")
		}
		return tierView{Name: "alert", Label: "Alert · " + sev.String(), Evidence: evidence,
			Sev: sev.String(), Serious: sev.Serious()}
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
