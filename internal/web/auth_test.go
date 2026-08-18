// These tests are in package web, not web_test, for one reason: the route table is the security
// boundary (QS-4.1), and asserting a property over *every* registered route — rather than over a
// list a future author must remember to extend — needs to see the table itself.
package web

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
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
	// Not merely "the token is not in there verbatim": a cookie carrying base64(ZORGSCOPE_TOKEN)
	// would pass that and still hand the credential to anything that reads the cookie jar.
	for name, enc := range map[string]string{
		"base64url":         base64.RawURLEncoding.EncodeToString([]byte(testToken)),
		"base64":            base64.StdEncoding.EncodeToString([]byte(testToken)),
		"base64 unpadded":   base64.RawStdEncoding.EncodeToString([]byte(testToken)),
		"url-encoded":       url.QueryEscape(testToken),
		"hex-ish uppercase": strings.ToUpper(testToken),
	} {
		if strings.Contains(c.Value, enc) {
			t.Errorf("the cookie carries the token as %s (FR-8.3 AC2)", name)
		}
	}
	// What it does carry is an expiry and a signature over it, and nothing else: the payload
	// decodes to exactly the session's expiry timestamp.
	encPayload, _, _ := strings.Cut(c.Value, ".")
	payload, err := base64.RawURLEncoding.DecodeString(encPayload)
	if err != nil {
		t.Fatalf("the cookie payload is not base64url: %v", err)
	}
	exp, err := strconv.ParseInt(string(payload), 10, 64)
	if err != nil {
		t.Fatalf("the cookie payload is not an expiry timestamp: %q", payload)
	}
	if want := testNow.Add(sessionTTL).Unix(); exp != want {
		t.Errorf("cookie expiry = %d, want %d", exp, want)
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
		"tampered signature":       payload + "." + flipped,
		"tampered payload":         "A" + payload[1:] + "." + sig,
		"no signature":             payload,
		"empty":                    "",
		"extra dots":               payload + "." + sig + "." + sig,
		"dot only":                 ".",
		"invalid base64 payload":   "!!not-base64!!." + sig,
		"invalid base64 signature": payload + ".!!not-base64!!",
		// A truncated signature still decodes; it is the length that is wrong, and
		// ConstantTimeCompare of unequal lengths must not be treated as a match.
		"short signature": payload + "." + sig[:len(sig)-4],
		"long signature":  payload + "." + sig + "AAAA",
		// A payload that is not a number cannot be an expiry.
		"non-numeric payload": base64.RawURLEncoding.EncodeToString([]byte("tomorrow")) + "." + sig,
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

// Credential independence, as a property of the route table rather than of a list of paths
// (QS-4.1). For every route in the table, the credential its authKind does *not* name is presented
// and must be refused. A route added by a later task is covered the moment it is declared, which a
// hand-written list of paths could never be.
func TestEveryRouteRefusesTheCredentialItsKindDoesNotName(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	session := cookieNamed(post(h, "/login", url.Values{"token": {testToken}}), sessionCookieName)
	if session == nil {
		t.Fatal("no session cookie")
	}

	for _, rt := range s.routes() {
		if rt.auth == authPublic {
			// A public route names no credential, so there is no other one to refuse. That it
			// is deliberately public is asserted by the test above.
			continue
		}
		t.Run(rt.method+" "+rt.pattern, func(t *testing.T) {
			req := httptest.NewRequest(rt.method, rt.probePath(), nil)
			var want int
			var presented string

			if rt.auth == authBearer {
				// The session must not open the refresh endpoint: a stolen browser session
				// must not become a way to hammer the upstream APIs.
				presented, want = "the session cookie", http.StatusUnauthorized
				req.AddCookie(session)
			} else {
				// The refresh bearer must not open anything the browser uses: cron-job.org
				// holds the ability to trigger a refresh and nothing else.
				presented, want = "the refresh bearer", http.StatusUnauthorized
				req.Header.Set("Authorization", "Bearer "+testSecret)
				if rt.auth == authSessionPage && rt.method == http.MethodGet {
					want = http.StatusSeeOther // FR-8.3 AC1: a navigation is redirected
				}
			}

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != want {
				t.Errorf("%s %s with %s = %d, want %d — the two credentials are independent",
					rt.method, rt.probePath(), presented, rec.Code, want)
			}
			if rec.Code == http.StatusOK {
				t.Errorf("%s %s was served with %s", rt.method, rt.probePath(), presented)
			}
		})
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
	// The exact answer matters, not merely "not 200": a navigation is redirected to sign-in
	// (FR-8.3 AC1) and a fragment is refused (a redirect would paint sign-in inside a tile).
	for path, want := range map[string]int{
		"/":            http.StatusSeeOther,
		"/tile/github": http.StatusUnauthorized,
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer "+testSecret)
			h.ServeHTTP(rec, req)
			if rec.Code != want {
				t.Errorf("GET %s with the refresh bearer = %d, want %d; the bearer must not "+
					"grant dashboard access", path, rec.Code, want)
			}
			body := rec.Body.String()
			for _, leak := range []string{`class="tiles"`, `id="tile-github"`, "Mark all seen"} {
				if strings.Contains(body, leak) {
					t.Errorf("GET %s with the refresh bearer rendered %q", path, leak)
				}
			}
			if rec.Code == http.StatusSeeOther {
				if got := rec.Header().Get("Location"); got != "/login" {
					t.Errorf("redirected to %q, want /login", got)
				}
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
		// The refresh endpoints are the one place a dependency's own error text is handed
		// straight to the caller: POST /api/refresh reports, per source, what failed. So the
		// runner the canary drives has a source that fails with the canary in its message, and
		// the JSON body below has to come back without it.
		//
		// The runner is given a discarding logger of its own: what a source's error does to a
		// log is internal/refresh's contract with its adapters — every adapter keeps credentials
		// out of its error text — while what it does to a *response* is this package's, and that
		// is what this test is for.
		o.Runner = refresh.New(store, []ports.SourceFetcher{
			&ports.FakeFetcher{
				SourceName: "github",
				Err:        errors.New("github rejected " + canary + "-github"),
			},
		}, &ports.FixedClock{T: testNow}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
	// default-src covers neither of these, and the one form on this site submits ZORGSCOPE_TOKEN.
	for _, directive := range []string{"form-action 'self'", "base-uri 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP = %q, want it to carry %q (QS-4.4)", csp, directive)
		}
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

// QS-4.2: the bearer endpoint drives every upstream call this application makes, so a brute force
// against REFRESH_SECRET must run out of budget exactly as one against the sign-in form does.
func TestFailedBearerAttemptsAreRateLimited(t *testing.T) {
	h := newTestServer(t).Handler()

	attempt := func(header string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
		req.Header.Set("Authorization", header)
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := range signInAttempts {
		if got := attempt("Bearer guess-" + strconv.Itoa(i)); got != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, got)
		}
	}
	if got := attempt("Bearer one-guess-too-many"); got != http.StatusTooManyRequests {
		t.Errorf("status after the budget is spent = %d, want 429 (QS-4.2)", got)
	}
	// The credential is checked before the limit is consulted, so the cron service is never
	// locked out of its own endpoint by someone else's guessing.
	if got := attempt("Bearer " + testSecret); got != http.StatusOK {
		t.Errorf("the real bearer after the limit = %d, want 200 (QS-4.2)", got)
	}
}

// QS-4.2: a lockout is a lockout for a while, not for as long as the Machine happens to stay up.
// The limiter's refill runs off the injected clock, so this is the only way to execute it.
func TestASignInLockoutRecoversAsTheClockAdvances(t *testing.T) {
	clock := &ports.FixedClock{T: testNow}
	h := newTestServerWith(t, func(o *Options) { o.Clock = clock }).Handler()

	for i := range signInAttempts {
		if got := post(h, "/login", url.Values{"token": {"wrong"}}); got.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, got.Code)
		}
	}
	if got := post(h, "/login", url.Values{"token": {"wrong"}}); got.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", got.Code)
	}

	clock.Advance(signInWindow)
	if got := post(h, "/login", url.Values{"token": {"wrong"}}); got.Code != http.StatusUnauthorized {
		t.Errorf("a failed attempt a whole window later = %d, want 401: the bucket never "+
			"refilled, so the legitimate user stays locked out until the Machine restarts", got.Code)
	}
}

// The refill arithmetic itself, at the resolution the HTTP tests cannot see.
func TestTheRateLimiterRefillsOverTheWindow(t *testing.T) {
	l := newRateLimiter(signInAttempts, signInWindow)
	now := testNow

	for i := range signInAttempts {
		if !l.allow("client", now) {
			t.Fatalf("token %d of the initial budget was refused", i+1)
		}
	}
	if l.allow("client", now) {
		t.Fatal("the budget is not spent after the whole of it was consumed")
	}

	// A tenth of the window restores exactly one of ten tokens.
	now = now.Add(signInWindow / 10)
	if !l.allow("client", now) {
		t.Error("a tenth of the window restored no token at all")
	}
	if l.allow("client", now) {
		t.Error("a tenth of the window restored more than one token")
	}

	// A whole window restores the budget, and no more than the budget: an idle client cannot
	// bank credit for a burst.
	now = now.Add(signInWindow)
	for i := range signInAttempts {
		if !l.allow("client", now) {
			t.Fatalf("token %d after a full window was refused", i+1)
		}
	}
	if l.allow("client", now) {
		t.Error("a full window restored more than the configured budget")
	}
}

// evictLocked bounds the memory a caller varying its apparent address can make the limiter
// allocate. What matters is which buckets it drops: a client that has fully recovered loses
// nothing by being forgotten, whereas forgetting a client that is mid-lockout hands it a fresh
// budget.
func TestTheRateLimiterEvictsRecoveredClientsFirst(t *testing.T) {
	l := newRateLimiter(signInAttempts, signInWindow)
	now := testNow

	// Three clients that have just spent their budget, and a full map of long-idle ones.
	live := []string{"live-a", "live-b", "live-c"}
	for _, key := range live {
		l.buckets[key] = &bucket{tokens: 0, last: now}
	}
	for i := len(live); i < maxTrackedClients; i++ {
		l.buckets["recovered-"+strconv.Itoa(i)] = &bucket{tokens: 0, last: now.Add(-2 * signInWindow)}
	}

	if !l.allow("newcomer", now) {
		t.Fatal("a newcomer was refused although eviction had made room")
	}

	if n := len(l.buckets); n != len(live)+1 {
		t.Errorf("after eviction the limiter tracks %d clients, want %d: every recovered bucket "+
			"should have been dropped and no live one", n, len(live)+1)
	}
	for _, key := range live {
		if _, ok := l.buckets[key]; !ok {
			t.Errorf("live client %q was evicted while recovered buckets were available", key)
		}
		if l.allow(key, now) {
			t.Errorf("evicting handed live client %q a fresh budget mid-lockout", key)
		}
	}
}

// When every tracked client is live the map must still be bounded, so the least recently seen one
// goes. That is the documented trade-off: it gives that client the budget waiting out the window
// would have given it anyway.
func TestTheRateLimiterDropsTheLeastRecentlySeenWhenAllAreLive(t *testing.T) {
	l := newRateLimiter(signInAttempts, signInWindow)
	now := testNow

	for i := range maxTrackedClients {
		// All within the window, so none has recovered; oldest-0 is the least recently seen.
		l.buckets["live-"+strconv.Itoa(i)] = &bucket{
			tokens: 0,
			last:   now.Add(-signInWindow + time.Duration(i+1)*time.Millisecond),
		}
	}

	l.allow("newcomer", now)

	if n := len(l.buckets); n != maxTrackedClients {
		t.Errorf("the limiter tracks %d clients, want the cap of %d", n, maxTrackedClients)
	}
	if _, ok := l.buckets["live-0"]; ok {
		t.Error("the least recently seen client survived eviction")
	}
	if _, ok := l.buckets["live-"+strconv.Itoa(maxTrackedClients-1)]; !ok {
		t.Error("the most recently seen client was evicted instead")
	}
	if _, ok := l.buckets["newcomer"]; !ok {
		t.Error("the newcomer got no bucket")
	}
}

// QS-4.2: Fly-Client-IP is a request header like any other unless Fly's proxy put it there. A
// process reachable without going through that proxy — over Fly's private 6PN network, in the
// Compose stack, on a laptop — must ignore it, or one varying string per request buys a fresh
// budget and the rate limit is gone from both credentials at once.
func TestClientIPBelievesTheFlyHeaderOnlyBehindFlysProxy(t *testing.T) {
	behind := newTestServerWith(t, func(o *Options) { o.BehindFlyProxy = boolPtr(true) })
	off := newTestServerWith(t, func(o *Options) { o.BehindFlyProxy = boolPtr(false) })

	request := func(header string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		r.RemoteAddr = "192.0.2.10:41234"
		if header != "" {
			r.Header.Set("Fly-Client-IP", header)
		}
		return r
	}

	// A RemoteAddr net/http could not split into host and port is still a stable identity: it
	// comes from the connection, not from the caller.
	odd := httptest.NewRequest(http.MethodPost, "/login", nil)
	odd.RemoteAddr = "a-unix-socket"
	for name, s := range map[string]*Server{"behind Fly's proxy": behind, "off the proxy": off} {
		if got := s.clientIP(odd); got != "a-unix-socket" {
			t.Errorf("%s: clientIP of an address with no port = %q, want %q", name, got, "a-unix-socket")
		}
	}

	cases := []struct {
		name            string
		header          string
		wantBehind      string
		wantOffTheProxy string
	}{
		{"a plausible client address", "203.0.113.7", "203.0.113.7", "192.0.2.10"},
		{"no header at all", "", "192.0.2.10", "192.0.2.10"},
		{"an unparseable value", "not-an-ip-address", "192.0.2.10", "192.0.2.10"},
		{"an empty value", " ", "192.0.2.10", "192.0.2.10"},
		{"an IPv6 address", "2001:db8::1", "2001:db8::1", "192.0.2.10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := behind.clientIP(request(tc.header)); got != tc.wantBehind {
				t.Errorf("behind Fly's proxy: clientIP = %q, want %q", got, tc.wantBehind)
			}
			if got := off.clientIP(request(tc.header)); got != tc.wantOffTheProxy {
				t.Errorf("off the proxy: clientIP = %q, want %q — the header must not be "+
					"believed there (QS-4.2)", got, tc.wantOffTheProxy)
			}
		})
	}
}

// The same property end to end: off the proxy, varying the header does not buy a fresh budget.
func TestVaryingTheFlyHeaderCannotEscapeTheRateLimitOffTheProxy(t *testing.T) {
	h := newTestServerWith(t, func(o *Options) { o.BehindFlyProxy = boolPtr(false) }).Handler()

	attempt := func(i int) int {
		rec := httptest.NewRecorder()
		form := url.Values{"token": {"wrong"}}
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Fly-Client-IP", "203.0.113."+strconv.Itoa(i))
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := range signInAttempts {
		if got := attempt(i); got != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, got)
		}
	}
	if got := attempt(signInAttempts); got != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429: a spoofed Fly-Client-IP minted a fresh bucket (QS-4.2)", got)
	}
}

// The signal itself: FLY_APP_NAME is set by Fly in every Machine it runs and by nothing else.
func TestBehindFlyProxyFollowsTheFlyRuntimeEnvironment(t *testing.T) {
	t.Setenv(flyAppName, "")
	if behindFlyProxy() {
		t.Error("behindFlyProxy() is true with FLY_APP_NAME empty")
	}
	if s := newTestServer(t); s.trustFlyClientIP {
		t.Error("a server built outside Fly trusts Fly-Client-IP")
	}

	t.Setenv(flyAppName, "zorgscope")
	if !behindFlyProxy() {
		t.Error("behindFlyProxy() is false with FLY_APP_NAME set")
	}
	if s := newTestServer(t); !s.trustFlyClientIP {
		t.Error("a server built inside a Fly Machine does not trust Fly-Client-IP")
	}
}

// QS-4.1: authKind's zero value must be the most restrictive kind, so that a table entry whose
// auth field an author forgot is refused rather than served to anyone who asks.
func TestARouteWithNoDeclaredCredentialFailsClosed(t *testing.T) {
	if authPublic == authKind(0) {
		t.Fatal("authPublic is authKind's zero value: an omitted auth field would make a route " +
			"public, which is exactly the mistake the route table exists to prevent (QS-4.1)")
	}

	s := newTestServer(t)
	// A route as a future author might write it, having forgotten the credential.
	forgotten := route{
		method:  http.MethodGet,
		pattern: "/forgotten",
		handler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("the content of a route nobody protected"))
		},
	}
	h := s.wrap(forgotten)

	anonymous := httptest.NewRecorder()
	h.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/forgotten", nil))
	if anonymous.Code == http.StatusOK {
		t.Errorf("an anonymous request to a route with no declared credential = %d", anonymous.Code)
	}
	if strings.Contains(anonymous.Body.String(), "the content of a route nobody protected") {
		t.Error("a route with no declared credential served its content to an anonymous caller")
	}

	withSession := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/forgotten", nil)
	req.AddCookie(cookieNamed(post(s.Handler(), "/login", url.Values{"token": {testToken}}), sessionCookieName))
	h.ServeHTTP(withSession, req)
	if withSession.Code == http.StatusOK {
		t.Errorf("a session request to a route with no declared credential = %d, want a refusal: "+
			"the zero value must be the most restrictive kind", withSession.Code)
	}
}

// Rotating ZORGSCOPE_TOKEN is this product's only sign-out (FR-8.3 AC3), and it cannot reach a
// page the browser has already stored. Authenticated responses therefore say no-store.
func TestAuthenticatedResponsesAreNotStoredByTheBrowser(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	c := cookieNamed(post(h, "/login", url.Values{"token": {testToken}}), sessionCookieName)
	if c == nil {
		t.Fatal("no session cookie")
	}

	for _, rt := range s.routes() {
		if rt.auth != authSessionPage && rt.auth != authSessionFragment {
			continue
		}
		t.Run(rt.method+" "+rt.pattern, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(rt.method, rt.probePath(), nil)
			req.AddCookie(c)
			h.ServeHTTP(rec, req)
			if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
				t.Errorf("Cache-Control = %q, want no-store: a rotated token cannot evict a "+
					"page the browser kept (FR-8.3 AC3)", got)
			}
		})
	}
}

// FR-1.3 AC3: "Mark all seen" is a plain form, so a visitor with JavaScript disabled and an
// expired session sees this 401 body itself. It has to lead somewhere.
func TestTheUnauthorisedFragmentBodyLeadsBackToSignIn(t *testing.T) {
	h := newTestServer(t).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/seen", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /seen anonymously = %d, want 401 (QS-4.1)", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/login"`) {
		t.Errorf("the 401 body offers no way back to sign-in:\n%s", body)
	}
	for _, forbidden := range []string{"<script", "style=", " onclick="} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the 401 body contains %q, which the CSP forbids (QS-4.4)", forbidden)
		}
	}
}

// GET /static/ must serve files, not an index of everything the binary embeds.
func TestStaticServesFilesAndNeverAListing(t *testing.T) {
	h := newTestServer(t).Handler()

	listing := get(h, "/static/")
	if listing.Code != http.StatusNotFound {
		t.Errorf("GET /static/ = %d, want 404", listing.Code)
	}
	if strings.Contains(listing.Body.String(), "htmx.min.js") {
		t.Error("GET /static/ listed the embedded assets")
	}
	if got := get(h, "/static/no-such-asset.css"); got.Code != http.StatusNotFound {
		t.Errorf("GET an unknown asset = %d, want 404", got.Code)
	}

	css := get(h, "/static/app.css")
	if css.Code != http.StatusOK {
		t.Fatalf("GET /static/app.css = %d, want 200", css.Code)
	}
	if got := css.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/css") {
		t.Errorf("Content-Type = %q, want text/css", got)
	}
}

// QS-4.3: Redact's coverage is derived from config.Secrets by reflection rather than from a list
// kept by hand, so this fails the moment a new secret field is not covered — including a field
// that reflection over string fields cannot see.
func TestRedactCoversEveryFieldOfSecrets(t *testing.T) {
	var secrets config.Secrets
	v := reflect.ValueOf(&secrets).Elem()
	values := make(map[string]string, v.NumField())

	for i := range v.NumField() {
		name := v.Type().Field(i).Name
		if v.Field(i).Kind() != reflect.String {
			t.Fatalf("config.Secrets.%s is not a string, so Redact cannot scrub it. Redaction "+
				"is the last line of defence for QS-4.3: either make the field a string or "+
				"teach Redact about its shape.", name)
		}
		value := "canary-value-of-" + name
		v.Field(i).SetString(value)
		values[name] = value
	}
	if len(values) == 0 {
		t.Fatal("config.Secrets has no fields; this test would prove nothing")
	}

	for name, value := range values {
		text := "upstream said: " + value + " is invalid"
		if got := Redact(secrets, text); strings.Contains(got, value) {
			t.Errorf("Redact left config.Secrets.%s in %q", name, got)
		}
	}
}

func boolPtr(b bool) *bool { return &b }

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
func (f *fakeStore) LastSuccessfulRun(context.Context) (domain.RefreshRun, error) {
	return domain.RefreshRun{}, f.err
}
func (f *fakeStore) MarkNotified(context.Context, []string, time.Time) error { return f.err }
func (f *fakeStore) UnnotifiedKeys(context.Context, []string) ([]string, error) {
	return nil, f.err
}
func (f *fakeStore) Close() error { return nil }
