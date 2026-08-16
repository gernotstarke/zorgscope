package github

import (
	"context"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	githubfake "github.com/gernotstarke/zorgscope/test/fakes/github"
)

func TestMentionsFetcher(t *testing.T) {
	fs, srv := newFake(t)
	fs.SetNotifications([]githubfake.Notification{
		{ID: "1", Reason: "mention", SubjectTitle: "Please look", SubjectType: "Issue", SubjectURL: "https://api.github.com/repos/x/y/issues/7", Repo: "x/y", UpdatedAt: now0, Unread: true},
		{ID: "2", Reason: "review_requested", SubjectTitle: "PR", SubjectType: "PullRequest", SubjectURL: "https://api.github.com/repos/x/y/pulls/8", Repo: "x/y", UpdatedAt: now0.Add(-time.Hour)},
		{ID: "3", Reason: "subscribed", SubjectTitle: "noise", SubjectType: "Issue", SubjectURL: "https://api.github.com/repos/x/y/issues/9", Repo: "x/y", UpdatedAt: now0},
		{ID: "4", Reason: "mention", SubjectTitle: "monitored", SubjectType: "Issue", SubjectURL: "https://api.github.com/repos/arc42/arc42-template/issues/240", Repo: "arc42/arc42-template", UpdatedAt: now0},
	})
	c := NewClient(srv.Client(), srv.URL, "tok", nil)
	f := NewMentionsFetcher(c, []string{"arc42/arc42-template"})
	if f.ID() != "github:mentions" || f.Kind() != ports.KindGitHubMentions {
		t.Fatalf("id/kind %s %s", f.ID(), f.Kind())
	}
	items, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	m := byExt(items)
	one := m["1"]
	if one.Kind != domain.KindMention || one.URL != "https://github.com/x/y/issues/7" || one.Title != "Please look" || !one.CreatedAt.Equal(now0) {
		t.Fatalf("mention 1 = %+v", one)
	}
	two := m["2"]
	if two.URL != "https://github.com/x/y/pull/8" {
		t.Fatalf("pull url conversion: %s", two.URL)
	}
	p, _ := domain.DecodePayload[domain.MentionPayload](two)
	if p.Reason != "review_requested" || p.Repo != "x/y" || p.SubjectType != "PullRequest" {
		t.Fatalf("payload = %+v", p)
	}
}
