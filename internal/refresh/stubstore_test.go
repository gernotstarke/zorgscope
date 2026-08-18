// The tests in this file drive the runner against a stub store instead of the real one. The suite
// in runner_test.go deliberately uses the live libSQL store, because the runner's contract is
// transactional and a fake would prove none of it — but a healthy store never fails on demand, so
// the store's error branches, the wiring preconditions and the QS-4.3 canary have to be driven
// from a stub. Nothing here asserts anything about persistence; that stays in runner_test.go.
package refresh_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/refresh"
)

// stubStore answers the calls one run makes and can be told to fail any of them.
//
// ports.Store is embedded rather than implemented in full: a method the runner is not expected to
// call is nil, so calling it panics the test rather than passing silently.
type stubStore struct {
	ports.Store

	// Failures to inject.
	acquireErr  error
	leaseHeld   bool // AcquireRefreshLease reports the lease as already taken
	startErr    error
	replaceErr  map[string]error // by source name
	recordOKErr error

	// What the run did, for the assertions.
	holder     string
	released   bool
	stored     map[string]int
	failures   map[string]string
	finished   bool
	finishedOK bool
	detail     string

	// ctxAt records the state of the context each cleanup write was called with, at the moment it
	// was called: afterwards the runner has cancelled it, so it can only be judged from inside.
	ctxAt map[string]ctxState

	// AuthToken stands in for a credential the store holds. The runner has a reference to the
	// store, so a log line or an error that rendered the store itself would expose it; nothing
	// may. See TestNoSecretShapedValueReachesTheLogOrTheReport.
	AuthToken string
}

func newStubStore() *stubStore {
	return &stubStore{
		replaceErr: map[string]error{},
		stored:     map[string]int{},
		failures:   map[string]string{},
		ctxAt:      map[string]ctxState{},
		AuthToken:  canarySecret,
	}
}

// ctxState is what a store call can tell about the context it was handed.
type ctxState struct {
	live        bool // not already cancelled or expired
	hasDeadline bool
	budget      time.Duration
}

func (s *stubStore) note(name string, ctx context.Context) {
	st := ctxState{live: ctx.Err() == nil}
	if d, ok := ctx.Deadline(); ok {
		st.hasDeadline, st.budget = true, time.Until(d)
	}
	s.ctxAt[name] = st
}

func (s *stubStore) AcquireRefreshLease(_ context.Context, holder string, _ time.Time, _ time.Duration) (bool, error) {
	if s.acquireErr != nil {
		return false, s.acquireErr
	}
	if s.leaseHeld {
		return false, nil
	}
	s.holder = holder
	return true, nil
}

func (s *stubStore) ReleaseRefreshLease(ctx context.Context, holder string) error {
	s.note("ReleaseRefreshLease", ctx)
	if holder != s.holder {
		return fmt.Errorf("released by %q, held by %q", holder, s.holder)
	}
	s.released = true
	return nil
}

func (s *stubStore) StartRun(_ context.Context, _ string, _ time.Time) (int64, error) {
	if s.startErr != nil {
		return 0, s.startErr
	}
	return 7, nil
}

func (s *stubStore) FinishRun(ctx context.Context, _ int64, _ time.Time, ok bool, detail string) error {
	s.note("FinishRun", ctx)
	s.finished, s.finishedOK, s.detail = true, ok, detail
	return nil
}

func (s *stubStore) ReplaceItems(_ context.Context, source string, items []domain.Item, _ time.Time) (int, error) {
	if err := s.replaceErr[source]; err != nil {
		return 0, err
	}
	s.stored[source] = len(items)
	return len(items), nil
}

func (s *stubStore) UpsertBuilds(context.Context, []domain.Build, time.Time) error   { return nil }
func (s *stubStore) UpsertMetrics(context.Context, []domain.Metric, time.Time) error { return nil }

func (s *stubStore) RecordSourceOK(_ context.Context, _ string, _ time.Time, _ int) error {
	return s.recordOKErr
}

func (s *stubStore) RecordSourceError(ctx context.Context, source string, _ time.Time, msg string) error {
	s.note("RecordSourceError", ctx)
	s.failures[source] = msg
	return nil
}

// FR-5.1 AC4 for the store side of a source: a write that fails must fail its own source and
// nothing else, and must be reported rather than counted as a success. Nothing exercised this
// before, so a runner that ignored ReplaceItems' error and reported the source as OK passed.
func TestStoreFailureFailsOnlyItsSourceAndIsReported(t *testing.T) {
	store := newStubStore()
	store.replaceErr["github"] = errors.New("libsql: write failed")
	r := refresh.New(store, []ports.SourceFetcher{
		fetcher("github", item("1")),
		fetcher("todoist", task("t1")),
	}, &ports.FixedClock{T: now}, nil, discardLogger())

	rep, err := r.Run(context.Background(), "cron")
	if err != nil {
		t.Fatalf("Run must not fail because one source's write did: %v", err)
	}
	if rep.OK {
		t.Error("report.OK must be false when a store write failed")
	}
	if rep.Sources[0].Stored != 0 {
		t.Errorf("failed source reported %d stored, want 0 — a failed write stored nothing",
			rep.Sources[0].Stored)
	}
	if want := "store items: libsql: write failed"; rep.Sources[0].Err != want {
		t.Errorf("failed source Err = %q, want %q", rep.Sources[0].Err, want)
	}
	if store.failures["github"] != rep.Sources[0].Err {
		t.Errorf("recorded source error = %q, want the reported one %q (FR-1.4 AC2)",
			store.failures["github"], rep.Sources[0].Err)
	}
	if _, ok := store.stored["github"]; ok {
		t.Error("the failing source must not have been recorded as stored")
	}

	if rep.Sources[1].Err != "" || store.stored["todoist"] != 1 {
		t.Errorf("healthy source = %+v, stored %d — one source's failure must not touch the others",
			rep.Sources[1], store.stored["todoist"])
	}
	if !store.finished || store.finishedOK {
		t.Errorf("run record finished = %v, ok = %v; want a finished run marked not ok",
			store.finished, store.finishedOK)
	}
	if !store.released {
		t.Error("the run must release its lease even when a source's write failed")
	}
}

// The writes that close a run out — recording a source's failure, finishing the run record,
// releasing the lease — run on a context that outlives the run's cancellation and still carries a
// deadline. Both halves matter and each protects against the other's fix:
//
//   - Without context.WithoutCancel a cancelled run cannot say why it stopped, cannot close its run
//     record and cannot free its lease, so one cancellation locks refreshes out for the whole
//     leaseTTL.
//   - Without context.WithTimeout those same writes have no deadline at all, so a half-open
//     database makes them the one part of the run that can block forever — holding the POST
//     /api/refresh request in flight, and with it the Machine that Fly will not stop while a
//     request is in flight (QS-2.5).
//
// The state of each context has to be captured inside the store call: the runner cancels it as soon
// as the call returns, so afterwards every one of them looks cancelled.
func TestCleanupWritesOutliveCancellationButKeepADeadline(t *testing.T) {
	store := newStubStore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The fetcher cancels the run from the inside, which is what a client hanging up or a Machine
	// being stopped looks like from between two sources.
	r := refresh.New(store, []ports.SourceFetcher{
		&cancelThenFetch{FakeFetcher: fetcher("github", item("1")), cancel: cancel},
	}, &ports.FixedClock{T: now}, nil, discardLogger())

	if _, err := r.Run(ctx, "cron"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, name := range []string{"RecordSourceError", "FinishRun", "ReleaseRefreshLease"} {
		got, called := store.ctxAt[name]
		if !called {
			t.Errorf("%s was never called; a cancelled run must still close itself out", name)
			continue
		}
		if !got.live {
			t.Errorf("%s ran on a cancelled context (context.WithoutCancel is missing): "+
				"a cancelled run cannot then free its lease or record why it stopped", name)
		}
		if !got.hasDeadline {
			t.Errorf("%s ran on a context with no deadline (context.WithTimeout is missing): "+
				"an unresponsive database would block it forever and hold the Machine awake", name)
			continue
		}
		if got.budget <= 0 || got.budget > time.Minute {
			t.Errorf("%s had %v left, want a small positive budget", name, got.budget)
		}
	}
}

// canarySecret is shaped like a credential: what a leaked auth token would look like in a log line
// or on the dashboard. It is not a real secret and grants nothing.
const canarySecret = "zs_live_9f3c1a7b2e5d4c8a0b6f2e1d7c3a9b45" //nolint:gosec // a canary, not a credential

// tokenCarryingFetcher is a fetcher that holds a credential, as every real one does — the GitHub
// and Todoist adapters carry their tokens in a field and send them in a header.
type tokenCarryingFetcher struct {
	*ports.FakeFetcher
	AuthToken string
}

// QS-4.3: no secret value may be logged or rendered. This package is the chokepoint where upstream
// and store error text becomes persisted, rendered content — Report.Sources[].Err reaches the
// dashboard and source_state.last_error reaches the database — so it is where a leak would become
// permanent.
//
// The adapters are the first layer: they keep credentials in headers and out of their error text.
// This is the second: whatever the adapters hand over, the runner must add nothing of its own. It
// holds a reference to every fetcher and to the store, both of which carry credentials, so a log
// line or a message that rendered one of those values — the obvious shape of a well-meaning "more
// diagnostics" change — would publish it. What the runner emits about a failure must be derived
// from the error text alone.
func TestNoSecretShapedValueReachesTheLogOrTheReport(t *testing.T) {
	var logged strings.Builder
	store := newStubStore()
	store.replaceErr["todoist"] = errors.New("libsql: exec: connection refused")
	failing := &tokenCarryingFetcher{
		FakeFetcher: &ports.FakeFetcher{SourceName: "github", Err: errors.New("upstream returned 500")},
		AuthToken:   canarySecret,
	}
	storing := &tokenCarryingFetcher{
		FakeFetcher: &ports.FakeFetcher{SourceName: "todoist", Result: ports.FetchResult{Items: []domain.Item{task("t1")}}},
		AuthToken:   canarySecret,
	}
	r := refresh.New(store, []ports.SourceFetcher{failing, storing}, &ports.FixedClock{T: now}, nil,
		slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))

	rep, err := r.Run(context.Background(), "cron")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	surfaces := map[string]string{
		"the log":               logged.String(),
		"the returned report":   fmt.Sprintf("%+v", rep),
		"the recorded failures": fmt.Sprintf("%+v", store.failures),
		"the run detail":        store.detail,
	}
	for name, text := range surfaces {
		if strings.Contains(text, canarySecret) {
			t.Errorf("%s contains a secret-shaped value (QS-4.3): %s", name, text)
		}
		if text == "" {
			t.Errorf("%s is empty; the check above proves nothing", name)
		}
	}

	// The failure text is the error's and nothing else: a prefix naming the step, no more.
	if want := "fetch: upstream returned 500"; rep.Sources[0].Err != want {
		t.Errorf("fetch failure = %q, want exactly %q — the runner must add nothing of its own",
			rep.Sources[0].Err, want)
	}
	if want := "store items: libsql: exec: connection refused"; rep.Sources[1].Err != want {
		t.Errorf("store failure = %q, want exactly %q", rep.Sources[1].Err, want)
	}
}

// A long error is clipped before it is reported and recorded, not only inside clip: an upstream
// answering with a page of HTML must not fill the database or the dashboard.
func TestALongSourceErrorIsClippedBeforeItIsStored(t *testing.T) {
	store := newStubStore()
	r := refresh.New(store, []ports.SourceFetcher{
		&ports.FakeFetcher{SourceName: "github", Err: errors.New(strings.Repeat("a", 5000))},
	}, &ports.FixedClock{T: now}, nil, discardLogger())

	rep, err := r.Run(context.Background(), "cron")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(rep.Sources[0].Err); got > 512 {
		t.Errorf("reported error is %d bytes, want it clipped to roughly 500", got)
	}
	if got := len(store.failures["github"]); got > 512 {
		t.Errorf("recorded error is %d bytes, want it clipped to roughly 500", got)
	}
	if !strings.HasSuffix(rep.Sources[0].Err, "…") {
		t.Error("a clipped error must say it was clipped")
	}
}

// FR-5.1 AC4's other half: Run returns a non-nil error only when the lease or the run record
// failed, i.e. when nothing was refreshed at all.
func TestRunFailsWhenTheLeaseCannotBeTaken(t *testing.T) {
	store := newStubStore()
	store.acquireErr = errors.New("libsql: exec: connection refused")
	r := refresh.New(store, []ports.SourceFetcher{fetcher("github", item("1"))},
		&ports.FixedClock{T: now}, nil, discardLogger())

	if _, err := r.Run(context.Background(), "cron"); err == nil {
		t.Fatal("Run must fail when the lease cannot be taken; nothing was refreshed")
	}
	if len(store.stored) != 0 {
		t.Errorf("stored %+v, want nothing: the run never started", store.stored)
	}
}

func TestRunFailsWhenTheRunRecordCannotBeStarted(t *testing.T) {
	store := newStubStore()
	store.startErr = errors.New("libsql: exec: connection refused")
	r := refresh.New(store, []ports.SourceFetcher{fetcher("github", item("1"))},
		&ports.FixedClock{T: now}, nil, discardLogger())

	if _, err := r.Run(context.Background(), "cron"); err == nil {
		t.Fatal("Run must fail when the run record cannot be started")
	}
	if len(store.stored) != 0 {
		t.Errorf("stored %+v, want nothing: no source runs without a run record", store.stored)
	}
	if !store.released {
		t.Error("a run that could not start must still release the lease it took")
	}
}

// A run that finds the lease held is busy, not broken, and must leave the holder's lease alone.
func TestRunIsBusyWhenTheLeaseIsHeld(t *testing.T) {
	store := newStubStore()
	store.leaseHeld = true
	r := refresh.New(store, []ports.SourceFetcher{fetcher("github", item("1"))},
		&ports.FixedClock{T: now}, nil, discardLogger())

	if _, err := r.Run(context.Background(), "cron"); !errors.Is(err, refresh.ErrBusy) {
		t.Fatalf("Run err = %v, want ErrBusy", err)
	}
	if store.released {
		t.Error("a busy run must not release the lease it never took")
	}
}

// New's precondition: one fetcher per source name. It is load-bearing — a source owns its rows and
// ReplaceItems deletes the rows the incoming set omits, so a second fetcher under the same name
// (say a builds fetcher called "github") deletes every item of the first on every refresh and the
// dashboard lights up NEW each time it recovers (QS-1.2). Wiring it wrong is a programmer error, so
// New panics on the spot rather than building a Runner that quietly destroys data.
func TestNewRejectsTwoFetchersSharingASourceName(t *testing.T) {
	defer func() {
		got := recover()
		if got == nil {
			t.Fatal("New accepted two fetchers under one source name; each refresh would delete the other's items")
		}
		if msg := fmt.Sprint(got); !strings.Contains(msg, "github") {
			t.Errorf("panic message = %q, want it to name the colliding source", msg)
		}
	}()

	// The second is a builds-only fetcher, which is exactly how this happens: it is GitHub, so it
	// gets called "github", and it returns no items — so it would replace GitHub's items with none.
	refresh.New(newStubStore(), []ports.SourceFetcher{
		fetcher("github", item("1")),
		&ports.FakeFetcher{SourceName: "github", Result: ports.FetchResult{
			Builds: []domain.Build{{Repo: "org/repo"}},
		}},
	}, &ports.FixedClock{T: now}, nil, discardLogger())
}

func TestNewAcceptsDistinctSourceNamesAndANilLogger(t *testing.T) {
	store := newStubStore()
	r := refresh.New(store, []ports.SourceFetcher{
		fetcher("github", item("1")),
		&ports.FakeFetcher{SourceName: "github-builds", Result: ports.FetchResult{
			Builds: []domain.Build{{Repo: "org/repo"}},
		}},
	}, &ports.FixedClock{T: now}, nil, nil) // nil logger: log output is discarded, not a crash

	rep, err := r.Run(context.Background(), "cron")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.OK || len(rep.Sources) != 2 {
		t.Errorf("report = %+v, want both sources fine", rep)
	}
}
