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
