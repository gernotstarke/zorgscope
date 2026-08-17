// Package config loads and validates config/zorgscope.yaml plus secrets from the environment
// (arc42 §8.3, ADR-0007).
package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	Server    ServerConfig    `yaml:"server"`
	UI        UIConfig        `yaml:"ui"`
	Snapshot  SnapshotConfig  `yaml:"snapshot"`
	GitHub    GitHubConfig    `yaml:"github"`
	Plausible PlausibleConfig `yaml:"plausible"`
	Watch     WatchConfig     `yaml:"watch"`

	// From environment (never from YAML):
	Secrets  Secrets `yaml:"-"`
	AuthMode string  `yaml:"-"` // "token" | "passkey" | "dev"
	LogLevel string  `yaml:"-"`
	DataPath string  `yaml:"-"`
}

// ServerConfig holds the `server` section: the base URL used for cookies, WebAuthn RP ID/origin
// and absolute links, the listen port, and the timezone used for snapshot time and day grouping.
type ServerConfig struct {
	BaseURL  string         `yaml:"base_url"`
	Port     int            `yaml:"port"`
	Timezone string         `yaml:"timezone"`
	Location *time.Location `yaml:"-"`
}

// UIConfig holds the `ui` section: tile polling and refresh cadence, the attention badge cap,
// and the ordered list of visible tiles.
type UIConfig struct {
	TilePollSeconds      int      `yaml:"tile_poll_seconds"`
	AttentionCap         int      `yaml:"attention_cap"`
	RefreshMinGapSeconds int      `yaml:"refresh_min_gap_seconds"`
	Tiles                []string `yaml:"tiles"`
}

// KnownTiles are the tile names accepted in ui.tiles.
var KnownTiles = []string{"attention", "repos", "sites", "watch"}

// SnapshotConfig holds the `snapshot` section: the daily snapshot time (HH:MM, resolved into
// Hour/Minute by Validate) and how many days of snapshots to retain.
type SnapshotConfig struct {
	Time          string `yaml:"time"` // HH:MM
	RetentionDays int    `yaml:"retention_days"`
	Hour, Minute  int    `yaml:"-"`
}

// GitHubConfig holds the `github` section: whether the source is enabled, the owner's login,
// polling and staleness thresholds, bot substrings, mention handling, monitored repos and the
// (overridable) API base URL.
type GitHubConfig struct {
	Enabled      bool          `yaml:"enabled"`
	Me           string        `yaml:"me"`
	PollInterval time.Duration `yaml:"poll_interval"`
	GracePeriod  time.Duration `yaml:"grace_period"`
	StaleAfter   time.Duration `yaml:"stale_after"`
	Bots         []string      `yaml:"bots"`
	Mentions     bool          `yaml:"mentions"`
	Repos        []RepoConfig  `yaml:"repos"`
	BaseURL      string        `yaml:"base_url"` // overridable by GITHUB_BASE_URL (fakes)
}

// RepoConfig accepts either a plain "owner/name" string or a mapping with overrides.
type RepoConfig struct {
	Name         string        `yaml:"name"`
	PollInterval time.Duration `yaml:"poll_interval"`
}

// repoConfigKeys are the only keys accepted in a repos: mapping entry. Node.Decode does not
// inherit the parent decoder's KnownFields(true) setting (yaml.v3 always decodes nested nodes
// loosely), so unknown keys are checked by hand here to preserve the "unknown keys are errors"
// guarantee for this sub-schema (§8.3).
var repoConfigKeys = map[string]bool{"name": true, "poll_interval": true}

// UnmarshalYAML implements the scalar-or-mapping form: a bare "owner/name" string, or a mapping
// with a `name` key plus per-repo overrides such as `poll_interval`. Unknown keys in the mapping
// form are rejected with a *ValidationError naming the offending key.
func (r *RepoConfig) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		r.Name = n.Value
		return nil
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			if !repoConfigKeys[key] {
				return &ValidationError{Key: fmt.Sprintf("github.repos[].%s", key), Msg: fmt.Sprintf("unknown key %q in repo config", key)}
			}
		}
	}
	type plain RepoConfig
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*r = RepoConfig(p)
	return nil
}

// RepoInterval returns the effective poll interval for a repo: its own override if set,
// otherwise the GitHub source's default poll interval.
func (g GitHubConfig) RepoInterval(name string) time.Duration {
	for _, r := range g.Repos {
		if r.Name == name && r.PollInterval > 0 {
			return r.PollInterval
		}
	}
	return g.PollInterval
}

// PlausibleConfig holds the `plausible` section: whether the source is enabled, polling
// interval, site ordering, monitored site hostnames and the (overridable) API base URL.
type PlausibleConfig struct {
	Enabled      bool          `yaml:"enabled"`
	PollInterval time.Duration `yaml:"poll_interval"`
	Order        string        `yaml:"order"` // visitors | config
	Sites        []string      `yaml:"sites"`
	BaseURL      string        `yaml:"base_url"`
}

// WatchConfig holds the `watch` section (FR-11.x): whether credential/URL watching is enabled,
// the default warning horizon in days, manually registered credentials and health-checked URLs.
type WatchConfig struct {
	Enabled      bool               `yaml:"enabled"`
	PollInterval time.Duration      `yaml:"poll_interval"`
	WarnDays     int                `yaml:"warn_days"`
	Credentials  []CredentialConfig `yaml:"credentials"`
	URLs         []URLCheckConfig   `yaml:"urls"`
}

// CredentialConfig is a manually registered expiring credential.
type CredentialConfig struct {
	Name      string    `yaml:"name"`
	Expires   string    `yaml:"expires"` // YYYY-MM-DD
	WarnDays  int       `yaml:"warn_days"`
	UsedBy    string    `yaml:"used_by"`
	URL       string    `yaml:"url"`
	ExpiresAt time.Time `yaml:"-"`
}

// URLCheckConfig is a health-checked URL.
type URLCheckConfig struct {
	Name               string        `yaml:"name"`
	URL                string        `yaml:"url"`
	ExpectStatus       int           `yaml:"expect_status"`
	ExpectBodyContains string        `yaml:"expect_body_contains"`
	PollInterval       time.Duration `yaml:"poll_interval"`
}

// Secrets come from the environment only. Never log a Secrets value or any of its fields
// (FR-8.2, QS-3.3); no String()/GoString() is defined on this type or its fields for that reason.
type Secrets struct {
	GitHubToken     string
	PlausibleAPIKey string
	SessionSecret   string
	EnrollToken     string
	APIToken        string
	ConfigKey       string
}

// Default returns the documented defaults; YAML overrides them.
func Default() Config {
	return Config{
		Server:   ServerConfig{Port: 8080, Timezone: "Europe/Berlin"},
		UI:       UIConfig{TilePollSeconds: 60, AttentionCap: 30, RefreshMinGapSeconds: 30, Tiles: append([]string(nil), KnownTiles...)},
		Snapshot: SnapshotConfig{Time: "03:00", RetentionDays: 30},
		GitHub: GitHubConfig{PollInterval: 10 * time.Minute, GracePeriod: 4 * time.Hour, StaleAfter: 30 * 24 * time.Hour,
			Bots: []string{"[bot]", "dependabot", "renovate"}, Mentions: true, BaseURL: "https://api.github.com"},
		Plausible: PlausibleConfig{PollInterval: 30 * time.Minute, Order: "visitors", BaseURL: "https://plausible.io"},
		Watch:     WatchConfig{Enabled: true, PollInterval: 15 * time.Minute, WarnDays: 14},
		AuthMode:  "passkey",
		LogLevel:  "info",
		DataPath:  "/data/zorgscope.db",
	}
}

// Rules derives the domain rules from the configuration. Me is load-bearing: IsUnanswered treats
// activity by Me on an item Me authored as an answer.
func (c *Config) Rules() domain.Rules {
	return domain.Rules{Grace: c.GitHub.GracePeriod, StaleAfter: c.GitHub.StaleAfter, Me: c.GitHub.Me,
		Bots: c.GitHub.Bots, WarnDays: c.Watch.WarnDays}
}
