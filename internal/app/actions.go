package app

import (
	"context"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// Dismiss records that the user has seen an item in its current state (FR-2.7).
func Dismiss(ctx context.Context, store ports.DismissalStore, clock ports.Clock, id domain.ItemID, updatedAt time.Time) error {
	return store.PutDismissal(ctx, domain.Dismissal{ID: id, UpdatedAt: updatedAt, DismissedAt: clock.Now()})
}

// DismissAll dismisses every given item (FR-2.7 AC4).
func DismissAll(ctx context.Context, store ports.DismissalStore, clock ports.Clock, evs []domain.Evaluated) error {
	for _, e := range evs {
		if err := Dismiss(ctx, store, clock, e.Item.ID, e.Item.UpdatedAt); err != nil {
			return err
		}
	}
	return nil
}
