package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const managerBootstrap = `
server:
  base_url: http://localhost:8080
  timezone: Europe/Berlin
github:
  enabled: false
  me: test-user
  repos: [owner/repo]
plausible:
  enabled: false
  sites: [example.com]
watch:
  enabled: true
  warn_days: 14
`

type managerFixture struct {
	dir       string
	bootstrap string
	runtime   string
	secrets   string
	env       map[string]string
}

func newManagerFixture(t *testing.T, bootstrap string) managerFixture {
	t.Helper()
	dir := t.TempDir()
	f := managerFixture{
		dir: dir, bootstrap: filepath.Join(dir, "image.yaml"), runtime: filepath.Join(dir, "runtime", "config.yaml"),
		secrets: filepath.Join(dir, "runtime", "secrets.enc"),
		env: map[string]string{
			"AUTH_MODE":            "dev",
			"ZORGSCOPE_CONFIG_KEY": base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901")),
			"GITHUB_TOKEN":         "environment-github-token",
			"SESSION_SECRET":       strings.Repeat("s", 32),
		},
	}
	if err := os.WriteFile(f.bootstrap, []byte(bootstrap), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f managerFixture) open(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(ManagerOptions{BootstrapPath: f.bootstrap, RuntimePath: f.runtime,
		SecretPath: f.secrets, Getenv: env(f.env)})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestManagerBootstrapsAndPrefersRuntimeConfig(t *testing.T) {
	f := newManagerFixture(t, managerBootstrap)
	if err := os.MkdirAll(filepath.Dir(f.runtime), 0o700); err != nil {
		t.Fatal(err)
	}
	runtime := strings.Replace(managerBootstrap, "Europe/Berlin", "UTC", 1)
	if err := os.WriteFile(f.runtime, []byte(runtime), 0o600); err != nil {
		t.Fatal(err)
	}
	m := f.open(t)
	doc := m.Document()
	if doc.Config.Server.Timezone != "UTC" {
		t.Fatalf("runtime config was not preferred: %+v", doc.Config.Server)
	}
	if doc.SchemaVersion != DocumentSchemaVersion || doc.Revision == "" || doc.Config.GitHub.PollInterval != "10m0s" ||
		doc.Config.Watch.PollInterval != "15m0s" || doc.Config.Watch.URLs != nil {
		t.Fatalf("incomplete/default-free document: %+v", doc)
	}
	if doc.Deployment.AuthMode != "dev" || doc.Deployment.DataPath != "/data/zorgscope.db" ||
		doc.Deployment.BaseURL != "http://localhost:8080" || doc.Deployment.Port != 8080 {
		t.Fatalf("deployment view: %+v", doc.Deployment)
	}
	if got := doc.Secrets.GitHubToken; !got.Configured || got.Source != "environment" {
		t.Fatalf("github status: %+v", got)
	}
}

func TestManagerUpdatePersistsCompleteDurationStringYAML(t *testing.T) {
	f := newManagerFixture(t, managerBootstrap)
	m := f.open(t)
	doc := m.Document()
	next := doc.Config
	next.UI.AttentionCap = 77
	next.GitHub.Enabled = true
	next.GitHub.PollInterval = "12m"
	next.GitHub.Repos = append(next.GitHub.Repos, EditableRepoConfig{Name: "other/project", PollInterval: "42m"})
	updated, err := m.Update(doc.Revision, next)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision == doc.Revision || updated.Config.UI.AttentionCap != 77 || updated.Config.GitHub.PollInterval != "12m0s" {
		t.Fatalf("update did not commit: %+v", updated)
	}
	data, err := os.ReadFile(f.runtime)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "poll_interval: 12m0s") || strings.Contains(text, "720000000000") || strings.Contains(text, "environment-github-token") {
		t.Fatalf("runtime YAML is not secret-safe duration text:\n%s", text)
	}
	if info, err := os.Stat(f.runtime); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("runtime mode = %v, err %v", info.Mode().Perm(), err)
	}
	restarted := f.open(t).Document()
	if restarted.Revision != updated.Revision || restarted.Config.UI.AttentionCap != 77 || !restarted.Config.GitHub.Enabled {
		t.Fatalf("restart did not recover update: %+v", restarted)
	}
}

func TestManagerSecretsAreEncryptedWriteOnlyAndSurviveRestart(t *testing.T) {
	f := newManagerFixture(t, managerBootstrap)
	m := f.open(t)
	value := "github-secret-that-must-never-appear-in-a-document"
	updated, err := m.SetSecret(m.Document().Revision, SecretGitHubToken, value)
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.Secrets.GitHubToken; !got.Configured || got.Source != "managed" {
		t.Fatalf("secret status = %+v", got)
	}
	encoded, err := json.Marshal(updated)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), value) || strings.Contains(string(encoded), f.env["GITHUB_TOKEN"]) {
		t.Fatalf("document disclosed a token: %s", encoded)
	}
	for _, deploymentSecret := range []string{"session_secret", "enroll_token", "api_token", "config_key"} {
		if strings.Contains(string(encoded), deploymentSecret) {
			t.Fatalf("document disclosed deployment-secret metadata %q: %s", deploymentSecret, encoded)
		}
	}
	ciphertext, err := os.ReadFile(f.secrets)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ciphertext), value) || !strings.HasPrefix(string(ciphertext), encryptedFilePrefix) {
		t.Fatalf("secret file is not encrypted: %q", ciphertext)
	}
	if info, err := os.Stat(f.secrets); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("secret mode = %v, err %v", info.Mode().Perm(), err)
	}
	restarted := f.open(t)
	cfg, revision := restarted.Current()
	if cfg.Secrets.GitHubToken != value || revision != updated.Revision || restarted.Document().Secrets.GitHubToken.Source != "managed" {
		t.Fatal("managed override was not recovered on restart")
	}
}

func TestManagerClearSecretUsesTombstoneInsteadOfEnvironmentFallback(t *testing.T) {
	f := newManagerFixture(t, managerBootstrap)
	m := f.open(t)
	updated, err := m.ClearSecret(m.Document().Revision, SecretGitHubToken)
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.Secrets.GitHubToken; got.Configured || got.Source != "none" {
		t.Fatalf("clear status = %+v", got)
	}
	cfg, _ := m.Current()
	if cfg.Secrets.GitHubToken != "" {
		t.Fatal("clear fell back to the environment")
	}
	restarted := f.open(t)
	cfg, _ = restarted.Current()
	if cfg.Secrets.GitHubToken != "" || restarted.Document().Secrets.GitHubToken.Source != "none" {
		t.Fatal("tombstone was not recovered on restart")
	}
}

func TestManagerRejectsInvalidMutationsWithoutChangingMemoryOrDisk(t *testing.T) {
	bootstrap := strings.Replace(managerBootstrap, "enabled: false", "enabled: true", 1)
	f := newManagerFixture(t, bootstrap)
	m := f.open(t)
	before := m.Document()
	next := before.Config
	next.Server.Timezone = "Mars/Olympus"
	if _, err := m.Update(before.Revision, next); err == nil {
		t.Fatal("invalid config update succeeded")
	}
	if after := m.Document(); after.Revision != before.Revision || after.Config.Server.Timezone != before.Config.Server.Timezone {
		t.Fatalf("invalid update changed state: %+v", after)
	}
	if _, err := os.Stat(f.runtime); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid update created runtime file: %v", err)
	}
	if _, err := m.ClearSecret(before.Revision, SecretGitHubToken); err == nil {
		t.Fatal("clearing the credential of an enabled source succeeded")
	}
	if _, err := os.Stat(f.secrets); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid secret mutation created file: %v", err)
	}
	if m.Document().Revision != before.Revision {
		t.Fatal("invalid secret mutation changed revision")
	}
}

func TestManagerRuntimeEditableURLsOverrideBootstrapEnvironment(t *testing.T) {
	f := newManagerFixture(t, managerBootstrap)
	f.env["ZORGSCOPE_BASE_URL"] = "http://localhost:9090"
	f.env["PORT"] = "9090"
	f.env["GITHUB_BASE_URL"] = "https://github-bootstrap.example"
	f.env["PLAUSIBLE_BASE_URL"] = "https://plausible-bootstrap.example"
	m := f.open(t)
	doc := m.Document()
	if doc.Deployment.BaseURL != "http://localhost:9090" || doc.Deployment.Port != 9090 {
		t.Fatalf("deployment env was not applied: %+v", doc.Deployment)
	}
	next := doc.Config
	next.GitHub.BaseURL = "https://github-runtime.example"
	next.Plausible.BaseURL = "https://plausible-runtime.example"
	updated, err := m.Update(doc.Revision, next)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Config.GitHub.BaseURL != next.GitHub.BaseURL || updated.Config.Plausible.BaseURL != next.Plausible.BaseURL {
		t.Fatalf("runtime URLs were re-overridden: %+v", updated.Config)
	}
	restarted := f.open(t).Document()
	if restarted.Config.GitHub.BaseURL != next.GitHub.BaseURL || restarted.Config.Plausible.BaseURL != next.Plausible.BaseURL {
		t.Fatalf("runtime URL precedence did not survive restart: %+v", restarted.Config)
	}
	if restarted.Deployment.BaseURL != "http://localhost:9090" || restarted.Deployment.Port != 9090 {
		t.Fatalf("deployment settings changed on restart: %+v", restarted.Deployment)
	}
}

func TestManagerOptimisticRevisionAllowsOnlyOneConcurrentWriter(t *testing.T) {
	f := newManagerFixture(t, managerBootstrap)
	m := f.open(t)
	before := m.Document()
	const writers = 12
	var wg sync.WaitGroup
	results := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			next := before.Config
			next.UI.AttentionCap = 100 + n
			_, err := m.Update(before.Revision, next)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrRevisionConflict):
			conflicted++
		default:
			t.Fatalf("unexpected writer error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != writers-1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
}

func TestManagerPersistenceFailureLeavesCurrentStateUnchanged(t *testing.T) {
	f := newManagerFixture(t, managerBootstrap)
	m := f.open(t)
	before := m.Document()
	m.runtimePath = f.dir // rename onto an existing directory must fail
	next := before.Config
	next.UI.AttentionCap++
	if _, err := m.Update(before.Revision, next); err == nil {
		t.Fatal("expected config persistence error")
	}
	if after := m.Document(); after.Revision != before.Revision || after.Config.UI.AttentionCap != before.Config.UI.AttentionCap {
		t.Fatal("config persistence failure changed current state")
	}
	m.secretPath = f.dir
	if _, err := m.SetSecret(before.Revision, SecretGitHubToken, "new-token"); err == nil {
		t.Fatal("expected secret persistence error")
	}
	cfg, revision := m.Current()
	if revision != before.Revision || cfg.Secrets.GitHubToken != f.env["GITHUB_TOKEN"] {
		t.Fatal("secret persistence failure changed current state")
	}
}

func TestManagerCurrentAndDocumentAreIndependentCopies(t *testing.T) {
	f := newManagerFixture(t, managerBootstrap)
	m := f.open(t)
	cfg, _ := m.Current()
	cfg.GitHub.Repos[0].Name = "mutated/repo"
	cfg.UI.Tiles[0] = "mutated"
	doc := m.Document()
	doc.Config.GitHub.Repos[0].Name = "also/mutated"
	doc.Config.UI.Tiles[0] = "also-mutated"
	cfg, _ = m.Current()
	if cfg.GitHub.Repos[0].Name != "owner/repo" || cfg.UI.Tiles[0] != "attention" {
		t.Fatalf("caller mutated manager state: %+v", cfg)
	}
}

func TestManagerRejectsBadKeyCorruptCiphertextAndUnknownSecrets(t *testing.T) {
	f := newManagerFixture(t, managerBootstrap)
	f.env["ZORGSCOPE_CONFIG_KEY"] = "not-base64"
	if _, err := NewManager(ManagerOptions{BootstrapPath: f.bootstrap, RuntimePath: f.runtime,
		SecretPath: f.secrets, Getenv: env(f.env)}); err == nil || !strings.Contains(err.Error(), "ZORGSCOPE_CONFIG_KEY") {
		t.Fatalf("bad key error = %v", err)
	}
	f.env["ZORGSCOPE_CONFIG_KEY"] = base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	if err := os.MkdirAll(filepath.Dir(f.secrets), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.secrets, []byte(encryptedFilePrefix+"corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(ManagerOptions{BootstrapPath: f.bootstrap, RuntimePath: f.runtime,
		SecretPath: f.secrets, Getenv: env(f.env)}); err == nil || !strings.Contains(err.Error(), "decrypt") {
		t.Fatalf("corrupt ciphertext error = %v", err)
	}
	if err := os.Remove(f.secrets); err != nil {
		t.Fatal(err)
	}
	m := f.open(t)
	if _, err := m.SetSecret(m.Document().Revision, SecretName("session_secret"), "nope"); !errors.Is(err, ErrUnknownSecret) {
		t.Fatalf("unknown secret error = %v", err)
	}
}

func TestManagerUpdateAndApplyRollsBackAbsentRuntimeFileAndState(t *testing.T) {
	f := newManagerFixture(t, managerBootstrap)
	m := f.open(t)
	before := m.Document()
	next := before.Config
	next.UI.AttentionCap = 71
	activationErr := errors.New("activation rejected")
	_, err := m.UpdateAndApply(before.Revision, next, func(candidate *Config) error {
		if candidate.UI.AttentionCap != 71 {
			t.Fatalf("callback candidate cap = %d", candidate.UI.AttentionCap)
		}
		candidate.UI.AttentionCap = 999 // callback must not be able to mutate Manager state
		return activationErr
	})
	if !errors.Is(err, activationErr) {
		t.Fatalf("activation error = %v", err)
	}
	if _, err := os.Stat(f.runtime); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime file was not restored to absence: %v", err)
	}
	after := m.Document()
	if after.Revision != before.Revision || after.Config.UI.AttentionCap != before.Config.UI.AttentionCap {
		t.Fatalf("failed activation changed Manager state: before=%+v after=%+v", before, after)
	}

	// The same revision remains valid after rollback, and callback mutation remains isolated on a
	// successful activation too.
	committed, err := m.UpdateAndApply(before.Revision, next, func(candidate *Config) error {
		candidate.UI.AttentionCap = 999
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if committed.Config.UI.AttentionCap != 71 || m.Document().Config.UI.AttentionCap != 71 {
		t.Fatal("callback mutated committed Manager configuration")
	}
}

func TestManagerSetSecretAndApplyRestoresExactCiphertextAndState(t *testing.T) {
	f := newManagerFixture(t, managerBootstrap)
	m := f.open(t)
	first, err := m.SetSecret(m.Document().Revision, SecretGitHubToken, "first-managed-secret")
	if err != nil {
		t.Fatal(err)
	}
	previousCiphertext, err := os.ReadFile(f.secrets)
	if err != nil {
		t.Fatal(err)
	}
	activationErr := errors.New("secret activation rejected")
	_, err = m.SetSecretAndApply(first.Revision, SecretGitHubToken, "second-managed-secret", func(candidate *Config) error {
		if candidate.Secrets.GitHubToken != "second-managed-secret" {
			t.Fatalf("callback received secret %q", candidate.Secrets.GitHubToken)
		}
		candidate.Secrets.GitHubToken = "callback-mutation"
		return activationErr
	})
	if !errors.Is(err, activationErr) {
		t.Fatalf("activation error = %v", err)
	}
	afterCiphertext, err := os.ReadFile(f.secrets)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterCiphertext) != string(previousCiphertext) {
		t.Fatal("secret rollback did not restore the exact previous ciphertext")
	}
	cfg, revision := m.Current()
	if revision != first.Revision || cfg.Secrets.GitHubToken != "first-managed-secret" {
		t.Fatalf("failed secret activation changed Manager state: revision=%q token=%q", revision, cfg.Secrets.GitHubToken)
	}
	restarted := f.open(t)
	restartedCfg, restartedRevision := restarted.Current()
	if restartedRevision != first.Revision || restartedCfg.Secrets.GitHubToken != "first-managed-secret" {
		t.Fatal("restart did not recover the pre-activation secret state")
	}
}

func TestManagerActivationSerializesWithFollowingMutation(t *testing.T) {
	f := newManagerFixture(t, managerBootstrap)
	m := f.open(t)
	before := m.Document()
	firstNext := before.Config
	firstNext.UI.AttentionCap = 71
	secondNext := before.Config
	secondNext.UI.AttentionCap = 72
	entered := make(chan struct{})
	release := make(chan struct{})
	firstResult := make(chan error, 1)
	secondResult := make(chan error, 1)
	secondApplied := make(chan struct{}, 1)

	go func() {
		_, err := m.UpdateAndApply(before.Revision, firstNext, func(*Config) error {
			close(entered)
			<-release
			return nil
		})
		firstResult <- err
	}()
	<-entered
	go func() {
		_, err := m.UpdateAndApply(before.Revision, secondNext, func(*Config) error {
			secondApplied <- struct{}{}
			return nil
		})
		secondResult <- err
	}()

	select {
	case err := <-secondResult:
		t.Fatalf("second mutation escaped activation lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
	if err := <-secondResult; !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("second mutation error = %v, want revision conflict", err)
	}
	select {
	case <-secondApplied:
		t.Fatal("stale second mutation activated")
	default:
	}
	if got := m.Document().Config.UI.AttentionCap; got != 71 {
		t.Fatalf("final cap = %d", got)
	}
}
