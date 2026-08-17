package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/clock"
	"github.com/gernotstarke/zorgscope/internal/app"
	"github.com/gernotstarke/zorgscope/internal/config"
)

func TestRegisterSourcesBuildsCompleteBackendSourceSet(t *testing.T) {
	yaml := `
server:
  base_url: http://localhost:8080
github:
  enabled: true
  me: owner
  repos: [owner/repo]
plausible:
  enabled: true
  sites: [example.com]
watch:
  enabled: true
  credentials:
    - name: deployment token
      expires: 2027-01-01
  urls:
    - name: example
      url: https://example.com/health
`
	env := map[string]string{"AUTH_MODE": "dev", "GITHUB_TOKEN": "github-token", "PLAUSIBLE_API_KEY": "plausible-key"}
	cfg, err := config.Parse(strings.NewReader(yaml), func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC))
	book := app.NewCredentialBook(clk)
	reg := app.NewRegistry()
	registerSources(reg)
	sources, err := reg.Build(app.Deps{Cfg: cfg, HTTP: &http.Client{}, Clock: clk, Sink: book, Credentials: book.List})
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 5 {
		t.Fatalf("sources = %d, want repo + mentions + plausible + credentials + URL", len(sources))
	}
	wantIDs := []string{"github:owner/repo", "github:mentions", "plausible:example.com", "watch:credentials", "watch:url:example"}
	for i, want := range wantIDs {
		if got := sources[i].Fetcher.ID(); got != want {
			t.Fatalf("source[%d] = %q, want %q", i, got, want)
		}
		if sources[i].Interval <= 0 {
			t.Fatalf("source[%d] has no poll interval", i)
		}
	}
}
