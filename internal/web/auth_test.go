// These tests are in package web, not web_test, for one reason: the route table is the security
// boundary (QS-4.1), and asserting a property over *every* registered route — rather than over a
// list a future author must remember to extend — needs to see the table itself.
package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/refresh"
)

const (
	testToken  = "test-app-token-0123456789abcdefghij"
	testSecret = "test-refresh-secret-0123456789abcdef"
)

var testNow = time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)

// QS-4.1 — the table is the point: a new route cannot be forgotten.
func TestEveryProtectedRouteRefusesAnonymousAccess(t *testing.T) {
	h := newTestServer(t).Handler()
	tests := []struct {
		method, path string
		wantStatus   int
	}{
		{http.MethodGet, "/", http.StatusSeeOther}, // FR-8.3 AC1: redirect, not 401
		{http.MethodGet, "/tile/github", http.StatusUnauthorized},
		{http.MethodPost, "/seen", http.StatusUnauthorized},
		{http.MethodPost, "/refresh", http.StatusUnauthorized},
		{http.MethodPost, "/api/refresh", http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if strings.Contains(rec.Body.String(), "arc42") {
				t.Error("an anonymous response leaked dashboard content")
			}
		})
	}
}

// QS-4.1, the general form of the test above: every route in the table is either explicitly
// public — and listed here, so making one public is a deliberate edit — or refuses anonymous
// access. Adding a route without doing one or the other fails this test.
func TestEveryRouteIsEitherDeliberatelyPublicOrRefusesAnonymousAccess(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()

	public := map[string]bool{
		"GET /healthz": true,
		"GET /login":   true,
		"POST /login":  true,
		"GET /docs":    true,
		"GET /docs/":   true,
		"GET /static/": true,
	}

	for _, rt := range s.routes() {
		key := rt.method + " " + rt.pattern
		t.Run(key, func(t *testing.T) {
			if rt.auth == authPublic {
				if !public[key] {
					t.Fatalf("route %s is public but is not in this test's list of "+
						"deliberately public routes (QS-4.1)", key)
				}
				return
			}
			if public[key] {
				t.Fatalf("route %s is listed as public but is not registered as public", key)
			}

			want := http.StatusUnauthorized
			if rt.auth == authSessionPage && rt.method == http.MethodGet {
				want = http.StatusSeeOther // FR-8.3 AC1: a browser navigation is redirected
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(rt.method, rt.probePath(), nil))
			if rec.Code != want {
				t.Errorf("anonymous %s %s = %d, want %d", rt.method, rt.probePath(), rec.Code, want)
			}
		})
	}
}

func TestPublicRoutesNeedNoSession(t *testing.T) {
	h := newTestServer(t).Handler()
	for _, path := range []string{"/healthz", "/login", "/docs", "/static/app.css"} {
		t.Run(path, func(t *testing.T) {
			rec := get(h, path)
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}
		})
	}
}

func TestSignInIssuesAHardenedCookie(t *testing.T) {
	h := newTestServer(t).Handler()

	rec := post(h, "/login", url.Values{"token": {testToken}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	c := cookieNamed(rec, sessionCookieName)
	if c == nil {
		t.Fatal("no session cookie")
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || !c.Secure {
		t.Errorf("cookie = %+v, want HttpOnly, SameSite=Lax, Secure (FR-8.3 AC2)", c)
	}
	if strings.Contains(c.Value, testToken) {
		t.Error("the cookie contains the token itself (FR-8.3 AC2)")
	}

	rec = getAs(h, "/", c)
	if rec.Code != http.StatusOK {
		t.Errorf("GET / with the session cookie = %d, want 200", rec.Code)
	}
}

// FR-8.3 AC3: a cookie minted by a server with token A is rejected by a server with token B.
func TestChangingTheTokenInvalidatesExistingSessions(t *testing.T) {
	old := newTestServer(t)
	rec := post(old.Handler(), "/login", url.Values{"token": {testToken}})
	c := cookieNamed(rec, sessionCookieName)
	if c == nil {
		t.Fatal("no session cookie")
	}

	rotated := newTestServerWith(t, func(o *Options) {
		o.Config.Secrets.AppToken = testToken + "-rotated"
	})
	if got := getAs(rotated.Handler(), "/", c); got.Code != http.StatusSeeOther {
		t.Errorf("GET / with a cookie from the old token = %d, want 303 (FR-8.3 AC3)", got.Code)
	}
}

func TestTamperedCookieIsRejected(t *testing.T) {
	h := newTestServer(t).Handler()
	c := cookieNamed(post(h, "/login", url.Values{"token": {testToken}}), sessionCookieName)
	if c == nil {
		t.Fatal("no session cookie")
	}

	payload, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		t.Fatalf("cookie value %q is not payload.signature", c.Value)
	}
	flipped := "A" + sig[1:]
	if flipped == sig {
		flipped = "B" + sig[1:]
	}

	for name, value := range map[string]string{
		"tampered signature": payload + "." + flipped,
		"tampered payload":   "A" + payload[1:] + "." + sig,
		"no signature":       payload,
		"empty":              "",
	} {
		t.Run(name, func(t *testing.T) {
			bad := &http.Cookie{Name: sessionCookieName, Value: value}
			if got := getAs(h, "/", bad); got.Code != http.StatusSeeOther {
				t.Errorf("GET / with a %s = %d, want 303", name, got.Code)
			}
		})
	}
}

func TestExpiredCookieIsRejected(t *testing.T) {
	clock := &ports.FixedClock{T: testNow}
	s := newTestServerWith(t, func(o *Options) { o.Clock = clock })
	h := s.Handler()

	c := cookieNamed(post(h, "/login", url.Values{"token": {testToken}}), sessionCookieName)
	if c == nil {
		t.Fatal("no session cookie")
	}
	if got := getAs(h, "/", c); got.Code != http.StatusOK {
		t.Fatalf("GET / right after sign-in = %d, want 200", got.Code)
	}

	clock.Advance(sessionTTL + time.Minute)
	if got := getAs(h, "/", c); got.Code != http.StatusSeeOther {
		t.Errorf("GET / with an expired cookie = %d, want 303", got.Code)
	}
}

// The two credentials are independent, so cron-job.org holds only refresh rights.
func TestRefreshEndpointTakesTheBearerNotTheCookie(t *testing.T) {
	h := newTestServer(t).Handler()
	c := cookieNamed(post(h, "/login", url.Values{"token": {testToken}}), sessionCookieName)
	if c == nil {
		t.Fatal("no session cookie")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
	req.AddCookie(c)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /api/refresh with only a session cookie = %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
	req.Header.Set("Authorization", "Bearer "+testSecret)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("POST /api/refresh with the bearer = %d, want 200", rec.Code)
	}
}

func TestBearerDoesNotGrantDashboardAccess(t *testing.T) {
	h := newTestServer(t).Handler()
	for _, path := range []string{"/", "/tile/github"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer "+testSecret)
			h.ServeHTTP(rec, req)
			if rec.Code == http.StatusOK {
				t.Errorf("GET %s with the refresh bearer = 200; the bearer must not grant "+
					"dashboard access", path)
			}
		})
	}
}

func TestWrongBearerIsRefused(t *testing.T) {
	h := newTestServer(t).Handler()
	for name, header := range map[string]string{
		"missing":          "",
		"wrong scheme":     "Token " + testSecret,
		"wrong value":      "Bearer " + testSecret + "x",
		"the app token":    "Bearer " + testToken,
		"empty credential": "Bearer ",
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
		})
	}
}

// QS-4.2
func TestFailedSignInsAreRateLimited(t *testing.T) {
	h := newTestServer(t).Handler()
	for i := 0; i < signInAttempts; i++ {
		if got := post(h, "/login", url.Values{"token": {"wrong"}}); got.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, got.Code)
		}
	}
	rec := post(h, "/login", url.Values{"token": {"wrong"}})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := post(h, "/login", url.Values{"token": {testToken}}); got.Code != http.StatusSeeOther {
		t.Error("a valid token must still be accepted after the limit (QS-4.2)")
	}
}

// QS-4.2: the submitted value never reaches a log, and neither does the configured one.
func TestFailedSignInsAreLoggedWithoutTheSubmittedValue(t *testing.T) {
	var logged strings.Builder
	s := newTestServerWith(t, func(o *Options) {
		o.Log = slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	post(s.Handler(), "/login", url.Values{"token": {"hunter2-the-submitted-value"}})

	out := logged.String()
	if out == "" {
		t.Fatal("a failed sign-in was not logged at all (FR-8.3 AC4)")
	}
	for _, forbidden := range []string{"hunter2-the-submitted-value", testToken, testSecret} {
		if strings.Contains(out, forbidden) {
			t.Errorf("the log contains %q:\n%s", forbidden, out)
		}
	}
}

// QS-4.3: fake secrets with recognisable values are configured, the whole surface is exercised —
// including error paths — and every response body, every response header and the log are searched
// for them.
func TestNoResponseEverContainsASecret(t *testing.T) {
	const canary = "canary-token-value"

	var logged strings.Builder
	store := &fakeStore{}
	s := newTestServerWith(t, func(o *Options) {
		o.Config.Secrets = config.Secrets{
			GitHubToken:    canary + "-github",
			PlausibleKey:   canary + "-plausible",
			TodoistToken:   canary + "-todoist",
			SlackWebhook:   "https://hooks.example/" + canary + "-slack",
			AppToken:       canary + "-app",
			RefreshSecret:  canary + "-refresh",
			TursoURL:       "libsql://db.example?authToken=" + canary + "-turso",
			TursoAuthToken: canary + "-turso",
		}
		o.Store = store
		o.Log = slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	h := s.Handler()

	// A signed-in browser, so the authenticated rendering of every route is exercised too.
	signIn := post(h, "/login", url.Values{"token": {canary + "-app"}})
	session := cookieNamed(signIn, sessionCookieName)
	if session == nil {
		t.Fatal("no session cookie")
	}

	responses := []*httptest.ResponseRecorder{signIn}
	record := func(req *http.Request) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		responses = append(responses, rec)
	}

	for _, rt := range s.routes() {
		// anonymous, with a session, and with the bearer — every route in every credential state
		record(httptest.NewRequest(rt.method, rt.probePath(), nil))

		withSession := httptest.NewRequest(rt.method, rt.probePath(), nil)
		withSession.AddCookie(session)
		record(withSession)

		withBearer := httptest.NewRequest(rt.method, rt.probePath(), nil)
		withBearer.Header.Set("Authorization", "Bearer "+canary+"-refresh")
		record(withBearer)
	}

	// Error paths: a wrong token, the rate limit, a malformed body, an unknown path, and — the one
	// that matters most — a store error whose message carries the secret, as an error from the
	// libSQL DSN would.
	for i := 0; i < signInAttempts+2; i++ {
		responses = append(responses, post(h, "/login", url.Values{"token": {canary + "-wrong"}}))
	}
	malformed := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("%zz"))
	malformed.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	record(malformed)
	record(httptest.NewRequest(http.MethodGet, "/no-such-page", nil))

	store.err = errors.New("dial libsql://db.example?authToken=" + canary + "-turso: refused")
	for _, rt := range s.routes() {
		withSession := httptest.NewRequest(rt.method, rt.probePath(), nil)
		withSession.AddCookie(session)
		record(withSession)
	}
	// The shared failure path every later task renders errors through.
	failing := httptest.NewRecorder()
	s.fail(failing, httptest.NewRequest(http.MethodGet, "/", nil), "refreshing", store.err)
	responses = append(responses, failing)

	for i, rec := range responses {
		if strings.Contains(rec.Body.String(), canary) {
			t.Errorf("response %d body contains the canary:\n%s", i, rec.Body.String())
		}
		for name, values := range rec.Header() {
			for _, v := range values {
				if strings.Contains(v, canary) {
					t.Errorf("response %d header %s contains the canary: %q", i, name, v)
				}
			}
		}
	}
	if out := logged.String(); strings.Contains(out, canary) {
		t.Errorf("the log contains the canary:\n%s", out)
	}
}

func TestRedactScrubsEverySecretValue(t *testing.T) {
	secrets := config.Secrets{
		AppToken:       "app-secret-value",
		RefreshSecret:  "refresh-secret-value",
		TursoAuthToken: "turso-secret-value",
		GitHubToken:    "",
	}
	in := "dial libsql://db.example?authToken=turso-secret-value failed for app-secret-value"
	got := Redact(secrets, in)
	for _, s := range []string{"turso-secret-value", "app-secret-value"} {
		if strings.Contains(got, s) {
			t.Errorf("Redact left %q in %q", s, got)
		}
	}
	if !strings.Contains(got, "libsql://db.example") {
		t.Errorf("Redact removed more than the secret: %q", got)
	}
	if Redact(secrets, "nothing to hide") != "nothing to hide" {
		t.Error("Redact changed a string containing no secret")
	}
	// An empty secret must not turn every string into redaction markers.
	if got := Redact(config.Secrets{}, "plain"); got != "plain" {
		t.Errorf("Redact with no secrets = %q, want %q", got, "plain")
	}
}

// QS-4.4
func TestSecurityHeaders(t *testing.T) {
	h := newTestServer(t).Handler()
	rec := get(h, "/login")
	want := map[string]string{
		"X-Content-Type-Options":    "nosniff",
		"Referrer-Policy":           "strict-origin-when-cross-origin",
		"Strict-Transport-Security": strictTransportSecurity,
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" || strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP = %q, want a policy without unsafe-inline", csp)
	}
}

func TestSecurityHeadersAreOnEveryResponse(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	paths := []string{"/no-such-page", "/healthz", "/static/app.css", "/"}
	for _, rt := range s.routes() {
		paths = append(paths, rt.probePath())
	}
	for _, p := range paths {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q, want nosniff", p, got)
		}
		if rec.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s: no Content-Security-Policy", p)
		}
	}
}

func TestNewRejectsAMissingCredential(t *testing.T) {
	for name, mutate := range map[string]func(*Options){
		"no app token":      func(o *Options) { o.Config.Secrets.AppToken = "" },
		"no refresh secret": func(o *Options) { o.Config.Secrets.RefreshSecret = "" },
		"no store":          func(o *Options) { o.Store = nil },
	} {
		t.Run(name, func(t *testing.T) {
			o := testOptions()
			mutate(&o)
			if _, err := New(o); err == nil {
				t.Fatal("New() error = nil, want an error")
			}
		})
	}
}

func TestSignInRedirectsAnAlreadySignedInBrowserToTheDashboard(t *testing.T) {
	h := newTestServer(t).Handler()
	c := cookieNamed(post(h, "/login", url.Values{"token": {testToken}}), sessionCookieName)
	if c == nil {
		t.Fatal("no session cookie")
	}
	rec := getAs(h, "/login", c)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("GET /login while signed in = %d, want 303", rec.Code)
	}
}

// ---------------------------------------------------------------- helpers

func testOptions() Options {
	cfg := config.Config{
		Timezone: "UTC",
		Refresh:  config.Refresh{Interval: 15 * time.Minute, StaleAfter: 45 * time.Minute},
		GitHub:   config.GitHub{Login: "someone", Repos: []string{"org/repo"}},
		Secrets: config.Secrets{
			AppToken:      testToken,
			RefreshSecret: testSecret,
		},
	}
	store := &fakeStore{}
	clock := &ports.FixedClock{T: testNow}
	return Options{
		Config: cfg,
		Store:  store,
		Runner: refresh.New(store, nil, clock, nil, slog.New(slog.NewTextHandler(io.Discard, nil))),
		Clock:  clock,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func newTestServer(t *testing.T) *Server { return newTestServerWith(t, func(*Options) {}) }

func newTestServerWith(t *testing.T, mutate func(*Options)) *Server {
	t.Helper()
	o := testOptions()
	mutate(&o)
	s, err := New(o)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return s
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func getAs(h http.Handler, path string, c *http.Cookie) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(c)
	h.ServeHTTP(rec, req)
	return rec
}

func post(h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	return rec
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// fakeStore is a ports.Store that returns zero values, or err from every method when it is set.
// The web layer barely touches the store yet; what it is for here is the canary test, which needs
// a store whose errors carry a secret the way a libSQL DSN error would.
type fakeStore struct{ err error }

func (f *fakeStore) Migrate(context.Context) error { return f.err }
func (f *fakeStore) ReplaceItems(context.Context, string, []domain.Item, time.Time) (int, error) {
	return 0, f.err
}
func (f *fakeStore) UpsertBuilds(context.Context, []domain.Build, time.Time) error   { return f.err }
func (f *fakeStore) UpsertMetrics(context.Context, []domain.Metric, time.Time) error { return f.err }
func (f *fakeStore) Items(context.Context) ([]domain.Item, error)                    { return nil, f.err }
func (f *fakeStore) Builds(context.Context) ([]domain.Build, error)                  { return nil, f.err }
func (f *fakeStore) Metrics(context.Context) ([]domain.Metric, error)                { return nil, f.err }
func (f *fakeStore) SourceStates(context.Context) (map[string]domain.SourceState, error) {
	return nil, f.err
}
func (f *fakeStore) RecordSourceOK(context.Context, string, time.Time, int) error { return f.err }
func (f *fakeStore) RecordSourceError(context.Context, string, time.Time, string) error {
	return f.err
}
func (f *fakeStore) LastVisit(context.Context) (time.Time, error)  { return time.Time{}, f.err }
func (f *fakeStore) SetLastVisit(context.Context, time.Time) error { return f.err }
func (f *fakeStore) AcquireRefreshLease(context.Context, string, time.Time, time.Duration) (bool, error) {
	return f.err == nil, f.err
}
func (f *fakeStore) ReleaseRefreshLease(context.Context, string) error { return f.err }
func (f *fakeStore) StartRun(context.Context, string, time.Time) (int64, error) {
	return 1, f.err
}
func (f *fakeStore) FinishRun(context.Context, int64, time.Time, bool, string) error { return f.err }
func (f *fakeStore) LastRun(context.Context) (domain.RefreshRun, error) {
	return domain.RefreshRun{}, f.err
}
func (f *fakeStore) MarkNotified(context.Context, []string, time.Time) error { return f.err }
func (f *fakeStore) UnnotifiedKeys(context.Context, []string) ([]string, error) {
	return nil, f.err
}
func (f *fakeStore) Close() error { return nil }
