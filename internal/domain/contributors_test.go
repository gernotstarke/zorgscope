package domain

import (
	"reflect"
	"testing"
	"time"
)

func TestBuildContributorsGroupsAndOrders(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 9, 1, h, 0, 0, 0, time.UTC) }
	items := []Item{
		{Kind: KindIssue, Repo: "o/b", Author: "Zed", UpdatedAt: at(1)},
		{Kind: KindPR, Repo: "o/a", Author: "amy", UpdatedAt: at(5)},
		{Kind: KindIssue, Repo: "o/b", Author: "amy", UpdatedAt: at(2)},
		{Kind: KindIssue, Repo: "o/c", Author: "", UpdatedAt: at(9)},
		{Kind: KindPR, Repo: "o/a", Author: "bob", UpdatedAt: at(3)},
		{Kind: KindIssue, Repo: "o/a", Author: "bob", UpdatedAt: at(4)},
		{Kind: KindIssue, Repo: "o/a", Author: "", UpdatedAt: at(1)},
	}
	got := BuildContributors(items, []string{"o/a", "o/b"})
	want := []Contributor{
		{Login: "amy", PRs: 1, Issues: 1, Repos: []string{"o/a", "o/b"}, LastActive: at(5)},
		{Login: "bob", PRs: 1, Issues: 1, Repos: []string{"o/a"}, LastActive: at(4)},
		{Login: "Zed", Issues: 1, Repos: []string{"o/b"}, LastActive: at(1)},
		{Login: "", Issues: 2, Repos: []string{"o/a", "o/c"}, LastActive: at(9)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildContributors =\n%+v\nwant\n%+v", got, want)
	}
	if BuildContributors(nil, nil) != nil {
		t.Error("no items should give nil")
	}
}

func TestBuildContributorsBreaksTiesByLoginCaseInsensitively(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	got := BuildContributors([]Item{
		{Kind: KindIssue, Repo: "o/a", Author: "bob", UpdatedAt: now},
		{Kind: KindIssue, Repo: "o/a", Author: "Alice", UpdatedAt: now},
	}, nil)
	if got[0].Login != "Alice" || got[1].Login != "bob" {
		t.Errorf("order = %s, %s", got[0].Login, got[1].Login)
	}
}

// BuildContributors' doc comment promises purity; this is what keeps the promise true.
func TestBuildContributorsNeverMutates(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	items := []Item{
		{Kind: KindPR, Repo: "o/b", Author: "bob", Labels: []string{"bug"}, UpdatedAt: now},
		{Kind: KindIssue, Repo: "o/a", Author: "amy", UpdatedAt: now.Add(-time.Hour)},
	}
	before := append([]Item(nil), items...)
	BuildContributors(items, []string{"o/a", "o/b"})
	if !reflect.DeepEqual(items, before) {
		t.Error("BuildContributors reordered or changed the caller's items")
	}
}
