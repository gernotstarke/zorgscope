package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/clock"
	"github.com/gernotstarke/zorgscope/internal/app"
	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports/memstore"
)

type testReloadable struct {
	dashboard *app.Dashboard
	cfg       *config.Config
	applied   int
	applyErr  error
}

func (r *testReloadable) Build(ctx context.Context) (app.View, error) { return r.dashboard.Build(ctx) }
func (r *testReloadable) EvaluateAll(ctx context.Context) ([]domain.Evaluated, error) {
	return r.dashboard.EvaluateAll(ctx)
}
func (*testReloadable) TriggerAll() int { return 0 }
func (*testReloadable) InFlight() int   { return 0 }
func (r *testReloadable) Apply(cfg *config.Config) error {
	r.applied++
	if r.applyErr != nil {
		return r.applyErr
	}
	r.cfg = cfg
	return nil
}
func (r *testReloadable) Config() (*config.Config, error) { return r.cfg, nil }

type testRedactor struct{ values []string }

func (r *testRedactor) Replace(values []string) { r.values = append([]string(nil), values...) }

func TestAPIDashboardAndActions(t *testing.T) {
	ts, st, ref := newTestServer(t, "dev")
	resp, body := get(t, ts, "/api/v1/dashboard")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") == "" {
		t.Fatalf("dashboard response: %d etag=%q body=%s", resp.StatusCode, resp.Header.Get("ETag"), body)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(body), &document); err != nil {
		t.Fatal(err)
	}
	if _, ok := document["attention"]; !ok {
		t.Fatalf("dashboard lacks stable JSON field names: %v", document)
	}
	if document["schema_version"] != float64(app.DashboardSchemaVersion) {
		t.Fatalf("dashboard schema version = %v", document["schema_version"])
	}
	request, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/dashboard", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("If-None-Match", resp.Header.Get("ETag"))
	notModified, err := ts.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = notModified.Body.Close() }()
	if notModified.StatusCode != http.StatusNotModified {
		t.Fatalf("unchanged dashboard = %d, want 304", notModified.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/dismiss", strings.NewReader(`{"id":"github:arc42/arc42-template|issues/240","updated_at":1786874400}`))
	req.Header.Set("Content-Type", "application/json")
	r, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		t.Fatalf("dismiss: %d", r.StatusCode)
	}
	if dismissals, _ := st.Dismissals(req.Context()); len(dismissals) != 1 {
		t.Fatalf("dismissals = %d", len(dismissals))
	}

	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/v1/refresh", strings.NewReader(`{}`))
	r, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Body.Close()
	if r.StatusCode != http.StatusAccepted || ref.calls != 1 {
		t.Fatalf("refresh: status=%d calls=%d", r.StatusCode, ref.calls)
	}
}

func TestDashboardETagIgnoresOnlyContinuouslyChangingLabels(t *testing.T) {
	first := app.View{
		SchemaVersion: app.DashboardSchemaVersion,
		GeneratedAt:   t0,
		Header: app.HeaderView{Date: "Sun 16 Aug 2026", Time: "12:00",
			Sources: []app.SourceStatusView{{ID: "github:owner/repo", Age: "1m", Healthy: true}}},
		Attention: app.AttentionView{Rows: []app.AttentionRow{{ID: "id", Title: "Issue", Age: "2h", Bucket: "lt24h", Badge: "NEW"}}},
		Repos:     []app.RepoCard{{Name: "owner/repo", Build: app.BuildView{State: "ok", Age: "3m"}}},
	}
	second := first
	second.GeneratedAt = t0.Add(time.Minute)
	second.Header.Date, second.Header.Time = "Sun 16 Aug 2026", "12:01"
	second.Header.Sources = []app.SourceStatusView{{ID: "github:owner/repo", Age: "2m", Healthy: true}}
	second.Attention.Rows = []app.AttentionRow{{ID: "id", Title: "Issue", Age: "3h", Bucket: "lt24h", Badge: "NEW"}}
	second.Repos = []app.RepoCard{{Name: "owner/repo", Build: app.BuildView{State: "ok", Age: "4m"}}}
	if dashboardETag(first) != dashboardETag(second) {
		t.Fatal("display-only time labels changed the semantic dashboard ETag")
	}
	second.Attention.Rows[0].Badge = "UNANSWERED"
	if dashboardETag(first) == dashboardETag(second) {
		t.Fatal("meaningful attention change did not invalidate dashboard ETag")
	}
}

func TestAPITokenAuthentication(t *testing.T) {
	ts, _, _ := newTestServer(t, "token")
	request := func(token string) int {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/dashboard", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if got := request(""); got != http.StatusUnauthorized {
		t.Fatalf("without token = %d", got)
	}
	if got := request(strings.Repeat("a", 32)); got != http.StatusOK {
		t.Fatalf("valid token = %d", got)
	}
}

func TestAPIAuthenticationRateLimitNeverLocksOutValidToken(t *testing.T) {
	ts, _, _ := newTestServer(t, "token")
	request := func(token string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/dashboard", nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}
	for i := 0; i < authFailureLimit; i++ {
		if got := request("wrong").StatusCode; got != http.StatusUnauthorized {
			t.Fatalf("invalid attempt %d = %d, want 401", i+1, got)
		}
	}
	limited := request("wrong")
	if limited.StatusCode != http.StatusTooManyRequests || limited.Header.Get("Retry-After") != "60" {
		t.Fatalf("limited response = %d retry-after=%q", limited.StatusCode, limited.Header.Get("Retry-After"))
	}
	if got := request(strings.Repeat("a", 32)).StatusCode; got != http.StatusOK {
		t.Fatalf("valid token after invalid-attempt limit = %d, want 200", got)
	}
}

func TestAPIConfigUpdateConcurrencyValidationAndWriteOnlySecrets(t *testing.T) {
	dir := t.TempDir()
	bootstrap := filepath.Join(dir, "bootstrap.yaml")
	runtimePath := filepath.Join(dir, "runtime", "config.yaml")
	secretPath := filepath.Join(dir, "runtime", "secrets.enc")
	yaml := "server:\n  base_url: http://localhost:8080\ngithub:\n  enabled: false\n  me: owner\n  repos: [owner/repo]\nplausible:\n  enabled: false\nwatch:\n  enabled: false\n"
	if err := os.WriteFile(bootstrap, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"AUTH_MODE":            "dev",
		"ZORGSCOPE_CONFIG_KEY": base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901")),
	}
	manager, err := config.NewManager(config.ManagerOptions{BootstrapPath: bootstrap, RuntimePath: runtimePath,
		SecretPath: secretPath, Getenv: func(key string) string { return env[key] }})
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := manager.Current()
	store := memstore.New()
	clk := clock.NewFake(t0)
	reloadable := &testReloadable{dashboard: app.NewDashboard(store, clk, cfg), cfg: cfg}
	redactor := &testRedactor{}
	srv, err := New(Deps{Runtime: reloadable, Config: manager, Redactor: redactor, Store: store,
		Clock: clk, Cfg: cfg, Log: slog.Default(), Ready: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	request := func(method, path string, body any, revision string) (*http.Response, string) {
		t.Helper()
		var encoded []byte
		if body != nil {
			encoded, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req, requestErr := http.NewRequest(method, ts.URL+path, bytes.NewReader(encoded))
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if revision != "" {
			req.Header.Set("If-Match", `"`+revision+`"`)
		}
		resp, requestErr := ts.Client().Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		defer func() { _ = resp.Body.Close() }()
		responseBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return resp, string(responseBody)
	}

	resp, body := request(http.MethodGet, "/api/v1/config", nil, "")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") == "" {
		t.Fatalf("GET config: %d etag=%q body=%s", resp.StatusCode, resp.Header.Get("ETag"), body)
	}
	var doc config.Document
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != config.DocumentSchemaVersion || doc.Deployment.Port != 8080 {
		t.Fatalf("incomplete document: %+v", doc)
	}
	firstRevision := doc.Revision
	doc.Config.UI.AttentionCap = 41
	resp, body = request(http.MethodPut, "/api/v1/config", doc.Config, firstRevision)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT config: %d %s", resp.StatusCode, body)
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Config.UI.AttentionCap != 41 || doc.Revision == firstRevision || reloadable.applied != 1 || reloadable.cfg.UI.AttentionCap != 41 {
		t.Fatalf("config was not activated: doc=%+v applied=%d", doc, reloadable.applied)
	}
	if _, err := os.Stat(runtimePath); err != nil {
		t.Fatalf("runtime config was not persisted: %v", err)
	}

	resp, _ = request(http.MethodPut, "/api/v1/config", doc.Config, firstRevision)
	if resp.StatusCode != http.StatusConflict || reloadable.applied != 1 {
		t.Fatalf("stale update: status=%d applied=%d", resp.StatusCode, reloadable.applied)
	}
	invalid := doc.Config
	invalid.Server.Timezone = "Mars/Olympus"
	resp, _ = request(http.MethodPut, "/api/v1/config", invalid, doc.Revision)
	if resp.StatusCode != http.StatusUnprocessableEntity || reloadable.applied != 1 {
		t.Fatalf("invalid update: status=%d applied=%d", resp.StatusCode, reloadable.applied)
	}

	reloadable.applyErr = errors.New("candidate sources could not be built")
	failed := doc.Config
	failed.UI.AttentionCap = 42
	resp, _ = request(http.MethodPut, "/api/v1/config", failed, doc.Revision)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("activation failure: status=%d", resp.StatusCode)
	}
	afterFailure := manager.Document()
	if afterFailure.Revision != doc.Revision || afterFailure.Config.UI.AttentionCap != 41 || reloadable.cfg.UI.AttentionCap != 41 {
		t.Fatalf("activation failure was not atomic: manager=%+v runtime-cap=%d", afterFailure, reloadable.cfg.UI.AttentionCap)
	}
	reloadable.applyErr = nil

	secret := "managed-secret-that-must-not-be-returned"
	resp, body = request(http.MethodPut, "/api/v1/config/secrets/github_token", map[string]string{"value": secret}, doc.Revision)
	if resp.StatusCode != http.StatusOK || strings.Contains(body, secret) {
		t.Fatalf("secret update disclosed or rejected the value: %d %s", resp.StatusCode, body)
	}
	foundSecret := false
	for _, value := range redactor.values {
		foundSecret = foundSecret || value == secret
	}
	if !foundSecret {
		t.Fatalf("dynamic redactor did not receive the managed secret: %v", redactor.values)
	}
	ciphertext, err := os.ReadFile(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ciphertext), secret) {
		t.Fatal("managed secret was persisted in plaintext")
	}
}
