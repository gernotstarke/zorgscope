package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
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
	for _, forbidden := range []string{`id="items"`, `class="filter"`, `class="tiles"`, "Mark all seen"} {
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
	c := signIn(t, h)
	getAs(h, "/", c) // starts the fetch
	close(release)

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
