package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"

	"golang.org/x/oauth2"

	"github.com/gernotstarke/zorgscope/internal/ports"
)

// oauthConfig is the client for the OAuth App this deployment was registered as. RedirectURL is
// deliberately empty: GitHub then sends the browser to the callback registered on the App, so the
// process never has to know or trust its own public host (design 2026-09-14 §2).
//
// AuthStyleInParams is named rather than left to be probed: GitHub takes the client credentials in
// the form body, and letting oauth2 discover that costs a failed request on the first sign-in after
// every cold start — which on a Machine that scales to zero is most of them.
func (s *Server) oauthConfig() *oauth2.Config {
	base := s.cfg.GitHub.OAuthBaseURL
	if base == "" {
		base = "https://github.com"
	}
	return &oauth2.Config{
		ClientID:     s.cfg.Secrets.OAuthClientID,
		ClientSecret: s.cfg.Secrets.OAuthClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:   base + "/login/oauth/authorize",
			TokenURL:  base + "/login/oauth/access_token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
		// No scopes at all. zorgscope is a public repository, and the authenticated user's own
		// permission on a public repository is readable without one, so the App asks the visitor
		// for nothing beyond their identity (design 2026-09-14 §2).
	}
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

// handleAuthStart begins the flow: a random state in a short-lived cookie, and a redirect to
// GitHub. It is a GET because the CSP's form-action would let Chrome block the redirect after a
// POST, and because all it does is set one cookie.
func (s *Server) handleAuthStart(w http.ResponseWriter, r *http.Request) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		s.fail(w, r, "starting sign-in", err)
		return
	}
	state := base64.RawURLEncoding.EncodeToString(raw[:])
	http.SetCookie(w, stateCookie(state, int(stateTTL.Seconds())))
	http.Redirect(w, r, s.oauthConfig().AuthCodeURL(state), http.StatusSeeOther)
}

// stateCookie is the only place the state cookie's shape is written down, so that setting it and
// clearing it cannot drift apart — a clear whose Path differed from the set's would leave the
// cookie in the browser and silently make the next sign-in's state check meaningless.
//
// Path is the callback alone: the state proves one thing to one route, and a cookie sent with
// every request to this site would be one more secret in every log of every proxy in between.
func stateCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     stateCookieName,
		Value:    value,
		Path:     "/auth/callback",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}

// handleAuthCallback finishes the flow. Every refusal clears the state cookie, sets no session,
// counts against the sign-in rate limit and is logged without the code, the state or the token.
//
// The visitor's access token lives for exactly one question — may this person push to the
// repository? — and is never stored, logged or rendered (design 2026-09-14 §2).
func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	clearState := func() { http.SetCookie(w, stateCookie("", -1)) }
	// refuse is the single exit for everything that is not a signed-in collaborator. why goes to
	// the log beside the client address and nothing else; msg is what the visitor is shown, and it
	// is a fixed sentence rather than an upstream error's own words (QS-4.3).
	refuse := func(status int, why, msg string) {
		clearState()
		ip := s.clientIP(r)
		if !s.signIn.allow(ip, s.clock.Now()) {
			// why goes into this line too: an operator reading a burst of 429s still wants to
			// know what the attempts underneath them were failing on.
			s.log.Warn("sign-in rate-limited", "ip", ip, "why", why)
			s.render(w, r, http.StatusTooManyRequests, "login.html", pageData{Title: "Sign in", Error: "Too many attempts. Try again later."})
			return
		}
		s.log.Warn("sign-in refused", "ip", ip, "why", why)
		s.render(w, r, status, "login.html", pageData{Title: "Sign in", Error: msg})
	}

	c, err := r.Cookie(stateCookieName)
	q := r.URL.Query()
	// Constant-time, because the state is a secret the browser and this process share for the
	// length of one sign-in, and a comparison that stopped at the first wrong byte would let it be
	// guessed a byte at a time.
	if err != nil || c.Value == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(q.Get("state"))) != 1 {
		refuse(http.StatusBadRequest, "state mismatch", "That sign-in did not start here. Try again.")
		return
	}
	// No code covers the visitor pressing "Cancel" on GitHub's authorisation page, which comes
	// back as error=access_denied rather than as a code.
	if q.Get("code") == "" {
		refuse(http.StatusBadRequest, "no code", "GitHub sent no code. Try again.")
		return
	}

	ctx := context.WithValue(r.Context(), oauth2.HTTPClient, s.httpClient)
	tok, err := s.oauthConfig().Exchange(ctx, q.Get("code"))
	if err != nil {
		refuse(http.StatusBadGateway, "exchange failed", "GitHub did not accept the sign-in. Try again.")
		return
	}
	notACollaborator := "This dashboard is for collaborators of " + s.cfg.GitHub.AuthRepo + "."
	ok, err := s.access.HasPushAccess(ctx, tok.AccessToken)
	switch {
	// GitHub answered without saying what the visitor may do. The visitor is refused exactly as a
	// stranger is — the check fails closed — but the log says which of the two happened, because
	// only this one is fixed by asking the App for the read:org or repo scope (design §8).
	case errors.Is(err, ports.ErrNoPermissionsBlock):
		refuse(http.StatusForbidden, "no permissions block", notACollaborator)
		return
	case err != nil:
		refuse(http.StatusBadGateway, "access check failed", "GitHub could not be asked who you are. Try again.")
		return
	case !ok:
		refuse(http.StatusForbidden, "no push access", notACollaborator)
		return
	}
	clearState()
	// The one audit line this product needs: a collaborator signed in, from where. Never a login
	// name — the visitor did not choose to publish one here — and never the token (FR-8.3 AC4).
	s.log.Info("sign-in accepted", "ip", s.clientIP(r))
	s.setSession(w, s.clock.Now())
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
