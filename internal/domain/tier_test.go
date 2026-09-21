package domain_test

import (
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// The tiers are drawn from evidence, not from who opened the item (FR-1.13 AC1). A bot is not by
// itself a security item: Copilot and GitHub Actions open pull requests too, and painting those
// red would teach the reader to ignore red.
func TestTierIsDrawnFromEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		item domain.Item
		want domain.Tier
	}{
		{"a cited CVE is security", domain.Item{Author: "someone", Advisories: []string{"CVE-2026-54904"}}, domain.TierSecurity},
		{"a security label is security", domain.Item{Author: "someone", Labels: []string{"Security"}}, domain.TierSecurity},
		{"a person's issue citing a CVE is security", domain.Item{Author: "gernotstarke", Advisories: []string{"GHSA-6wx8-w4f5-wwcr"}}, domain.TierSecurity},
		{"dependabot citing a CVE is security, not dependency", domain.Item{Author: "dependabot", Advisories: []string{"CVE-2026-33168"}}, domain.TierSecurity},
		{"dependabot with no advisory is dependency", domain.Item{Author: "dependabot"}, domain.TierDependency},
		{"the login is compared without regard to case", domain.Item{Author: "Dependabot"}, domain.TierDependency},
		{"renovate is dependency", domain.Item{Author: "renovate"}, domain.TierDependency},
		{"a dependencies label is dependency", domain.Item{Author: "someone", Labels: []string{"Dependencies"}}, domain.TierDependency},
		{"copilot is not security", domain.Item{Author: "copilot-swe-agent"}, domain.TierNone},
		{"github-actions is not security", domain.Item{Author: "github-actions"}, domain.TierNone},
		{"an ordinary issue is nothing", domain.Item{Author: "someone", Labels: []string{"bug"}}, domain.TierNone},
	} {
		if got := tc.item.Tier(); got != tc.want {
			t.Errorf("%s: Tier() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The tier's String is what the page uses as a class suffix, so it is fixed text and never
// anything upstream said.
func TestTierStringsAreFixed(t *testing.T) {
	for tier, want := range map[domain.Tier]string{
		domain.TierNone: "", domain.TierDependency: "dependency", domain.TierSecurity: "security",
	} {
		if got := tier.String(); got != want {
			t.Errorf("Tier(%d).String() = %q, want %q", tier, got, want)
		}
	}
}

// FR-1.13 AC3: an old, unfixed vulnerability is the item a reader most needs to see, so a Security
// item is never dimmed as quiet. Anything else behaves exactly as IsQuiet does.
func TestShowsQuietNeverHidesASecurityItem(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -400)

	security := domain.Item{UpdatedAt: old, Advisories: []string{"CVE-2026-54904"}}
	if security.ShowsQuiet(now, domain.QuietAfter) {
		t.Error("a year-old security item was marked quiet")
	}

	dependency := domain.Item{UpdatedAt: old, Author: "dependabot"}
	ordinary := domain.Item{UpdatedAt: old}
	for name, it := range map[string]domain.Item{"dependency": dependency, "ordinary": ordinary} {
		if got, want := it.ShowsQuiet(now, domain.QuietAfter), it.IsQuiet(now, domain.QuietAfter); got != want {
			t.Errorf("%s item: ShowsQuiet = %v, IsQuiet = %v; they must agree for anything but security", name, got, want)
		}
	}
}
