package main

import (
	"testing"

	"github.com/gernotstarke/zorgscope/internal/config"
)

func TestLogLevelFallsBackToInfo(t *testing.T) {
	t.Setenv("LOG_LEVEL", "not-a-level")
	if got := logLevel().String(); got != "INFO" {
		t.Errorf("logLevel() = %s, want INFO", got)
	}
}

// GITHUB_BASE_URL names an API root, and an unset one must leave BaseURL empty so the adapter
// falls back to its own real GitHub default. Getting this wrong would send production traffic to
// the relative path "/graphql".
func TestGitHubConfigDerivesTheGraphQLEndpoint(t *testing.T) {
	tests := []struct {
		name        string
		base        string
		wantGraphQL string
	}{
		{"unset means the real GitHub", "", ""},
		{"fake sources", "http://localhost:9090", "http://localhost:9090/graphql"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{GitHub: config.GitHub{BaseURL: tc.base, Repos: []string{"org/repo"}}}
			got := githubConfig(cfg)
			if got.BaseURL != tc.wantGraphQL {
				t.Errorf("BaseURL = %q, want %q", got.BaseURL, tc.wantGraphQL)
			}
			if got.RESTBaseURL != tc.base {
				t.Errorf("RESTBaseURL = %q, want %q", got.RESTBaseURL, tc.base)
			}
		})
	}
}
