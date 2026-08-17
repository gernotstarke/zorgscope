package config

import (
	"fmt"
	"time"
)

// Document is the complete, secret-safe representation returned by the configuration API.
// Config contains every runtime-editable property. Deployment contains effective properties
// controlled by the process environment. Secrets reports presence and provenance only.
type Document struct {
	SchemaVersion int              `json:"schema_version"`
	Revision      string           `json:"revision"`
	Config        EditableConfig   `json:"config"`
	Deployment    DeploymentView   `json:"deployment"`
	Secrets       SecretStatusView `json:"secrets"`
}

// DocumentSchemaVersion is incremented when the client-facing configuration shape changes
// incompatibly.
const DocumentSchemaVersion = 1

// DeploymentView contains process/deployment settings which are deliberately not accepted by
// Update. Changing any of these requires updating the Fly deployment and restarting the process.
type DeploymentView struct {
	AuthMode string `json:"auth_mode"`
	LogLevel string `json:"log_level"`
	DataPath string `json:"data_path"`
	BaseURL  string `json:"base_url"`
	Port     int    `json:"port"`
}

// SecretState never contains the value of a secret. Source is "managed", "environment", or
// "none" and makes it possible for a client to explain how a credential is configured.
type SecretState struct {
	Configured bool   `json:"configured"`
	Source     string `json:"source"`
}

// SecretStatusView reports only the two API-manageable upstream credentials. Deployment trust
// secrets are deliberately absent, even as presence bits.
type SecretStatusView struct {
	GitHubToken     SecretState `json:"github_token"`
	PlausibleAPIKey SecretState `json:"plausible_api_key"`
}

// EditableConfig mirrors every non-secret YAML property. Durations are strings both on the wire
// and in the persisted runtime YAML so clients never have to deal with nanoseconds.
type EditableConfig struct {
	Server    EditableServerConfig    `json:"server" yaml:"server"`
	UI        EditableUIConfig        `json:"ui" yaml:"ui"`
	Snapshot  EditableSnapshotConfig  `json:"snapshot" yaml:"snapshot"`
	GitHub    EditableGitHubConfig    `json:"github" yaml:"github"`
	Plausible EditablePlausibleConfig `json:"plausible" yaml:"plausible"`
	Watch     EditableWatchConfig     `json:"watch" yaml:"watch"`
}

// EditableServerConfig contains runtime-adjustable server behaviour.
type EditableServerConfig struct {
	Timezone string `json:"timezone" yaml:"timezone"`
}

// EditableUIConfig contains client presentation hints and refresh limits.
type EditableUIConfig struct {
	TilePollSeconds      int      `json:"tile_poll_seconds" yaml:"tile_poll_seconds"`
	AttentionCap         int      `json:"attention_cap" yaml:"attention_cap"`
	RefreshMinGapSeconds int      `json:"refresh_min_gap_seconds" yaml:"refresh_min_gap_seconds"`
	Tiles                []string `json:"tiles" yaml:"tiles"`
}

// EditableSnapshotConfig contains daily snapshot scheduling and retention.
type EditableSnapshotConfig struct {
	Time          string `json:"time" yaml:"time"`
	RetentionDays int    `json:"retention_days" yaml:"retention_days"`
}

// EditableGitHubConfig contains all runtime-adjustable GitHub source properties.
type EditableGitHubConfig struct {
	Enabled      bool                 `json:"enabled" yaml:"enabled"`
	Me           string               `json:"me" yaml:"me"`
	PollInterval string               `json:"poll_interval" yaml:"poll_interval"`
	GracePeriod  string               `json:"grace_period" yaml:"grace_period"`
	StaleAfter   string               `json:"stale_after" yaml:"stale_after"`
	Bots         []string             `json:"bots" yaml:"bots"`
	Mentions     bool                 `json:"mentions" yaml:"mentions"`
	Repos        []EditableRepoConfig `json:"repos" yaml:"repos"`
	BaseURL      string               `json:"base_url" yaml:"base_url"`
}

// EditableRepoConfig identifies a monitored GitHub repository and optional cadence override.
type EditableRepoConfig struct {
	Name         string `json:"name" yaml:"name"`
	PollInterval string `json:"poll_interval,omitempty" yaml:"poll_interval,omitempty"`
}

// EditablePlausibleConfig contains all runtime-adjustable Plausible source properties.
type EditablePlausibleConfig struct {
	Enabled      bool     `json:"enabled" yaml:"enabled"`
	PollInterval string   `json:"poll_interval" yaml:"poll_interval"`
	Order        string   `json:"order" yaml:"order"`
	Sites        []string `json:"sites" yaml:"sites"`
	BaseURL      string   `json:"base_url" yaml:"base_url"`
}

// EditableWatchConfig contains credential and URL/TLS observation settings.
type EditableWatchConfig struct {
	Enabled      bool                       `json:"enabled" yaml:"enabled"`
	PollInterval string                     `json:"poll_interval" yaml:"poll_interval"`
	WarnDays     int                        `json:"warn_days" yaml:"warn_days"`
	Credentials  []EditableCredentialConfig `json:"credentials" yaml:"credentials"`
	URLs         []EditableURLCheckConfig   `json:"urls" yaml:"urls"`
}

// EditableCredentialConfig describes one manually tracked expiring credential.
type EditableCredentialConfig struct {
	Name     string `json:"name" yaml:"name"`
	Expires  string `json:"expires" yaml:"expires"`
	WarnDays int    `json:"warn_days" yaml:"warn_days"`
	UsedBy   string `json:"used_by" yaml:"used_by"`
	URL      string `json:"url" yaml:"url"`
}

// EditableURLCheckConfig describes one URL health and certificate check.
type EditableURLCheckConfig struct {
	Name               string `json:"name" yaml:"name"`
	URL                string `json:"url" yaml:"url"`
	ExpectStatus       int    `json:"expect_status" yaml:"expect_status"`
	ExpectBodyContains string `json:"expect_body_contains" yaml:"expect_body_contains"`
	PollInterval       string `json:"poll_interval" yaml:"poll_interval"`
}

func editableFromConfig(c *Config) EditableConfig {
	e := EditableConfig{
		Server: EditableServerConfig{Timezone: c.Server.Timezone},
		UI: EditableUIConfig{TilePollSeconds: c.UI.TilePollSeconds, AttentionCap: c.UI.AttentionCap,
			RefreshMinGapSeconds: c.UI.RefreshMinGapSeconds, Tiles: cloneStrings(c.UI.Tiles)},
		Snapshot: EditableSnapshotConfig{Time: c.Snapshot.Time, RetentionDays: c.Snapshot.RetentionDays},
		GitHub: EditableGitHubConfig{Enabled: c.GitHub.Enabled, Me: c.GitHub.Me,
			PollInterval: c.GitHub.PollInterval.String(), GracePeriod: c.GitHub.GracePeriod.String(),
			StaleAfter: c.GitHub.StaleAfter.String(), Bots: cloneStrings(c.GitHub.Bots), Mentions: c.GitHub.Mentions,
			BaseURL: c.GitHub.BaseURL},
		Plausible: EditablePlausibleConfig{Enabled: c.Plausible.Enabled, PollInterval: c.Plausible.PollInterval.String(),
			Order: c.Plausible.Order, Sites: cloneStrings(c.Plausible.Sites), BaseURL: c.Plausible.BaseURL},
		Watch: EditableWatchConfig{Enabled: c.Watch.Enabled, PollInterval: c.Watch.PollInterval.String(), WarnDays: c.Watch.WarnDays},
	}
	for _, r := range c.GitHub.Repos {
		poll := ""
		if r.PollInterval != 0 {
			poll = r.PollInterval.String()
		}
		e.GitHub.Repos = append(e.GitHub.Repos, EditableRepoConfig{Name: r.Name, PollInterval: poll})
	}
	for _, cr := range c.Watch.Credentials {
		e.Watch.Credentials = append(e.Watch.Credentials, EditableCredentialConfig{
			Name: cr.Name, Expires: cr.Expires, WarnDays: cr.WarnDays, UsedBy: cr.UsedBy, URL: cr.URL,
		})
	}
	for _, u := range c.Watch.URLs {
		e.Watch.URLs = append(e.Watch.URLs, EditableURLCheckConfig{
			Name: u.Name, URL: u.URL, ExpectStatus: u.ExpectStatus,
			ExpectBodyContains: u.ExpectBodyContains, PollInterval: u.PollInterval.String(),
		})
	}
	return e
}

func (e EditableConfig) config() (*Config, error) {
	c := Default()
	c.Server.Timezone = e.Server.Timezone
	c.UI = UIConfig{TilePollSeconds: e.UI.TilePollSeconds, AttentionCap: e.UI.AttentionCap,
		RefreshMinGapSeconds: e.UI.RefreshMinGapSeconds, Tiles: cloneStrings(e.UI.Tiles)}
	c.Snapshot = SnapshotConfig{Time: e.Snapshot.Time, RetentionDays: e.Snapshot.RetentionDays}

	var err error
	c.GitHub = GitHubConfig{Enabled: e.GitHub.Enabled, Me: e.GitHub.Me, Bots: cloneStrings(e.GitHub.Bots),
		Mentions: e.GitHub.Mentions, BaseURL: e.GitHub.BaseURL}
	if c.GitHub.PollInterval, err = parseDuration("github.poll_interval", e.GitHub.PollInterval, false); err != nil {
		return nil, err
	}
	if c.GitHub.GracePeriod, err = parseDuration("github.grace_period", e.GitHub.GracePeriod, false); err != nil {
		return nil, err
	}
	if c.GitHub.StaleAfter, err = parseDuration("github.stale_after", e.GitHub.StaleAfter, false); err != nil {
		return nil, err
	}
	for i, r := range e.GitHub.Repos {
		poll, parseErr := parseDuration(fmt.Sprintf("github.repos[%d].poll_interval", i), r.PollInterval, true)
		if parseErr != nil {
			return nil, parseErr
		}
		c.GitHub.Repos = append(c.GitHub.Repos, RepoConfig{Name: r.Name, PollInterval: poll})
	}

	c.Plausible = PlausibleConfig{Enabled: e.Plausible.Enabled, Order: e.Plausible.Order,
		Sites: cloneStrings(e.Plausible.Sites), BaseURL: e.Plausible.BaseURL}
	if c.Plausible.PollInterval, err = parseDuration("plausible.poll_interval", e.Plausible.PollInterval, false); err != nil {
		return nil, err
	}

	c.Watch = WatchConfig{Enabled: e.Watch.Enabled, WarnDays: e.Watch.WarnDays}
	if c.Watch.PollInterval, err = parseDuration("watch.poll_interval", e.Watch.PollInterval, false); err != nil {
		return nil, err
	}
	for _, cr := range e.Watch.Credentials {
		c.Watch.Credentials = append(c.Watch.Credentials, CredentialConfig{
			Name: cr.Name, Expires: cr.Expires, WarnDays: cr.WarnDays, UsedBy: cr.UsedBy, URL: cr.URL,
		})
	}
	for i, u := range e.Watch.URLs {
		poll, parseErr := parseDuration(fmt.Sprintf("watch.urls[%d].poll_interval", i), u.PollInterval, true)
		if parseErr != nil {
			return nil, parseErr
		}
		c.Watch.URLs = append(c.Watch.URLs, URLCheckConfig{Name: u.Name, URL: u.URL,
			ExpectStatus: u.ExpectStatus, ExpectBodyContains: u.ExpectBodyContains, PollInterval: poll})
	}
	return &c, nil
}

func parseDuration(key, value string, emptyOK bool) (time.Duration, error) {
	if value == "" && emptyOK {
		return 0, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fail(key, "must be a duration, got %q", value)
	}
	return d, nil
}

func cloneStrings(in []string) []string { return append([]string(nil), in...) }
