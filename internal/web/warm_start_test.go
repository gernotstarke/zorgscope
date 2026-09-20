package web

import (
	"strings"
	"testing"
)

// The script is a file under /static, versioned like every other asset. It cannot be inline: the
// Content-Security-Policy grants no 'unsafe-inline' (see contentSecurityPolicy), so an inline
// script would simply not run, and the failure would be silent.
func TestTheWarmStartScriptIsLinkedAndVersioned(t *testing.T) {
	s := newTestServer(t)
	if _, ok := s.assets["warm-start.js"]; !ok {
		t.Fatal("warm-start.js is not among the embedded assets")
	}
	page := getAuthed(t, s.Handler(), "/").Body.String()
	if !strings.Contains(page, `<script src="/static/warm-start.js?`) {
		t.Error("the dashboard does not link warm-start.js with a version")
	}
}

// Every page stamps the epoch, because every page either saves, restores or clears, and all three
// need to know which client secret they are working under. A page that silently lost the attribute
// would have its stored list refused for ever, and the failure would look like the cache simply
// not working.
func TestEveryPageStampsTheEpoch(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	for _, path := range []string{"/", "/sites", "/contributors"} {
		page := getAuthed(t, h, path).Body.String()
		if !strings.Contains(page, `data-cache-epoch="`+s.cacheEpoch+`"`) {
			t.Errorf("%s carries no data-cache-epoch, so the script cannot tell a stale copy from a current one", path)
		}
	}
}

// The sign-in page clears rather than saves: reaching it means there is no session, whether Log
// out was pressed or the cookie expired, and a stored list must not outlive one.
func TestTheSignInPageTellsTheScriptToClear(t *testing.T) {
	if !strings.Contains(get(newTestServer(t).Handler(), "/login").Body.String(), `id="signed-out"`) {
		t.Error("the sign-in page does not tell warm-start.js to clear the stored list")
	}
}

// The wait page — the only page that restores — carries the mount point and the script. Without
// the mount point the script has nothing to fill, and the placeholder would never appear, which
// looks exactly like the cache being empty.
func TestTheWaitPageCarriesThePlaceholderMount(t *testing.T) {
	h, release := coldServer(t) // blocks on the first fetch: a genuinely cold Machine
	defer close(release)
	c := signIn(t, h)

	body := getAs(h, "/", c).Body.String()
	if !strings.Contains(body, waitingMarker) {
		t.Fatal("a cold Machine did not show the wait page")
	}
	if !strings.Contains(body, `id="placeholder"`) {
		t.Error("the wait page has nowhere to restore a stored list into")
	}
	if !strings.Contains(body, `<script src="/static/warm-start.js?`) {
		t.Error("the wait page does not link the script that would restore one")
	}
}
