package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

func TestLogLevelFallsBackToInfo(t *testing.T) {
	t.Setenv("LOG_LEVEL", "not-a-level")
	if got := logLevel().String(); got != "INFO" {
		t.Errorf("logLevel() = %s, want INFO", got)
	}
}

// The two GitHub base URLs name different things — a GraphQL endpoint and a REST root — and an
// unset GITHUB_BASE_URL must leave both empty so the adapters use their own real defaults. Getting
// this wrong would send production traffic to the relative path "/graphql".
func TestGitHubConfigDerivesBothBaseURLs(t *testing.T) {
	tests := []struct {
		name            string
		base            string
		wantGraphQL     string
		wantRESTBaseURL string
	}{
		{"unset means the real GitHub", "", "", ""},
		{"fake sources", "http://localhost:9090", "http://localhost:9090/graphql", "http://localhost:9090"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{GitHub: config.GitHub{BaseURL: tc.base, Repos: []string{"org/repo"}}}
			got := githubConfig(cfg)
			if got.BaseURL != tc.wantGraphQL {
				t.Errorf("BaseURL = %q, want %q", got.BaseURL, tc.wantGraphQL)
			}
			if got.RESTBaseURL != tc.wantRESTBaseURL {
				t.Errorf("RESTBaseURL = %q, want %q", got.RESTBaseURL, tc.wantRESTBaseURL)
			}
		})
	}
}

// FR-8.2 AC2: a source without its credential is absent from the refresh, not present and failing.
func TestBuildFetchersOnlyBuildsEnabledSources(t *testing.T) {
	full := config.Config{
		GitHub:    config.GitHub{Repos: []string{"org/repo"}},
		Plausible: config.Plausible{Sites: []string{"example.org"}},
		Todoist:   config.Todoist{Filter: "overdue | today"},
		Secrets: config.Secrets{
			GitHubToken:  "gh",
			PlausibleKey: "pl",
			TodoistToken: "td",
		},
	}

	tests := []struct {
		name string
		cfg  func(config.Config) config.Config
		want []string
	}{
		{
			"everything configured",
			func(c config.Config) config.Config { return c },
			[]string{"github", "github-builds", "plausible", "todoist"},
		},
		{
			"no credentials at all",
			func(config.Config) config.Config { return config.Config{} },
			nil,
		},
		{
			"github without a token",
			func(c config.Config) config.Config { c.Secrets.GitHubToken = ""; return c },
			[]string{"plausible", "todoist"},
		},
		{
			"github without repositories",
			func(c config.Config) config.Config { c.GitHub.Repos = nil; return c },
			[]string{"plausible", "todoist"},
		},
		{
			"todoist without a filter",
			func(c config.Config) config.Config { c.Todoist.Filter = ""; return c },
			[]string{"github", "github-builds", "plausible"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fetchers := buildFetchers(tc.cfg(full), &http.Client{}, ports.SystemClock{}, time.UTC)
			var got []string
			for _, f := range fetchers {
				got = append(got, f.Name())
			}
			if len(got) != len(tc.want) {
				t.Fatalf("fetchers = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("fetchers = %v, want %v", got, tc.want)
					break
				}
			}
		})
	}
}
