// Package config loads and validates config/zorgscope.yaml plus secrets from the environment
// (arc42 §8.3, ADR-0007).
package config

import (
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
	Todoist   TodoistConfig   `yaml:"todoist"`
	Feeds     FeedsConfig     `yaml:"feeds"`
	Watch     WatchConfig     `yaml:"watch"`

	// From environment (never from YAML):
	Secrets  Secrets `yaml:"-"`
	AuthMode string  `yaml:"-"` // "passkey" | "dev"
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
var KnownTiles = []string{"attention", "repos", "sites", "todoist", "news", "watch"}

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

// UnmarshalYAML implements the scalar-or-mapping form: a bare "owner/name" string, or a mapping
// with a `name` key plus per-repo overrides such as `poll_interval`.
func (r *RepoConfig) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		r.Name = n.Value
		return nil
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

// TodoistConfig holds the `todoist` section: whether the source is enabled, polling interval,
// the task-due horizon in days and the (overridable) API base URL.
type TodoistConfig struct {
	Enabled      bool          `yaml:"enabled"`
	PollInterval time.Duration `yaml:"poll_interval"`
	HorizonDays  int           `yaml:"horizon_days"`
	BaseURL      string        `yaml:"base_url"`
}

// FeedsConfig holds the `feeds` section: whether the source is enabled, polling interval,
// per-tile item cap, topic grouping and the configured feed sources.
type FeedsConfig struct {
	Enabled      bool          `yaml:"enabled"`
	PollInterval time.Duration `yaml:"poll_interval"`
	MaxItems     int           `yaml:"max_items"`
	GroupByTopic bool          `yaml:"group_by_topic"`
	Sources      []FeedSource  `yaml:"sources"`
}

// FeedSource is one feed.
type FeedSource struct {
	Name     string `yaml:"name"`
	URL      string `yaml:"url"`
	Topic    string `yaml:"topic"`
	MaxItems int    `yaml:"max_items"`
}

// WatchConfig holds the `watch` section (FR-11.x): whether credential/URL watching is enabled,
// the default warning horizon in days, manually registered credentials and health-checked URLs.
type WatchConfig struct {
	Enabled     bool               `yaml:"enabled"`
	WarnDays    int                `yaml:"warn_days"`
	Credentials []CredentialConfig `yaml:"credentials"`
	URLs        []URLCheckConfig   `yaml:"urls"`
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
	TodoistToken    string
	SessionSecret   string
	EnrollToken     string
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
		Todoist:   TodoistConfig{PollInterval: 5 * time.Minute, HorizonDays: 7, BaseURL: "https://api.todoist.com"},
		Feeds:     FeedsConfig{PollInterval: 30 * time.Minute, MaxItems: 20},
		Watch:     WatchConfig{Enabled: true, WarnDays: 14},
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
