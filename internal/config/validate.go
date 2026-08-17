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

var (
	repoRe          = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	plausibleSiteRe = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
	snapshotTimeRe  = regexp.MustCompile(`^[0-9]{2}:[0-9]{2}$`)
)

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
	case "token":
		if len(c.Secrets.APIToken) < 32 {
			return fail("ZORGSCOPE_API_TOKEN", "must be at least 32 characters in token mode")
		}
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
		return fail("AUTH_MODE", "must be token, passkey or dev, got %q", c.AuthMode)
	}

	if c.UI.TilePollSeconds < 5 {
		return fail("ui.tile_poll_seconds", "must be >= 5")
	}
	if c.UI.RefreshMinGapSeconds < 1 {
		return fail("ui.refresh_min_gap_seconds", "must be >= 1")
	}
	if c.UI.AttentionCap < 1 {
		return fail("ui.attention_cap", "must be >= 1")
	}
	tiles := make(map[string]struct{}, len(c.UI.Tiles))
	for i, t := range c.UI.Tiles {
		if !contains(KnownTiles, t) {
			return fail(fmt.Sprintf("ui.tiles[%d]", i), "unknown tile %q (known: %s)", t, strings.Join(KnownTiles, ", "))
		}
		if _, duplicate := tiles[t]; duplicate {
			return fail(fmt.Sprintf("ui.tiles[%d]", i), "duplicate tile %q", t)
		}
		tiles[t] = struct{}{}
	}

	snapshotTime, err := time.Parse("15:04", c.Snapshot.Time)
	if err != nil || !snapshotTimeRe.MatchString(c.Snapshot.Time) {
		return fail("snapshot.time", "must be HH:MM, got %q", c.Snapshot.Time)
	}
	c.Snapshot.Hour, c.Snapshot.Minute = snapshotTime.Hour(), snapshotTime.Minute()
	if c.Snapshot.RetentionDays < 1 {
		return fail("snapshot.retention_days", "must be >= 1")
	}

	if err := checkHTTPURL("github.base_url", c.GitHub.BaseURL); err != nil {
		return err
	}
	if err := checkInterval("github.poll_interval", c.GitHub.PollInterval); err != nil {
		return err
	}
	if c.GitHub.GracePeriod < 0 {
		return fail("github.grace_period", "must be positive")
	}
	if c.GitHub.StaleAfter <= 0 {
		return fail("github.stale_after", "must be positive")
	}
	repos := make(map[string]struct{}, len(c.GitHub.Repos))
	for i, r := range c.GitHub.Repos {
		key := fmt.Sprintf("github.repos[%d]", i)
		if !repoRe.MatchString(r.Name) {
			return fail(key, "must be owner/name, got %q", r.Name)
		}
		if _, duplicate := repos[r.Name]; duplicate {
			return fail(key, "duplicate repository %q", r.Name)
		}
		repos[r.Name] = struct{}{}
		if r.PollInterval != 0 {
			if err := checkInterval(key+".poll_interval", r.PollInterval); err != nil {
				return err
			}
		}
	}
	if c.GitHub.Enabled {
		if c.GitHub.Me == "" {
			return fail("github.me", "required when github is enabled")
		}
		if c.Secrets.GitHubToken == "" {
			return fail("GITHUB_TOKEN", "required when github is enabled")
		}
	}
	if err := checkHTTPURL("plausible.base_url", c.Plausible.BaseURL); err != nil {
		return err
	}
	if err := checkInterval("plausible.poll_interval", c.Plausible.PollInterval); err != nil {
		return err
	}
	if c.Plausible.Order != "visitors" && c.Plausible.Order != "config" {
		return fail("plausible.order", "must be visitors or config")
	}
	sites := make(map[string]struct{}, len(c.Plausible.Sites))
	for i, site := range c.Plausible.Sites {
		key := fmt.Sprintf("plausible.sites[%d]", i)
		if !plausibleSiteRe.MatchString(site) || strings.HasPrefix(site, ".") || strings.HasSuffix(site, ".") {
			return fail(key, "must be a bare hostname, got %q", site)
		}
		if _, duplicate := sites[site]; duplicate {
			return fail(key, "duplicate site %q", site)
		}
		sites[site] = struct{}{}
	}
	if c.Plausible.Enabled && c.Secrets.PlausibleAPIKey == "" {
		return fail("PLAUSIBLE_API_KEY", "required when plausible is enabled")
	}
	if err := checkInterval("watch.poll_interval", c.Watch.PollInterval); err != nil {
		return err
	}
	if c.Watch.WarnDays < 1 {
		return fail("watch.warn_days", "must be >= 1")
	}
	credentialNames := make(map[string]struct{}, len(c.Watch.Credentials))
	for i := range c.Watch.Credentials {
		cr := &c.Watch.Credentials[i]
		key := fmt.Sprintf("watch.credentials[%d]", i)
		if strings.TrimSpace(cr.Name) == "" || strings.Contains(cr.Name, "|") {
			return fail(key+".name", "required and must not contain '|'")
		}
		if _, duplicate := credentialNames[cr.Name]; duplicate {
			return fail(key+".name", "duplicate credential %q", cr.Name)
		}
		credentialNames[cr.Name] = struct{}{}
		if cr.WarnDays < 0 {
			return fail(key+".warn_days", "must be >= 0 (zero inherits watch.warn_days)")
		}
		if cr.URL != "" {
			if err := checkHTTPURL(key+".url", cr.URL); err != nil {
				return err
			}
		}
		t, err := time.ParseInLocation("2006-01-02", cr.Expires, loc)
		if err != nil {
			return fail(key+".expires", "must be YYYY-MM-DD, got %q", cr.Expires)
		}
		cr.ExpiresAt = t
	}
	urlNames := make(map[string]struct{}, len(c.Watch.URLs))
	for i := range c.Watch.URLs {
		w := &c.Watch.URLs[i]
		key := fmt.Sprintf("watch.urls[%d]", i)
		if strings.TrimSpace(w.Name) == "" || strings.Contains(w.Name, "|") {
			return fail(key+".name", "required and must not contain '|'")
		}
		if _, duplicate := urlNames[w.Name]; duplicate {
			return fail(key+".name", "duplicate URL check %q", w.Name)
		}
		urlNames[w.Name] = struct{}{}
		if err := checkHTTPURL(key+".url", w.URL); err != nil {
			return err
		}
		if w.ExpectStatus == 0 {
			w.ExpectStatus = 200
		}
		if w.ExpectStatus < 100 || w.ExpectStatus > 599 {
			return fail(key+".expect_status", "must be 100..599")
		}
		if w.PollInterval == 0 {
			w.PollInterval = 15 * time.Minute
		}
		if err := checkInterval(key+".poll_interval", w.PollInterval); err != nil {
			return err
		}
	}
	return nil
}

func checkHTTPURL(key, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fail(key, "must be an absolute http(s) URL")
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
