// These tests cover the two endpoints that drive every write this product ever makes. zorgscope
// runs on a Machine that scales to zero: there is no scheduler and no process between requests, so
// POST /api/refresh — and nothing else — is what puts fresh data in the database.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/refresh"
)

// apiBody is the shape the cron service reads: the run's outcome, and per source how much was
// stored and what failed (FR-5.1 AC3).
type apiBody struct {
	OK        bool
	Trigger   string
	StartedAt time.Time
	EndedAt   time.Time
	Sources   []struct {
		Source string
		Stored int
		Err    string
	}
}

// FR-5.1 AC3
func TestAPIRefreshRunsAndReportsPerSource(t *testing.T) {
	store := newLeaseStore()
	h := newRefreshServer(t, store, &ports.FixedClock{T: testNow},
		fetcherWith("github", ghItem(1, "an issue", testNow)),
		fetcherWith("todoist"),
	).Handler()

	rec := postBearer(t, h, "/api/refresh", testSecret)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	body := decodeAPI(t, rec)
	if !body.OK {
		t.Errorf("ok = false, want true — every source succeeded")
	}
	if body.Trigger != "cron" {
		t.Errorf("trigger = %q, want %q — the run record has to say who asked", body.Trigger, "cron")
	}
	if len(body.Sources) != 2 {
		t.Fatalf("the response reports %d sources, want 2 (FR-5.1 AC3): %+v", len(body.Sources), body.Sources)
	}
	if body.Sources[0].Source != "github" || body.Sources[0].Stored != 1 {
		t.Errorf("first source = %+v, want github with 1 item stored", body.Sources[0])
	}
	if got := store.replaced("github"); got != 1 {
		t.Errorf("the store was given %d github items, want 1 — the report must describe what "+
			"was actually stored", got)
	}
}

// FR-5.1 AC2: a wrong or missing secret must be refused *and* must fetch nothing. The status code
// alone would not say that; the upstream APIs are rate-limited and are the thing being protected.
func TestAPIRefreshRejectsAWrongSecretAndFetchesNothing(t *testing.T) {
	for _, tt := range []struct {
		name, header string
	}{
		{"a wrong secret", "Bearer wrong"},
		{"no Authorization header at all", ""},
		{"the app token instead of the refresh secret", "Bearer " + testToken},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &ports.FakeFetcher{SourceName: "github"}
			store := newLeaseStore()
			h := newRefreshServer(t, store, &ports.FixedClock{T: testNow}, f).Handler()

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if f.CallCount() != 0 {
				t.Errorf("an unauthenticated refresh fetched anyway (FR-5.1 AC2)")
			}
			if store.startedRuns() != 0 {
				t.Errorf("an unauthenticated refresh recorded a run (FR-5.1 AC2)")
			}
		})
	}
}

// FR-5.1 AC4 / QS-1.4: one failing source never prevents the others from being stored, and the
// run is still answered — a failing source is not a failing request.
func TestOneFailingSourceNeverStopsTheOthers(t *testing.T) {
	store := newLeaseStore()
	h := newRefreshServer(t, store, &ports.FixedClock{T: testNow},
		&ports.FakeFetcher{SourceName: "github", Err: errors.New("upstream said no")},
		fetcherWith("todoist", ghItem(2, "a task", testNow)),
	).Handler()

	rec := postBearer(t, h, "/api/refresh", testSecret)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a failing source is not a failing request (FR-5.1 AC4)", rec.Code)
	}
	body := decodeAPI(t, rec)
	if body.OK {
		t.Error("ok = true, want false — one source failed")
	}
	if len(body.Sources) != 2 {
		t.Fatalf("the response reports %d sources, want 2", len(body.Sources))
	}
	if body.Sources[0].Err == "" {
		t.Error("the failing source is reported with no error (FR-5.1 AC3)")
	}
	if body.Sources[1].Err != "" || body.Sources[1].Stored != 1 {
		t.Errorf("the healthy source = %+v, want 1 item stored and no error (FR-5.1 AC4)", body.Sources[1])
	}
	if got := store.replaced("todoist"); got != 1 {
		t.Errorf("the healthy source stored %d items, want 1: a failing source must not stop "+
			"the others being written (FR-5.1 AC4)", got)
	}
	if _, wrote := store.replacedOK("github"); wrote {
		t.Error("the failing source's items were written; a partial result must be left alone")
	}
}

// QS-1.7 / FR-5.2 AC2: only one refresh runs at a time, and the second is refused with 409. The
// exclusion is the database lease, not a mutex in this process, so it survives a Machine restart —
// which is why the store here models the lease rather than always granting it.
func TestSecondConcurrentRefreshGets409(t *testing.T) {
	block := make(chan struct{})
	f := &ports.FakeFetcher{SourceName: "github", Block: block}
	store := newLeaseStore()
	h := newRefreshServer(t, store, &ports.FixedClock{T: testNow}, f).Handler()

	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- postBearer(t, h, "/api/refresh", testSecret) }()
	// The first run holds the lease from the moment it is inside its fetch.
	waitFor(t, "the first refresh to start fetching", func() bool { return f.CallCount() > 0 })

	second := postBearer(t, h, "/api/refresh", testSecret)

	if second.Code != http.StatusConflict {
		t.Errorf("a second concurrent refresh = %d, want 409 (FR-5.2 AC2, QS-1.7)", second.Code)
	}
	if got := f.CallCount(); got != 1 {
		t.Errorf("the source was fetched %d times, want 1: the refused refresh must not have "+
			"fetched anything (QS-1.7)", got)
	}
	if got := store.startedRuns(); got != 1 {
		t.Errorf("%d runs were recorded, want 1: the refused refresh must not open a run", got)
	}

	close(block)
	if rec := <-first; rec.Code != http.StatusOK {
		t.Errorf("the first refresh = %d, want 200: the one that holds the lease must succeed", rec.Code)
	}
}

// QS-2.5, measured where the requirement says to measure it: in the run record, against the fake
// sources. The clock is the real one here on purpose — the duration the record carries is the one
// the runner stamped from it, so a fixed clock would make the assertion say nothing.
func TestRefreshCompletesInsideTheBudget(t *testing.T) {
	store := newLeaseStore()
	h := newRefreshServer(t, store, ports.SystemClock{},
		fetcherWith("github", ghItem(1, "an issue", testNow)),
		fetcherWith("todoist", ghItem(2, "a task", testNow)),
		fetcherWith("plausible"),
	).Handler()

	rec := postBearer(t, h, "/api/refresh", testSecret)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	started, ended, finished := store.record()
	if !finished {
		t.Fatal("no run record was closed out, so there is no duration to judge (FR-5.4)")
	}
	if d := ended.Sub(started); d > 30*time.Second {
		t.Fatalf("the run record says the refresh took %v; the budget is 30 s (QS-2.5)", d)
	}
	// The same figure travels to the caller, so cron-job.org's own history shows it too.
	body := decodeAPI(t, rec)
	if body.EndedAt.Before(body.StartedAt) {
		t.Errorf("the reported run ended (%v) before it started (%v)", body.EndedAt, body.StartedAt)
	}
}

// The other half of QS-2.5: nothing may hold the Machine awake. Fly does not stop a Machine with a
// request in flight, so an upstream that accepts a connection and never answers would keep this
// process billed and running — unless the run carries a ceiling of its own.
func TestTheRefreshRunCarriesACeiling(t *testing.T) {
	f := &deadlineFetcher{name: "github"}
	h := newRefreshServer(t, newLeaseStore(), &ports.FixedClock{T: testNow}, f).Handler()

	postBearer(t, h, "/api/refresh", testSecret)

	deadline, ok := f.seen()
	if !ok {
		t.Fatal("the run was given a context with no deadline: a hung upstream would hold the " +
			"Machine awake for as long as it stayed silent (QS-2.5)")
	}
	// Generous on the lower bound, because the runner's own closing writes each carry a
	// five-second deadline of their own and a ceiling sized to the 30 s budget would cut them
	// off exactly when they matter. Strict on the upper bound, because that is the point.
	if left := time.Until(deadline); left <= time.Minute || left > refreshCeiling {
		t.Errorf("the run's deadline is %v away, want more than a minute and at most %v",
			left, refreshCeiling)
	}
}

// FR-1.3 AC3: the dashboard's "Refresh now" is a plain form, so the answer has to be a redirect
// back to the page — a 204 leaves the browser on the old page with nothing having happened.
func TestDashboardRefreshRunsAsTheUserAndReturnsToTheDashboard(t *testing.T) {
	store := newLeaseStore()
	s := newRefreshServer(t, store, &ports.FixedClock{T: testNow}, fetcherWith("github"))
	h := s.Handler()

	rec := postAs(h, "/refresh", url.Values{}, signIn(t, h))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /refresh = %d, want 303 back to the dashboard", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want %q", got, "/")
	}
	// The two endpoints run the same runner, and the run record has to distinguish who asked.
	if got := store.trigger(); got != "user" {
		t.Errorf("the run was recorded with trigger %q, want %q", got, "user")
	}
}

// FR-5.2 AC2 from the browser's side. The control is a plain form post, so a bare error status
// would show the browser's own error page instead of the site; the visitor gets the dashboard back
// with a notice, at 409.
func TestTheDashboardSaysSoWhenARefreshIsAlreadyRunning(t *testing.T) {
	block := make(chan struct{})
	f := &ports.FakeFetcher{SourceName: "github", Block: block}
	store := newLeaseStore()
	s := newRefreshServer(t, store, &ports.FixedClock{T: testNow}, f)
	h := s.Handler()
	session := signIn(t, h)

	done := make(chan struct{})
	go func() {
		defer close(done)
		postBearer(t, h, "/api/refresh", testSecret)
	}()
	waitFor(t, "the cron refresh to start fetching", func() bool { return f.CallCount() > 0 })

	rec := postAs(h, "/refresh", url.Values{}, session)

	if rec.Code != http.StatusConflict {
		t.Errorf("POST /refresh while one is running = %d, want 409 (FR-5.2 AC2)", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, busyNotice) {
		t.Errorf("the 409 page does not tell the visitor what happened:\n%s", body)
	}
	// The page is rendered rather than replaced by the browser's error page, so the visitor can
	// carry on from it.
	if body := rec.Body.String(); !strings.Contains(body, `class="tiles"`) {
		t.Error("the 409 answered with something other than the dashboard")
	}
	// QS-4.4: the notice must not need an inline style or handler, which the CSP forbids.
	for _, forbidden := range []string{"style=", " onclick="} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Errorf("the 409 page contains %q, which the CSP forbids (QS-4.4)", forbidden)
		}
	}

	close(block)
	<-done
}

// The lease is what excludes a second run, so it has to be given back — otherwise the first cron
// trigger locks every later one out for the whole lease TTL.
func TestARefreshReleasesTheLeaseSoTheNextOneCanRun(t *testing.T) {
	store := newLeaseStore()
	h := newRefreshServer(t, store, &ports.FixedClock{T: testNow}, fetcherWith("github")).Handler()

	for i := 1; i <= 3; i++ {
		if rec := postBearer(t, h, "/api/refresh", testSecret); rec.Code != http.StatusOK {
			t.Fatalf("refresh %d = %d, want 200: the previous run did not give the lease back", i, rec.Code)
		}
	}
	if held, holder := store.leaseHeld(); held {
		t.Errorf("the lease is still held by %q after the last run finished", holder)
	}
}

// ---------------------------------------------------------------- helpers

func newRefreshServer(t *testing.T, store ports.Store, clock ports.Clock, fetchers ...ports.SourceFetcher) *Server {
	t.Helper()
	return newTestServerWith(t, func(o *Options) {
		o.Store = store
		o.Clock = clock
		o.Runner = refresh.New(store, fetchers, clock, nil,
			slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
}

func fetcherWith(name string, items ...domain.Item) *ports.FakeFetcher {
	return &ports.FakeFetcher{SourceName: name, Result: ports.FetchResult{Items: items}}
}

func postBearer(t *testing.T, h http.Handler, path, secret string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	h.ServeHTTP(rec, req)
	return rec
}

func decodeAPI(t *testing.T, rec *httptest.ResponseRecorder) apiBody {
	t.Helper()
	var body apiBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

// waitFor polls cond until it holds, and fails the test if it never does. It reads no clock of its
// own: the timeout is a channel, so nothing here depends on wall-clock arithmetic.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		if cond() {
			return
		}
		select {
		case <-timeout:
			t.Fatalf("timed out waiting for %s", what)
		case <-time.After(time.Millisecond):
		}
	}
}

// deadlineFetcher records the deadline of the context its fetch was handed, which is how the
// ceiling is observed without waiting two minutes for it.
type deadlineFetcher struct {
	name string
	mu   sync.Mutex
	dl   time.Time
	ok   bool
}

func (f *deadlineFetcher) Name() string { return f.name }

func (f *deadlineFetcher) Fetch(ctx context.Context) (ports.FetchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dl, f.ok = ctx.Deadline()
	return ports.FetchResult{}, nil
}

func (f *deadlineFetcher) seen() (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dl, f.ok
}

// leaseStore is a fakeStore that actually models the refresh lease and the run record.
//
// The default fakeStore grants every lease, which would make "one refresh at a time" untestable
// here: the guarantee is the database's, and a store that always says yes cannot express it. Only
// the lease, the run record and the item writes are modelled; everything else falls through to
// fakeStore's zero values, which is all the dashboard rendering needs.
//
// Every field is behind the mutex because the concurrency tests drive two requests at once and the
// suite runs under -race.
type leaseStore struct {
	fakeStore

	mu       sync.Mutex
	holder   string
	runs     int
	lastTrig string
	started  time.Time
	ended    time.Time
	finished bool
	items    map[string]int
}

func newLeaseStore() *leaseStore { return &leaseStore{items: make(map[string]int)} }

func (s *leaseStore) AcquireRefreshLease(_ context.Context, holder string, _ time.Time, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.holder != "" && s.holder != holder {
		return false, nil
	}
	s.holder = holder
	return true, nil
}

func (s *leaseStore) ReleaseRefreshLease(_ context.Context, holder string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.holder == holder {
		s.holder = ""
	}
	return nil
}

func (s *leaseStore) StartRun(_ context.Context, trigger string, at time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs++
	s.lastTrig = trigger
	s.started = at
	s.finished = false
	return int64(s.runs), nil
}

func (s *leaseStore) FinishRun(_ context.Context, _ int64, at time.Time, _ bool, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = at
	s.finished = true
	return nil
}

func (s *leaseStore) ReplaceItems(_ context.Context, source string, items []domain.Item, _ time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[source] = len(items)
	return len(items), nil
}

func (s *leaseStore) replaced(source string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.items[source]
}

func (s *leaseStore) replacedOK(source string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.items[source]
	return n, ok
}

func (s *leaseStore) startedRuns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs
}

func (s *leaseStore) trigger() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastTrig
}

func (s *leaseStore) record() (start, end time.Time, finished bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started, s.ended, s.finished
}

func (s *leaseStore) leaseHeld() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.holder != "", s.holder
}
