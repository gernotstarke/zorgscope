// The sign-in flow, driven end to end against the fixture GitHub (design 2026-09-14 §2). These
// tests live in package web because what they assert is partly unexported: the state cookie's name,
// the session codec behind the cookie, and the rate limiter the refusals count against.
package web

import (
	"bytes"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/fakesources"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// FR-8.3: there is nothing to type any more. A password field on this page would mean the shared
// token came back.
func TestTheLoginPageOffersOneLinkToGitHub(t *testing.T) {
	body := get(newTestServer(t).Handler(), "/login").Body.String()
	if !strings.Contains(body, `href="/auth/github"`) || strings.Contains(body, `type="password"`) {
		t.Fatalf("login page = %s", body)
	}
}

func TestStartingSignInSetsAStateCookieAndRedirectsToGitHub(t *testing.T) {
	var o Options
	s := newTestServerWith(t, func(opt *Options) { startFakeGitHub(t, opt); o = *opt })
	rec := get(s.Handler(), "/auth/github")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code = %d", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if !strings.HasPrefix(loc.String(), o.Config.GitHub.OAuthBaseURL+"/login/oauth/authorize") {
		t.Fatalf("Location = %s", loc)
	}
	// No scope, because reading a public repository's permissions needs none; no redirect_uri,
	// because GitHub must use the callback registered on the App rather than anything this
	// process says about its own host (design 2026-09-14 §2).
	if loc.Query().Get("client_id") != "test-client-id" || loc.Query().Get("scope") != "" || loc.Query().Get("redirect_uri") != "" {
		t.Fatalf("query = %v", loc.Query())
	}
	c := cookieNamed(rec, stateCookieName)
	if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Value != loc.Query().Get("state") {
		t.Fatalf("state cookie = %+v", c)
	}
}

// Two starts must not produce the same state, or the cookie would prove nothing about which
// browser began the flow.
func TestEverySignInGetsItsOwnState(t *testing.T) {
	h := newTestServerWith(t, func(o *Options) { startFakeGitHub(t, o) }).Handler()
	first := cookieNamed(get(h, "/auth/github"), stateCookieName)
	second := cookieNamed(get(h, "/auth/github"), stateCookieName)
	if first == nil || second == nil {
		t.Fatal("a start set no state cookie")
	}
	if first.Value == second.Value {
		t.Errorf("two sign-ins share the state %q", first.Value)
	}
}

// signInThroughGitHub drives the whole flow against the fake and returns the callback response.
func signInThroughGitHub(t *testing.T, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	start := get(h, "/auth/github")
	state := cookieNamed(start, stateCookieName)
	if state == nil {
		t.Fatal("starting sign-in set no state cookie")
	}
	// The fake would redirect the browser to /auth/callback?code=fake-code&state=…; drive that
	// request directly, with the state cookie the browser would carry.
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=fake-code&state="+url.QueryEscape(state.Value), nil)
	req.AddCookie(state)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestACollaboratorIsSignedIn(t *testing.T) {
	s := newTestServerWith(t, func(o *Options) { startFakeGitHub(t, o) })
	rec := signInThroughGitHub(t, s.Handler())
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("callback = %d %s", rec.Code, rec.Header().Get("Location"))
	}
	sessionCookie := cookieNamed(rec, sessionCookieName)
	if sessionCookie == nil {
		t.Fatal("no session cookie")
	}
	if sess, ok := s.codec.decode(sessionCookie.Value, testNow); !ok {
		t.Fatal("no valid session cookie")
	} else if want := testNow.Add(sessionTTL); !sess.Expiry.Equal(want) {
		t.Errorf("a fresh sign-in expires at %v, want %v", sess.Expiry, want)
	}
	// FR-8.3 AC2: the cookie is hardened, and it carries an expiry and a signature rather than
	// anything the visitor presented.
	if !sessionCookie.HttpOnly || !sessionCookie.Secure || sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("session cookie = %+v, want HttpOnly, Secure, SameSite=Lax", sessionCookie)
	}
	for _, forbidden := range []string{"fake-token", "fake-code", testClientSecret} {
		if strings.Contains(sessionCookie.Value, forbidden) {
			t.Errorf("the session cookie carries %q", forbidden)
		}
	}
	if c := cookieNamed(rec, stateCookieName); c == nil || c.MaxAge >= 0 {
		t.Fatal("the state cookie was not cleared")
	}
	if dash := getAs(s.Handler(), "/", sessionCookie); dash.Code != http.StatusOK {
		t.Fatalf("dashboard with the new session = %d", dash.Code)
	}
}

// FR-8.3 AC2, in the form the old token sign-in was pinned in: not merely "no credential appears
// verbatim" — a cookie carrying base64(secret) would pass that and still hand the credential to
// anything that reads the cookie jar — but "the value is an expiry and a signature over it, and
// nothing else".
func TestTheSessionCookieCarriesOnlyAnExpiryAndASignature(t *testing.T) {
	s := newTestServerWith(t, func(o *Options) { startFakeGitHub(t, o) })
	c := cookieNamed(signInThroughGitHub(t, s.Handler()), sessionCookieName)
	if c == nil {
		t.Fatal("no session cookie")
	}

	for _, credential := range []string{testClientSecret, "fake-token", "fake-code"} {
		for name, enc := range map[string]string{
			"verbatim":          credential,
			"base64url":         base64.RawURLEncoding.EncodeToString([]byte(credential)),
			"base64":            base64.StdEncoding.EncodeToString([]byte(credential)),
			"base64 unpadded":   base64.RawStdEncoding.EncodeToString([]byte(credential)),
			"url-encoded":       url.QueryEscape(credential),
			"hex-ish uppercase": strings.ToUpper(credential),
		} {
			if strings.Contains(c.Value, enc) {
				t.Errorf("the cookie carries %q as %s (FR-8.3 AC2)", credential, name)
			}
		}
	}

	encPayload, _, _ := strings.Cut(c.Value, ".")
	payload, err := base64.RawURLEncoding.DecodeString(encPayload)
	if err != nil {
		t.Fatalf("the cookie payload is not base64url: %v", err)
	}
	if strings.Contains(string(payload), ":") {
		t.Errorf("the cookie payload carries a second field: %q", payload)
	}
	exp, err := strconv.ParseInt(string(payload), 10, 64)
	if err != nil {
		t.Fatalf("the cookie payload is not a timestamp: %q", payload)
	}
	if want := testNow.Add(sessionTTL).Unix(); exp != want {
		t.Errorf("cookie expiry = %d, want %d", exp, want)
	}
}

func TestAStrangerIsRefusedWithoutASession(t *testing.T) {
	s := newTestServerWith(t, func(o *Options) {
		startFakeGitHub(t, o)
		o.Access = &stubAccess{allow: map[string]bool{}}
	})
	rec := signInThroughGitHub(t, s.Handler())
	if rec.Code != http.StatusForbidden || cookieNamed(rec, sessionCookieName) != nil {
		t.Fatalf("stranger: %d, cookie %v", rec.Code, cookieNamed(rec, sessionCookieName))
	}
	if !strings.Contains(rec.Body.String(), "collaborators of gernotstarke/zorgscope") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// A refusal must not leave the state behind: the next attempt has to start at /auth/github.
	if c := cookieNamed(rec, stateCookieName); c == nil || c.MaxAge >= 0 {
		t.Errorf("the refusal did not clear the state cookie: %+v", c)
	}
}

// Design §8: an answer with no permissions block at all refuses like any stranger — the check
// fails closed — but the log has to say which of the two it was, because only this one is fixed by
// asking the OAuth App for a scope.
func TestAMissingPermissionsBlockIsRefusedAndNamedInTheLog(t *testing.T) {
	var logs bytes.Buffer
	s := newTestServerWith(t, func(o *Options) {
		startFakeGitHub(t, o)
		o.Log = slog.New(slog.NewTextHandler(&logs, nil))
		o.Access = &stubAccess{err: ports.ErrNoPermissionsBlock}
	})
	rec := signInThroughGitHub(t, s.Handler())

	if rec.Code != http.StatusForbidden || cookieNamed(rec, sessionCookieName) != nil {
		t.Fatalf("missing permissions block: %d, cookie %v", rec.Code, cookieNamed(rec, sessionCookieName))
	}
	if !strings.Contains(rec.Body.String(), "collaborators of gernotstarke/zorgscope") {
		t.Errorf("body = %s", rec.Body.String())
	}
	if !strings.Contains(logs.String(), "no permissions block") {
		t.Errorf("the log does not name the missing block, so nobody will know to add a scope:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "no push access") {
		t.Errorf("the log calls a missing block a permissions problem:\n%s", logs.String())
	}
}

func TestAMismatchedStateIsRefused(t *testing.T) {
	s := newTestServerWith(t, func(o *Options) { startFakeGitHub(t, o) })
	start := get(s.Handler(), "/auth/github")
	state := cookieNamed(start, stateCookieName)
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=fake-code&state=forged", nil)
	req.AddCookie(state)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || cookieNamed(rec, sessionCookieName) != nil {
		t.Fatalf("forged state: %d", rec.Code)
	}
}

func TestACallbackWithoutAStateCookieIsRefused(t *testing.T) {
	s := newTestServerWith(t, func(o *Options) { startFakeGitHub(t, o) })
	rec := get(s.Handler(), "/auth/callback?code=fake-code&state=anything")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d", rec.Code)
	}
}

// GitHub can come back with an error instead of a code — the visitor pressed "Cancel" — and that
// is a refusal like any other rather than a half-finished sign-in.
func TestACallbackWithoutACodeIsRefused(t *testing.T) {
	s := newTestServerWith(t, func(o *Options) { startFakeGitHub(t, o) })
	start := get(s.Handler(), "/auth/github")
	state := cookieNamed(start, stateCookieName)
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?error=access_denied&state="+url.QueryEscape(state.Value), nil)
	req.AddCookie(state)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || cookieNamed(rec, sessionCookieName) != nil {
		t.Fatalf("no code: %d", rec.Code)
	}
}

func TestAFailedExchangeIsRefused(t *testing.T) {
	s := newTestServerWith(t, func(o *Options) { startFakeGitHub(t, o) })
	start := get(s.Handler(), "/auth/github")
	state := cookieNamed(start, stateCookieName)
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=stale&state="+url.QueryEscape(state.Value), nil)
	req.AddCookie(state)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway || cookieNamed(rec, sessionCookieName) != nil {
		t.Fatalf("failed exchange: %d", rec.Code)
	}
}

func TestAnAccessCheckErrorFailsClosed(t *testing.T) {
	s := newTestServerWith(t, func(o *Options) {
		startFakeGitHub(t, o)
		o.Access = &stubAccess{err: errors.New("github is down")}
	})
	rec := signInThroughGitHub(t, s.Handler())
	if rec.Code != http.StatusBadGateway || cookieNamed(rec, sessionCookieName) != nil {
		t.Fatalf("access error: %d", rec.Code)
	}
}

// QS-4.2
func TestRefusedCallbacksAreRateLimited(t *testing.T) {
	s := newTestServerWith(t, func(o *Options) {
		startFakeGitHub(t, o)
		o.Access = &stubAccess{allow: map[string]bool{}}
	})
	for i := 0; i < signInAttempts; i++ {
		if rec := signInThroughGitHub(t, s.Handler()); rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d = %d", i, rec.Code)
		}
	}
	if rec := signInThroughGitHub(t, s.Handler()); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after the budget: %d", rec.Code)
	}
}

// QS-4.2: a lockout is a lockout for a while, not for as long as the Machine happens to stay up.
// The limiter's refill runs off the injected clock, so this is the only way to execute it.
func TestASignInLockoutRecoversAsTheClockAdvances(t *testing.T) {
	clock := &ports.FixedClock{T: testNow}
	s := newTestServerWith(t, func(o *Options) {
		startFakeGitHub(t, o)
		o.Clock = clock
		o.Access = &stubAccess{allow: map[string]bool{}}
	})
	h := s.Handler()

	for i := range signInAttempts {
		if rec := signInThroughGitHub(t, h); rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d = %d, want 403", i+1, rec.Code)
		}
	}
	if rec := signInThroughGitHub(t, h); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after the budget: %d, want 429", rec.Code)
	}

	clock.Advance(signInWindow)
	if rec := signInThroughGitHub(t, h); rec.Code != http.StatusForbidden {
		t.Errorf("a refused sign-in a whole window later = %d, want 403: the bucket never "+
			"refilled, so the legitimate user stays locked out until the Machine restarts", rec.Code)
	}
}

// startCountingFakeGitHub is startFakeGitHub with a counter around the fixture's token endpoint.
// It exists so that a test can assert what the callback did *not* do: an outbound POST leaves no
// trace in the response, so counting it at the fake is the only way to see it.
func startCountingFakeGitHub(t *testing.T, o *Options) *atomic.Int64 {
	t.Helper()
	var exchanges atomic.Int64
	fixture := fakesources.NewServer()
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login/oauth/access_token" {
			exchanges.Add(1)
		}
		fixture.ServeHTTP(w, r)
	}))
	t.Cleanup(fake.Close)
	o.Config.GitHub.OAuthBaseURL = fake.URL
	o.HTTPClient = fake.Client()
	return &exchanges
}

// QS-4.2: the budget has to be charged before zorgscope talks to GitHub, not after. If the limiter
// only decided what the visitor is shown, an anonymous caller could loop the callback for as long
// as it liked and still make this process POST to GitHub once per attempt — which spends an
// upstream quota it does not own and holds a Machine that scales to zero awake for the whole loop.
func TestASignInBeyondTheBudgetNeverReachesGitHub(t *testing.T) {
	var exchanges *atomic.Int64
	s := newTestServerWith(t, func(o *Options) {
		exchanges = startCountingFakeGitHub(t, o)
		o.Access = &stubAccess{allow: map[string]bool{}}
	})
	h := s.Handler()

	for i := range signInAttempts {
		if rec := signInThroughGitHub(t, h); rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d = %d, want 403", i+1, rec.Code)
		}
	}
	spent := exchanges.Load()
	if rec := signInThroughGitHub(t, h); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after the budget: %d, want 429", rec.Code)
	}
	if got := exchanges.Load() - spent; got != 0 {
		t.Errorf("a callback beyond the budget made %d call(s) to GitHub's token endpoint, want 0", got)
	}
}

// QS-4.2 again, from the legitimate visitor's side: the budget is ten attempts and the tenth is
// still one of them. Charging the limiter earlier must not quietly turn the limit into nine.
func TestTheLastAttemptInTheBudgetCanStillSignIn(t *testing.T) {
	access := &stubAccess{allow: map[string]bool{}}
	s := newTestServerWith(t, func(o *Options) {
		startFakeGitHub(t, o)
		o.Access = access
	})
	h := s.Handler()

	for i := range signInAttempts - 1 {
		if rec := signInThroughGitHub(t, h); rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d = %d, want 403", i+1, rec.Code)
		}
	}
	// The visitor finally signs in as themselves rather than as somebody with no push access.
	access.allow = map[string]bool{"fake-token": true}
	rec := signInThroughGitHub(t, h)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("the tenth attempt = %d, want 303: nine failures must not cost the tenth", rec.Code)
	}
	if cookieNamed(rec, sessionCookieName) == nil {
		t.Error("the tenth attempt set no session cookie")
	}
}

// FR-8.3 AC4: rotating the client secret is this product's only sign-out, and it works because the
// session key is derived from that secret (design 2026-09-14 §2).
func TestRotatingTheClientSecretInvalidatesExistingSessions(t *testing.T) {
	old := newTestServerWith(t, func(o *Options) { startFakeGitHub(t, o) })
	session := cookieNamed(signInThroughGitHub(t, old.Handler()), sessionCookieName)
	if session == nil {
		t.Fatal("no session cookie to rotate away from")
	}
	rotated := newTestServerWith(t, func(o *Options) { o.Config.Secrets.OAuthClientSecret = testClientSecret + "-rotated" })
	if rec := getAs(rotated.Handler(), "/", session); rec.Code != http.StatusSeeOther {
		t.Fatalf("old session after rotation = %d, want a redirect to sign-in", rec.Code)
	}
}

// QS-4.3: the code, the token and the client secret are credentials, and the state is what proves
// the flow started here. None of them may reach a page or a log line.
func TestNoSignInResponseOrLogLineCarriesACodeStateTokenOrSecret(t *testing.T) {
	var logs bytes.Buffer
	s := newTestServerWith(t, func(o *Options) {
		startFakeGitHub(t, o)
		o.Log = slog.New(slog.NewTextHandler(&logs, nil))
		o.Access = &stubAccess{allow: map[string]bool{}}
	})
	start := get(s.Handler(), "/auth/github")
	state := cookieNamed(start, stateCookieName)
	rec := signInThroughGitHub(t, s.Handler())
	for _, secret := range []string{"fake-code", "fake-token", testClientSecret, state.Value} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("response carries %q", secret)
		}
		if strings.Contains(logs.String(), secret) {
			t.Errorf("log carries %q", secret)
		}
	}
	if logs.Len() == 0 {
		t.Error("a refused sign-in was not logged at all (FR-8.3 AC5)")
	}
}

// The same sweep over the path that succeeds. It is the one that has a token in its hands and a
// cookie to set, so it is the one with something to leak (QS-4.3).
func TestNoAcceptedSignInResponseOrLogLineCarriesACodeStateTokenOrSecret(t *testing.T) {
	var logs bytes.Buffer
	s := newTestServerWith(t, func(o *Options) {
		startFakeGitHub(t, o)
		o.Log = slog.New(slog.NewTextHandler(&logs, nil))
	})
	start := get(s.Handler(), "/auth/github")
	state := cookieNamed(start, stateCookieName)
	rec := signInThroughGitHub(t, s.Handler())
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("this test needs an accepted sign-in; got %d", rec.Code)
	}
	if !strings.Contains(logs.String(), "sign-in accepted") {
		t.Errorf("an accepted sign-in was not logged at all:\n%s", logs.String())
	}

	// Headers as well as the body: the session cookie and the redirect both travel as headers, and
	// the state cookie is cleared through one.
	var headers strings.Builder
	for name, values := range rec.Header() {
		for _, v := range values {
			headers.WriteString(name + ": " + v + "\n")
		}
	}
	for _, secret := range []string{"fake-code", "fake-token", testClientSecret, state.Value} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("response carries %q", secret)
		}
		if strings.Contains(headers.String(), secret) {
			t.Errorf("a response header carries %q:\n%s", secret, headers.String())
		}
		if strings.Contains(logs.String(), secret) {
			t.Errorf("log carries %q", secret)
		}
	}
}

// The state cookie is scoped to the one route that reads it and expires on its own, so an
// abandoned sign-in leaves nothing behind in the browser.
func TestTheStateCookieIsScopedAndShortLived(t *testing.T) {
	h := newTestServerWith(t, func(o *Options) { startFakeGitHub(t, o) }).Handler()
	c := cookieNamed(get(h, "/auth/github"), stateCookieName)
	if c == nil {
		t.Fatal("no state cookie")
	}
	if c.Path != "/auth/callback" {
		t.Errorf("state cookie path = %q, want /auth/callback", c.Path)
	}
	if want := int(stateTTL / time.Second); c.MaxAge != want {
		t.Errorf("state cookie Max-Age = %d, want %d", c.MaxAge, want)
	}
}
