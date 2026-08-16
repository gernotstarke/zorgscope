package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/clock"
	"github.com/gernotstarke/zorgscope/internal/app"
	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports/memstore"
)

var t0 = time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

type fakeRefresher struct{ calls int }

func (f *fakeRefresher) TriggerAll() int { f.calls++; return 2 }
func (f *fakeRefresher) InFlight() int   { return 0 }

func newTestServer(t *testing.T, authMode string) (*httptest.Server, *memstore.Store, *fakeRefresher) {
	t.Helper()
	y := "server:\n  base_url: http://localhost:8080\ngithub:\n  enabled: true\n  me: gernotstarke\n  repos: [arc42/arc42-template]\n"
	env := map[string]string{"GITHUB_TOKEN": "t", "AUTH_MODE": authMode, "SESSION_SECRET": strings.Repeat("s", 32)}
	cfg, err := config.Parse(strings.NewReader(y), func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	st := memstore.New()
	clk := clock.NewFake(t0)
	ctx := context.Background()
	_ = st.ReplaceItems(ctx, "github:arc42/arc42-template", []domain.Item{{
		ID: domain.ItemID{SourceID: "github:arc42/arc42-template", ExternalID: "issues/240"}, Kind: domain.KindIssue,
		Title: "Typo in section 8", URL: "https://github.com/arc42/arc42-template/issues/240", Author: "newcomer",
		CreatedAt: t0.Add(-2 * time.Hour), UpdatedAt: t0.Add(-2 * time.Hour)}}, t0)
	_ = st.RecordStatus(ctx, domain.FetchStatus{SourceID: "github:arc42/arc42-template", Kind: "github-repo", LastSuccess: t0.Add(-time.Minute), ItemCount: 1})
	ref := &fakeRefresher{}
	srv, err := New(Deps{Dashboard: app.NewDashboard(st, clk, cfg), Refresher: ref, Store: st, Clock: clk, Cfg: cfg, Log: slog.Default(), Ready: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st, ref
}

func get(t *testing.T, ts *httptest.Server, path string) (*http.Response, string) {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	buf := make([]byte, 64*1024)
	for {
		n, err := resp.Body.Read(buf)
		_, _ = sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	_ = resp.Body.Close()
	return resp, sb.String()
}

func TestPageRendersDashboardWithSecurityHeaders(t *testing.T) {
	ts, _, _ := newTestServer(t, "dev")
	resp, body := get(t, ts, "/")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	for _, want := range []string{"<title>(1) zorgscope</title>", `id="tile-attention"`, "Typo in section 8", "NEW", `id="tile-repos"`, "arc42-template", "/static/htmx.min.js", `hx-headers=`} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
	h := resp.Header
	if !strings.HasPrefix(h.Get("Content-Security-Policy"), "default-src 'self'") || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Referrer-Policy") != "no-referrer" || h.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("security headers: %v", h)
	}
	if h.Get("Strict-Transport-Security") != "" {
		t.Fatal("no HSTS on http base_url")
	}
	var csrf *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "zs_csrf" {
			csrf = c
		}
	}
	if csrf == nil || !csrf.HttpOnly || csrf.SameSite != http.SameSiteLaxMode {
		t.Fatalf("csrf cookie = %+v", csrf)
	}
}

func TestTileFragments(t *testing.T) {
	ts, _, _ := newTestServer(t, "dev")
	resp, body := get(t, ts, "/tiles/attention")
	if resp.StatusCode != 200 || !strings.HasPrefix(strings.TrimSpace(body), `<section id="tile-attention"`) || strings.Contains(body, "<html") {
		t.Fatalf("attention fragment: %d %s", resp.StatusCode, body[:min(len(body), 200)])
	}
	resp, _ = get(t, ts, "/tiles/nope")
	if resp.StatusCode != 404 {
		t.Fatalf("unknown tile → 404, got %d", resp.StatusCode)
	}
	resp, body = get(t, ts, "/tiles/header")
	if resp.StatusCode != 200 || !strings.Contains(body, "13:59") { // data as of, Berlin time
		t.Fatalf("header fragment: %d %s", resp.StatusCode, body)
	}
}

func TestDismissRequiresCSRFAndStores(t *testing.T) {
	ts, st, _ := newTestServer(t, "dev")
	// obtain csrf cookie
	resp, _ := get(t, ts, "/")
	var csrf string
	for _, c := range resp.Cookies() {
		if c.Name == "zs_csrf" {
			csrf = c.Value
		}
	}
	form := url.Values{"id": {"github:arc42/arc42-template|issues/240"}, "updated_at": {"1786874400"}} // t0-2h = 2026-08-16T10:00:00Z
	post := func(withCookie, withHeader bool) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/dismiss", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if withCookie {
			req.AddCookie(&http.Cookie{Name: "zs_csrf", Value: csrf})
		}
		if withHeader {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		r, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
		return r
	}
	if r := post(true, false); r.StatusCode != http.StatusForbidden {
		t.Fatalf("missing header → 403, got %d", r.StatusCode)
	}
	if r := post(false, true); r.StatusCode != http.StatusForbidden {
		t.Fatalf("missing cookie → 403, got %d", r.StatusCode)
	}
	if r := post(true, true); r.StatusCode != http.StatusOK {
		t.Fatalf("valid → 200 fragment, got %d", r.StatusCode)
	}
	d, _ := st.Dismissal(context.Background(), domain.ItemID{SourceID: "github:arc42/arc42-template", ExternalID: "issues/240"})
	if d == nil || d.UpdatedAt.Unix() != 1786874400 || !d.DismissedAt.Equal(t0) {
		t.Fatalf("dismissal = %+v", d)
	}
	_, body := get(t, ts, "/tiles/attention")
	if !strings.Contains(body, "Nothing needs your attention") {
		t.Fatalf("after dismiss the tile must be empty: %s", body)
	}
}

func TestRefreshAndStatus(t *testing.T) {
	ts, _, ref := newTestServer(t, "dev")
	resp, _ := get(t, ts, "/")
	var csrf string
	for _, c := range resp.Cookies() {
		if c.Name == "zs_csrf" {
			csrf = c.Value
		}
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "zs_csrf", Value: csrf})
	req.Header.Set("X-CSRF-Token", csrf)
	r, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Body.Close()
	if r.StatusCode != http.StatusAccepted || ref.calls != 1 {
		t.Fatalf("refresh: %d calls=%d", r.StatusCode, ref.calls)
	}
	resp, body := get(t, ts, "/status")
	if resp.StatusCode != 200 || !strings.Contains(body, `"github:arc42/arc42-template"`) || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("status: %d %s", resp.StatusCode, body)
	}
}

func TestPasskeyModeBlocksEverythingButHealth(t *testing.T) {
	ts, _, _ := newTestServer(t, "passkey")
	for _, p := range []string{"/", "/tiles/attention", "/status"} {
		resp, _ := get(t, ts, p)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: got %d want 401", p, resp.StatusCode)
		}
	}
	if resp, _ := get(t, ts, "/healthz"); resp.StatusCode != 200 {
		t.Fatal("healthz must be public")
	}
	if resp, _ := get(t, ts, "/static/app.css"); resp.StatusCode != 200 {
		t.Fatal("static must be public")
	}
}

func TestStaticAssets(t *testing.T) {
	ts, _, _ := newTestServer(t, "dev")
	resp, body := get(t, ts, "/static/app.css")
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "text/css") || resp.Header.Get("Cache-Control") == "" || len(body) == 0 {
		t.Fatalf("app.css: %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = get(t, ts, "/static/htmx.min.js")
	if resp.StatusCode != 200 {
		t.Fatal("htmx must be served")
	}
}

// TestPostRoutesRequireCSRF checks the 403 path for every POST route, not just /dismiss
// (QS-3.5): a request carrying neither the cookie nor the header must never reach the handler.
func TestPostRoutesRequireCSRF(t *testing.T) {
	ts, _, ref := newTestServer(t, "dev")
	for _, route := range []string{"/dismiss", "/dismiss-all", "/refresh"} {
		resp, err := ts.Client().Post(ts.URL+route, "application/x-www-form-urlencoded", strings.NewReader(""))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s without CSRF: got %d want 403", route, resp.StatusCode)
		}
	}
	if ref.calls != 0 {
		t.Fatalf("refresh handler must not run without CSRF, calls=%d", ref.calls)
	}
}
