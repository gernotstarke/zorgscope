package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load reads the YAML file at path and applies environment overrides and validation.
func Load(path string, getenv func(string) string) (*Config, error) {
	f, err := os.Open(path) //nolint:gosec // path comes from the operator
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	cfg, err := Parse(f, getenv)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// Parse decodes YAML from r on top of Default(), applies env, validates.
func Parse(r io.Reader, getenv func(string) string) (*Config, error) {
	cfg := Default()
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		// A nested UnmarshalYAML (e.g. RepoConfig) may already have returned a *ValidationError;
		// yaml.v3 propagates such errors unwrapped (see decode.go's fail/handleErr), so pass it
		// through as-is instead of re-deriving a key from its text via yamlErrorKey.
		var ve *ValidationError
		if errors.As(err, &ve) {
			return nil, ve
		}
		return nil, &ValidationError{Key: yamlErrorKey(err), Msg: err.Error()}
	}
	if err := applyEnv(&cfg, getenv); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyEnv(cfg *Config, getenv func(string) string) error {
	set := func(dst *string, key string) {
		if v := getenv(key); v != "" {
			*dst = v
		}
	}
	set(&cfg.Secrets.GitHubToken, "GITHUB_TOKEN")
	set(&cfg.Secrets.PlausibleAPIKey, "PLAUSIBLE_API_KEY")
	set(&cfg.Secrets.TodoistToken, "TODOIST_TOKEN")
	set(&cfg.Secrets.SessionSecret, "SESSION_SECRET")
	set(&cfg.Secrets.EnrollToken, "ENROLL_TOKEN")
	set(&cfg.AuthMode, "AUTH_MODE")
	set(&cfg.LogLevel, "LOG_LEVEL")
	set(&cfg.DataPath, "ZORGSCOPE_DATA")
	set(&cfg.Server.BaseURL, "ZORGSCOPE_BASE_URL")
	set(&cfg.GitHub.BaseURL, "GITHUB_BASE_URL")
	set(&cfg.Plausible.BaseURL, "PLAUSIBLE_BASE_URL")
	set(&cfg.Todoist.BaseURL, "TODOIST_BASE_URL")
	if p := getenv("PORT"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			// FR-8.1 AC1 / QS-4.3: invalid config must abort startup naming the offending key,
			// not silently fall back to the default port.
			return &ValidationError{Key: "PORT", Msg: fmt.Sprintf("invalid integer: %q", p)}
		}
		cfg.Server.Port = n
	}
	return nil
}

// yamlErrorKey extracts the offending field name from yaml.v3 messages like
// `yaml: unmarshal errors:\n  line 3: field prot not found in type config.ServerConfig`.
func yamlErrorKey(err error) string {
	msg := err.Error()
	const marker = "field "
	if i := strings.Index(msg, marker); i >= 0 {
		rest := msg[i+len(marker):]
		if j := strings.Index(rest, " "); j > 0 {
			return rest[:j]
		}
		return rest
	}
	return "yaml"
}
