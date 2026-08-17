package app

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/clock"
	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/ports/memstore"
)

type runtimeFetcher struct{ id string }

func (f runtimeFetcher) ID() string                                 { return f.id }
func (runtimeFetcher) Kind() string                                 { return ports.KindGitHubRepo }
func (runtimeFetcher) Fetch(context.Context) ([]domain.Item, error) { return nil, nil }

func runtimeConfig(t *testing.T, repo string) *config.Config {
	t.Helper()
	y := "server:\n  base_url: http://localhost:8080\ngithub:\n  enabled: true\n  me: zorg\n  poll_interval: 1h\n  repos: [" + repo + "]\n"
	cfg, err := config.Parse(strings.NewReader(y), func(k string) string {
		if k == "GITHUB_TOKEN" {
			return "token"
		}
		if k == "AUTH_MODE" {
			return "dev"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestRuntimeAppliesNewGeneration(t *testing.T) {
	st := memstore.New()
	reg := NewRegistry()
	reg.Register(ports.KindGitHubRepo, func(d Deps) ([]Source, error) {
		return []Source{{Fetcher: runtimeFetcher{id: "github:" + d.Cfg.GitHub.Repos[0].Name}, Interval: time.Hour}}, nil
	})
	rt := NewRuntime(st, clock.NewFake(time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)), reg, &http.Client{}, nil, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rt.Start(ctx, runtimeConfig(t, "a/one")); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := rt.Config(); cfg.GitHub.Repos[0].Name != "a/one" {
		t.Fatalf("first config: %+v", cfg.GitHub.Repos)
	}
	if err := rt.Apply(runtimeConfig(t, "b/two")); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := rt.Config(); cfg.GitHub.Repos[0].Name != "b/two" {
		t.Fatalf("updated config: %+v", cfg.GitHub.Repos)
	}
	rt.Close()
}
