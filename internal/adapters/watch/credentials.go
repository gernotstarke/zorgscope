// Package watch implements credential-expiry and URL-health source adapters.
package watch

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// Credential is the adapter-boundary representation of a configured or automatically detected
// credential. Wiring maps config credentials and app.CredentialBook entries to this type so this
// adapter retains the documented domain/ports-only dependency direction.
type Credential struct {
	Name         string
	Expires      *time.Time
	WarnDays     int
	UsedBy       string
	URL          string
	AutoDetected bool
}

// CredentialsFetcher publishes configured credentials plus the latest auto-detected credentials.
type CredentialsFetcher struct {
	manual   []Credential
	auto     func() []Credential
	warnDays int
	clock    ports.Clock
}

// NewCredentialsFetcher creates the single credential-watch source. auto may be nil. The callback
// is evaluated on every fetch because app.CredentialBook is updated by source adapters at runtime.
func NewCredentialsFetcher(manual []Credential, auto func() []Credential, warnDays int, clock ports.Clock) (*CredentialsFetcher, error) {
	if clock == nil {
		return nil, fmt.Errorf("%w: credential watch clock is required", ports.ErrPermanent)
	}
	if warnDays <= 0 {
		return nil, fmt.Errorf("%w: credential warning days must be positive", ports.ErrPermanent)
	}
	copyManual := append([]Credential(nil), manual...)
	for i := range copyManual {
		if strings.TrimSpace(copyManual[i].Name) == "" || strings.Contains(copyManual[i].Name, "|") {
			return nil, fmt.Errorf("%w: credential name is empty or contains the reserved '|' character", ports.ErrPermanent)
		}
	}
	return &CredentialsFetcher{manual: copyManual, auto: auto, warnDays: warnDays, clock: clock}, nil
}

// ID implements ports.SourceFetcher.
func (*CredentialsFetcher) ID() string { return "watch:credentials" }

// Kind implements ports.SourceFetcher.
func (*CredentialsFetcher) Kind() string { return ports.KindWatchCredentials }

// Fetch implements ports.SourceFetcher.
func (f *CredentialsFetcher) Fetch(context.Context) ([]domain.Item, error) {
	merged := make(map[string]Credential, len(f.manual))
	for _, credential := range f.manual {
		merged[credential.Name] = credential
	}
	if f.auto != nil {
		for _, detected := range f.auto() {
			if strings.TrimSpace(detected.Name) == "" || strings.Contains(detected.Name, "|") {
				continue
			}
			detected.AutoDetected = true
			if configured, ok := merged[detected.Name]; ok {
				configured.Expires = detected.Expires
				configured.AutoDetected = true
				if detected.UsedBy != "" {
					configured.UsedBy = detected.UsedBy
				}
				merged[detected.Name] = configured
			} else {
				merged[detected.Name] = detected
			}
		}
	}
	credentials := make([]Credential, 0, len(merged))
	for _, credential := range merged {
		credentials = append(credentials, credential)
	}
	sort.Slice(credentials, func(i, j int) bool {
		left, right := credentials[i].Expires, credentials[j].Expires
		if left == nil || right == nil {
			if left == nil && right == nil {
				return credentials[i].Name < credentials[j].Name
			}
			return right == nil
		}
		if left.Equal(*right) {
			return credentials[i].Name < credentials[j].Name
		}
		return left.Before(*right)
	})

	now := f.clock.Now().UTC()
	items := make([]domain.Item, 0, len(credentials))
	for _, credential := range credentials {
		warnDays := credential.WarnDays
		if warnDays <= 0 {
			warnDays = f.warnDays
		}
		updated := time.Time{}
		if credential.Expires != nil {
			updated = credential.Expires.UTC()
		}
		items = append(items, domain.Item{
			ID:        domain.ItemID{SourceID: f.ID(), ExternalID: url.QueryEscape(credential.Name)},
			Kind:      domain.KindCredential,
			Title:     credential.Name,
			URL:       credential.URL,
			CreatedAt: now,
			UpdatedAt: updated,
			Payload: domain.MustPayload(domain.CredentialPayload{
				Expires: credential.Expires, WarnDays: warnDays, UsedBy: credential.UsedBy,
				URL: credential.URL, AutoDetected: credential.AutoDetected,
			}),
		})
	}
	return items, nil
}
