// Package config loads zorgscope's non-secret YAML configuration (FR-8.1) and overlays it with
// secrets read from the environment (FR-8.2). Config values, once loaded, are validated so that a
// bad configuration fails at start-up rather than mid-refresh.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"
	_ "time/tzdata" // the distroless runtime image (deploy/Dockerfile) ships no /usr/share/zoneinfo

	"gopkg.in/yaml.v3"
)

// Config is the fully validated, ready-to-use application configuration.
type Config struct {
	Timezone      string
	Refresh       Refresh
	GitHub        GitHub
	Plausible     Plausible
	Todoist       Todoist
	Notifications Notifications
	Secrets       Secrets
}

// Refresh controls the freshness display; it does not itself schedule anything (ADR-0003).
type Refresh struct {
	Interval   time.Duration
	StaleAfter time.Duration
}

// GitHub is the non-secret GitHub configuration.
type GitHub struct {
	Login   string
	Repos   []string
	BaseURL string // "" means api.github.com; make fakes sets this via GITHUB_BASE_URL.
}

// Plausible is the non-secret Plausible configuration.
type Plausible struct {
	Sites   []string
	BaseURL string
}

// Todoist is the non-secret Todoist configuration.
type Todoist struct {
	Filter  string
	BaseURL string
}

// Notifications holds outbound notification settings.
type Notifications struct {
	Slack struct{ Enabled bool }
}

// Secrets holds every value read from the environment. None of these are ever logged, rendered,
// or included in an error message (QS-4.3).
type Secrets struct {
	GitHubToken, PlausibleKey, TodoistToken, SlackWebhook string
	AppToken, RefreshSecret                               string
	TursoURL, TursoAuthToken                              string
}

// fileConfig mirrors the shape of the YAML file. Durations are strings here so that a malformed
// value can be rejected with a field-naming error rather than failing YAML decoding generically.
type fileConfig struct {
	Timezone string `yaml:"timezone"`
	Refresh  struct {
		Interval   string `yaml:"interval"`
		StaleAfter string `yaml:"stale_after"`
	} `yaml:"refresh"`
	GitHub struct {
		Login string   `yaml:"login"`
		Repos []string `yaml:"repos"`
	} `yaml:"github"`
	Plausible struct {
		Sites []string `yaml:"sites"`
	} `yaml:"plausible"`
	Todoist struct {
		Filter string `yaml:"filter"`
	} `yaml:"todoist"`
	Notifications struct {
		Slack struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"slack"`
	} `yaml:"notifications"`
}

// repoPattern matches a GitHub "owner/name" repository reference.
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// minSecretLen is the minimum length required of ZORGSCOPE_TOKEN and REFRESH_SECRET.
const minSecretLen = 32

// Load reads the YAML file at path, overlays secrets from env, and validates the result.
//
// env is injected rather than reading os.Getenv directly so tests need no process environment.
func Load(path string, env func(string) string) (Config, error) {
	// #nosec G304 -- path is supplied by the caller (CONFIG_PATH / a flag), not by an end user.
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("reading %s: %w", path, err)
	}

	var fc fileConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&fc); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", path, err)
	}

	interval, err := time.ParseDuration(fc.Refresh.Interval)
	if err != nil {
		return Config{}, fmt.Errorf("refresh.interval: %w", err)
	}
	staleAfter, err := time.ParseDuration(fc.Refresh.StaleAfter)
	if err != nil {
		return Config{}, fmt.Errorf("refresh.stale_after: %w", err)
	}

	if _, err := time.LoadLocation(fc.Timezone); err != nil {
		return Config{}, fmt.Errorf("timezone: %w", err)
	}

	for i, repo := range fc.GitHub.Repos {
		if !repoPattern.MatchString(repo) {
			return Config{}, fmt.Errorf("github.repos[%d]: %q is not in owner/name form", i, repo)
		}
	}

	cfg := Config{
		Timezone: fc.Timezone,
		Refresh: Refresh{
			Interval:   interval,
			StaleAfter: staleAfter,
		},
		GitHub: GitHub{
			Login:   fc.GitHub.Login,
			Repos:   fc.GitHub.Repos,
			BaseURL: env("GITHUB_BASE_URL"),
		},
		Plausible: Plausible{
			Sites:   fc.Plausible.Sites,
			BaseURL: env("PLAUSIBLE_BASE_URL"),
		},
		Todoist: Todoist{
			Filter:  fc.Todoist.Filter,
			BaseURL: env("TODOIST_BASE_URL"),
		},
		Secrets: Secrets{
			GitHubToken:    env("GITHUB_TOKEN"),
			PlausibleKey:   env("PLAUSIBLE_API_KEY"),
			TodoistToken:   env("TODOIST_TOKEN"),
			SlackWebhook:   env("SLACK_WEBHOOK_URL"),
			AppToken:       env("ZORGSCOPE_TOKEN"),
			RefreshSecret:  env("REFRESH_SECRET"),
			TursoURL:       env("TURSO_URL"),
			TursoAuthToken: env("TURSO_AUTH_TOKEN"),
		},
	}
	cfg.Notifications.Slack.Enabled = fc.Notifications.Slack.Enabled

	if len(cfg.Secrets.AppToken) < minSecretLen {
		return Config{}, errors.New("ZORGSCOPE_TOKEN must be at least 32 characters")
	}
	if len(cfg.Secrets.RefreshSecret) < minSecretLen {
		return Config{}, errors.New("REFRESH_SECRET must be at least 32 characters")
	}

	return cfg, nil
}

// Enabled reports whether the named source has its credential (FR-8.2 AC2). An unknown source
// name reports false. A source with a credential but nothing configured to watch (no repos, no
// sites, no filter) also reports false: there would be nothing for it to do.
func (c Config) Enabled(source string) bool {
	switch source {
	case "github":
		return c.Secrets.GitHubToken != "" && len(c.GitHub.Repos) > 0
	case "plausible":
		return c.Secrets.PlausibleKey != "" && len(c.Plausible.Sites) > 0
	case "todoist":
		return c.Secrets.TodoistToken != "" && c.Todoist.Filter != ""
	default:
		return false
	}
}
