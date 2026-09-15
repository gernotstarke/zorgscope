package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
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
		})
	}
}

// A failed fetch is logged once, at Warn, scrubbed of every configured secret (QS-4.3), and a
// clean fetch is not logged at all; either way the source's result passes through untouched.
func TestLoggingSourceLogsFailuresRedacted(t *testing.T) {
	const secret = "ghp_canary-token-value"
	items := []domain.Item{{Repo: "a/b", Number: 1}, {Repo: "a/b", Number: 2}}
	tests := []struct {
		name    string
		err     error
		wantLog bool
	}{
		{"a clean fetch is not logged", nil, false},
		{"a failed fetch is logged redacted", errors.New("github: 401 for token " + secret), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&buf, nil))
			inner := ports.SourceFunc(func(context.Context) ([]domain.Item, error) { return items, tc.err })

			got, err := loggingSource(inner, log, config.Secrets{GitHubToken: secret}).Fetch(context.Background())

			if len(got) != len(items) || !errors.Is(err, tc.err) {
				t.Errorf("Fetch = %d items, %v; want the source's %d items, %v", len(got), err, len(items), tc.err)
			}
			out := buf.String()
			if !tc.wantLog {
				if out != "" {
					t.Errorf("a clean fetch logged: %s", out)
				}
				return
			}
			if strings.Count(out, "\n") != 1 {
				t.Errorf("want exactly one log line, got: %s", out)
			}
			for _, want := range []string{`"level":"WARN"`, "github: 401 for token", `"items":2`} {
				if !strings.Contains(out, want) {
					t.Errorf("log line lacks %s: %s", want, out)
				}
			}
			if strings.Contains(out, secret) {
				t.Errorf("log line carries the secret (QS-4.3): %s", out)
			}
		})
	}
}
