package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const csrfCookie = "zs_csrf"

// componentServer names this component's log lines (arc42 §8.8).
const componentServer = "server"

func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic", "component", componentServer, "err", rec, "path", r.URL.Path)
					http.Error(w, "internal error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }

// requestLog logs one line per request. time.Now() here is an accepted waiver (see
// docs/plans/README.md): request latency is measured at the delivery edge, not derived from the
// domain clock.
func requestLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			if strings.HasPrefix(r.URL.Path, "/static/") || r.URL.Path == "/healthz" {
				return
			}
			log.Info("http", "component", componentServer, "method", r.Method, "path", r.URL.Path, "status", sw.status, "duration_ms", time.Since(start).Milliseconds())
		})
	}
}

// securityHeaders implements arc42 §8.6 (QS-3.4).
func securityHeaders(https bool) func(http.Handler) http.Handler {
	csp := "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", csp)
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("X-Frame-Options", "DENY")
			if https {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// csrf uses the double-submit pattern: an HttpOnly cookie whose value the server also renders into
// htmx's hx-headers; POSTs must echo it in X-CSRF-Token (QS-3.5), or in a "csrf_token" hidden form
// field for the no-JS fallback (§8.5). The token is stored on the request context by csrfToken().
func csrf(secure bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := ""
			if c, err := r.Cookie(csrfCookie); err == nil && len(c.Value) == 64 {
				token = c.Value
			}
			if token == "" {
				b := make([]byte, 32)
				if _, err := rand.Read(b); err != nil {
					http.Error(w, "internal error", http.StatusInternalServerError)
					return
				}
				token = hex.EncodeToString(b)
				http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 86400 * 30}) //nolint:gosec // Secure follows the scheme of base_url
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
				c, err := r.Cookie(csrfCookie)
				hdr := r.Header.Get("X-CSRF-Token")
				if hdr == "" {
					// No-JS fallback (§8.5 "everything works without JS"): a plain <form> POST
					// cannot set a header, so accept the token from a hidden form field too.
					// ParseForm is idempotent; handlers calling it again reuse the cached result.
					if ferr := r.ParseForm(); ferr == nil {
						hdr = r.PostForm.Get("csrf_token")
					}
				}
				if err != nil || hdr == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(hdr)) != 1 {
					http.Error(w, "csrf token mismatch", http.StatusForbidden)
					return
				}
				if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
					http.Error(w, "cross-site request rejected", http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, withCSRF(r, token))
		})
	}
}

// devAuth lets everything through in dev mode and rejects otherwise (replaced by passkeys in M3, FR-9.x).
func devAuth(mode string, s *Server) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if mode == "dev" {
				next.ServeHTTP(w, r)
				return
			}
			if strings.Contains(r.Header.Get("Accept"), "text/html") && r.Header.Get("HX-Request") == "" {
				s.render(w, http.StatusUnauthorized, "unauthorized", pageData{})
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
}

const (
	authFailureLimit  = 10
	authFailureWindow = time.Minute
)

// authFailureLimiter bounds invalid bootstrap-token attempts without ever delaying a request that
// supplied the correct token. A single global window is intentional for this single-user service:
// it is bounded in memory and cannot be bypassed by rotating source addresses.
type authFailureLimiter struct {
	mu          sync.Mutex
	windowStart time.Time
	failures    int
}

func newAuthFailureLimiter() *authFailureLimiter { return &authFailureLimiter{} }

func (l *authFailureLimiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.windowStart.IsZero() || now.Before(l.windowStart) || now.Sub(l.windowStart) >= authFailureWindow {
		l.windowStart = now
		l.failures = 0
	}
	if l.failures >= authFailureLimit {
		return false
	}
	l.failures++
	return true
}

// apiAuth protects the client-neutral JSON API. Token mode is the deployable bootstrap mechanism
// for native clients; the token is stored in the macOS Keychain. Browser/passkey sessions can be
// added behind this boundary without changing the API handlers. Dev mode is permitted only for a
// localhost base URL by config validation.
func apiAuth(mode, token string, log *slog.Logger, failures *authFailureLimiter) func(http.Handler) http.Handler {
	expected := sha256.Sum256([]byte(token))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if mode == "dev" {
				next.ServeHTTP(w, r)
				return
			}
			if mode == "token" {
				got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
				actual := sha256.Sum256([]byte(got))
				if ok && got != "" && subtle.ConstantTimeCompare(actual[:], expected[:]) == 1 {
					next.ServeHTTP(w, r)
					return
				}
			}
			if failures != nil && !failures.allow(time.Now()) {
				log.Warn("api authentication rate limited", "component", componentServer, "remote", r.RemoteAddr, "path", r.URL.Path)
				w.Header().Set("Retry-After", "60")
				writeAPIError(w, http.StatusTooManyRequests, "authentication_rate_limited", "too many invalid authentication attempts; retry later")
				return
			}
			log.Warn("api authentication failed", "component", componentServer, "remote", r.RemoteAddr, "path", r.URL.Path)
			w.Header().Set("WWW-Authenticate", `Bearer realm="zorgscope-api"`)
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "a valid API bearer token is required")
		})
	}
}
