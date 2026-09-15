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
	"sync"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/fakesources"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/snapshot"
)

const (
	// The OAuth App every test server signs people in as. The client secret is also the session
	// key's seed (design 2026-09-14 §2), which is why rotating it in a test invalidates cookies.
	testClientID     = "test-client-id"
	testClientSecret = "test-client-secret-0123456789abcdef"
	testAuthRepo     = "gernotstarke/zorgscope"
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
		{http.MethodGet, "/items", http.StatusUnauthorized},
		{http.MethodPost, "/seen", http.StatusUnauthorized},
		{http.MethodPost, "/refresh", http.StatusUnauthorized},
		{http.MethodPost, "/logout", http.StatusUnauthorized},
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
		// The two halves of the sign-in flow. They have to be public — they are how a visitor
		// acquires a session in the first place — and neither reads or writes anything of this
		// application's: one sets a random state cookie, the other trades a code GitHub issued
		// for a session, refusing anyone GitHub does not vouch for (FR-8.3).
		"GET /auth/github":   true,
		"GET /auth/callback": true,
		"GET /static/":       true,
		// It sets a display-preference cookie and redirects: no data is read, none is written,
		// and no access is granted. /login is public, and the sign-in page is the one page an
		// anonymous visitor sees, so a switch that needed a session would be missing exactly
		// where the appearance is the whole of the page.
		"POST /theme": true,
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
	for _, path := range []string{"/healthz", "/login", "/static/app.css"} {
		t.Run(path, func(t *testing.T) {
			rec := get(h, path)
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}
		})
	}
}

func TestTamperedCookieIsRejected(t *testing.T) {
	h := newTestServer(t).Handler()
	c := signIn(t, h)

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
		// A payload that is not "expiry:seen" cannot be a session.
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

// A cookie minted under the reset's predecessor — a single integer, no ":" — proves nothing this
// codec ever signed: FR-8.3 AC4 read against the format change itself, not only the key rotation.
func TestACookieFromTheOldSingleIntegerFormatIsRejected(t *testing.T) {
	h := newTestServer(t).Handler()
	codec := newSessionCodec(testClientSecret)
	// The old mint: base64(expiry) + "." + base64(HMAC(expiry)), no seen mark at all.
	payload := strconv.FormatInt(testNow.Add(sessionTTL).Unix(), 10)
	old := &http.Cookie{
		Name: sessionCookieName,
		Value: sessionEncoding.EncodeToString([]byte(payload)) + "." +
			sessionEncoding.EncodeToString(codec.sign(payload)),
	}
	if got := getAs(h, "/", old); got.Code != http.StatusSeeOther {
		t.Errorf("GET / with an old-format cookie = %d, want 303", got.Code)
	}
}

func TestExpiredCookieIsRejected(t *testing.T) {
	clock := &ports.FixedClock{T: testNow}
	s := newTestServerWith(t, func(o *Options) { o.Clock = clock })
	h := s.Handler()

	c := signIn(t, h)
	if got := getAs(h, "/", c); got.Code != http.StatusOK {
		t.Fatalf("GET / right after sign-in = %d, want 200", got.Code)
	}

	clock.Advance(sessionTTL + time.Minute)
	if got := getAs(h, "/", c); got.Code != http.StatusSeeOther {
		t.Errorf("GET / with an expired cookie = %d, want 303", got.Code)
	}
}

// QS-4.3: fake secrets with recognisable values are configured, the whole surface is exercised —
// including error paths — and every response body, every response header and the log are searched
// for them.
func TestNoResponseEverContainsASecret(t *testing.T) {
	const canary = "canary-token-value"
	// The OAuth client id is the one configured value that is public by construction: it travels
	// in the authorize URL the browser is redirected to, and a sign-in that hid it could not
	// happen at all. It gets a canary of its own so that this sweep can say precisely where it is
	// allowed to appear, rather than exempting the field and letting some other handler quietly
	// start echoing it.
	const publicID = "public-client-id-canary"

	var logged strings.Builder
	// A failing source, so the error notice — the one place an upstream error's own text reaches
	// a response — is exercised too. Its message carries the canary the way a real GitHub error
	// might quote a bad token.
	src := &fakeSource{err: errors.New("github rejected " + canary + "-github")}
	s := newTestServerWith(t, func(o *Options) {
		o.Config.Secrets = config.Secrets{
			GitHubToken:       canary + "-github",
			SlackWebhook:      "https://hooks.example/" + canary + "-slack",
			OAuthClientID:     publicID,
			OAuthClientSecret: canary + "-client-secret",
			RefreshSecret:     canary + "-refresh",
			TursoURL:          "libsql://db.example?authToken=" + canary + "-turso",
			TursoAuthToken:    canary + "-turso",
		}
		o.Cache = snapshot.New(src, time.Hour, o.Clock)
		o.Log = slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	h := s.Handler()

	// A signed-in browser, so the authenticated rendering of every route is exercised too. The
	// cookie is minted from this server's own codec, because its client secret is a canary rather
	// than the one every other test server uses.
	cookie := &http.Cookie{Name: sessionCookieName, Value: s.codec.mint(session{Expiry: testNow.Add(sessionTTL)})}

	var responses []*httptest.ResponseRecorder
	record := func(req *http.Request) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		responses = append(responses, rec)
	}

	for _, rt := range s.routes() {
		// anonymous and with a session — every route in every credential state
		record(httptest.NewRequest(rt.method, rt.probePath(), nil))

		withSession := httptest.NewRequest(rt.method, rt.probePath(), nil)
		withSession.AddCookie(cookie)
		record(withSession)
	}

	// Error paths: every refusal the callback can produce, the rate limit behind them, and an
	// unknown path.
	for i := 0; i < signInAttempts+2; i++ {
		record(httptest.NewRequest(http.MethodGet, "/auth/callback?code="+canary+"-code&state="+canary+"-state", nil))
	}
	record(httptest.NewRequest(http.MethodGet, "/no-such-page", nil))

	// The shared failure path every handler renders errors through.
	failing := httptest.NewRecorder()
	s.fail(failing, httptest.NewRequest(http.MethodGet, "/", nil),
		"refreshing", errors.New("dial libsql://db.example?authToken="+canary+"-turso: refused"))
	responses = append(responses, failing)

	const authorizePrefix = "https://github.com/login/oauth/authorize"
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
		carriesID := strings.Contains(rec.Body.String(), publicID) ||
			strings.Contains(rec.Header().Get("Location"), publicID)
		if carriesID && !strings.HasPrefix(rec.Header().Get("Location"), authorizePrefix) {
			t.Errorf("response %d carries the OAuth client id outside the redirect to GitHub:\n%s",
				i, rec.Body.String())
		}
	}
	if out := logged.String(); strings.Contains(out, canary) {
		t.Errorf("the log contains the canary:\n%s", out)
	}
	if out := logged.String(); strings.Contains(out, publicID) {
		t.Errorf("the log contains the OAuth client id:\n%s", out)
	}
}

func TestRedactScrubsEverySecretValue(t *testing.T) {
	secrets := config.Secrets{
		OAuthClientID:     "client-id-value",
		OAuthClientSecret: "client-secret-value",
		RefreshSecret:     "refresh-secret-value",
		TursoAuthToken:    "turso-secret-value",
		GitHubToken:       "",
	}
	in := "dial libsql://db.example?authToken=turso-secret-value failed for client-secret-value"
	got := Redact(secrets, in)
	for _, s := range []string{"turso-secret-value", "client-secret-value"} {
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
	// default-src covers neither of these, and an injected <base> would re-point every relative
	// URL on the page — including the sign-in link that starts the OAuth flow.
	for _, directive := range []string{"form-action 'self'", "base-uri 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP = %q, want it to carry %q (QS-4.4)", csp, directive)
		}
	}
	// The badges are gone, so nothing on this page needs a data: image any more (QS-4.4).
	if strings.Contains(csp, "https://") || strings.Contains(csp, "data:") {
		t.Errorf("CSP = %q, want it to name no external host and no data: scheme", csp)
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
		"no client id":      func(o *Options) { o.Config.Secrets.OAuthClientID = "" },
		"no client secret":  func(o *Options) { o.Config.Secrets.OAuthClientSecret = "" },
		"no access checker": func(o *Options) { o.Access = nil },
		"no cache":          func(o *Options) { o.Cache = nil },
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
	c := signIn(t, h)
	rec := getAs(h, "/login", c)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("GET /login while signed in = %d, want 303", rec.Code)
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
// budget and the rate limit is gone.
func TestClientIPBelievesTheFlyHeaderOnlyBehindFlysProxy(t *testing.T) {
	behind := newTestServerWith(t, func(o *Options) { o.BehindFlyProxy = boolPtr(true) })
	off := newTestServerWith(t, func(o *Options) { o.BehindFlyProxy = boolPtr(false) })

	request := func(header string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/auth/callback", nil)
		r.RemoteAddr = "192.0.2.10:41234"
		if header != "" {
			r.Header.Set("Fly-Client-IP", header)
		}
		return r
	}

	// A RemoteAddr net/http could not split into host and port is still a stable identity: it
	// comes from the connection, not from the caller.
	odd := httptest.NewRequest(http.MethodGet, "/auth/callback", nil)
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

	// A callback with no state cookie: the refusal the flow produces for anyone who did not start
	// here, and one of the refusals that counts against the budget (design 2026-09-14 §2).
	attempt := func(i int) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=guess&state=guess", nil)
		req.Header.Set("Fly-Client-IP", "203.0.113."+strconv.Itoa(i))
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := range signInAttempts {
		if got := attempt(i); got != http.StatusBadRequest {
			t.Fatalf("attempt %d: status = %d, want 400", i+1, got)
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

	// A valid session is still admitted: every remaining credential kind is a session
	// requirement of one shape or another, so "most restrictive" is not "unreachable by anyone"
	// — it is refusing anonymous access outright (401) rather than redirecting it to sign-in,
	// which is what the zero value chooses over authSessionPage.
	withSession := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/forgotten", nil)
	req.AddCookie(signIn(t, s.Handler()))
	h.ServeHTTP(withSession, req)
	if withSession.Code != http.StatusOK {
		t.Errorf("a session request to a route with no declared credential = %d, want 200", withSession.Code)
	}
}

// Rotating the OAuth client secret is this product's only sign-out (FR-8.3 AC4), and it cannot
// reach a page the browser has already stored. Authenticated responses therefore say no-store.
func TestAuthenticatedResponsesAreNotStoredByTheBrowser(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	c := signIn(t, h)

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
					"page the browser kept (FR-8.3 AC4)", got)
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

// fakeSource is a ports.Source whose behaviour a test can change between calls: fixed items, an
// error, or a count of how many times it was actually asked to fetch — which is what proves the
// snapshot cache, not the handler, decides when to refetch.
type fakeSource struct {
	mu    sync.Mutex
	items []domain.Item
	err   error
	calls int
}

func (f *fakeSource) Fetch(context.Context) ([]domain.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.items, f.err
}

// CallCount returns the number of times Fetch has been called so far.
func (f *fakeSource) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// setItems replaces the items Fetch returns and clears any configured error.
func (f *fakeSource) setItems(items []domain.Item) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items, f.err = items, nil
}

// setErr makes Fetch fail with err from the next call on.
func (f *fakeSource) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func testOptions() Options {
	cfg := config.Config{
		Timezone: "UTC",
		GitHub: config.GitHub{
			Login:    "someone",
			AuthRepo: testAuthRepo,
			Repos:    []string{"org/repo"},
		},
		Secrets: config.Secrets{
			OAuthClientID:     testClientID,
			OAuthClientSecret: testClientSecret,
		},
	}
	clock := &ports.FixedClock{T: testNow}
	return Options{
		Config: cfg,
		Cache:  snapshot.New(&fakeSource{}, time.Hour, clock),
		Clock:  clock,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		// The visitor GitHub vouches for in the tests is whoever presents the fake's token. A test
		// about being refused replaces this with a stub that admits nobody.
		Access: &stubAccess{allow: map[string]bool{"fake-token": true}},
	}
}

// stubAccess stands in for the GitHub adapter: it decides by token, or fails outright, so a test
// can exercise the collaborator, the stranger and an unreachable GitHub without a fixture for each.
type stubAccess struct {
	allow map[string]bool
	err   error
}

func (s *stubAccess) HasPushAccess(_ context.Context, token string) (bool, error) {
	return s.allow[token], s.err
}

// startFakeGitHub starts the fixture server, points the options' OAuth base URL at it, and
// registers the app's callback with it. It returns the fake's URL.
//
// The callback is registered rather than sent as redirect_uri because that is the shape of the
// real request: zorgscope never sends redirect_uri, so GitHub uses the one on the App (design
// 2026-09-14 §2) — and the fake insists on the same.
func startFakeGitHub(t *testing.T, o *Options) string {
	t.Helper()
	fake := httptest.NewServer(fakesources.NewServer())
	t.Cleanup(fake.Close)
	o.Config.GitHub.OAuthBaseURL = fake.URL
	o.HTTPClient = fake.Client()
	resp, err := http.Post(fake.URL+"/_control/oauth-callback?url=http://zorgscope.test/auth/callback", "", nil) //nolint:noctx // a test helper against a local fixture server
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return fake.URL
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
