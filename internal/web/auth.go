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
	// sessionCookieName is the only cookie this application sets.
	sessionCookieName = "zorgscope_session"
	// sessionTTL is how long one sign-in lasts. It is deliberately long: this is a single-user
	// dashboard on a private token, and the cost of a short session is re-typing a 32-character
	// secret on a phone.
	sessionTTL = 30 * 24 * time.Hour
	// sessionKeyContext domain-separates the signing key, so that the same token used for
	// anything else in future cannot produce the same key.
	sessionKeyContext = "zorgscope-session-v1"

	// signInAttempts and signInWindow are the rate limit on failed credentials (QS-4.2).
	signInAttempts = 10
	signInWindow   = 15 * time.Minute
)

// sessionEncoding is URL-safe and unpadded, so a cookie value never needs quoting.
var sessionEncoding = base64.RawURLEncoding

// sessionCodec mints and verifies session cookie values.
//
// A cookie value is base64(expiry) + "." + base64(HMAC-SHA256(key, expiry)), where the key is
// SHA-256 of sessionKeyContext concatenated with ZORGSCOPE_TOKEN. Two properties fall out of that
// derivation. The browser never holds the token — only an expiry and a signature over it, so
// FR-8.3 AC2 needs no separate store. And changing the token changes the key, which invalidates
// every signature ever minted under the old one: FR-8.3 AC3 without a session table, a revocation
// list or anything else that would have to survive the Machine being stopped.
type sessionCodec struct{ key [32]byte }

func newSessionCodec(token string) *sessionCodec {
	return &sessionCodec{key: sha256.Sum256([]byte(sessionKeyContext + token))}
}

// mint returns the cookie value for a session expiring at exp.
func (c *sessionCodec) mint(exp time.Time) string {
	payload := strconv.FormatInt(exp.Unix(), 10)
	return sessionEncoding.EncodeToString([]byte(payload)) + "." +
		sessionEncoding.EncodeToString(c.sign(payload))
}

// valid reports whether value is a signature this codec produced over an expiry still in the
// future at now.
func (c *sessionCodec) valid(value string, now time.Time) bool {
	encPayload, encSig, ok := strings.Cut(value, ".")
	if !ok {
		return false
	}
	payload, err := sessionEncoding.DecodeString(encPayload)
	if err != nil {
		return false
	}
	sig, err := sessionEncoding.DecodeString(encSig)
	if err != nil {
		return false
	}
	if subtle.ConstantTimeCompare(sig, c.sign(string(payload))) != 1 {
		return false
	}
	exp, err := strconv.ParseInt(string(payload), 10, 64)
	if err != nil {
		return false
	}
	return now.Before(time.Unix(exp, 0))
}

func (c *sessionCodec) sign(payload string) []byte {
	mac := hmac.New(sha256.New, c.key[:])
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}

// setSession writes the session cookie.
//
// Secure is unconditional. Fly terminates TLS in front of this process and serves it over HTTPS
// only, so there is no deployment where the flag would lock the user out — while making it depend
// on the request's scheme would silently drop it behind exactly that proxy, which is the one place
// it matters.
func (s *Server) setSession(w http.ResponseWriter, now time.Time) {
	exp := now.Add(sessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    s.session.mint(exp),
		Path:     "/",
		Expires:  exp,
		MaxAge:   int(sessionTTL / time.Second),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// signedIn reports whether the request carries a valid session cookie. It looks at nothing else:
// in particular the refresh bearer does not sign anyone in.
func (s *Server) signedIn(r *http.Request) bool {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	return s.session.valid(c.Value, s.clock.Now())
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
		if s.signedIn(r) {
			// Rotating ZORGSCOPE_TOKEN is the only sign-out this product has (FR-8.3 AC3), and
			// a rotation cannot reach a page the browser has already stored. Without no-store
			// the dashboard — names of repositories, issue titles, task content — stays in the
			// back-forward cache and in any disk cache after the token is rotated, which is
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
// The status has to stay 401: QS-4.1's table says so, and /tile/{source} is swapped into the page
// by htmx, which must not paint a sign-in form inside a tile. But POST /seen is a plain browser
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

// requireBearer refuses a request that does not carry REFRESH_SECRET as a bearer token.
//
// It never consults the session cookie. ZORGSCOPE_TOKEN and REFRESH_SECRET are independent
// credentials on purpose: the cron service that calls this endpoint holds the ability to trigger a
// refresh and nothing else, and a stolen session must not become a way to hammer the upstream
// APIs. The comparison is constant-time, and failures are rate-limited per client just as
// sign-ins are (QS-4.2).
func (s *Server) requireBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if ok && subtle.ConstantTimeCompare([]byte(presented), []byte(s.cfg.Secrets.RefreshSecret)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		ip := s.clientIP(r)
		if !s.bearer.allow(ip, s.clock.Now()) {
			s.log.Warn("refresh authentication rate-limited", "ip", ip)
			http.Error(w, "Too many attempts.", http.StatusTooManyRequests)
			return
		}
		// The presented value is never logged: it may be the real secret, mistyped somewhere
		// else, and a log is not the place for either (QS-4.2, FR-8.3 AC4).
		s.log.Warn("refresh authentication failed", "ip", ip)
		http.Error(w, "Not authorised.", http.StatusUnauthorized)
	})
}

// handleLoginForm shows the sign-in page, or sends an already signed-in browser to the dashboard
// so that a bookmarked /login is not a dead end.
func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if s.signedIn(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "login.html", pageData{Title: "Sign in"})
}

// handleLoginSubmit checks the submitted token and, on success, issues the session cookie.
//
// The token is compared before the rate limit is consulted, and the limit counts failures only.
// That is what QS-4.2 asks for: an attacker who has burned the budget gains nothing, while the
// legitimate user — who is behind the same NAT as nobody in particular, but may well have mistyped
// the token ten times — is never locked out of their own dashboard by their own typos.
func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.render(w, r, http.StatusBadRequest, "login.html", pageData{
			Title: "Sign in",
			Error: "That sign-in form could not be read.",
		})
		return
	}
	submitted := r.PostFormValue("token")
	if subtle.ConstantTimeCompare([]byte(submitted), []byte(s.cfg.Secrets.AppToken)) == 1 {
		s.setSession(w, s.clock.Now())
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	ip := s.clientIP(r)
	if !s.signIn.allow(ip, s.clock.Now()) {
		s.log.Warn("sign-in rate-limited", "ip", ip)
		s.render(w, r, http.StatusTooManyRequests, "login.html", pageData{
			Title: "Sign in",
			Error: "Too many attempts. Try again later.",
		})
		return
	}
	// FR-8.3 AC4: the attempt is logged, the submitted value is not.
	s.log.Warn("sign-in failed", "ip", ip)
	s.render(w, r, http.StatusUnauthorized, "login.html", pageData{
		Title: "Sign in",
		Error: "That token was not accepted.",
	})
}

// clientIP identifies the caller for rate-limiting purposes.
//
// Fly-Client-IP is believed only when this process is actually running behind Fly's proxy, which
// is what trustFlyClientIP records (see behindFlyProxy). The header is trustworthy there and only
// there: Fly's proxy sets it itself, overwriting whatever the client sent. Anywhere else — the
// Compose stack, a local `go run`, a Machine reached over Fly's private 6PN network rather than
// through the proxy — it is just a request header, and believing it would hand every caller a
// fresh 10-token bucket per request by varying one string. That would remove QS-4.2 from both
// credentials at once, since the sign-in form and the refresh bearer share this function.
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
// in the database would survive a restart the attacker can simply wait out — while adding a write
// to every failed attempt, which is a denial-of-service amplifier rather than a defence. What the
// limit is actually for is making an online brute force of a 32-character secret pointless, and an
// in-memory bucket does that for exactly as long as the process is up to be attacked.
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
