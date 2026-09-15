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
		t.Error("an unknown source is never enabled: there is nothing to fetch for it")
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

// GITHUB_OAUTH_BASE_URL exists so that `make fakes` and the tests can point the sign-in flow at a
// local fixture server. A value that is not a loopback address is therefore either a mistake or an
// attempt to send the visitor's authorisation — and this deployment's client secret with it — to
// somebody else's host, and either way it must not start (QS-4.3).
func TestLoadRejectsAnOAuthBaseURLThatIsNeitherHTTPSNorLoopback(t *testing.T) {
	base := map[string]string{
		"GITHUB_OAUTH_CLIENT_ID":     "id",
		"GITHUB_OAUTH_CLIENT_SECRET": "secret",
		"REFRESH_SECRET":             strings.Repeat("r", 32),
	}
	load := func(value string) error {
		m := maps.Clone(base)
		if value != "" {
			m["GITHUB_OAUTH_BASE_URL"] = value
		}
		_, err := config.Load("testdata/valid.yaml", env(m))
		return err
	}

	for _, ok := range []string{
		"",                                 // unset: the real github.com
		"https://github.example",           // an enterprise host, over TLS
		"http://localhost:9090",            // make fakes, from the host
		"http://127.0.0.1:9090",            // the same, by address
		"http://[::1]:9090",                // and over IPv6
		"http://host.docker.internal:9090", // make fakes, from inside the Compose network
	} {
		if err := load(ok); err != nil {
			t.Errorf("GITHUB_OAUTH_BASE_URL=%q: %v", ok, err)
		}
	}

	for _, bad := range []string{
		"http://github.com",    // plaintext to a host that is not this machine
		"http://evil.example",  // the attack this check exists for
		"ftp://localhost:9090", // loopback, but not a scheme an OAuth endpoint speaks
		"not a url",            // no scheme and no host at all
		"https://",             // a scheme and nothing to talk to
	} {
		err := load(bad)
		if err == nil {
			t.Errorf("GITHUB_OAUTH_BASE_URL=%q was accepted", bad)
			continue
		}
		if !strings.Contains(err.Error(), "GITHUB_OAUTH_BASE_URL") {
			t.Errorf("GITHUB_OAUTH_BASE_URL=%q: error %q must name the variable (FR-8.1 AC3)", bad, err)
		}
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

// QS-4.3: no secret value may reach an error message. The canary only proves that where it has
// actually travelled through a branch capable of printing it, so every validation that runs after
// the secrets are read gets its own case here — an unparsable duration fails before `Load` has so
// much as looked at the environment, and on its own would prove nothing at all.
func TestErrorNeverContainsSecretValues(t *testing.T) {
	const canary = "canary-secret-value-canary"
	full := map[string]string{
		"GITHUB_OAUTH_CLIENT_ID":     canary + "-client-id",
		"GITHUB_OAUTH_CLIENT_SECRET": canary + "-client-secret",
		"GITHUB_OAUTH_BASE_URL":      "http://" + canary + ".example",
		"REFRESH_SECRET":             canary + strings.Repeat("r", 32),
		"GITHUB_TOKEN":               canary + "-github-token",
		"SLACK_WEBHOOK_URL":          "https://hooks.example/" + canary,
		"TURSO_URL":                  "libsql://" + canary + ".example",
		"TURSO_AUTH_TOKEN":           canary + "-turso",
	}
	// An empty value means "unset this one", so that a case can reach the branch the one before it
	// stopped at.
	envWith := func(changes map[string]string) map[string]string {
		m := maps.Clone(full)
		for k, v := range changes {
			if v == "" {
				delete(m, k)
				continue
			}
			m[k] = v
		}
		return m
	}

	for name, tc := range map[string]struct {
		path string
		env  map[string]string
	}{
		"an unparsable duration":    {"testdata/bad-interval.yaml", full},
		"a missing auth repo":       {"testdata/no-auth-repo.yaml", full},
		"a missing client id":       {"testdata/valid.yaml", envWith(map[string]string{"GITHUB_OAUTH_CLIENT_ID": ""})},
		"a missing client secret":   {"testdata/valid.yaml", envWith(map[string]string{"GITHUB_OAUTH_CLIENT_SECRET": ""})},
		"an off-machine OAuth host": {"testdata/valid.yaml", full},
		"a short refresh secret":    {"testdata/valid.yaml", envWith(map[string]string{"GITHUB_OAUTH_BASE_URL": "", "REFRESH_SECRET": canary})},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(tc.path, env(tc.env))
			if err == nil {
				t.Fatal("this case has to fail, or the canary never reaches an error message")
			}
			if strings.Contains(err.Error(), canary) {
				t.Errorf("a secret value leaked into %q (QS-4.3)", err)
			}
		})
	}
}
