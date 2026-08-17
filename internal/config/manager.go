package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// ManagerOptions identifies the immutable image configuration and the two writable Fly volume
// files. RuntimePath is preferred over BootstrapPath whenever it exists.
type ManagerOptions struct {
	BootstrapPath string
	RuntimePath   string
	SecretPath    string
	Getenv        func(string) string
}

// SecretName is one of the upstream credentials managed by the authenticated config API.
type SecretName string

// API-manageable provider-secret names.
const (
	SecretGitHubToken     SecretName = "github_token"
	SecretPlausibleAPIKey SecretName = "plausible_api_key"
)

var (
	// ErrRevisionConflict is returned when a mutation was based on a stale Document.
	ErrRevisionConflict = errors.New("config revision conflict")
	// ErrUnknownSecret is returned for deployment secrets and unrecognised secret names.
	ErrUnknownSecret = errors.New("secret is not API-manageable")
)

// RevisionConflictError includes the current revision without exposing configuration secrets.
type RevisionConflictError struct {
	Expected string
	Current  string
}

func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("%v: expected %q, current %q", ErrRevisionConflict, e.Expected, e.Current)
}
func (e *RevisionConflictError) Unwrap() error { return ErrRevisionConflict }

type secretOverrides struct {
	// A nil pointer means fall back to the bootstrap environment. A pointer to the empty string is
	// an explicit tombstone, so ClearSecret cannot accidentally reveal an old Fly secret again.
	GitHubToken     *string `json:"github_token,omitempty"`
	PlausibleAPIKey *string `json:"plausible_api_key,omitempty"`
}

type encryptedSecretPayload struct {
	Version   int             `json:"version"`
	Overrides secretOverrides `json:"overrides"`
}

// Manager owns the validated effective configuration. All returned values are independent copies.
type Manager struct {
	mu sync.RWMutex

	runtimePath string
	secretPath  string
	envSecrets  Secrets
	key         []byte
	overrides   secretOverrides
	current     *Config
	revision    string
}

var managedEnvKeys = []string{
	"GITHUB_TOKEN", "PLAUSIBLE_API_KEY", "SESSION_SECRET", "ENROLL_TOKEN",
	"ZORGSCOPE_API_TOKEN", "ZORGSCOPE_CONFIG_KEY", "AUTH_MODE", "LOG_LEVEL",
	"ZORGSCOPE_DATA", "ZORGSCOPE_BASE_URL", "GITHUB_BASE_URL", "PLAUSIBLE_BASE_URL", "PORT",
}

// NewManager resolves deployment settings from the image bootstrap plus environment. When a
// writable runtime YAML exists, its editable fields take precedence while deployment settings and
// environment secrets remain fixed.
func NewManager(opts ManagerOptions) (*Manager, error) {
	if opts.BootstrapPath == "" {
		return nil, errors.New("config manager: BootstrapPath is required")
	}
	if opts.RuntimePath == "" {
		return nil, errors.New("config manager: RuntimePath is required")
	}
	if opts.SecretPath == "" {
		return nil, errors.New("config manager: SecretPath is required")
	}
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	frozenEnv := make(map[string]string, len(managedEnvKeys))
	for _, key := range managedEnvKeys {
		frozenEnv[key] = getenv(key)
	}
	envfn := func(key string) string { return frozenEnv[key] }

	bootstrap, err := decodeFile(opts.BootstrapPath)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", opts.BootstrapPath, err)
	}
	if err := ApplyEnv(bootstrap, envfn); err != nil {
		return nil, err
	}
	cfg := bootstrap
	if _, statErr := os.Stat(opts.RuntimePath); statErr == nil {
		cfg, err = decodeFile(opts.RuntimePath)
		if err != nil {
			return nil, fmt.Errorf("config %s: %w", opts.RuntimePath, err)
		}
		applyDeployment(cfg, bootstrap, bootstrap.Secrets)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("stat runtime config %s: %w", opts.RuntimePath, statErr)
	}
	key, err := decodeConfigKey(bootstrap.Secrets.ConfigKey)
	if err != nil {
		return nil, err
	}

	overrides, err := loadSecretOverrides(opts.SecretPath, key)
	if err != nil {
		return nil, err
	}
	envSecrets := bootstrap.Secrets
	applySecretOverrides(&cfg.Secrets, overrides)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	m := &Manager{runtimePath: opts.RuntimePath, secretPath: opts.SecretPath,
		envSecrets: envSecrets, key: key, overrides: overrides, current: cfg}
	m.revision = m.calculateRevision(cfg, overrides)
	return m, nil
}

func decodeFile(path string) (*Config, error) {
	f, err := os.Open(path) //nolint:gosec // paths are operator-owned ManagerOptions
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return Decode(f)
}

// Current returns a deep clone and its opaque optimistic-concurrency revision.
func (m *Manager) Current() (*Config, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneConfig(m.current), m.revision
}

// Document returns a complete deep-copied API DTO. It never includes a secret value.
func (m *Manager) Document() Document {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.documentLocked()
}

// Update validates and atomically persists a complete editable configuration. The deployment
// environment and managed secrets are reapplied before validation and cannot be changed here.
func (m *Manager) Update(expectedRevision string, next EditableConfig) (Document, error) {
	return m.UpdateAndApply(expectedRevision, next, nil)
}

// UpdateAndApply validates and persists a complete editable configuration, invokes apply with an
// independent candidate, and commits the Manager state only if activation succeeds. The mutation
// lock deliberately spans persistence and activation so concurrent requests cannot activate
// generations out of revision order. The callback must not call back into this Manager.
func (m *Manager) UpdateAndApply(expectedRevision string, next EditableConfig, apply func(*Config) error) (Document, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkRevision(expectedRevision); err != nil {
		return Document{}, err
	}
	candidate, err := next.config()
	if err != nil {
		return Document{}, err
	}
	applyDeployment(candidate, m.current, m.envSecrets)
	applySecretOverrides(&candidate.Secrets, m.overrides)
	if err := candidate.Validate(); err != nil {
		return Document{}, err
	}

	// Persist exactly the validated/effective editable state, including defaults resolved by
	// Validate. Deployment-only fields and secrets have no representation in this DTO.
	persisted := editableFromConfig(candidate)
	data, err := yaml.Marshal(persisted)
	if err != nil {
		return Document{}, fmt.Errorf("marshal runtime config: %w", err)
	}
	previousFile, err := snapshotFile(m.runtimePath)
	if err != nil {
		return Document{}, fmt.Errorf("snapshot runtime config: %w", err)
	}
	if err := atomicWrite(m.runtimePath, data); err != nil {
		return Document{}, fmt.Errorf("persist runtime config: %w", err)
	}
	if apply != nil {
		if err := apply(cloneConfig(candidate)); err != nil {
			if restoreErr := restoreFile(m.runtimePath, previousFile); restoreErr != nil {
				return Document{}, errors.Join(fmt.Errorf("activate runtime config: %w", err),
					fmt.Errorf("restore runtime config: %w", restoreErr))
			}
			return Document{}, fmt.Errorf("activate runtime config: %w", err)
		}
	}
	m.current = candidate
	m.revision = m.calculateRevision(candidate, m.overrides)
	return m.documentLocked(), nil
}

// SetSecret validates and atomically stores an encrypted upstream credential. Empty values are
// rejected so clients must use ClearSecret for an intentional removal.
func (m *Manager) SetSecret(expectedRevision string, name SecretName, value string) (Document, error) {
	return m.SetSecretAndApply(expectedRevision, name, value, nil)
}

// SetSecretAndApply is SetSecret with atomic runtime activation. See UpdateAndApply for callback
// locking and rollback semantics.
func (m *Manager) SetSecretAndApply(expectedRevision string, name SecretName, value string, apply func(*Config) error) (Document, error) {
	if value == "" {
		return Document{}, fail(string(name), "secret value must not be empty; use clear")
	}
	return m.mutateSecret(expectedRevision, name, &value, apply)
}

// ClearSecret installs an explicit empty override instead of falling back to a deployment value.
// Validation therefore prevents clearing a credential while its source remains enabled.
func (m *Manager) ClearSecret(expectedRevision string, name SecretName) (Document, error) {
	return m.ClearSecretAndApply(expectedRevision, name, nil)
}

// ClearSecretAndApply is ClearSecret with atomic runtime activation. See UpdateAndApply for
// callback locking and rollback semantics.
func (m *Manager) ClearSecretAndApply(expectedRevision string, name SecretName, apply func(*Config) error) (Document, error) {
	value := ""
	return m.mutateSecret(expectedRevision, name, &value, apply)
}

func (m *Manager) mutateSecret(expectedRevision string, name SecretName, value *string, apply func(*Config) error) (Document, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkRevision(expectedRevision); err != nil {
		return Document{}, err
	}
	next := cloneOverrides(m.overrides)
	switch name {
	case SecretGitHubToken:
		next.GitHubToken = copyStringPointer(value)
	case SecretPlausibleAPIKey:
		next.PlausibleAPIKey = copyStringPointer(value)
	default:
		return Document{}, fmt.Errorf("%w: %q", ErrUnknownSecret, name)
	}
	candidate := cloneConfig(m.current)
	// Restore environment values before applying the complete next override set. This matters when
	// a future mutation implementation removes an override rather than writing a tombstone.
	candidate.Secrets.GitHubToken = m.envSecrets.GitHubToken
	candidate.Secrets.PlausibleAPIKey = m.envSecrets.PlausibleAPIKey
	applySecretOverrides(&candidate.Secrets, next)
	if err := candidate.Validate(); err != nil {
		return Document{}, err
	}
	previousFile, err := snapshotFile(m.secretPath)
	if err != nil {
		return Document{}, fmt.Errorf("snapshot encrypted secrets: %w", err)
	}
	if err := saveSecretOverrides(m.secretPath, m.key, next); err != nil {
		return Document{}, err
	}
	if apply != nil {
		if err := apply(cloneConfig(candidate)); err != nil {
			if restoreErr := restoreFile(m.secretPath, previousFile); restoreErr != nil {
				return Document{}, errors.Join(fmt.Errorf("activate encrypted secrets: %w", err),
					fmt.Errorf("restore encrypted secrets: %w", restoreErr))
			}
			return Document{}, fmt.Errorf("activate encrypted secrets: %w", err)
		}
	}
	m.overrides = next
	m.current = candidate
	m.revision = m.calculateRevision(candidate, next)
	return m.documentLocked(), nil
}

func (m *Manager) checkRevision(expected string) error {
	if !hmac.Equal([]byte(expected), []byte(m.revision)) {
		return &RevisionConflictError{Expected: expected, Current: m.revision}
	}
	return nil
}

func (m *Manager) documentLocked() Document {
	return Document{
		SchemaVersion: DocumentSchemaVersion,
		Revision:      m.revision,
		Config:        editableFromConfig(m.current),
		Deployment:    deploymentFromConfig(m.current),
		Secrets:       m.secretStatusLocked(),
	}
}

func (m *Manager) secretStatusLocked() SecretStatusView {
	managedState := func(p *string, envValue string) SecretState {
		if p != nil {
			if *p == "" {
				return SecretState{Source: "none"}
			}
			return SecretState{Configured: true, Source: "managed"}
		}
		return environmentState(envValue)
	}
	return SecretStatusView{
		GitHubToken:     managedState(m.overrides.GitHubToken, m.envSecrets.GitHubToken),
		PlausibleAPIKey: managedState(m.overrides.PlausibleAPIKey, m.envSecrets.PlausibleAPIKey),
	}
}

func environmentState(value string) SecretState {
	if value == "" {
		return SecretState{Source: "none"}
	}
	return SecretState{Configured: true, Source: "environment"}
}

func (m *Manager) calculateRevision(cfg *Config, overrides secretOverrides) string {
	payload := struct {
		Config     EditableConfig
		Deployment DeploymentView
		GitHub     string
		Plausible  string
		Overrides  secretOverrides
	}{
		Config:     editableFromConfig(cfg),
		Deployment: deploymentFromConfig(cfg),
		GitHub:     cfg.Secrets.GitHubToken, Plausible: cfg.Secrets.PlausibleAPIKey, Overrides: overrides,
	}
	data, _ := json.Marshal(payload) // all fields above are JSON-marshalable
	mac := hmac.New(sha256.New, m.key)
	_, _ = mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func decodeConfigKey(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, fail("ZORGSCOPE_CONFIG_KEY", "required for encrypted runtime secrets")
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil || len(key) != 32 {
		return nil, fail("ZORGSCOPE_CONFIG_KEY", "must be base64 encoding of exactly 32 bytes")
	}
	return key, nil
}

func loadSecretOverrides(path string, key []byte) (secretOverrides, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-owned ManagerOptions path
	if errors.Is(err, os.ErrNotExist) {
		return secretOverrides{}, nil
	}
	if err != nil {
		return secretOverrides{}, fmt.Errorf("read encrypted secrets: %w", err)
	}
	plain, err := decryptSecretData(data, key)
	if err != nil {
		return secretOverrides{}, fmt.Errorf("decrypt encrypted secrets: %w", err)
	}
	var payload encryptedSecretPayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return secretOverrides{}, fmt.Errorf("decode encrypted secrets: %w", err)
	}
	if payload.Version != 1 {
		return secretOverrides{}, fmt.Errorf("decode encrypted secrets: unsupported version %d", payload.Version)
	}
	return cloneOverrides(payload.Overrides), nil
}

func saveSecretOverrides(path string, key []byte, overrides secretOverrides) error {
	plain, err := json.Marshal(encryptedSecretPayload{Version: 1, Overrides: overrides})
	if err != nil {
		return fmt.Errorf("encode encrypted secrets: %w", err)
	}
	data, err := encryptSecretData(plain, key)
	if err != nil {
		return fmt.Errorf("encrypt secrets: %w", err)
	}
	if err := atomicWrite(path, data); err != nil {
		return fmt.Errorf("persist encrypted secrets: %w", err)
	}
	return nil
}

const encryptedFilePrefix = "zorgscope-secrets-v1\n"

func encryptSecretData(plain, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, plain, []byte(encryptedFilePrefix))
	combined := make([]byte, 0, len(nonce)+len(sealed))
	combined = append(combined, nonce...)
	combined = append(combined, sealed...)
	encoded := make([]byte, base64.StdEncoding.EncodedLen(len(combined)))
	base64.StdEncoding.Encode(encoded, combined)
	return append([]byte(encryptedFilePrefix), encoded...), nil
}

func decryptSecretData(data, key []byte) ([]byte, error) {
	if len(data) < len(encryptedFilePrefix) || string(data[:len(encryptedFilePrefix)]) != encryptedFilePrefix {
		return nil, errors.New("invalid encrypted secret file header")
	}
	combined, err := base64.StdEncoding.DecodeString(string(data[len(encryptedFilePrefix):]))
	if err != nil {
		return nil, errors.New("invalid encrypted secret file encoding")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(combined) < gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("truncated encrypted secret file")
	}
	nonce, ciphertext := combined[:gcm.NonceSize()], combined[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte(encryptedFilePrefix))
	if err != nil {
		return nil, errors.New("encrypted secret authentication failed")
	}
	return plain, nil
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".zorgscope-config-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	committed = true
	// Once Rename succeeds the new file is the visible state. Directory fsync improves crash
	// durability, but reporting its failure as an uncommitted write would split Manager memory from
	// the already-renamed file. It is therefore intentionally best-effort.
	syncDirectory(dir)
	return nil
}

type fileSnapshot struct {
	exists bool
	data   []byte
}

func snapshotFile(path string) (fileSnapshot, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-owned Manager path
	if errors.Is(err, os.ErrNotExist) {
		return fileSnapshot{}, nil
	}
	if err != nil {
		return fileSnapshot{}, err
	}
	return fileSnapshot{exists: true, data: data}, nil
}

func restoreFile(path string, previous fileSnapshot) error {
	if previous.exists {
		return atomicWrite(path, previous.data)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	syncDirectory(filepath.Dir(path))
	return nil
}

func syncDirectory(dir string) {
	dirFile, err := os.Open(dir) //nolint:gosec // dir is derived from an operator-owned path
	if err != nil {
		return
	}
	_ = dirFile.Sync()
	_ = dirFile.Close()
}

func applyDeployment(dst, src *Config, secrets Secrets) {
	dst.Server.BaseURL = src.Server.BaseURL
	dst.Server.Port = src.Server.Port
	dst.AuthMode = src.AuthMode
	dst.LogLevel = src.LogLevel
	dst.DataPath = src.DataPath
	dst.Secrets = secrets
}

func deploymentFromConfig(cfg *Config) DeploymentView {
	return DeploymentView{AuthMode: cfg.AuthMode, LogLevel: cfg.LogLevel, DataPath: cfg.DataPath,
		BaseURL: cfg.Server.BaseURL, Port: cfg.Server.Port}
}

func applySecretOverrides(s *Secrets, overrides secretOverrides) {
	if overrides.GitHubToken != nil {
		s.GitHubToken = *overrides.GitHubToken
	}
	if overrides.PlausibleAPIKey != nil {
		s.PlausibleAPIKey = *overrides.PlausibleAPIKey
	}
}

func cloneOverrides(in secretOverrides) secretOverrides {
	return secretOverrides{GitHubToken: copyStringPointer(in.GitHubToken),
		PlausibleAPIKey: copyStringPointer(in.PlausibleAPIKey)}
}

func copyStringPointer(in *string) *string {
	if in == nil {
		return nil
	}
	v := *in
	return &v
}

func cloneConfig(in *Config) *Config {
	if in == nil {
		return nil
	}
	out := *in
	out.UI.Tiles = cloneStrings(in.UI.Tiles)
	out.GitHub.Bots = cloneStrings(in.GitHub.Bots)
	out.GitHub.Repos = append([]RepoConfig(nil), in.GitHub.Repos...)
	out.Plausible.Sites = cloneStrings(in.Plausible.Sites)
	out.Watch.Credentials = append([]CredentialConfig(nil), in.Watch.Credentials...)
	out.Watch.URLs = append([]URLCheckConfig(nil), in.Watch.URLs...)
	return &out
}
