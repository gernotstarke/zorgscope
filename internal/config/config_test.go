package config

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadRepoConfigDefaultsAndOverrides(t *testing.T) {
	cfg, err := Load("testdata/minimal.yaml", env(map[string]string{"GITHUB_TOKEN": "t", "AUTH_MODE": "dev"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 8080 || cfg.Server.Timezone != "Europe/Berlin" || cfg.Server.Location == nil {
		t.Fatalf("server defaults: %+v", cfg.Server)
	}
	if cfg.GitHub.PollInterval != 10*time.Minute || cfg.GitHub.GracePeriod != 4*time.Hour || len(cfg.GitHub.Bots) != 3 {
		t.Fatalf("github defaults: %+v", cfg.GitHub)
	}
	if len(cfg.GitHub.Repos) != 2 || cfg.GitHub.Repos[0].Name != "arc42/arc42-template" || cfg.GitHub.Repos[1].PollInterval != 5*time.Minute {
		t.Fatalf("repos: %+v", cfg.GitHub.Repos)
	}
	if cfg.GitHub.RepoInterval("arc42/arc42-template") != 10*time.Minute || cfg.GitHub.RepoInterval("gernotstarke/esabuch.de-site") != 5*time.Minute {
		t.Fatal("RepoInterval")
	}
	if cfg.Secrets.GitHubToken != "t" || cfg.AuthMode != "dev" || cfg.DataPath != "/data/zorgscope.db" {
		t.Fatalf("env: %+v %s %s", cfg.Secrets, cfg.AuthMode, cfg.DataPath)
	}
	if cfg.Snapshot.Time != "03:00" || cfg.Snapshot.Hour != 3 || cfg.Snapshot.Minute != 0 || cfg.Snapshot.RetentionDays != 30 {
		t.Fatalf("snapshot defaults: %+v", cfg.Snapshot)
	}
	if cfg.UI.TilePollSeconds != 60 || cfg.UI.AttentionCap != 30 || cfg.UI.RefreshMinGapSeconds != 30 || len(cfg.UI.Tiles) != 6 {
		t.Fatalf("ui defaults: %+v", cfg.UI)
	}
	if !cfg.Watch.Enabled || cfg.Watch.WarnDays != 14 || cfg.Rules().WarnDays != 14 || cfg.Rules().Me != "gernotstarke" {
		t.Fatalf("watch/rules: %+v", cfg.Watch)
	}
	if cfg.GitHub.BaseURL != "https://api.github.com" {
		t.Fatalf("github base url default: %s", cfg.GitHub.BaseURL)
	}
}

func TestLoadRealConfig(t *testing.T) {
	cfg, err := Load("../../config/zorgscope.yaml", env(map[string]string{"GITHUB_TOKEN": "t", "PLAUSIBLE_API_KEY": "p", "TODOIST_TOKEN": "d", "SESSION_SECRET": strings.Repeat("x", 32), "ENROLL_TOKEN": "e"}))
	if err != nil {
		t.Fatalf("the shipped config must load: %v", err)
	}
	if len(cfg.GitHub.Repos) != 8 || len(cfg.Plausible.Sites) != 7 || len(cfg.Watch.URLs) != 1 {
		t.Fatalf("shipped config content changed unexpectedly: %d repos, %d sites, %d urls", len(cfg.GitHub.Repos), len(cfg.Plausible.Sites), len(cfg.Watch.URLs))
	}
}

func TestValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		env  map[string]string
		key  string
	}{
		{"unknown key", "server:\n  base_url: http://localhost:8080\n  prot: 1\n", nil, "prot"},
		{"bad base url", "server:\n  base_url: localhost\n", nil, "server.base_url"},
		{"dev mode on non-localhost", "server:\n  base_url: https://zorgscope.fly.dev\n", map[string]string{"AUTH_MODE": "dev"}, "AUTH_MODE"},
		{"bad auth mode", "server:\n  base_url: http://localhost:8080\n", map[string]string{"AUTH_MODE": "magic"}, "AUTH_MODE"},
		{"interval too small", "server:\n  base_url: http://localhost:8080\ngithub:\n  enabled: true\n  me: x\n  poll_interval: 5s\n  repos: [a/b]\n", map[string]string{"GITHUB_TOKEN": "t", "AUTH_MODE": "dev"}, "github.poll_interval"},
		{"bad repo name", "server:\n  base_url: http://localhost:8080\ngithub:\n  enabled: true\n  me: x\n  repos: [nope]\n", map[string]string{"GITHUB_TOKEN": "t", "AUTH_MODE": "dev"}, "github.repos[0]"},
		{"github enabled without token", "server:\n  base_url: http://localhost:8080\ngithub:\n  enabled: true\n  me: x\n  repos: [a/b]\n", map[string]string{"AUTH_MODE": "dev"}, "GITHUB_TOKEN"},
		{"github enabled without me", "server:\n  base_url: http://localhost:8080\ngithub:\n  enabled: true\n  repos: [a/b]\n", map[string]string{"GITHUB_TOKEN": "t", "AUTH_MODE": "dev"}, "github.me"},
		{"bad snapshot time", "server:\n  base_url: http://localhost:8080\nsnapshot:\n  time: 25:00\n", map[string]string{"AUTH_MODE": "dev"}, "snapshot.time"},
		{"bad timezone", "server:\n  base_url: http://localhost:8080\n  timezone: Mars/Olympus\n", nil, "server.timezone"},
		{"unknown tile", "server:\n  base_url: http://localhost:8080\nui:\n  tiles: [attention, weather]\n", map[string]string{"AUTH_MODE": "dev"}, "ui.tiles[1]"},
		{"bad credential date", "server:\n  base_url: http://localhost:8080\nwatch:\n  credentials:\n    - name: x\n      expires: 31.12.2026\n", map[string]string{"AUTH_MODE": "dev"}, "watch.credentials[0].expires"},
		{"passkey without session secret", "server:\n  base_url: https://zorgscope.fly.dev\n", map[string]string{"AUTH_MODE": "passkey"}, "SESSION_SECRET"},
		{"unknown key in repo mapping", "server:\n  base_url: http://localhost:8080\ngithub:\n  enabled: true\n  me: x\n  repos:\n    - name: a/b\n      pol_interval: 5m\n", map[string]string{"GITHUB_TOKEN": "t", "AUTH_MODE": "dev"}, "pol_interval"},
		{"plausible enabled without token", "server:\n  base_url: http://localhost:8080\nplausible:\n  enabled: true\n  sites: [arc42.org]\n", map[string]string{"AUTH_MODE": "dev"}, "PLAUSIBLE_API_KEY"},
		{"todoist enabled without token", "server:\n  base_url: http://localhost:8080\ntodoist:\n  enabled: true\n", map[string]string{"AUTH_MODE": "dev"}, "TODOIST_TOKEN"},
		{"bad plausible site", "server:\n  base_url: http://localhost:8080\nplausible:\n  enabled: true\n  sites: [arc42.org/bad]\n", map[string]string{"PLAUSIBLE_API_KEY": "p", "AUTH_MODE": "dev"}, "plausible.sites[0]"},
		{"malformed PORT", "server:\n  base_url: http://localhost:8080\n", map[string]string{"AUTH_MODE": "dev", "PORT": "abc"}, "PORT"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(c.yaml), env(c.env))
			if err == nil {
				t.Fatal("expected error")
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("expected ValidationError, got %T %v", err, err)
			}
			if !strings.Contains(ve.Key, c.key) {
				t.Fatalf("key = %q, want it to contain %q (msg %q)", ve.Key, c.key, ve.Msg)
			}
		})
	}
}

func TestDisabledSourcesNeedNoSecrets(t *testing.T) {
	y := "server:\n  base_url: http://localhost:8080\ngithub:\n  enabled: false\n"
	if _, err := Parse(strings.NewReader(y), env(map[string]string{"AUTH_MODE": "dev"})); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load("testdata/does-not-exist.yaml", env(nil)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
}

// TestLoadUnknownKeyFixture loads testdata/unknown-key.yaml through Load (rather than an inline
// string via Parse, as TestValidationErrors/unknown_key does) so the fixture file is exercised.
func TestLoadUnknownKeyFixture(t *testing.T) {
	_, err := Load("testdata/unknown-key.yaml", env(nil))
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationError, got %T %v", err, err)
	}
	if !strings.Contains(ve.Key, "prot") {
		t.Fatalf("key = %q, want it to contain %q", ve.Key, "prot")
	}
}

// TestIntervalBoundaryAccepted pins checkInterval's bound at >=, not >: exactly the minimum
// interval (10s) must be accepted.
func TestIntervalBoundaryAccepted(t *testing.T) {
	y := "server:\n  base_url: http://localhost:8080\ngithub:\n  enabled: true\n  me: x\n  poll_interval: 10s\n  repos: [a/b]\n"
	if _, err := Parse(strings.NewReader(y), env(map[string]string{"GITHUB_TOKEN": "t", "AUTH_MODE": "dev"})); err != nil {
		t.Fatalf("10s (the minimum) should be accepted: %v", err)
	}
}
