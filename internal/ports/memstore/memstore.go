// Package memstore is an in-memory ports.Store for tests and fakes.
package memstore

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// Store implements ports.Store in memory. Safe for concurrent use.
type Store struct {
	mu         sync.RWMutex
	items      map[string]map[string]domain.Item // sourceID → externalID → item
	snapshots  map[string][]domain.Snapshot      // sourceID → sorted by Date asc
	dismissals map[domain.ItemID]domain.Dismissal
	statuses   map[string]domain.FetchStatus
}

var _ ports.Store = (*Store)(nil)

// New returns an empty store.
func New() *Store {
	return &Store{items: map[string]map[string]domain.Item{}, snapshots: map[string][]domain.Snapshot{},
		dismissals: map[domain.ItemID]domain.Dismissal{}, statuses: map[string]domain.FetchStatus{}}
}

// ReplaceItems implements ports.Store.
func (s *Store) ReplaceItems(_ context.Context, sourceID string, items []domain.Item, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.items[sourceID]
	next := make(map[string]domain.Item, len(items))
	for _, it := range items {
		if prev, ok := old[it.ID.ExternalID]; ok {
			it.FirstSeen = prev.FirstSeen
		} else {
			it.FirstSeen = now
		}
		next[it.ID.ExternalID] = it
	}
	s.items[sourceID] = next
	return nil
}

func sortNewest(list []domain.Item) {
	sort.SliceStable(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
}

// Items implements ports.Store.
func (s *Store) Items(_ context.Context, sourceID string) ([]domain.Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Item, 0, len(s.items[sourceID]))
	for _, it := range s.items[sourceID] {
		out = append(out, it)
	}
	sortNewest(out)
	return out, nil
}

// AllItems implements ports.Store.
func (s *Store) AllItems(_ context.Context) ([]domain.Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Item
	for _, m := range s.items {
		for _, it := range m {
			out = append(out, it)
		}
	}
	sortNewest(out)
	return out, nil
}

// ExternalIDs implements ports.Store.
func (s *Store) ExternalIDs(_ context.Context, sourceID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.items[sourceID]))
	for id := range s.items[sourceID] {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// PutSnapshot implements ports.Store.
func (s *Store) PutSnapshot(_ context.Context, snap domain.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.snapshots[snap.SourceID]
	replaced := false
	for i := range list {
		if list[i].Date == snap.Date {
			list[i] = snap
			replaced = true
		}
	}
	if !replaced {
		list = append(list, snap)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Date < list[j].Date })
	s.snapshots[snap.SourceID] = list
	return nil
}

// LatestSnapshot implements ports.Store.
func (s *Store) LatestSnapshot(_ context.Context, sourceID string) (*domain.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.snapshots[sourceID]
	if len(list) == 0 {
		return nil, nil
	}
	snap := list[len(list)-1]
	return &snap, nil
}

// SnapshotBefore implements ports.Store.
func (s *Store) SnapshotBefore(_ context.Context, sourceID, date string) (*domain.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.snapshots[sourceID]
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].Date < date {
			snap := list[i]
			return &snap, nil
		}
	}
	return nil, nil
}

// PruneSnapshots implements ports.Store.
func (s *Store) PruneSnapshots(_ context.Context, beforeDate string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for src, list := range s.snapshots {
		kept := list[:0]
		for _, snap := range list {
			if snap.Date >= beforeDate {
				kept = append(kept, snap)
			}
		}
		s.snapshots[src] = kept
	}
	return nil
}

// PutDismissal implements ports.Store.
func (s *Store) PutDismissal(_ context.Context, d domain.Dismissal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dismissals[d.ID] = d
	return nil
}

// Dismissal implements ports.Store.
func (s *Store) Dismissal(_ context.Context, id domain.ItemID) (*domain.Dismissal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.dismissals[id]
	if !ok {
		return nil, nil
	}
	return &d, nil
}

// Dismissals implements ports.Store.
func (s *Store) Dismissals(_ context.Context) (map[domain.ItemID]domain.Dismissal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[domain.ItemID]domain.Dismissal, len(s.dismissals))
	for k, v := range s.dismissals {
		out[k] = v
	}
	return out, nil
}

// RecordStatus implements ports.Store.
func (s *Store) RecordStatus(_ context.Context, st domain.FetchStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses[st.SourceID] = st
	return nil
}

// Status implements ports.Store.
func (s *Store) Status(_ context.Context, sourceID string) (*domain.FetchStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.statuses[sourceID]
	if !ok {
		return nil, nil
	}
	return &st, nil
}

// Statuses implements ports.Store.
func (s *Store) Statuses(_ context.Context) ([]domain.FetchStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.FetchStatus, 0, len(s.statuses))
	for _, st := range s.statuses {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SourceID < out[j].SourceID })
	return out, nil
}
