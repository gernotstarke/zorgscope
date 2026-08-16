package app

import (
	"context"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/clock"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports/memstore"
)

func TestDismissAll(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	clk := clock.NewFake(t0)
	evs := []domain.Evaluated{
		{Item: domain.Item{ID: domain.ItemID{SourceID: "s", ExternalID: "a"}, UpdatedAt: t0.Add(-time.Hour)}},
		{Item: domain.Item{ID: domain.ItemID{SourceID: "s", ExternalID: "b"}, UpdatedAt: t0.Add(-2 * time.Hour)}},
	}
	if err := DismissAll(ctx, st, clk, evs); err != nil {
		t.Fatal(err)
	}
	all, _ := st.Dismissals(ctx)
	if len(all) != 2 || !all[evs[1].Item.ID].DismissedAt.Equal(t0) || !all[evs[1].Item.ID].UpdatedAt.Equal(evs[1].Item.UpdatedAt) {
		t.Fatalf("dismissals = %+v", all)
	}
}
