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
	Login string
	// AuthRepo is the repository whose push access admits a visitor to the dashboard (FR-8.3), in
	// owner/name form. It is required, and it is deliberately not derived from Repos: the list of
	// repositories being watched is a product decision that changes often, while who may read the
	// dashboard is a security decision that should change only when someone means it to.
	AuthRepo string
	Repos    []string
	BaseURL  string // "" means api.github.com; make fakes sets this via GITHUB_BASE_URL.
	// BadgeBaseURL is where a workflow's badge image comes from; "" means shields.io. It is its
	// own setting rather than derived from BaseURL because badges are a different service
	// entirely — a deployment against real GitHub still wants real badges, and one against the
	// fixture server wants neither.
	BadgeBaseURL string // "" means img.shields.io; set via GITHUB_BADGE_BASE_URL.
	// OAuthBaseURL is where the OAuth App's authorize and token endpoints live; "" means
	// github.com. It is separate from BaseURL because those two endpoints are not on the API host
	// even at the real GitHub: the API answers at api.github.com and sign-in at github.com.
	OAuthBaseURL string // "" means github.com; set via GITHUB_OAUTH_BASE_URL.
}

// Notifications holds outbound notification settings.
type Notifications struct {
	Slack struct{ Enabled bool }
}

// Secrets holds every value read from the environment. None of these is ever logged or included
// in an error message, and none but OAuthClientID is ever rendered (QS-4.3) — the client id has to
// reach the browser, because it is half of the authorize URL a sign-in is redirected to.
type Secrets struct {
	GitHubToken, SlackWebhook string
	// OAuthClientID and OAuthClientSecret are the GitHub OAuth App this deployment signs people in
	// as (FR-8.3). The id is not really a secret — it travels in the browser's address bar — but it
	// is read from the environment beside its secret and redacted with it, because a value that is
	// only sometimes worth hiding is a value someone eventually forgets to hide.
	OAuthClientID, OAuthClientSecret string
	RefreshSecret                    string
	TursoURL, TursoAuthToken         string
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
		Login    string   `yaml:"login"`
		AuthRepo string   `yaml:"auth_repo"`
		Repos    []string `yaml:"repos"`
	} `yaml:"github"`
	Notifications struct {
		Slack struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"slack"`
	} `yaml:"notifications"`
}

// repoPattern matches a GitHub "owner/name" repository reference.
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// minSecretLen is the minimum length required of REFRESH_SECRET. The OAuth pair has no such
// minimum: GitHub chooses both values, so a length check here would only reject what GitHub issued.
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
			Login:        fc.GitHub.Login,
			AuthRepo:     fc.GitHub.AuthRepo,
			Repos:        fc.GitHub.Repos,
			BaseURL:      env("GITHUB_BASE_URL"),
			BadgeBaseURL: env("GITHUB_BADGE_BASE_URL"),
			OAuthBaseURL: env("GITHUB_OAUTH_BASE_URL"),
		},
		Secrets: Secrets{
			GitHubToken:       env("GITHUB_TOKEN"),
			SlackWebhook:      env("SLACK_WEBHOOK_URL"),
			OAuthClientID:     env("GITHUB_OAUTH_CLIENT_ID"),
			OAuthClientSecret: env("GITHUB_OAUTH_CLIENT_SECRET"),
			RefreshSecret:     env("REFRESH_SECRET"),
			TursoURL:          env("TURSO_URL"),
			TursoAuthToken:    env("TURSO_AUTH_TOKEN"),
		},
	}
	cfg.Notifications.Slack.Enabled = fc.Notifications.Slack.Enabled

	// The three values sign-in is made of. A deployment missing any of them would start, serve the
	// sign-in page and refuse everybody at the callback, which looks like an outage rather than a
	// misconfiguration; it fails here instead, where a deployment notices (FR-8.3).
	if !repoPattern.MatchString(fc.GitHub.AuthRepo) {
		return Config{}, fmt.Errorf("github.auth_repo: %q is not in owner/name form", fc.GitHub.AuthRepo)
	}
	if cfg.Secrets.OAuthClientID == "" {
		return Config{}, errors.New("GITHUB_OAUTH_CLIENT_ID is not set")
	}
	if cfg.Secrets.OAuthClientSecret == "" {
		return Config{}, errors.New("GITHUB_OAUTH_CLIENT_SECRET is not set")
	}
	if len(cfg.Secrets.RefreshSecret) < minSecretLen {
		return Config{}, errors.New("REFRESH_SECRET must be at least 32 characters")
	}

	return cfg, nil
}

// Enabled reports whether the named source has its credential (FR-8.2 AC2). An unknown source
// name reports false. A source with a credential but nothing configured to watch (no repos) also
// reports false: there would be nothing for it to do.
func (c Config) Enabled(source string) bool {
	switch source {
	case "github":
		return c.Secrets.GitHubToken != "" && len(c.GitHub.Repos) > 0
	default:
		return false
	}
}
