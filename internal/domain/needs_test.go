package domain

import (
	"testing"
	"time"
)

// FR-1.14 AC1: each reason, the loudest winning, and the items that need nobody.
func TestNeedForClassifiesByTheLoudestReason(t *testing.T) {
	const owner = "gernotstarke"
	tests := []struct {
		name string
		it   Item
		want Need
	}{
		{"an advisory makes any item security", Item{Kind: KindIssue, Advisories: []string{"CVE-2026-1"}}, NeedSecurity},
		{"a Dependabot bump citing a CVE is security, not a contribution", Item{Kind: KindPR, Author: "dependabot", Advisories: []string{"GHSA-x"}}, NeedSecurity},
		{"a plain bump is dependency", Item{Kind: KindPR, Author: "dependabot"}, NeedDependency},
		{"the owner's own marked issue still counts", Item{Kind: KindIssue, Author: owner, Labels: []string{"security"}}, NeedSecurity},
		{"a review request for the owner, in any case", Item{Kind: KindPR, Author: "amy", ReviewRequested: []string{"bob", "GernotStarke"}}, NeedReview},
		{"a review request on the owner's own draft", Item{Kind: KindPR, Author: owner, State: "DRAFT", ReviewRequested: []string{owner}}, NeedReview},
		{"someone else's ready pull request", Item{Kind: KindPR, Author: "amy"}, NeedContribution},
		{"someone else's draft waits on its author", Item{Kind: KindPR, Author: "amy", State: "DRAFT"}, NeedNone},
		{"the owner's own pull request", Item{Kind: KindPR, Author: owner}, NeedNone},
		{"a review asked of someone else only", Item{Kind: KindPR, Author: owner, ReviewRequested: []string{"amy"}}, NeedNone},
		{"an unmarked issue", Item{Kind: KindIssue, Author: "amy"}, NeedNone},
	}
	for _, tc := range tests {
		if got := NeedFor(tc.it, owner); got != tc.want {
			t.Errorf("%s: NeedFor = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Without an owner nobody's pull request is a contribution; only the marked items need anyone.
func TestNeedForWithoutAnOwnerKeepsOnlyTheMarkedItems(t *testing.T) {
	if got := NeedFor(Item{Kind: KindPR, Author: "amy"}, ""); got != NeedNone {
		t.Errorf("contribution without an owner = %v, want none", got)
	}
	if got := NeedFor(Item{Kind: KindPR, Author: "renovate"}, ""); got != NeedDependency {
		t.Errorf("bump without an owner = %v, want dependency", got)
	}
}

// FR-1.14 AC2: loudest reason first, most recently updated first within a reason, nothing else.
func TestBuildNeedsYouOrdersByReasonThenRecency(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	items := []Item{
		{Number: 1, Kind: KindPR, Author: "amy", UpdatedAt: now.Add(-time.Hour)},
		{Number: 2, Kind: KindIssue, Author: "amy", UpdatedAt: now},
		{Number: 3, Kind: KindPR, Author: "dependabot", UpdatedAt: now.Add(-3 * time.Hour)},
		{Number: 4, Kind: KindPR, Author: "bob", UpdatedAt: now},
		{Number: 5, Kind: KindPR, Author: "amy", ReviewRequested: []string{"me"}, UpdatedAt: now.Add(-9 * time.Hour)},
		{Number: 6, Kind: KindIssue, Labels: []string{"Security"}, UpdatedAt: now.Add(-48 * time.Hour)},
	}
	got := BuildNeedsYou(items, "me")
	want := []int{6, 3, 5, 4, 1}
	if len(got) != len(want) {
		t.Fatalf("BuildNeedsYou returned %d items, want %d", len(got), len(want))
	}
	for i, n := range want {
		if got[i].Item.Number != n {
			t.Errorf("position %d = #%d, want #%d", i, got[i].Item.Number, n)
		}
	}
	if items[0].Number != 1 {
		t.Error("BuildNeedsYou reordered its input")
	}
}
