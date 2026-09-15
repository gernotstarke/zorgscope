package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// sessionCookieName is the only cookie this application sets to carry authentication.
	sessionCookieName = "zorgscope_session"
	// sessionTTL is how long one sign-in lasts. It is deliberately long: the cost of a short
	// session is a round trip to GitHub on a phone, for a dashboard whose whole purpose is being
	// glanced at.
	sessionTTL = 30 * 24 * time.Hour
	// sessionKeyContext domain-separates the signing key, so that the client secret used for
	// anything else cannot produce the same key. It says v2 because the seed changed from
	// ZORGSCOPE_TOKEN to the OAuth client secret (design 2026-09-14 §2): every cookie minted under
	// the old scheme stops verifying the moment this deploys, which is the correct answer to
	// replacing the credential a session stood for.
	sessionKeyContext = "zorgscope-session-v2"

	// stateCookieName holds the CSRF state of a sign-in in flight, and stateTTL is how long that
	// sign-in may take. Ten minutes is long enough to authorise the App and log into GitHub on the
	// way, and short enough that an abandoned attempt leaves nothing behind.
	stateCookieName = "zorgscope_oauth_state"
	stateTTL        = 10 * time.Minute

	// signInAttempts and signInWindow are the rate limit on sign-in attempts (QS-4.2). Every
	// callback spends a token, admitted or refused, because the budget has to be charged before
	// the outbound token exchange rather than after it — see handleAuthCallback.
	signInAttempts = 10
	signInWindow   = 15 * time.Minute
)

// sessionEncoding is URL-safe and unpadded, so a cookie value never needs quoting.
var sessionEncoding = base64.RawURLEncoding

// session is what the cookie proves: when the sign-in expires, and when the visitor last marked
// the list as seen (zero until they do). Both travel inside the signed payload, so neither can be
// forged, and neither needs a row anywhere — the stateless design's whole point (design §4).
type session struct {
	Expiry time.Time
	Seen   time.Time
}

// sessionCodec mints and verifies session cookie values.
//
// A cookie value is base64(expiry:seen) + "." + base64(HMAC-SHA256(key, expiry:seen)), where the
// key is SHA-256 of sessionKeyContext concatenated with GITHUB_OAUTH_CLIENT_SECRET. Two properties
// fall out of that derivation. The browser never holds a credential — only an expiry, a seen mark
// and a signature over them, so FR-8.3 AC2 needs no separate store, and neither the visitor's
// GitHub token nor the client secret is ever in the cookie jar. And rotating the client secret
// changes the key, which invalidates every signature ever minted under the old one: FR-8.3 AC4
// without a session table, a revocation list or anything else that would have to survive the
// Machine being stopped.
//
// The cookie holds no identity on purpose. The product has no per-user state, and "which
// collaborator is this" is a question it would then have to keep answering correctly.
type sessionCodec struct{ key [32]byte }

func newSessionCodec(secret string) *sessionCodec {
	return &sessionCodec{key: sha256.Sum256([]byte(sessionKeyContext + secret))}
}

// mint returns the cookie value for s.
func (c *sessionCodec) mint(s session) string {
	seen := int64(0)
	if !s.Seen.IsZero() {
		seen = s.Seen.Unix()
	}
	payload := strconv.FormatInt(s.Expiry.Unix(), 10) + ":" + strconv.FormatInt(seen, 10)
	return sessionEncoding.EncodeToString([]byte(payload)) + "." +
		sessionEncoding.EncodeToString(c.sign(payload))
}

// decode returns the session a cookie value proves, and false when the value is not a signature
// this codec produced over a session still valid at now. A value minted under the pre-reset,
// single-integer format has no ":" in its payload and is rejected the same way: it proves nothing
// this codec ever signed.
func (c *sessionCodec) decode(value string, now time.Time) (session, bool) {
	encPayload, encSig, ok := strings.Cut(value, ".")
	if !ok {
		return session{}, false
	}
	payload, err := sessionEncoding.DecodeString(encPayload)
	if err != nil {
		return session{}, false
	}
	sig, err := sessionEncoding.DecodeString(encSig)
	if err != nil {
		return session{}, false
	}
	if subtle.ConstantTimeCompare(sig, c.sign(string(payload))) != 1 {
		return session{}, false
	}
	expStr, seenStr, ok := strings.Cut(string(payload), ":")
	if !ok {
		return session{}, false
	}
	exp, err1 := strconv.ParseInt(expStr, 10, 64)
	seen, err2 := strconv.ParseInt(seenStr, 10, 64)
	if err1 != nil || err2 != nil {
		return session{}, false
	}
	s := session{Expiry: time.Unix(exp, 0)}
	if seen > 0 {
		s.Seen = time.Unix(seen, 0)
	}
	return s, now.Before(s.Expiry)
}

func (c *sessionCodec) sign(payload string) []byte {
	mac := hmac.New(sha256.New, c.key[:])
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}

// setSession writes the session cookie for sess.
//
// Secure is unconditional. Fly terminates TLS in front of this process and serves it over HTTPS
// only, so there is no deployment where the flag would lock the user out — while making it depend
// on the request's scheme would silently drop it behind exactly that proxy, which is the one place
// it matters.
func (s *Server) setSession(w http.ResponseWriter, sess session) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    s.codec.mint(sess),
		Path:     "/",
		Expires:  sess.Expiry,
		MaxAge:   int(sess.Expiry.Sub(s.clock.Now()) / time.Second),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// session reports the session a request's cookie proves, and whether it is currently valid. It
// looks at nothing else: in particular the request's path or method never decide it.
func (s *Server) session(r *http.Request) (session, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return session{}, false
	}
	return s.codec.decode(c.Value, s.clock.Now())
}

// requireSession refuses a request without a valid session cookie.
//
// When redirect is set the route is a browser navigation, and an anonymous GET is sent to the
// sign-in page rather than answered with a bare 401 (FR-8.3 AC1). Everything else — htmx
// fragments, form posts — gets 401, because redirecting a fragment swap would paint the sign-in
// page inside a tile. An htmx request is additionally told where to go with HX-Redirect, so an
// expired session takes the browser to sign-in instead of leaving a silently dead button.
func (s *Server) requireSession(next http.Handler, redirect bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.session(r); ok {
			// Rotating the OAuth client secret is the only sign-out this product has
			// (FR-8.3 AC4), and a rotation cannot reach a page the browser has already stored.
			// Without no-store the dashboard — names of repositories, issue titles — stays in
			// the back-forward cache and in any disk cache after the secret is rotated, which is
			// precisely the state the rotation was performed to end.
			w.Header().Set("Cache-Control", "no-store")
			next.ServeHTTP(w, r)
			return
		}
		if redirect && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", "/login")
		}
		s.unauthorised(w)
	})
}

// unauthorisedPage is the body of a 401 on a session route.
//
// The status has to stay 401: QS-4.1's table says so, and /items is swapped into the page by
// htmx, which must not paint a sign-in form into the list. But POST /seen is a plain browser
// form (FR-1.3 AC3), so with JavaScript disabled the HX-Redirect above is never read and the
// visitor is left looking at whatever this body says. A sentence and a link is the difference
// between an expired session and a dead end.
const unauthorisedPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<title>Sign in · zorgscope</title>
<link rel="stylesheet" href="/static/app.css">
</head>
<body>
<main>
<section class="signin">
<h1>Not signed in</h1>
<p>Your session has expired. <a href="/login">Sign in again</a>.</p>
</section>
</main>
</body>
</html>
`

func (s *Server) unauthorised(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(unauthorisedPage))
}

// clientIP identifies the caller for rate-limiting purposes.
//
// Fly-Client-IP is believed only when this process is actually running behind Fly's proxy, which
// is what trustFlyClientIP records (see behindFlyProxy). The header is trustworthy there and only
// there: Fly's proxy sets it itself, overwriting whatever the client sent. Anywhere else — the
// Compose stack, a local `go run`, a Machine reached over Fly's private 6PN network rather than
// through the proxy — it is just a request header, and believing it would hand every caller a
// fresh 10-token bucket per request by varying one string. That would remove QS-4.2 from the
// sign-in rate limit, the one credential left that this function guards.
//
// X-Forwarded-For is never consulted, under any circumstances: it is appended to rather than
// replaced, so even behind a trustworthy proxy its left-hand entries are the client's own words.
//
// Whatever is taken from the header must parse as an IP address. An unparseable value is not a
// client identity, and letting one through would make the bucket key attacker-chosen text.
func (s *Server) clientIP(r *http.Request) string {
	if s.trustFlyClientIP {
		if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("Fly-Client-IP"))); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	// RemoteAddr is written by net/http, not by the caller, so an address that does not parse is
	// still a stable identity rather than something an attacker chose.
	return host
}

// rateLimiter is a token bucket per client, refilling at limit tokens per window.
//
// It is in memory on purpose (QS-4.2). The Machine stops when nothing is in flight, so a counter
// in a database would survive a restart the attacker can simply wait out — while adding a write to
// every failed attempt, which is a denial-of-service amplifier rather than a defence. What the
// limit is actually for is making the sign-in state worth guessing pointless to guess online, and
// an in-memory bucket does that for exactly as long as the process is up to be attacked.
type rateLimiter struct {
	mu      sync.Mutex
	limit   float64
	window  time.Duration
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// maxTrackedClients bounds the memory the limiter can be made to allocate. A caller able to vary
// its apparent address could otherwise grow the map without limit; when it is full the least
// recently seen client is dropped, which at worst gives that client a fresh budget — the same
// thing waiting out the window would.
const maxTrackedClients = 4096

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		limit:   float64(limit),
		window:  window,
		buckets: make(map[string]*bucket),
	}
}

// allow consumes one token for key and reports whether there was one to consume.
func (l *rateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		l.evictLocked(now)
		b = &bucket{tokens: l.limit, last: now}
		l.buckets[key] = b
	}

	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens += elapsed.Seconds() * l.limit / l.window.Seconds()
		if b.tokens > l.limit {
			b.tokens = l.limit
		}
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// evictLocked drops fully recovered buckets, and then the least recently seen one, until there is
// room for another client. The caller holds the mutex.
func (l *rateLimiter) evictLocked(now time.Time) {
	if len(l.buckets) < maxTrackedClients {
		return
	}
	for key, b := range l.buckets {
		if now.Sub(b.last) >= l.window {
			delete(l.buckets, key)
		}
	}
	for len(l.buckets) >= maxTrackedClients {
		oldestKey, oldest := "", time.Time{}
		for key, b := range l.buckets {
			if oldest.IsZero() || b.last.Before(oldest) {
				oldestKey, oldest = key, b.last
			}
		}
		delete(l.buckets, oldestKey)
	}
}
