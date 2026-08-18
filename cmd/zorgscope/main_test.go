package main

import (
	"io"
	"log/slog"
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

// The nil buildNotifier returns has to stay an untyped one.
//
// The runner reads "no notifier" as r.notifier == nil, and a (*slack.Notifier)(nil) handed back
// through a ports.Notifier interface is *not* nil: the check would pass it through and the first
// refresh would dereference it and panic. Nothing but a test pins that, because changing the
// return type to *slack.Notifier compiles perfectly well and breaks it.
func TestBuildNotifierReturnsATrulyNilNotifierWhenNothingShouldBeAnnounced(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	var off config.Config // notifications not switched on
	if got := buildNotifier(off, nil, log); got != nil {
		t.Errorf("buildNotifier with notifications off = %v, want nil", got)
	}

	var noWebhook config.Config
	noWebhook.Notifications.Slack.Enabled = true
	if got := buildNotifier(noWebhook, nil, log); got != nil {
		t.Errorf("buildNotifier without a webhook = %v, want nil: the switch alone posts nowhere", got)
	}

	// Both halves present: the notifier is built. A test that only checked the nil cases would
	// pass for a buildNotifier that never returns one at all.
	var on config.Config
	on.Notifications.Slack.Enabled = true
	on.Secrets.SlackWebhook = "https://hooks.slack.test/services/T0/B0/x"
	if got := buildNotifier(on, nil, log); got == nil {
		t.Error("buildNotifier with the switch and a webhook returned nil")
	}
}
