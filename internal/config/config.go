// Package config loads zorgscope's non-secret YAML configuration and overlays it with secrets
// read from the environment (FR-8.1). Config values, once loaded, are validated so that a bad
// configuration fails at start-up rather than mid-request.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"time"
	_ "time/tzdata" // the distroless runtime image (deploy/Dockerfile) ships no /usr/share/zoneinfo

	"gopkg.in/yaml.v3"
)

// defaultCacheTTL is used when github.cache_ttl names none.
const defaultCacheTTL = 5 * time.Minute

// Config is the fully validated, ready-to-use application configuration.
type Config struct {
	Timezone string
	GitHub   GitHub
	Secrets  Secrets
}

// GitHub is the non-secret GitHub configuration.
type GitHub struct {
	// AuthRepo is the repository whose push access admits a visitor to the dashboard (FR-8.3), in
	// owner/name form. It is required, and it is deliberately not derived from Repos: the list of
	// repositories being watched is a product decision that changes often, while who may read the
	// dashboard is a security decision that should change only when someone means it to.
	AuthRepo string
	Repos    []string
	// CacheTTL is how old the fetched item list may be before a page view refetches it. It
	// defaults to 5 minutes when the configuration names none.
	CacheTTL time.Duration
	BaseURL  string // "" means api.github.com; make fakes sets this via GITHUB_BASE_URL.
	// OAuthBaseURL is where the OAuth App's authorize and token endpoints live; "" means
	// github.com. It is separate from BaseURL because those two endpoints are not on the API host
	// even at the real GitHub: the API answers at api.github.com and sign-in at github.com.
	OAuthBaseURL string // "" means github.com; set via GITHUB_OAUTH_BASE_URL.
}

// Secrets holds every value read from the environment. None of these is ever logged or included
// in an error message, and none but OAuthClientID is ever rendered (QS-4.3) — the client id has to
// reach the browser, because it is half of the authorize URL a sign-in is redirected to.
type Secrets struct {
	GitHubToken string
	// OAuthClientID and OAuthClientSecret are the GitHub OAuth App this deployment signs people in
	// as (FR-8.3). The id is not really a secret — it travels in the browser's address bar — but it
	// is read from the environment beside its secret and redacted with it, because a value that is
	// only sometimes worth hiding is a value someone eventually forgets to hide.
	OAuthClientID, OAuthClientSecret string
}

// fileConfig mirrors the shape of the YAML file. CacheTTL is a string here so that a malformed
// value can be rejected with a field-naming error rather than failing YAML decoding generically.
type fileConfig struct {
	Timezone string `yaml:"timezone"`
	GitHub   struct {
		AuthRepo string   `yaml:"auth_repo"`
		CacheTTL string   `yaml:"cache_ttl"`
		Repos    []string `yaml:"repos"`
	} `yaml:"github"`
}

// repoPattern matches a GitHub "owner/name" repository reference.
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

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

	if _, err := time.LoadLocation(fc.Timezone); err != nil {
		return Config{}, fmt.Errorf("timezone: %w", err)
	}

	if len(fc.GitHub.Repos) == 0 {
		return Config{}, errors.New("github.repos: at least one repository is required")
	}
	for i, repo := range fc.GitHub.Repos {
		if !repoPattern.MatchString(repo) {
			return Config{}, fmt.Errorf("github.repos[%d]: %q is not in owner/name form", i, repo)
		}
	}

	cacheTTL := defaultCacheTTL
	if fc.GitHub.CacheTTL != "" {
		d, err := time.ParseDuration(fc.GitHub.CacheTTL)
		if err != nil {
			return Config{}, fmt.Errorf("github.cache_ttl: %w", err)
		}
		if d <= 0 {
			return Config{}, fmt.Errorf("github.cache_ttl: must be positive, got %s", fc.GitHub.CacheTTL)
		}
		cacheTTL = d
	}

	cfg := Config{
		Timezone: fc.Timezone,
		GitHub: GitHub{
			AuthRepo:     fc.GitHub.AuthRepo,
			Repos:        fc.GitHub.Repos,
			CacheTTL:     cacheTTL,
			BaseURL:      env("GITHUB_BASE_URL"),
			OAuthBaseURL: env("GITHUB_OAUTH_BASE_URL"),
		},
		Secrets: Secrets{
			GitHubToken:       env("GITHUB_TOKEN"),
			OAuthClientID:     env("GITHUB_OAUTH_CLIENT_ID"),
			OAuthClientSecret: env("GITHUB_OAUTH_CLIENT_SECRET"),
		},
	}

	// The three values sign-in and fetching are made of. A deployment missing any of them would
	// start and then either refuse everybody at the callback or show an empty page, both of which
	// look like an outage rather than a misconfiguration; it fails here instead, where a
	// deployment notices (FR-8.3).
	if !repoPattern.MatchString(fc.GitHub.AuthRepo) {
		return Config{}, fmt.Errorf("github.auth_repo: %q is not in owner/name form", fc.GitHub.AuthRepo)
	}
	if cfg.Secrets.OAuthClientID == "" {
		return Config{}, errors.New("GITHUB_OAUTH_CLIENT_ID is not set")
	}
	if cfg.Secrets.OAuthClientSecret == "" {
		return Config{}, errors.New("GITHUB_OAUTH_CLIENT_SECRET is not set")
	}
	if cfg.Secrets.GitHubToken == "" {
		return Config{}, errors.New("GITHUB_TOKEN is not set")
	}
	if err := checkBaseURL("GITHUB_BASE_URL", cfg.GitHub.BaseURL); err != nil {
		return Config{}, err
	}
	if err := checkBaseURL("GITHUB_OAUTH_BASE_URL", cfg.GitHub.OAuthBaseURL); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// checkBaseURL validates GITHUB_BASE_URL or GITHUB_OAUTH_BASE_URL, named by name, both of which
// are empty in every real deployment (design §8).
//
// The variables exist so that `make fakes` and the tests can point fetching and the sign-in flow
// at a fixture server on this machine; they are not general redirects. Left unvalidated they would
// be: GITHUB_BASE_URL is where this process sends GITHUB_TOKEN and every visitor's access token,
// and GITHUB_OAUTH_BASE_URL becomes the authorize URL the visitor's browser is sent to and the
// token URL this process posts the client secret to, so a typo — or an environment somebody else
// can write — would hand those to a host of their choosing. Anything but https is therefore refused
// unless it is talking to this machine, where there is no network to intercept and no TLS
// certificate to have.
func checkBaseURL(name, raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	// The error is never included: the value is not a secret, but it is attacker-influenceable
	// text, and the name of the variable is what the operator needs (FR-8.1 AC3, QS-4.3).
	if err != nil || u.Host == "" {
		return errors.New(name + " is not a URL")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && isLoopbackHost(u.Hostname()) {
		return nil
	}
	return errors.New(name + " must be https, unless it points at this machine")
}

// isLoopbackHost reports whether host is this machine. host.docker.internal is included because
// that is how a container reaches `make fakes` running on the host, which is the whole reason the
// variable exists.
func isLoopbackHost(host string) bool {
	if host == "localhost" || host == "host.docker.internal" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Enabled reports whether the named source has its credential (FR-8.1). An unknown source
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
