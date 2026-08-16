package app

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

type nopFetcher struct{ id string }

func (n nopFetcher) ID() string                                   { return n.id }
func (n nopFetcher) Kind() string                                 { return "nop" }
func (n nopFetcher) Fetch(context.Context) ([]domain.Item, error) { return nil, nil }

func TestRegistryBuildsAllKinds(t *testing.T) {
	r := NewRegistry()
	r.Register("a", func(Deps) ([]Source, error) { return []Source{{Fetcher: nopFetcher{"a1"}, Interval: time.Minute}}, nil })
	r.Register("b", func(Deps) ([]Source, error) {
		return []Source{{Fetcher: nopFetcher{"b1"}, Interval: time.Minute}, {Fetcher: nopFetcher{"b2"}, Interval: 2 * time.Minute}}, nil
	})
	srcs, err := r.Build(Deps{Log: slog.Default()})
	if err != nil || len(srcs) != 3 {
		t.Fatalf("Build: %d %v", len(srcs), err)
	}
	if srcs[0].Fetcher.ID() != "a1" || srcs[2].Interval != 2*time.Minute {
		t.Fatalf("order/intervals wrong: %+v", srcs)
	}
}
