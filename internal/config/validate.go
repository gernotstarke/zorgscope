package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ValidationError names the offending key (dotted config path or env var name) and the problem
// found with it (QS-4.3).
type ValidationError struct {
	Key string
	Msg string
}

// Error implements the error interface, formatting the offending key and the problem.
func (e *ValidationError) Error() string { return fmt.Sprintf("invalid config: %s: %s", e.Key, e.Msg) }

func fail(key, format string, a ...any) error {
	return &ValidationError{Key: key, Msg: fmt.Sprintf(format, a...)}
}

var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

const minInterval = 10 * time.Second // small enough for e2e configs, large enough for rate limits

// Validate checks the configuration for errors and, as a side effect (deviation D-12), also
// normalises the receiver in place by resolving derived fields that Tasks 9-15 depend on:
//   - Server.Location, from Server.Timezone
//   - Snapshot.Hour and Snapshot.Minute, from Snapshot.Time
//   - Watch.Credentials[i].ExpiresAt, from Watch.Credentials[i].Expires
//   - Watch.URLs[i].ExpectStatus and Watch.URLs[i].PollInterval, defaulted when unset
//
// Validate returns the first *ValidationError encountered, or nil if the configuration
// (including the resolved fields) is valid.
func (c *Config) Validate() error {
	u, err := url.Parse(c.Server.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fail("server.base_url", "must be an absolute http(s) URL, got %q", c.Server.BaseURL)
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fail("server.port", "must be 1..65535")
	}
	loc, err := time.LoadLocation(c.Server.Timezone)
	if err != nil {
		return fail("server.timezone", "unknown timezone %q", c.Server.Timezone)
	}
	c.Server.Location = loc

	switch c.AuthMode {
	case "passkey":
		if len(c.Secrets.SessionSecret) < 32 {
			return fail("SESSION_SECRET", "must be at least 32 characters in passkey mode")
		}
	case "dev":
		host := u.Hostname()
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return fail("AUTH_MODE", "dev mode is only allowed when server.base_url points to localhost")
		}
	default:
		return fail("AUTH_MODE", "must be passkey or dev, got %q", c.AuthMode)
	}

	if c.UI.TilePollSeconds < 5 {
		return fail("ui.tile_poll_seconds", "must be >= 5")
	}
	if c.UI.RefreshMinGapSeconds < 1 {
		return fail("ui.refresh_min_gap_seconds", "must be >= 1")
	}
	for i, t := range c.UI.Tiles {
		if !contains(KnownTiles, t) {
			return fail(fmt.Sprintf("ui.tiles[%d]", i), "unknown tile %q (known: %s)", t, strings.Join(KnownTiles, ", "))
		}
	}

	if _, err := fmt.Sscanf(c.Snapshot.Time, "%d:%d", &c.Snapshot.Hour, &c.Snapshot.Minute); err != nil ||
		c.Snapshot.Hour < 0 || c.Snapshot.Hour > 23 || c.Snapshot.Minute < 0 || c.Snapshot.Minute > 59 {
		return fail("snapshot.time", "must be HH:MM, got %q", c.Snapshot.Time)
	}
	if c.Snapshot.RetentionDays < 1 {
		return fail("snapshot.retention_days", "must be >= 1")
	}

	if c.GitHub.Enabled {
		if c.GitHub.Me == "" {
			return fail("github.me", "required when github is enabled")
		}
		if c.Secrets.GitHubToken == "" {
			return fail("GITHUB_TOKEN", "required when github is enabled")
		}
		if err := checkInterval("github.poll_interval", c.GitHub.PollInterval); err != nil {
			return err
		}
		if c.GitHub.GracePeriod < 0 || c.GitHub.StaleAfter <= 0 {
			return fail("github.grace_period/stale_after", "must be positive")
		}
		for i, r := range c.GitHub.Repos {
			if !repoRe.MatchString(r.Name) {
				return fail(fmt.Sprintf("github.repos[%d]", i), "must be owner/name, got %q", r.Name)
			}
			if r.PollInterval != 0 {
				if err := checkInterval(fmt.Sprintf("github.repos[%d].poll_interval", i), r.PollInterval); err != nil {
					return err
				}
			}
		}
	}
	if c.Plausible.Enabled {
		if c.Secrets.PlausibleAPIKey == "" {
			return fail("PLAUSIBLE_API_KEY", "required when plausible is enabled")
		}
		if err := checkInterval("plausible.poll_interval", c.Plausible.PollInterval); err != nil {
			return err
		}
		if c.Plausible.Order != "visitors" && c.Plausible.Order != "config" {
			return fail("plausible.order", "must be visitors or config")
		}
		for i, s := range c.Plausible.Sites {
			if s == "" || strings.ContainsAny(s, "/ :") {
				return fail(fmt.Sprintf("plausible.sites[%d]", i), "must be a bare hostname, got %q", s)
			}
		}
	}
	if c.Todoist.Enabled {
		if c.Secrets.TodoistToken == "" {
			return fail("TODOIST_TOKEN", "required when todoist is enabled")
		}
		if err := checkInterval("todoist.poll_interval", c.Todoist.PollInterval); err != nil {
			return err
		}
		if c.Todoist.HorizonDays < 1 {
			return fail("todoist.horizon_days", "must be >= 1")
		}
	}
	if c.Feeds.Enabled {
		if err := checkInterval("feeds.poll_interval", c.Feeds.PollInterval); err != nil {
			return err
		}
		for i, f := range c.Feeds.Sources {
			if f.Name == "" {
				return fail(fmt.Sprintf("feeds.sources[%d].name", i), "required")
			}
			if fu, err := url.Parse(f.URL); err != nil || fu.Host == "" {
				return fail(fmt.Sprintf("feeds.sources[%d].url", i), "must be an absolute URL")
			}
		}
	}
	if c.Watch.WarnDays < 1 {
		return fail("watch.warn_days", "must be >= 1")
	}
	for i := range c.Watch.Credentials {
		cr := &c.Watch.Credentials[i]
		if cr.Name == "" {
			return fail(fmt.Sprintf("watch.credentials[%d].name", i), "required")
		}
		t, err := time.ParseInLocation("2006-01-02", cr.Expires, loc)
		if err != nil {
			return fail(fmt.Sprintf("watch.credentials[%d].expires", i), "must be YYYY-MM-DD, got %q", cr.Expires)
		}
		cr.ExpiresAt = t
	}
	for i := range c.Watch.URLs {
		w := &c.Watch.URLs[i]
		if w.Name == "" {
			return fail(fmt.Sprintf("watch.urls[%d].name", i), "required")
		}
		if wu, err := url.Parse(w.URL); err != nil || wu.Host == "" || (wu.Scheme != "http" && wu.Scheme != "https") {
			return fail(fmt.Sprintf("watch.urls[%d].url", i), "must be an absolute http(s) URL")
		}
		if w.ExpectStatus == 0 {
			w.ExpectStatus = 200
		}
		if w.PollInterval == 0 {
			w.PollInterval = 15 * time.Minute
		}
		if err := checkInterval(fmt.Sprintf("watch.urls[%d].poll_interval", i), w.PollInterval); err != nil {
			return err
		}
	}
	return nil
}

func checkInterval(key string, d time.Duration) error {
	if d < minInterval {
		return fail(key, "must be at least %s, got %s", minInterval, d)
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
