package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/snapshot"
)

// errTestUpstream stands in for whatever GitHub itself might say; no test here cares about its
// text, only that a fetch failed.
var errTestUpstream = errors.New("upstream on fire")

// blockingSource answers only once released, so a test can hold a fetch in flight for as long
// as it needs and observe what the pages do meanwhile.
type blockingSource struct {
	release chan struct{}
	items   []domain.Item
}

func (s *blockingSource) Fetch(ctx context.Context) ([]domain.Item, error) {
	select {
	case <-s.release:
		return s.items, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// coldServer is a server whose first fetch will block until release is closed.
func coldServer(t *testing.T) (h http.Handler, release chan struct{}) {
	t.Helper()
	release = make(chan struct{})
	src := &blockingSource{release: release, items: []domain.Item{ghItem(1, "An issue", testNow.Add(-time.Hour))}}
	s, _ := newColdServerWith(t, func(o *Options) {
		o.Config.GitHub.Repos = append([]string{"org/repo"}, representativeRepos()...)
		o.Cache = snapshot.New(src, time.Hour, o.Clock)
	})
	return s.Handler(), release
}

func getWith(h http.Handler, path string, c *http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(c)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(rec, req)
	return rec
}

// QS-2.6: a page view never waits for GitHub. With the source blocked, GET / answers within the
// budget — with the wait page, not the list.
func TestPageNeverWaitsForGitHub(t *testing.T) {
	h, release := coldServer(t)
	defer close(release)
	c := signIn(t, h)

	start := time.Now()
	rec := getAs(h, "/", c)
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("GET / took %v with the source blocked; it must not wait for GitHub (QS-2.6)", elapsed)
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), waitingMarker) {
		t.Fatalf("GET / = %d, body lacks %s", rec.Code, waitingMarker)
	}
}

// FR-1.9 AC1, AC2, AC4: what the wait page holds.
func TestWaitPageWhileFetching(t *testing.T) {
	h, release := coldServer(t)
	defer close(release)
	c := signIn(t, h)

	rec := getAs(h, "/?kind=pr", c)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	for _, want := range []string{
		`class="orbit-stage"`,
		`<div class="orbit-stage" data-state="refreshing" aria-busy="true">`,
		`/static/logo-large.jpg?`,
		`class="orbit-beam"`,
		`class="polling-pulse"`,
		`<p class="waiting-status" role="status" aria-live="polite">Asking GitHub about 11 repositories…</p>`,
		`<div id="waiting" hx-get="/?kind=pr" hx-trigger="every 500ms" hx-target="main" hx-swap="outerHTML" hx-select="main"></div>`,
		`<noscript><meta http-equiv="refresh" content="2"></noscript>`,
		`<script src="/static/htmx.min.js?`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("wait page lacks %s", want)
		}
	}
	if got := openingTag(t, body, `<section class="waiting"`); strings.Contains(got, "aria-busy") {
		t.Errorf(`<section class="waiting"> carries aria-busy: %s — a busy ancestor withholds the`+
			" status line below it from assistive technology; aria-busy belongs on the decorative"+
			" orbit stage alone", got)
	}
	// The chrome in the top bar stays — it is the layout's, not the list's (FR-1.11); what must
	// be gone is everything the page below would have shown, the Fetched line included.
	for _, forbidden := range []string{`id="items"`, `class="filter"`, `class="tiles"`, `class="fetched-line"`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("wait page shows %s; it must show nothing of the list while the fetch runs (FR-1.9 AC1)", forbidden)
		}
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store: a wait page must never be served from a browser cache", cc)
	}
	if rec.Header().Get("Content-Security-Policy") != contentSecurityPolicy {
		t.Error("the wait page does not carry the Content-Security-Policy every page carries")
	}
}

// The poll during a fetch is answered 204, so htmx swaps nothing and the animation keeps running.
func TestPollAnswers204WhileFetching(t *testing.T) {
	h, release := coldServer(t)
	defer close(release)
	c := signIn(t, h)

	rec := getWith(h, "/", c, map[string]string{"HX-Request": "true", "HX-Trigger": waitingPollID})
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("poll = %d with %d bytes, want 204 and no body", rec.Code, rec.Body.Len())
	}
}

// Once the fetch has landed, the poll receives the page: a title for htmx to apply, a main to
// swap in, and the list.
func TestPollReceivesThePageAfterTheFetch(t *testing.T) {
	h, release := coldServer(t)
	defer close(release)
	c := signIn(t, h)
	getAs(h, "/", c)      // starts the fetch
	release <- struct{}{} // unblocks it now; the deferred close above is only cleanup once the test ends

	rec := getSettled(t, h, "/", c)
	body := rec.Body.String()
	for _, want := range []string{"<title>", "<main>", `id="items"`, "An issue"} {
		if !strings.Contains(body, want) {
			t.Errorf("the settled page lacks %s", want)
		}
	}
	poll := getWith(h, "/", c, map[string]string{"HX-Request": "true", "HX-Trigger": waitingPollID})
	if poll.Code != http.StatusOK || !strings.Contains(poll.Body.String(), `id="items"`) {
		t.Errorf("poll after the fetch = %d; want the page with the list", poll.Code)
	}
}

// FR-1.9 AC5: the filter form's htmx request during a fetch is served from the current snapshot,
// never with the wait page — an hx-select="#items" against a wait page would empty the list.
func TestFilterRequestDuringFetchIsServedFromTheSnapshot(t *testing.T) {
	h, release := coldServer(t)
	defer close(release)
	c := signIn(t, h)

	rec := getWith(h, "/?kind=pr", c, map[string]string{"HX-Request": "true"})
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `id="items"`) {
		t.Fatalf("htmx GET / during a fetch = %d, body lacks the list", rec.Code)
	}
	if strings.Contains(body, waitingMarker) {
		t.Error("the filter request was answered with the wait page")
	}
	frag := getWith(h, "/items", c, map[string]string{"HX-Request": "true"})
	if frag.Code != http.StatusOK || strings.Contains(frag.Body.String(), waitingMarker) {
		t.Errorf("GET /items during a fetch = %d, or carried the wait page", frag.Code)
	}
}

// The Sites view waits the same way and polls its own path.
func TestSitesViewWaitsToo(t *testing.T) {
	h, release := coldServer(t)
	defer close(release)
	c := signIn(t, h)

	body := getAs(h, "/sites", c).Body.String()
	if !strings.Contains(body, `hx-get="/sites"`) || !strings.Contains(body, waitingMarker) {
		t.Errorf("GET /sites during a fetch is not the wait page polling /sites")
	}
}

// refetchBlockSource answers its first fetch — the warm-up — at once, and blocks every fetch after
// that until release is closed or sent to, so a test can warm the cache and then hold a refetch in
// flight while the list the warm-up fetched is already on hand.
type refetchBlockSource struct {
	mu      sync.Mutex
	calls   int
	items   []domain.Item
	release chan struct{}
}

func (s *refetchBlockSource) Fetch(ctx context.Context) ([]domain.Item, error) {
	s.mu.Lock()
	s.calls++
	first := s.calls == 1
	s.mu.Unlock()
	if first {
		return s.items, nil
	}
	select {
	case <-s.release:
		return s.items, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// FR-1.9 AC1, and the decision behind it (design §2): the wait page appears on every fetch, a
// Refresh included — a list already held is not shown while a fresh one is on its way.
func TestRefreshShowsTheWaitPageEvenThoughAListIsAlreadyHeld(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	src := &refetchBlockSource{
		items:   []domain.Item{ghItem(1, "Held already", testNow.Add(-time.Hour))},
		release: release,
	}
	s, cache := newColdServerWith(t, func(o *Options) {
		o.Config.GitHub.Repos = append([]string{"org/repo"}, representativeRepos()...)
		o.Cache = snapshot.New(src, time.Hour, o.Clock)
	})
	warmCache(t, cache) // the warm-up fetch, answered at once
	h := s.Handler()
	c := signIn(t, h)

	rec := postAs(h, "/refresh", nil, c)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /refresh = %d, want %d", rec.Code, http.StatusSeeOther)
	}

	body := getAs(h, "/", c).Body.String()
	if !strings.Contains(body, waitingMarker) {
		t.Error("GET / after Refresh did not show the wait page")
	}
	if strings.Contains(body, "Held already") {
		t.Error("GET / after Refresh showed the list already held, instead of waiting for the fresh one")
	}

	release <- struct{}{} // unblocks the refetch; the deferred close is only cleanup once the test ends
	body = getSettled(t, h, "/", c).Body.String()
	if !strings.Contains(body, "Held already") {
		t.Error("once the refetch landed the list did not come back")
	}
}

// FR-1.9 AC1: the same rule holds for a TTL that has expired as for a Refresh — a list already held
// is not shown while the TTL's own refetch is on its way.
func TestAnExpiredTTLShowsTheWaitPageNotTheOldList(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	src := &refetchBlockSource{
		items:   []domain.Item{ghItem(1, "Held already", testNow.Add(-time.Hour))},
		release: release,
	}
	var clock *ports.FixedClock
	s, cache := newColdServerWith(t, func(o *Options) {
		o.Config.GitHub.Repos = append([]string{"org/repo"}, representativeRepos()...)
		clock = o.Clock.(*ports.FixedClock)
		o.Cache = snapshot.New(src, time.Minute, o.Clock)
	})
	warmCache(t, cache) // the warm-up fetch, answered at once
	h := s.Handler()
	c := signIn(t, h)

	clock.Advance(2 * time.Minute) // past the one-minute TTL

	body := getAs(h, "/", c).Body.String()
	if !strings.Contains(body, waitingMarker) {
		t.Error("GET / past the TTL did not show the wait page")
	}
	if strings.Contains(body, "Held already") {
		t.Error("GET / past the TTL showed the old list, instead of waiting for the fresh one")
	}

	release <- struct{}{} // unblocks the refetch; the deferred close is only cleanup once the test ends
	body = getSettled(t, h, "/", c).Body.String()
	if !strings.Contains(body, "Held already") {
		t.Error("once the refetch landed the list did not come back")
	}
}

// FR-1.9 AC3: a fetch that fails ends the waiting the ordinary way — the page with the notice.
func TestAFailedFetchEndsTheWaiting(t *testing.T) {
	src := &fakeSource{err: errTestUpstream}
	s, _ := newColdServerWith(t, func(o *Options) { o.Cache = snapshot.New(src, time.Hour, o.Clock) })
	h := s.Handler()
	c := signIn(t, h)

	rec := getSettled(t, h, "/", c)
	body := rec.Body.String()
	if !strings.Contains(body, "GitHub unreachable since") || !strings.Contains(body, "Fetched never") {
		t.Errorf("after a failed first fetch the page lacks the notice or 'Fetched never'")
	}
	if src.CallCount() != 1 {
		t.Errorf("fetches = %d, want 1: a failure is not retried within the TTL", src.CallCount())
	}
}

// QS-2.3, the wait page's own measure: HTML and static assets on the wire.
func TestWaitPageStaysInsideItsBudget(t *testing.T) {
	h, release := coldServer(t)
	defer close(release)
	c := signIn(t, h)

	page := getAs(h, "/", c).Body.String()
	if n := len(page); n > 20*1024 {
		t.Errorf("wait page is %d bytes of HTML, budget is 20 kB", n)
	}
	assets := linkedStaticAssets(page)
	if len(assets) < 3 {
		t.Fatalf("found %d static assets on the wait page (%v); the stylesheet, htmx and the mark are all linked", len(assets), assets)
	}
	total := 0
	for _, asset := range assets {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, asset, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", asset, rec.Code)
		}
		total += rec.Body.Len()
		t.Logf("%s: %d bytes on the wire", asset, rec.Body.Len())
	}
	if total > 100*1024 {
		t.Errorf("the wait page's static assets are %d bytes on the wire, budget is 100 kB", total)
	}
}
