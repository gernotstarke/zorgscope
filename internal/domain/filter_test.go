package domain_test

import (
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

func TestFilterMatchesEveryAxis(t *testing.T) {
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	it := domain.Item{Repo: "arc42/arc42.org-site", Kind: domain.KindPR, Title: "Fix the Header", Summary: "broken on mobile", CreatedAt: created}

	cases := []struct {
		name string
		f    domain.Filter
		want bool
	}{
		{"empty matches", domain.Filter{}, true},
		{"repo equal", domain.Filter{Repo: "arc42/arc42.org-site"}, true},
		{"repo other", domain.Filter{Repo: "arc42/arc42.de-site"}, false},
		{"kind equal", domain.Filter{Kind: domain.KindPR}, true},
		{"kind other", domain.Filter{Kind: domain.KindIssue}, false},
		{"since before created", domain.Filter{CreatedSince: created.Add(-time.Hour)}, true},
		{"since at created", domain.Filter{CreatedSince: created}, true},
		{"since after created", domain.Filter{CreatedSince: created.Add(time.Hour)}, false},
		{"text in title, case-insensitive", domain.Filter{Text: "header"}, true},
		{"text in summary", domain.Filter{Text: "MOBILE"}, true},
		{"text absent", domain.Filter{Text: "footer"}, false},
		{"text is trimmed", domain.Filter{Text: "  header "}, true},
		{"all axes together", domain.Filter{Repo: "arc42/arc42.org-site", Kind: domain.KindPR, CreatedSince: created, Text: "fix"}, true},
		{"one axis fails the whole filter", domain.Filter{Repo: "arc42/arc42.org-site", Kind: domain.KindIssue}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.Match(it); got != tc.want {
				t.Fatalf("Match() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFilterEmpty(t *testing.T) {
	if !(domain.Filter{}).Empty() {
		t.Fatal("zero filter should be empty")
	}
	if (domain.Filter{Text: " "}).Empty() == false {
		t.Fatal("whitespace-only text is still empty")
	}
	if (domain.Filter{Kind: domain.KindIssue}).Empty() {
		t.Fatal("a kind makes the filter non-empty")
	}
}

// FR-1.13: the tier axis is "this tier or louder", so asking for dependencies also shows the
// security items — a security fix is a dependency update as well, and the Sites view's Security
// tile links here for everything it marks.
func TestFilterMinTierMatchesThatTierOrLouder(t *testing.T) {
	security := domain.Item{Title: "Bump nokogiri", Advisories: []string{"CVE-2024-1234"}}
	dependency := domain.Item{Title: "Bump rake", Author: "dependabot"}
	plain := domain.Item{Title: "Fix the header"}

	cases := []struct {
		name                        string
		min                         domain.Tier
		security, dependency, plain bool
	}{
		{"unset lets everything through", domain.TierNone, true, true, true},
		{"dependency keeps both marks", domain.TierDependency, true, true, false},
		{"security keeps only security", domain.TierSecurity, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := domain.Filter{MinTier: tc.min}
			if got := f.Match(security); got != tc.security {
				t.Errorf("Match(security) = %v, want %v", got, tc.security)
			}
			if got := f.Match(dependency); got != tc.dependency {
				t.Errorf("Match(dependency) = %v, want %v", got, tc.dependency)
			}
			if got := f.Match(plain); got != tc.plain {
				t.Errorf("Match(plain) = %v, want %v", got, tc.plain)
			}
		})
	}
}

// A tier narrows the list, so it must make the filter non-empty: otherwise the page would say
// "Nothing open" where it means "nothing matches this filter" (FR-2.1 AC3).
func TestFilterWithATierIsNotEmpty(t *testing.T) {
	if (domain.Filter{MinTier: domain.TierSecurity}).Empty() {
		t.Fatal("a tier makes the filter non-empty")
	}
}
