package config_test

import (
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
)

func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

func TestLoadValid(t *testing.T) {
	cfg, err := config.Load("testdata/valid.yaml", env(map[string]string{
		"GITHUB_OAUTH_CLIENT_ID":     "id",
		"GITHUB_OAUTH_CLIENT_SECRET": "secret",
		"REFRESH_SECRET":             strings.Repeat("r", 32),
		"GITHUB_TOKEN":               "ghp_x",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Refresh.Interval != 15*time.Minute {
		t.Errorf("interval = %v, want 15m", cfg.Refresh.Interval)
	}
	if len(cfg.GitHub.Repos) != 2 {
		t.Errorf("repos = %d, want 2", len(cfg.GitHub.Repos))
	}
	if !cfg.Enabled("github") {
		t.Error("github should be enabled: its token is present")
	}
	if cfg.Enabled("todoist") {
		t.Error("todoist should be disabled: no token (FR-8.2 AC2)")
	}
}

func TestLoadRejectsBadDuration(t *testing.T) {
	_, err := config.Load("testdata/bad-interval.yaml", env(map[string]string{
		"GITHUB_OAUTH_CLIENT_ID":     "id",
		"GITHUB_OAUTH_CLIENT_SECRET": "secret",
		"REFRESH_SECRET":             strings.Repeat("r", 32),
	}))
	if err == nil {
		t.Fatal("want an error for an unparsable interval (FR-8.1 AC3)")
	}
	if !strings.Contains(err.Error(), "refresh.interval") {
		t.Errorf("error %q must name the offending field (FR-8.1 AC3)", err)
	}
}

func TestLoadRequiresALongEnoughRefreshSecret(t *testing.T) {
	_, err := config.Load("testdata/valid.yaml", env(map[string]string{
		"GITHUB_OAUTH_CLIENT_ID":     "id",
		"GITHUB_OAUTH_CLIENT_SECRET": "secret",
		"REFRESH_SECRET":             "short",
	}))
	if err == nil {
		t.Fatal("want an error: REFRESH_SECRET below 32 characters")
	}
}

// FR-8.3: sign-in needs the OAuth App's pair and the repository whose push access admits a
// visitor, so a deployment missing any of the three fails at start-up rather than at the callback.
func TestLoadRequiresTheOAuthPairAndTheAuthRepo(t *testing.T) {
	base := map[string]string{"REFRESH_SECRET": strings.Repeat("r", 32), "GITHUB_OAUTH_CLIENT_ID": "id", "GITHUB_OAUTH_CLIENT_SECRET": "secret"}

	if _, err := config.Load("testdata/valid.yaml", env(base)); err != nil {
		t.Fatalf("valid: %v", err)
	}
	for _, missing := range []string{"GITHUB_OAUTH_CLIENT_ID", "GITHUB_OAUTH_CLIENT_SECRET"} {
		m := maps.Clone(base)
		delete(m, missing)
		if _, err := config.Load("testdata/valid.yaml", env(m)); err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("without %s: err = %v", missing, err)
		}
	}
	if _, err := config.Load("testdata/no-auth-repo.yaml", env(base)); err == nil || !strings.Contains(err.Error(), "github.auth_repo") {
		t.Errorf("without auth_repo: err = %v", err)
	}
}

// TestLoadRejectsUnknownField guards against a misspelled top-level key (e.g. "githbu" instead of
// "github") being silently dropped, which would leave that source unconfigured with no error.
func TestLoadRejectsUnknownField(t *testing.T) {
	_, err := config.Load("testdata/unknown-key.yaml", env(map[string]string{
		"GITHUB_OAUTH_CLIENT_ID":     "id",
		"GITHUB_OAUTH_CLIENT_SECRET": "secret",
		"REFRESH_SECRET":             strings.Repeat("r", 32),
	}))
	if err == nil {
		t.Fatal("want an error for an unknown top-level key")
	}
	if !strings.Contains(err.Error(), "githbu") {
		t.Errorf("error %q must name the offending field", err)
	}
}

// TestLoadRealConfigFile guards against the loader rejecting the config the app actually ships
// with — config/zorgscope.yaml is baked into the production image, so a mismatch here would only
// surface as a start-up failure in production.
func TestLoadRealConfigFile(t *testing.T) {
	_, err := config.Load("../../config/zorgscope.yaml", env(map[string]string{
		"GITHUB_OAUTH_CLIENT_ID":     "id",
		"GITHUB_OAUTH_CLIENT_SECRET": "secret",
		"REFRESH_SECRET":             strings.Repeat("r", 32),
	}))
	if err != nil {
		t.Fatalf("Load(config/zorgscope.yaml): %v", err)
	}
}

func TestErrorNeverContainsSecretValues(t *testing.T) {
	const canary = "canary-secret-value-canary"
	_, err := config.Load("testdata/bad-interval.yaml", env(map[string]string{
		"GITHUB_OAUTH_CLIENT_ID":     canary + "-client-id",
		"GITHUB_OAUTH_CLIENT_SECRET": canary + "-client-secret",
		"REFRESH_SECRET":             canary + strings.Repeat("r", 32),
	}))
	if err != nil && strings.Contains(err.Error(), canary) {
		t.Fatal("a secret value leaked into an error message (QS-4.3)")
	}
}
