package app

import (
	"sort"
	"sync"
	"time"

	"github.com/gernotstarke/zorgscope/internal/ports"
)

// Credential is an expiry reported by an adapter (FR-11.2).
type Credential struct {
	Name       string
	Expires    *time.Time
	UsedBy     string
	ReportedAt time.Time
}

// CredentialBook collects auto-detected credential expiries; the watch source (M2) reads them.
type CredentialBook struct {
	mu      sync.Mutex
	clock   ports.Clock
	entries map[string]Credential
}

var _ ports.CredentialSink = (*CredentialBook)(nil)

// NewCredentialBook creates an empty book.
func NewCredentialBook(clock ports.Clock) *CredentialBook {
	return &CredentialBook{clock: clock, entries: map[string]Credential{}}
}

// ReportCredential implements ports.CredentialSink (idempotent by name).
func (b *CredentialBook) ReportCredential(name string, expires *time.Time, usedBy string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries[name] = Credential{Name: name, Expires: expires, UsedBy: usedBy, ReportedAt: b.clock.Now()}
}

// List returns all credentials sorted by name.
func (b *CredentialBook) List() []Credential {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Credential, 0, len(b.entries))
	for _, c := range b.entries {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
