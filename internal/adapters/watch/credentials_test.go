package watch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/clock"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

var watchNow = time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

func TestCredentialsFetcherMergesConfiguredAndAutoDetectedCredentials(t *testing.T) {
	manualExpiry := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	detectedExpiry := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	f, err := NewCredentialsFetcher([]Credential{
		{Name: "Plausible API key", Expires: &manualExpiry, WarnDays: 30, UsedBy: "analytics", URL: "https://plausible.io/settings/api-keys"},
		{Name: "zorgscope GitHub token", Expires: &manualExpiry, URL: "https://github.com/settings/tokens"},
	}, func() []Credential {
		return []Credential{{Name: "zorgscope GitHub token", Expires: &detectedExpiry, UsedBy: "zorgscope"}}
	}, 14, clock.NewFake(watchNow))
	if err != nil {
		t.Fatal(err)
	}
	if f.ID() != "watch:credentials" || f.Kind() != ports.KindWatchCredentials {
		t.Fatalf("id/kind = %s/%s", f.ID(), f.Kind())
	}
	items, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Title != "zorgscope GitHub token" || items[1].Title != "Plausible API key" {
		t.Fatalf("sorted items = %+v", items)
	}
	byTitle := map[string]domain.Item{}
	for _, item := range items {
		byTitle[item.Title] = item
		if item.Kind != domain.KindCredential || item.ID.SourceID != f.ID() || item.CreatedAt != watchNow {
			t.Fatalf("item = %+v", item)
		}
	}
	githubPayload, _ := domain.DecodePayload[domain.CredentialPayload](byTitle["zorgscope GitHub token"])
	if githubPayload.Expires == nil || !githubPayload.Expires.Equal(detectedExpiry) || githubPayload.WarnDays != 14 || !githubPayload.AutoDetected || githubPayload.URL != "https://github.com/settings/tokens" {
		t.Fatalf("merged GitHub payload = %+v", githubPayload)
	}
	if !byTitle["zorgscope GitHub token"].UpdatedAt.Equal(detectedExpiry) {
		t.Fatalf("updated_at must track expiry for dismissal invalidation: %s", byTitle["zorgscope GitHub token"].UpdatedAt)
	}
	plausiblePayload, _ := domain.DecodePayload[domain.CredentialPayload](byTitle["Plausible API key"])
	if plausiblePayload.WarnDays != 30 || plausiblePayload.AutoDetected {
		t.Fatalf("manual payload = %+v", plausiblePayload)
	}
}

func TestNewCredentialsFetcherValidatesInputs(t *testing.T) {
	if _, err := NewCredentialsFetcher(nil, nil, 0, clock.NewFake(watchNow)); !errors.Is(err, ports.ErrPermanent) {
		t.Fatalf("warn days error = %v", err)
	}
	if _, err := NewCredentialsFetcher([]Credential{{Name: "bad|name"}}, nil, 14, clock.NewFake(watchNow)); !errors.Is(err, ports.ErrPermanent) {
		t.Fatalf("name error = %v", err)
	}
	if _, err := NewCredentialsFetcher(nil, nil, 14, nil); !errors.Is(err, ports.ErrPermanent) {
		t.Fatalf("clock error = %v", err)
	}
}
