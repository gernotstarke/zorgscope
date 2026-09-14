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
	// The status code is only half of what the caller is told; the body is the half it logs.
	assertBusyBody(t, second)

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

// The other half of the ceiling's context, and the reason WithoutCancel is paired with WithTimeout:
// a client hanging up must not abandon the run. cron-job.org gives up at its own timeout, well
// inside a run that is merely slow, and on the request's own context that disconnect would cancel
// the fetch of every source not yet reached — recording "context canceled" as their failure and
// rendering them on the dashboard as broken sources that were never broken (FR-1.4 AC3). On a
// Machine where the cron trigger is the only thing that ever writes, the data would then stay stale
// until the next trigger, which would be cut off at the same point.
//
// Both endpoints are driven, because the guarantee is claimed for both: the visitor who clicks
// "Refresh now" and then navigates away must no more leave half-written data and invented errors
// behind than the cron service that times out. They share runRefresh, so the claim holds by
// construction — but a construction nothing exercises is one a later edit can quietly take apart.
func TestAClientHangingUpDoesNotAbandonTheRun(t *testing.T) {
	for _, tt := range []struct {
		name, path string
		// authorize presents the credential the route's own table entry names.
		authorize func(t *testing.T, h http.Handler, req *http.Request)
	}{
		{"the cron endpoint", "/api/refresh", func(_ *testing.T, _ http.Handler, req *http.Request) {
			req.Header.Set("Authorization", "Bearer "+testSecret)
		}},
		{"the dashboard's button", "/refresh", func(t *testing.T, h http.Handler, req *http.Request) {
			t.Helper()
			req.AddCookie(signIn(t, h))
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			block := make(chan struct{})
			slow := &ports.FakeFetcher{
				SourceName: "github",
				Block:      block,
				Result:     ports.FetchResult{Items: []domain.Item{ghItem(1, "an issue", testNow)}},
			}
			later := fetcherWith("todoist", ghItem(2, "a task", testNow))
			store := newLeaseStore()
			h := newRefreshServer(t, store, &ports.FixedClock{T: testNow}, slow, later).Handler()

			ctx, hangUp := context.WithCancel(context.Background())
			req := httptest.NewRequest(http.MethodPost, tt.path, nil).WithContext(ctx)
			tt.authorize(t, h, req)
			rec := httptest.NewRecorder()

			done := make(chan struct{})
			go func() {
				defer close(done)
				h.ServeHTTP(rec, req)
			}()

			waitFor(t, "the first source to start fetching", func() bool { return slow.CallCount() > 0 })
			hangUp() // the caller's own timeout expires, or the visitor closes the tab
			close(block)
			<-done

			if got := slow.CallCount(); got != 1 {
				t.Errorf("the in-flight source was fetched %d times, want 1", got)
			}
			if got := store.replaced("github"); got != 1 {
				t.Errorf("the in-flight source stored %d items, want 1: the run it was already doing "+
					"must finish, not be thrown away because the caller left", got)
			}
			// The source that had not been reached yet is the one the request's own context would
			// have killed, and the one that would have been given a fabricated error.
			if got := later.CallCount(); got != 1 {
				t.Errorf("the source after it was fetched %d times, want 1: a client hanging up carries "+
					"no information about whether the refresh should finish", got)
			}
			if got := store.replaced("todoist"); got != 1 {
				t.Errorf("the source after it stored %d items, want 1", got)
			}
			if _, _, finished := store.record(); !finished {
				t.Error("the run record was never closed out")
			}
			if held, holder := store.leaseHeld(); held {
				t.Errorf("the lease is still held by %q after the abandoned run finished", holder)
			}
		})
	}
}

// FR-1.3 AC3: the dashboard's "Refresh now" is a plain form, so the answer has to be a redirect
// back to the page — a 204 leaves the browser on the old page with nothing having happened.
func TestDashboardRefreshRunsAsTheUserAndReturnsToTheDashboard(t *testing.T) {
	store := newLeaseStore()
	f := fetcherWith("github", ghItem(1, "an issue", testNow))
	s := newRefreshServer(t, store, &ports.FixedClock{T: testNow}, f)
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
	// "The same refresh" (FR-5.2 AC1) has to mean work was done, not merely that a run was
	// opened: a handler that recorded a run and fetched nothing would satisfy everything above.
	if got := f.CallCount(); got != 1 {
		t.Errorf("the source was fetched %d times, want 1: the button must run the same refresh "+
			"the cron endpoint does (FR-5.2 AC1)", got)
	}
	if got := store.replaced("github"); got != 1 {
		t.Errorf("the source stored %d items, want 1 (FR-5.2 AC1)", got)
	}
}

// The other order of the same collision. FR-5.2 AC2 is symmetric — the lease does not know which
// endpoint asked — but only cron-then-user was exercised, so the JSON 409 was only ever produced
// by a cron-versus-cron race. This is the order that actually happens: a visitor clicks "Refresh
// now" seconds before the cron tick lands.
func TestACronRefreshIsRefusedWhileTheUserIsRefreshing(t *testing.T) {
	block := make(chan struct{})
	f := &ports.FakeFetcher{SourceName: "github", Block: block}
	store := newLeaseStore()
	h := newRefreshServer(t, store, &ports.FixedClock{T: testNow}, f).Handler()
	session := signIn(t, h)

	done := make(chan struct{})
	go func() {
		defer close(done)
		postAs(h, "/refresh", url.Values{}, session)
	}()
	waitFor(t, "the user's refresh to start fetching", func() bool { return f.CallCount() > 0 })

	rec := postBearer(t, h, "/api/refresh", testSecret)

	if rec.Code != http.StatusConflict {
		t.Errorf("a cron refresh during the user's = %d, want 409 (FR-5.2 AC2, QS-1.7)", rec.Code)
	}
	assertBusyBody(t, rec)
	if got := f.CallCount(); got != 1 {
		t.Errorf("the source was fetched %d times, want 1: the refused refresh must not fetch", got)
	}
	if got := store.startedRuns(); got != 1 {
		t.Errorf("%d runs were recorded, want 1: the refused refresh must not open a run", got)
	}

	close(block)
	<-done
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
	if body := rec.Body.String(); !strings.Contains(body, `id="items"`) {
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

// QS-1.7. refreshCeiling and internal/refresh's leaseTTL are two constants in two packages that
// look unrelated and are not: a run holds the lease for leaseTTL, and the store hands an expired
// lease to whoever asks next. A ceiling above the TTL therefore lets a slow run keep working while
// its lease expires underneath it, the next cron trigger acquires that lease, and two runs write
// the same sources at once — with nothing to report it, because both answer 200.
//
// The TTL here is the one the runner actually asked the store for, not a copy of the literal: it
// travels as the fourth argument of AcquireRefreshLease, so this fails if either constant moves
// past the other, in either package, without anyone having to remember this test exists.
func TestTheCeilingStaysInsideTheLease(t *testing.T) {
	store := newLeaseStore()
	h := newRefreshServer(t, store, &ports.FixedClock{T: testNow}, fetcherWith("github")).Handler()

	if rec := postBearer(t, h, "/api/refresh", testSecret); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	ttl, ok := store.leaseTTLAsked()
	if !ok {
		t.Fatal("no lease was taken, so there is no TTL to hold the ceiling against")
	}
	if refreshCeiling >= ttl {
		t.Fatalf("refreshCeiling is %v and the refresh lease lasts %v: a run may outlive its own "+
			"lease, the next trigger can then acquire it, and two refreshes write the same "+
			"sources at once (QS-1.7)", refreshCeiling, ttl)
	}
}

// The ceiling firing is not a hypothetical: sources are fetched sequentially against a 20-second
// per-call timeout, and GitHub and Plausible each fan out inside one Fetch, so an ordinary
// configuration can exceed the ceiling (see refreshCeiling for the arithmetic). This pins what the
// caller and the dashboard are then told — every source the run had not reached records "context
// deadline exceeded" and shows up as a broken source that was never broken (FR-1.4 AC3), which is
// the cost of the ceiling being too small for its configuration.
func TestTheCeilingFiringIsReportedAsSuchPerSource(t *testing.T) {
	// An upstream that accepts the connection and then never answers, which is the case the
	// ceiling exists for: the block is never closed, so only the deadline ends this fetch.
	block := make(chan struct{})
	defer close(block)
	hung := &ports.FakeFetcher{SourceName: "github", Block: block}
	later := fetcherWith("todoist", ghItem(2, "a task", testNow))

	store := newLeaseStore()
	s := newRefreshServer(t, store, &ports.FixedClock{T: testNow}, hung, later)
	// The real ceiling is four minutes; the behaviour under it is the same at fifty milliseconds.
	s.ceiling = 50 * time.Millisecond
	h := s.Handler()

	rec := postBearer(t, h, "/api/refresh", testSecret)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a run cut short still reports what happened", rec.Code)
	}
	body := decodeAPI(t, rec)
	if body.OK {
		t.Error("ok = true, want false — the run did not finish its sources")
	}
	if len(body.Sources) != 2 {
		t.Fatalf("the response reports %d sources, want 2: a run cut short still reports every "+
			"source (FR-5.1 AC3)", len(body.Sources))
	}
	for _, src := range body.Sources {
		if !strings.Contains(src.Err, context.DeadlineExceeded.Error()) {
			t.Errorf("source %q reports %q, want the deadline: this is what the dashboard shows "+
				"as the source's failure when the ceiling fires", src.Source, src.Err)
		}
	}
	// The source the run never reached is fetched no more than the deadline allows, and nothing
	// half-fetched is written.
	if got := later.CallCount(); got != 0 {
		t.Errorf("the source after the hung one was fetched %d times, want 0", got)
	}
	if got, wrote := store.replacedOK("todoist"); wrote {
		t.Errorf("the unreached source stored %d items; a run cut short must write nothing for it", got)
	}
	// The run is still closed out and the lease still given back, or the next trigger is locked
	// out for the whole lease TTL by a run that timed out.
	if _, _, finished := store.record(); !finished {
		t.Error("the run record was never closed out after the ceiling fired")
	}
	if held, holder := store.leaseHeld(); held {
		t.Errorf("the lease is still held by %q after the ceiling fired", holder)
	}
}

// A panicking source frees the lease, because Runner.Run releases it in a defer — but FinishRun is
// a plain call after the fetch loop, so without help the refresh_run row stays "running" forever
// and the dashboard's last-refresh line never moves again. The caller, meanwhile, would get a
// dropped connection with no status at all: cron-job.org's history would show a failure it cannot
// describe.
func TestAPanickingSourceIsA500ThatStillClosesTheRun(t *testing.T) {
	var logged strings.Builder
	store := newLeaseStore()
	s := newTestServerWith(t, func(o *Options) {
		o.Store = store
		o.Clock = &ports.FixedClock{T: testNow}
		o.Log = slog.New(slog.NewTextHandler(&logged, nil))
		// The panic value carries the refresh secret, because a panic value carries whatever the
		// panicking code was holding — an upstream URL, a header, a DSN (QS-4.3).
		o.Runner = refresh.New(store, []ports.SourceFetcher{&panicFetcher{name: "github",
			value: "malformed payload from " + testSecret}},
			&ports.FixedClock{T: testNow}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	h := s.Handler()

	rec := postBearer(t, h, "/api/refresh", testSecret)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: a panicking handler must answer, not drop the connection", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, internalErrorNotice) {
		t.Errorf("the 500 does not tell the caller anything:\n%s", body)
	}
	// QS-4.3: this is a public surface, and neither the panic value nor the stack may reach it.
	for _, forbidden := range []string{testSecret, "malformed payload", "goroutine", ".go:", "/src/"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the 500 body contains %q, which no response may carry (QS-4.3):\n%s", forbidden, body)
		}
	}
	// The recovery sits inside securityHeaders, so even this answer carries them (QS-4.4).
	if got := rec.Header().Get("Content-Security-Policy"); got != contentSecurityPolicy {
		t.Errorf("Content-Security-Policy on the 500 = %q, want the policy", got)
	}

	// The row the runner opened and could not close.
	ok, detail, finished := store.outcome()
	if !finished {
		t.Error("the refresh_run row is still open after the panic: it stays 'running' forever " +
			"and the dashboard's last-refresh line never moves again")
	}
	if ok {
		t.Error("the run a panic ended was recorded as successful")
	}
	if detail != panicDetail {
		t.Errorf("the run's detail = %q, want %q", detail, panicDetail)
	}
	if held, holder := store.leaseHeld(); held {
		t.Errorf("the lease is still held by %q after the panic", holder)
	}

	// The log is where the diagnosis lives — scrubbed, but complete enough to be one.
	log := logged.String()
	if !strings.Contains(log, "handler panicked") {
		t.Errorf("nothing was logged about the panic:\n%s", log)
	}
	if strings.Contains(log, testSecret) {
		t.Error("the panic value reached the log with the secret still in it (QS-4.3)")
	}
	if !strings.Contains(log, "[redacted]") {
		t.Errorf("the panic value was not scrubbed, so nothing was logged to scrub:\n%s", log)
	}
	if !strings.Contains(log, "stack") {
		t.Errorf("no stack was logged, so the panic cannot be diagnosed:\n%s", log)
	}
}

// The recovery wraps the whole mux rather than the refresh endpoints, so it is not something a
// route can be registered past (QS-4.1). Driven here against a handler of its own, because no
// route in the table is supposed to be able to panic.
func TestThePanicRecoveryAnswersAnyHandlerThatPanics(t *testing.T) {
	s := newTestServer(t)

	t.Run("a panic becomes a 500 and nothing else", func(t *testing.T) {
		h := s.recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("any handler at all")
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/anything", nil))

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
		if body := rec.Body.String(); strings.Contains(body, "any handler at all") {
			t.Errorf("the panic value reached the response (QS-4.3):\n%s", body)
		}
	})

	t.Run("a response already on the wire is left alone", func(t *testing.T) {
		h := s.recoverPanics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("half a page"))
			panic("too late")
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/anything", nil))

		// The status line cannot be taken back; writing a second one would only add noise.
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want the 200 that was already sent", rec.Code)
		}
		if body := rec.Body.String(); strings.Contains(body, "too late") {
			t.Errorf("the panic value reached the response (QS-4.3):\n%s", body)
		}
	})

	t.Run("http.ErrAbortHandler is passed on", func(t *testing.T) {
		h := s.recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic(http.ErrAbortHandler)
		}))
		defer func() {
			if p := recover(); p != http.ErrAbortHandler {
				t.Errorf("recovered %v, want net/http's own abort: swallowing it would turn a "+
					"deliberate abort into a 500 and log a bug that is not one", p)
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/anything", nil))
		t.Error("the abort was swallowed")
	})
}

// The refresh endpoints are the only thing that ever writes upstream data on a Machine that scales
// to zero, so a process without a runner is a process that serves a dashboard which silently goes
// stale while every cron trigger is answered with 500. That belongs in New's required-dependency
// check, next to the store and the two credentials, rather than in a per-request branch nobody
// sees.
func TestNewRefusesAServerThatCannotRefresh(t *testing.T) {
	o := testOptions()
	o.Runner = nil

	s, err := New(o)

	if err == nil {
		t.Fatal("New accepted Options with no runner: the process would start and serve a " +
			"dashboard it can never update")
	}
	if s != nil {
		t.Error("New returned a Server alongside its error")
	}
	if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), testSecret) {
		t.Errorf("the error names a credential: %q (QS-4.3)", err)
	}
	// And with the check in New, the per-request branch that used to answer "no refresh runner is
	// configured" has nothing left to guard: every Server that exists has a runner.
	if newTestServer(t).runner == nil {
		t.Error("New built a Server with no runner")
	}
}

// The report's "sources" is documented as never null, because a caller reading "which sources
// failed" should find a list, empty or otherwise. The empty configuration is the one input that
// tells the two apart: with make() it is [], with a var declaration it is null, and a consumer
// doing sources.length breaks on precisely the deployment that has nothing configured yet.
func TestAReportWithNoSourcesStillCarriesAList(t *testing.T) {
	h := newRefreshServer(t, newLeaseStore(), &ports.FixedClock{T: testNow}).Handler()

	rec := postBearer(t, h, "/api/refresh", testSecret)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a configuration with no sources is not an error", rec.Code)
	}
	raw := rec.Body.String()
	if !strings.Contains(raw, `"sources":[]`) {
		t.Errorf("the body does not carry an empty list of sources:\n%s", raw)
	}
	var body struct {
		Sources []apiSource `json:"sources"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Sources == nil {
		t.Error("sources decoded to null, not to an empty list")
	}
}

// The 500 of a JSON endpoint has to be JSON. A caller that decodes the body — which is the only
// kind of caller this endpoint has — otherwise meets a syntax error exactly when the one response
// with something to tell it arrives.
func TestAFailedRefreshAnswersTheJSONCallerInJSON(t *testing.T) {
	store := newLeaseStore()
	// A store error of the shape the libSQL driver produces: it quotes the DSN, and the DSN
	// carries the token.
	store.failAcquire(errors.New("libsql: dial libsql://db.example?authToken=" + testSecret))
	h := newRefreshServer(t, store, &ports.FixedClock{T: testNow}, fetcherWith("github")).Handler()

	rec := postBearer(t, h, "/api/refresh", testSecret)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	var body apiError
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("the 500 body is not decodable JSON: %v", err)
	}
	if body.OK {
		t.Error("ok = true on a failed refresh")
	}
	if body.Error != internalErrorNotice {
		t.Errorf("error = %q, want the fixed notice: the driver's own text may quote the DSN "+
			"(QS-4.3)", body.Error)
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

// assertBusyBody pins the whole 409 answer, not only its status: the body is what a cron service
// records, and "already running" is the one outcome it must be able to tell apart from a failure.
func assertBusyBody(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("the 409 Content-Type = %q, want application/json", ct)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("the 409 Cache-Control = %q, want no-store", got)
	}
	var body apiError
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("the 409 body is not decodable JSON: %v", err)
	}
	if body.OK {
		t.Error("the 409 says ok = true; a refused refresh did not happen")
	}
	if body.Error != refresh.ErrBusy.Error() {
		t.Errorf("the 409 says %q, want %q", body.Error, refresh.ErrBusy.Error())
	}
}

// panicFetcher is a source that panics instead of returning, the way an adapter meeting a
// malformed upstream payload can.
type panicFetcher struct{ name, value string }

func (f *panicFetcher) Name() string { return f.name }

func (f *panicFetcher) Fetch(context.Context) (ports.FetchResult, error) { panic(f.value) }

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
// ceiling is observed without waiting the whole four minutes out.
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
	runOK    bool
	detail   string
	items    map[string]int
	// ttl is the lease TTL the runner asked for, which is internal/refresh's own leaseTTL
	// travelling through the port. It is how the ceiling can be held against the real value
	// rather than against a copy of the literal.
	ttl    time.Duration
	tookIt bool
	// acquireErr, when set, makes taking the lease fail — the shape of a store that cannot be
	// reached at all.
	acquireErr error
}

func newLeaseStore() *leaseStore { return &leaseStore{items: make(map[string]int)} }

func (s *leaseStore) failAcquire(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquireErr = err
}

func (s *leaseStore) AcquireRefreshLease(_ context.Context, holder string, _ time.Time, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.acquireErr != nil {
		return false, s.acquireErr
	}
	s.ttl, s.tookIt = ttl, true
	if s.holder != "" && s.holder != holder {
		return false, nil
	}
	s.holder = holder
	return true, nil
}

func (s *leaseStore) leaseTTLAsked() (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ttl, s.tookIt
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

func (s *leaseStore) FinishRun(_ context.Context, _ int64, at time.Time, ok bool, detail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = at
	s.finished = true
	s.runOK = ok
	s.detail = detail
	return nil
}

// LastRun models the row as it stands: FinishedAt is zero while the run is open, which is how a
// run nobody closed is told from one that closed itself.
func (s *leaseStore) LastRun(context.Context) (domain.RefreshRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs == 0 {
		return domain.RefreshRun{}, nil
	}
	run := domain.RefreshRun{
		ID:        int64(s.runs),
		StartedAt: s.started,
		Trigger:   s.lastTrig,
	}
	if s.finished {
		run.FinishedAt, run.OK, run.Detail = s.ended, s.runOK, s.detail
	}
	return run, nil
}

func (s *leaseStore) outcome() (ok bool, detail string, finished bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runOK, s.detail, s.finished
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

// FR-5.2 AC2 and FR-1.1 AC3 meeting on the one page where the contradiction was guaranteed. This
// page is reached only while another run holds the lease — that is what it is for — so the run the
// header reads is always the open one, and reading its finishing time alone made every 409 say
// that no refresh had ever run, directly under a notice saying one was running right now.
func TestTheBusyPageDoesNotClaimThatNoRefreshHasEverRun(t *testing.T) {
	block := make(chan struct{})
	f := &ports.FakeFetcher{SourceName: "github", Block: block}
	store := newLeaseStore()
	h := newRefreshServer(t, store, &ports.FixedClock{T: testNow}, f).Handler()
	session := signIn(t, h)

	done := make(chan struct{})
	go func() {
		defer close(done)
		postBearer(t, h, "/api/refresh", testSecret)
	}()
	waitFor(t, "the cron refresh to start fetching", func() bool { return f.CallCount() > 0 })

	body := postAs(h, "/refresh", url.Values{}, session).Body.String()

	if strings.Contains(body, "No refresh has run yet") {
		t.Errorf("the 409 page says no refresh has ever run while telling the visitor one is "+
			"running (FR-1.1 AC3):\n%s", body)
	}
	if !strings.Contains(body, "Refreshing now") {
		t.Errorf("the 409 page does not report the run that is holding the lease:\n%s", body)
	}

	close(block)
	<-done
}
