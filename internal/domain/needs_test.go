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
		{"someone else's draft is still a contribution", Item{Kind: KindPR, Author: "amy", State: "DRAFT"}, NeedContribution},
		{"the owner's own pull request, in any case", Item{Kind: KindPR, Author: "GernotStarke"}, NeedOwn},
		{"the owner's own draft", Item{Kind: KindPR, Author: owner, State: "DRAFT"}, NeedOwn},
		{"a review asked of someone else only", Item{Kind: KindPR, Author: owner, ReviewRequested: []string{"amy"}}, NeedOwn},
		{"an unmarked issue", Item{Kind: KindIssue, Author: "amy"}, NeedNone},
	}
	for _, tc := range tests {
		if got := NeedFor(tc.it, owner); got != tc.want {
			t.Errorf("%s: NeedFor = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Without an owner every pull request is a contribution: nobody's can be the owner's own, and none
// can ask the owner for a review.
func TestNeedForWithoutAnOwner(t *testing.T) {
	if got := NeedFor(Item{Kind: KindPR, Author: "amy"}, ""); got != NeedContribution {
		t.Errorf("pull request without an owner = %v, want contribution", got)
	}
	if got := NeedFor(Item{Kind: KindIssue, Author: "amy"}, ""); got != NeedNone {
		t.Errorf("issue without an owner = %v, want none", got)
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

// FR-1.14 AC10: six months without activity and an item no longer needs the owner now — except a
// Security item, which never goes stale, and an item whose update time is unknown.
func TestStaleNeedsAfterSixMonthsExceptSecurity(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-NeedsWindow)
	old := now.Add(-NeedsWindow - time.Hour)
	tests := []struct {
		name string
		n    Needed
		want bool
	}{
		{"a contribution exactly six months old", Needed{Item{UpdatedAt: fresh}, NeedContribution}, false},
		{"a contribution just older", Needed{Item{UpdatedAt: old}, NeedContribution}, true},
		{"an old review request", Needed{Item{UpdatedAt: old}, NeedReview}, true},
		{"an old bump", Needed{Item{UpdatedAt: old}, NeedDependency}, true},
		{"an old vulnerability", Needed{Item{UpdatedAt: old}, NeedSecurity}, false},
		{"an unknown update time", Needed{Item{}, NeedContribution}, false},
	}
	for _, tc := range tests {
		if got := tc.n.Stale(now); got != tc.want {
			t.Errorf("%s: Stale = %v, want %v", tc.name, got, tc.want)
		}
	}
	if NeedsNow(Item{Kind: KindPR, Author: "amy", UpdatedAt: old}, "me", now) {
		t.Error("NeedsNow holds for a stale contribution")
	}
	if !NeedsNow(Item{Kind: KindPR, Author: "amy", UpdatedAt: fresh}, "me", now) {
		t.Error("NeedsNow does not hold for a fresh contribution")
	}
}
