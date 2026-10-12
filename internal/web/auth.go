package web

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
)

const (
	// sessionCookieName is the only cookie this application sets to carry authentication.
	sessionCookieName = "zorgscope_session"
	// sessionTTL is how long one sign-in lasts. It is deliberately long: the cost of a short
	// session is a round trip to GitHub on a phone, for a dashboard whose whole purpose is being
	// glanced at.
	sessionTTL = 30 * 24 * time.Hour
	// sessionKeyContext domain-separates the key. v3 (spec 2026-10-12 §4): the cookie became
	// encrypted and started carrying a role, a login and — for a member — a token and a
	// selection. Every v2 cookie fails to open under the new key and is refused, which is the
	// right answer to a cookie whose meaning changed.
	sessionKeyContext = "zorgscope-session-v3"

	// memberSessionTTL is a member's session (spec 2026-10-12 D9): shorter than the owner's,
	// because the cookie now carries a token, and a lost phone should carry it for days, not a
	// month.
	memberSessionTTL = 7 * 24 * time.Hour

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

// sessionEncoding is URL-safe and unpadded, so a cookie value never needs quoting. It is Strict:
// the lenient decoder ignores the unused trailing bits of the last character, so two different
// strings would open to one valid cookie. Strict refuses non-zero padding bits, leaving exactly one
// string per cookie.
var sessionEncoding = base64.RawURLEncoding.Strict()

// role is what a session may see: the configured scope (owner) or a personal one (member).
type role string

const (
	roleOwner  role = "owner"
	roleMember role = "member"
)

// sessionTTLFor is how long a sign-in of role r lasts.
func sessionTTLFor(r role) time.Duration {
	if r == roleMember {
		return memberSessionTTL
	}
	return sessionTTL
}

// session is what the cookie proves: who signed in, as what, until when — and for a member, the
// token their own view is fetched with and the repositories it shows (spec 2026-10-12 §4).
type session struct {
	Expiry time.Time
	Role   role
	Login  string
	// Token is a member's OAuth access token; empty for an owner, whose view runs on the
	// configured token. Repos is a member's selection, at most config.MaxMemberRepos.
	Token string
	Repos []string
}

// member reports whether this is a member's session.
func (s session) member() bool { return s.Role == roleMember }

// sessionWire is the cookie's plaintext.
type sessionWire struct {
	Exp   int64    `json:"exp"`
	Role  string   `json:"role"`
	Login string   `json:"login"`
	Token string   `json:"tok,omitempty"`
	Repos []string `json:"repos,omitempty"`
}

// sessionCodec mints and opens session cookie values.
//
// A cookie value is base64url(nonce ‖ AES-256-GCM(key, nonce, json)), where the key is SHA-256 of
// sessionKeyContext concatenated with GITHUB_OAUTH_CLIENT_SECRET. GCM authenticates as well as
// encrypts, so a forged or altered value fails to open, and rotating the client secret changes the
// key and ends every session at once — FR-8.3 AC4 without a session table. Encryption rather than
// a signature because a member's cookie carries their token, which must be confidential to the
// browser and to anyone who copies the cookie, not merely tamper-proof.
type sessionCodec struct{ aead cipher.AEAD }

func newSessionCodec(secret string) *sessionCodec {
	key := sha256.Sum256([]byte(sessionKeyContext + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		panic(err) // a 32-byte key never fails
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return &sessionCodec{aead: aead}
}

// mint returns the cookie value for s.
func (c *sessionCodec) mint(s session) string {
	plain, err := json.Marshal(sessionWire{
		Exp: s.Expiry.Unix(), Role: string(s.Role), Login: s.Login, Token: s.Token, Repos: s.Repos,
	})
	if err != nil {
		panic(err) // strings and ints only
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err) // crypto/rand never fails on a platform this runs on; Go crashes before returning one
	}
	return sessionEncoding.EncodeToString(c.aead.Seal(nonce, nonce, plain, nil))
}

// decode returns the session a cookie value proves, and false when the value did not come from
// this codec, has expired, or names something a session may not: an unknown role, a login that is
// not one, a member without a token, more than config.MaxMemberRepos repositories or one not in
// owner/name form. Every refusal is a sign-in, never a 500 (spec 2026-10-12 §4).
func (c *sessionCodec) decode(value string, now time.Time) (session, bool) {
	raw, err := sessionEncoding.DecodeString(value)
	if err != nil || len(raw) < c.aead.NonceSize() {
		return session{}, false
	}
	nonce, sealed := raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():]
	plain, err := c.aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return session{}, false
	}
	var w sessionWire
	if err := json.Unmarshal(plain, &w); err != nil {
		return session{}, false
	}
	s := session{Expiry: time.Unix(w.Exp, 0), Role: role(w.Role), Login: w.Login, Token: w.Token, Repos: w.Repos}
	switch {
	case s.Role != roleOwner && s.Role != roleMember:
		return session{}, false
	case !config.IsLogin(s.Login):
		return session{}, false
	case s.member() && s.Token == "":
		return session{}, false
	case len(s.Repos) > config.MaxMemberRepos:
		return session{}, false
	}
	for _, repo := range s.Repos {
		if !config.IsRepo(repo) {
			return session{}, false
		}
	}
	return s, now.Before(s.Expiry)
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
//
// It is the one definition of "a valid session" — requireSession, /login and /logout all ask it —
// so it carries the one rule the codec cannot know: a member's cookie is no session on a
// deployment with members switched off. Checked anywhere else, one route would refuse such a
// cookie while another accepted it, and / and /login redirected to each other forever.
func (s *Server) session(r *http.Request) (session, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return session{}, false
	}
	sess, ok := s.codec.decode(c.Value, s.clock.Now())
	if !ok || (sess.member() && !s.cfg.GitHub.MembersEnabled()) {
		return session{}, false
	}
	return sess, true
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
		if sess, ok := s.session(r); ok {
			// Neither way a session ends — signing out, which clears this browser's cookie, or
			// rotating the OAuth client secret, which ends every session at once (FR-8.3 AC4) —
			// can reach a page the browser has already stored. Without no-store the dashboard —
			// names of repositories, issue titles — stays in the back-forward cache and in any
			// disk cache after the session ended, which is precisely the state ending it was
			// meant to end.
			w.Header().Set("Cache-Control", "no-store")
			if redirect {
				// One line per page view (spec §10): who, as what, where. Fragments and polls are
				// not pages and would flood the log.
				s.log.Info("page view", "login", sess.Login, "role", sess.Role, "path", r.URL.Path)
			}
			next.ServeHTTP(w, withSession(r, sess))
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

// requireSignOut guards /logout. A valid session goes on to the handler like any session route.
// A cookie that is no valid session — expired, altered, minted under a rotated secret, or a
// member's after members were switched off — is cleared and the browser sent to sign in: it
// proves nothing, clearing it grants nothing, and refusing it would leave the visitor no way to
// be rid of it. A request carrying no cookie at all is refused like every other session route, so
// /logout still refuses anonymous access (QS-4.1) and a cross-site post, which SameSite=Lax sends
// without the cookie, cannot sign anyone out.
func (s *Server) requireSignOut(next http.Handler) http.Handler {
	guarded := s.requireSession(next, false)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie(sessionCookieName); err == nil {
			if _, ok := s.session(r); !ok {
				s.log.Info("sign-out", "role", "none", "revoked", false)
				w.Header().Set("Cache-Control", "no-store")
				clearSession(w)
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
		}
		guarded.ServeHTTP(w, r)
	})
}

// clearSession tells the browser to drop the session cookie. Its attributes are setSession's, so
// the clear names the very cookie the set wrote.
func clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

// unauthorisedPage is the body of a 401 on a session route.
//
// The status has to stay 401: QS-4.1's table says so, and /items is swapped into the page by
// htmx, which must not paint a sign-in form into the list. But POST /refresh is a plain browser
// form, so with JavaScript disabled the HX-Redirect above is never read and the visitor is left
// looking at whatever this body says. A sentence and a link is the difference between an expired
// session and a dead end.
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
