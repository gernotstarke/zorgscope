package config_test

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
)

func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

// fullEnv is every environment variable Load requires, present. Individual tests delete one to
// see it named in the error, or override GITHUB_OAUTH_BASE_URL.
func fullEnv() map[string]string {
	return map[string]string{
		"GITHUB_OAUTH_CLIENT_ID":     "id",
		"GITHUB_OAUTH_CLIENT_SECRET": "secret",
		"GITHUB_TOKEN":               "ghp_x",
	}
}

func TestLoadValid(t *testing.T) {
	cfg, err := config.Load("testdata/valid.yaml", env(fullEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GitHub.CacheTTL != 5*time.Minute {
		t.Errorf("cache_ttl = %v, want the 5m default (github.cache_ttl was not set)", cfg.GitHub.CacheTTL)
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

// FR-8.1 AC3: github.cache_ttl defaults to 5 minutes when the configuration names none.
func TestLoadCacheTTLDefaultsTo5m(t *testing.T) {
	cfg, err := config.Load("testdata/valid.yaml", env(fullEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GitHub.CacheTTL != 5*time.Minute {
		t.Errorf("cache_ttl = %v, want 5m", cfg.GitHub.CacheTTL)
	}
}

func TestLoadCacheTTLExplicitValue(t *testing.T) {
	cfg, err := config.Load("testdata/cache-ttl.yaml", env(fullEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GitHub.CacheTTL != 2*time.Minute {
		t.Errorf("cache_ttl = %v, want 2m", cfg.GitHub.CacheTTL)
	}
}

func TestLoadRejectsBadCacheTTL(t *testing.T) {
	for name, path := range map[string]string{
		"unparsable":   "testdata/bad-cache-ttl.yaml",
		"not positive": "testdata/negative-cache-ttl.yaml",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(path, env(fullEnv()))
			if err == nil {
				t.Fatal("want an error for a bad github.cache_ttl (FR-8.1 AC3)")
			}
			if !strings.Contains(err.Error(), "github.cache_ttl") {
				t.Errorf("error %q must name the offending field (FR-8.1 AC3)", err)
			}
		})
	}
}

// FR-8.3: sign-in needs the OAuth App's pair and the repository whose push access admits a
// visitor, and fetching needs a token, so a deployment missing any of the four fails at start-up
// rather than at the callback or with an empty page.
func TestLoadRequiresTheOAuthPairAuthRepoAndToken(t *testing.T) {
	base := fullEnv()

	if _, err := config.Load("testdata/valid.yaml", env(base)); err != nil {
		t.Fatalf("valid: %v", err)
	}
	for _, missing := range []string{"GITHUB_OAUTH_CLIENT_ID", "GITHUB_OAUTH_CLIENT_SECRET", "GITHUB_TOKEN"} {
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

// GITHUB_BASE_URL and GITHUB_OAUTH_BASE_URL exist so that the fake GitHub (cmd/fakesources) and the tests can point
// fetching and the sign-in flow at a local fixture server. A value that is neither https nor a
// loopback address is therefore either a mistake or an attempt to send this deployment's token, the
// visitor's authorisation or the client secret to somebody else's host in plaintext, and either way
// it must not start (QS-4.3, design §8).
func TestLoadRejectsABaseURLThatIsNeitherHTTPSNorLoopback(t *testing.T) {
	for _, variable := range []string{"GITHUB_BASE_URL", "GITHUB_OAUTH_BASE_URL"} {
		t.Run(variable, func(t *testing.T) {
			load := func(value string) error {
				m := fullEnv()
				if value != "" {
					m[variable] = value
				}
				_, err := config.Load("testdata/valid.yaml", env(m))
				return err
			}

			for _, ok := range []string{
				"",                                 // unset: the real GitHub
				"https://github.example",           // an enterprise host, over TLS
				"http://localhost:9090",            // the fake GitHub, from the host
				"http://127.0.0.1:9090",            // the same, by address
				"http://[::1]:9090",                // and over IPv6
				"http://host.docker.internal:9090", // the fake GitHub, from inside the Compose network
			} {
				if err := load(ok); err != nil {
					t.Errorf("%s=%q: %v", variable, ok, err)
				}
			}

			for _, bad := range []string{
				"http://github.com",    // plaintext to a host that is not this machine
				"http://evil.example",  // the attack this check exists for
				"ftp://localhost:9090", // loopback, but not a scheme GitHub speaks
				"not a url",            // no scheme and no host at all
				"https://",             // a scheme and nothing to talk to
			} {
				err := load(bad)
				if err == nil {
					t.Errorf("%s=%q was accepted", variable, bad)
					continue
				}
				if !strings.Contains(err.Error(), variable) {
					t.Errorf("%s=%q: error %q must name the variable (FR-8.1 AC3)", variable, bad, err)
				}
			}
		})
	}
}

// FR-8.1 Step 1: "at least one repo, every one in owner/name form" — an empty or omitted
// github.repos must fail at start-up, not load into an app with nothing to fetch.
func TestLoadRejectsNoRepos(t *testing.T) {
	for name, path := range map[string]string{
		"empty":   "testdata/no-repos.yaml",
		"omitted": "testdata/omitted-repos.yaml",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(path, env(fullEnv()))
			if err == nil {
				t.Fatal("want an error for github.repos with no entries")
			}
			if !strings.Contains(err.Error(), "github.repos") {
				t.Errorf("error %q must name the offending field", err)
			}
		})
	}
}

// TestLoadRejectsUnknownField guards against a misspelled top-level key (e.g. "githbu" instead of
// "github") being silently dropped, which would leave that source unconfigured with no error.
func TestLoadRejectsUnknownField(t *testing.T) {
	_, err := config.Load("testdata/unknown-key.yaml", env(fullEnv()))
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
	_, err := config.Load("../../config/zorgscope.yaml", env(fullEnv()))
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
		"GITHUB_TOKEN":               canary + "-github-token",
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
		"an unparsable cache_ttl":   {"testdata/bad-cache-ttl.yaml", full},
		"a missing auth repo":       {"testdata/no-auth-repo.yaml", full},
		"a missing client id":       {"testdata/valid.yaml", envWith(map[string]string{"GITHUB_OAUTH_CLIENT_ID": ""})},
		"a missing client secret":   {"testdata/valid.yaml", envWith(map[string]string{"GITHUB_OAUTH_CLIENT_SECRET": ""})},
		"a missing github token":    {"testdata/valid.yaml", envWith(map[string]string{"GITHUB_TOKEN": ""})},
		"an off-machine OAuth host": {"testdata/valid.yaml", full},
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

// FR-8.1 AC5: github.sites loads in configuration order with every field.
func TestLoadSites(t *testing.T) {
	cfg, err := config.Load("testdata/sites.yaml", env(fullEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []config.Site{
		{Name: "one.example", URL: "https://one.example", Repo: "org/one", Hue: "navy"},
		{Name: "two.example", URL: "https://two.example", Repo: "org/two", Hue: "rose", Tag: "DE"},
	}
	if !slices.Equal(cfg.GitHub.Sites, want) {
		t.Errorf("sites = %+v, want %+v", cfg.GitHub.Sites, want)
	}
}

// FR-8.1 AC5: github.sites is optional.
func TestLoadWithoutSitesHasNone(t *testing.T) {
	cfg, err := config.Load("testdata/valid.yaml", env(fullEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.GitHub.Sites) != 0 {
		t.Errorf("sites = %+v, want none", cfg.GitHub.Sites)
	}
}

// FR-8.1 AC5: every rule a site breaks aborts start-up with an error naming the field.
func TestLoadRejectsBadSites(t *testing.T) {
	const head = "timezone: Europe/Berlin\n" +
		"github:\n" +
		"  auth_repo: gernotstarke/zorgscope\n" +
		"  repos: [org/one, org/two]\n" +
		"  sites:\n"
	cases := []struct {
		name, sites, field string
	}{
		{"missing name",
			`    - {url: "https://one.example", repo: org/one, hue: navy}`,
			"github.sites[0].name"},
		{"duplicate name",
			`    - {name: one.example, url: "https://one.example", repo: org/one, hue: navy}` + "\n" +
				`    - {name: one.example, url: "https://two.example", repo: org/two, hue: navy}`,
			"github.sites[1].name"},
		{"http url",
			`    - {name: one.example, url: "http://one.example", repo: org/one, hue: navy}`,
			"github.sites[0].url"},
		{"relative url",
			`    - {name: one.example, url: "/one", repo: org/one, hue: navy}`,
			"github.sites[0].url"},
		{"repo not in owner/name form",
			`    - {name: one.example, url: "https://one.example", repo: one, hue: navy}`,
			"github.sites[0].repo"},
		{"repo not watched",
			`    - {name: one.example, url: "https://one.example", repo: org/three, hue: navy}`,
			"github.sites[0].repo"},
		{"repo claimed twice",
			`    - {name: one.example, url: "https://one.example", repo: org/one, hue: navy}` + "\n" +
				`    - {name: two.example, url: "https://two.example", repo: org/one, hue: rose}`,
			"github.sites[1].repo"},
		{"unknown hue",
			`    - {name: one.example, url: "https://one.example", repo: org/one, hue: lime}`,
			"github.sites[0].hue"},
		{"tag too long",
			`    - {name: one.example, url: "https://one.example", repo: org/one, hue: navy, tag: DEUT}`,
			"github.sites[0].tag"},
		{"unknown field",
			`    - {name: one.example, url: "https://one.example", repo: org/one, hue: navy, colour: navy}`,
			"colour"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "zorgscope.yaml")
			if err := os.WriteFile(path, []byte(head+tc.sites+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := config.Load(path, env(fullEnv()))
			if err == nil {
				t.Fatalf("want an error naming %s (FR-8.1 AC5)", tc.field)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("error %q must name %s (FR-8.1 AC5)", err, tc.field)
			}
		})
	}
}

// FR-1.8 AC1: the committed configuration names the ten arc42 and zorgscope sites, in the order
// their tiles are drawn.
func TestTheRealConfigNamesTheTenSitesInOrder(t *testing.T) {
	cfg, err := config.Load("../../config/zorgscope.yaml", env(fullEnv()))
	if err != nil {
		t.Fatalf("Load(config/zorgscope.yaml): %v", err)
	}
	var names []string
	for _, s := range cfg.GitHub.Sites {
		names = append(names, s.Name)
	}
	want := []string{
		"arc42-template", "arc42.org", "arc42.de", "quality.arc42.org", "docs.arc42.org",
		"faq.arc42.org", "examples.arc42.org", "trainings.arc42.org", "arc42-generator", "zorgscope",
	}
	if !slices.Equal(names, want) {
		t.Errorf("sites = %v, want %v", names, want)
	}
}

// FR-1.8 AC1: the shipped configuration claims every watched repository, so the Other tile it
// ships with is empty — every entry of github.repos is named by exactly one github.sites entry,
// in the same order.
func TestTheRealConfigSitesClaimEveryRepoInOrder(t *testing.T) {
	cfg, err := config.Load("../../config/zorgscope.yaml", env(fullEnv()))
	if err != nil {
		t.Fatalf("Load(config/zorgscope.yaml): %v", err)
	}
	var claimed []string
	for _, s := range cfg.GitHub.Sites {
		claimed = append(claimed, s.Repo)
	}
	if !slices.Equal(claimed, cfg.GitHub.Repos) {
		t.Errorf("sites claim repos %v in order, want repos %v", claimed, cfg.GitHub.Repos)
	}
}

// FR-1.14: github.owner names the login zorgscope works for. It is optional — without it only
// the marked items need anyone — but a value that cannot be a GitHub login is refused at start-up
// rather than silently matching nobody.
func TestLoadOwner(t *testing.T) {
	cfg, err := config.Load("testdata/owner.yaml", env(fullEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GitHub.Owner != "gernotstarke" {
		t.Errorf("owner = %q, want gernotstarke", cfg.GitHub.Owner)
	}
	cfg, err = config.Load("testdata/valid.yaml", env(fullEnv()))
	if err != nil || cfg.GitHub.Owner != "" {
		t.Errorf("owner without github.owner = %q (err %v), want empty", cfg.GitHub.Owner, err)
	}
	if _, err := config.Load("testdata/bad-owner.yaml", env(fullEnv())); err == nil || !strings.Contains(err.Error(), "github.owner") {
		t.Errorf("a malformed github.owner must be refused naming the field, got %v", err)
	}
}
