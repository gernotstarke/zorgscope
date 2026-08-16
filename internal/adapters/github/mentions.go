package github

import (
	"context"
	"strings"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// MentionsFetcher turns GitHub notifications addressed to the user into mention items (FR-2.6).
type MentionsFetcher struct {
	c       *Client
	exclude map[string]bool
}

// NewMentionsFetcher creates the fetcher; excludeRepos are monitored repos already covered by RepoFetchers.
func NewMentionsFetcher(c *Client, excludeRepos []string) *MentionsFetcher {
	ex := make(map[string]bool, len(excludeRepos))
	for _, r := range excludeRepos {
		ex[strings.ToLower(r)] = true
	}
	return &MentionsFetcher{c: c, exclude: ex}
}

// ID implements ports.SourceFetcher.
func (f *MentionsFetcher) ID() string { return "github:mentions" }

// Kind implements ports.SourceFetcher.
func (f *MentionsFetcher) Kind() string { return ports.KindGitHubMentions }

var mentionReasons = map[string]bool{"mention": true, "team_mention": true, "review_requested": true, "assign": true, "author": true}

type notification struct {
	ID        string    `json:"id"`
	Reason    string    `json:"reason"`
	Unread    bool      `json:"unread"`
	UpdatedAt time.Time `json:"updated_at"`
	Subject   struct {
		Title string `json:"title"`
		URL   string `json:"url"`
		Type  string `json:"type"`
	} `json:"subject"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// Fetch implements ports.SourceFetcher.
func (f *MentionsFetcher) Fetch(ctx context.Context) ([]domain.Item, error) {
	var list []notification
	if _, err := f.c.do(ctx, "GET", "/notifications?all=false&participating=true&per_page=50", nil, &list); err != nil {
		return nil, err
	}
	var items []domain.Item
	for _, n := range list {
		if !mentionReasons[n.Reason] || f.exclude[strings.ToLower(n.Repository.FullName)] {
			continue
		}
		items = append(items, domain.Item{
			ID: domain.ItemID{SourceID: f.ID(), ExternalID: n.ID}, Kind: domain.KindMention,
			Title: n.Subject.Title, URL: htmlURL(n.Subject.URL), CreatedAt: n.UpdatedAt, UpdatedAt: n.UpdatedAt,
			Payload: domain.MustPayload(domain.MentionPayload{Reason: n.Reason, Repo: n.Repository.FullName, SubjectType: n.Subject.Type}),
		})
	}
	return items, nil
}

// htmlURL converts an API subject URL into the web URL.
func htmlURL(api string) string {
	u := strings.Replace(api, "https://api.github.com/repos/", "https://github.com/", 1)
	return strings.Replace(u, "/pulls/", "/pull/", 1)
}
