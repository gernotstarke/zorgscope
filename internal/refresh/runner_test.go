// The refresh runner is tested against the real libSQL store, not a fake one: its contract is
// transactional — a partial result must not replace stored items, an earlier source must survive a
// later cancellation, one refresh at a time must hold in the database — and a fake store would
// prove none of that. The suite therefore skips itself unless TEST_TURSO_URL is set, exactly as
// the store's own tests do; `make test` sets it.
package refresh_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/libsql"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/refresh"
)

// now is the fixed time every test's clock starts at.
var now = at("2026-08-17T10:00:00Z")

func at(s string) time.Time {
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return ts
}

func newTestStore(t *testing.T) *libsql.Store {
	t.Helper()
	url := os.Getenv("TEST_TURSO_URL")
	if url == "" {
		t.Skip("TEST_TURSO_URL not set; run via `make test`")
	}
	s, err := libsql.Open(url, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := s.TruncateAll(context.Background()); err != nil {
		t.Fatalf("TruncateAll: %v", err)
	}
	return s
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func item(id string) domain.Item {
	return domain.Item{
		Source: "github", ExternalID: id, Kind: domain.KindIssue,
		Repo: "org/repo", Number: 1, Title: "issue " + id, URL: "https://example/" + id,
		Author: "someone", State: "open",
		CreatedAt: at("2026-08-01T00:00:00Z"), UpdatedAt: at("2026-08-10T00:00:00Z"),
	}
}

func task(id string) domain.Item {
	return domain.Item{
		Source: "todoist", ExternalID: id, Kind: domain.KindTask,
		Title: "task " + id, URL: "https://todoist/" + id, State: "open",
		CreatedAt: at("2026-08-01T00:00:00Z"), UpdatedAt: at("2026-08-11T00:00:00Z"),
	}
}

func fetcher(name string, items ...domain.Item) *ports.FakeFetcher {
	return &ports.FakeFetcher{SourceName: name, Result: ports.FetchResult{Items: items}}
}

// FR-5.4
func TestRunStoresEverySourceAndRecordsTheRun(t *testing.T) {
	store, ctx := newTestStore(t), context.Background()
	fetchers := []ports.SourceFetcher{
		fetcher("github", item("1")),
		fetcher("todoist", task("t1")),
	}
	r := refresh.New(store, fetchers, &ports.FixedClock{T: now}, nil, discardLogger())

	rep, err := r.Run(ctx, "cron")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.OK || len(rep.Sources) != 2 {
		t.Fatalf("report = %+v, want OK with two sources", rep)
	}
	if rep.Sources[0].Source != "github" || rep.Sources[0].Stored != 1 {
		t.Errorf("first source report = %+v, want github with 1 item stored", rep.Sources[0])
	}

	if got := mustItems(t, store, ctx); len(got) != 2 {
		t.Errorf("len(items) = %d, want 2 — both sources must be stored", len(got))
	}
	states, err := store.SourceStates(ctx)
	if err != nil {
		t.Fatalf("SourceStates: %v", err)
	}
	for _, s := range []string{"github", "todoist"} {
		if !states[s].LastSuccessAt.Equal(now) || states[s].LastError != "" {
			t.Errorf("source state %s = %+v, want a clean success at %v", s, states[s], now)
		}
	}

	last, err := store.LastRun(ctx)
	if err != nil {
		t.Fatalf("LastRun: %v", err)
	}
	if last.Trigger != "cron" || !last.OK {
		t.Errorf("LastRun = %+v, want a successful cron run (FR-5.4)", last)
	}
	if last.ID != rep.RunID {
		t.Errorf("LastRun.ID = %d, want the reported run id %d", last.ID, rep.RunID)
	}
}

// FR-5.1 AC4 / QS-1.4: one failing source must not stop the others, and must not fail the run.
func TestOneFailingSourceDoesNotStopTheOthers(t *testing.T) {
	store, ctx := newTestStore(t), context.Background()
	fetchers := []ports.SourceFetcher{
		&ports.FakeFetcher{SourceName: "github", Err: errors.New("boom")},
		fetcher("todoist", task("t1")),
	}
	r := refresh.New(store, fetchers, &ports.FixedClock{T: now}, nil, discardLogger())

	rep, err := r.Run(ctx, "cron")
	if err != nil {
		t.Fatalf("Run must not fail because one source did: %v", err)
	}
	if rep.OK {
		t.Error("report.OK must be false when a source failed")
	}

	items := mustItems(t, store, ctx)
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1 — the healthy source must be stored", len(items))
	}
	states, err := store.SourceStates(ctx)
	if err != nil {
		t.Fatalf("SourceStates: %v", err)
	}
	if states["github"].LastError == "" {
		t.Error("the failing source must record its error (FR-1.4 AC2)")
	}
	if !states["todoist"].LastSuccessAt.Equal(now) {
		t.Error("the healthy source must record its success")
	}
	last, err := store.LastRun(ctx)
	if err != nil {
		t.Fatalf("LastRun: %v", err)
	}
	if last.OK || last.FinishedAt.IsZero() {
		t.Errorf("LastRun = %+v, want a finished run marked not ok", last)
	}
}

// QS-1.4: the rule the whole package exists to protect. A fetcher may hand back the items it did
// get together with an error — the GitHub adapter does, when one repository of several fails.
// Storing that would delete the failed repository's items and re-insert them with a fresh
// first_seen_at when it recovered, manufacturing a screen of false NEW badges (QS-1.2).
func TestPartialResultIsNotStored(t *testing.T) {
	store, ctx := newTestStore(t), context.Background()
	clock := &ports.FixedClock{T: now}

	good := refresh.New(store, []ports.SourceFetcher{fetcher("github", item("1"), item("2"))}, clock, nil, discardLogger())
	if _, err := good.Run(ctx, "cron"); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	clock.Advance(time.Hour)
	partial := &ports.FakeFetcher{
		SourceName: "github",
		Result:     ports.FetchResult{Items: []domain.Item{item("1")}}, // repo two errored
		Err:        errors.New("list org/two: 500"),
	}
	r := refresh.New(store, []ports.SourceFetcher{partial}, clock, nil, discardLogger())

	rep, err := r.Run(ctx, "cron")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.OK || rep.Sources[0].Stored != 0 {
		t.Errorf("report = %+v, want a failed source that stored nothing", rep.Sources[0])
	}

	items := mustItems(t, store, ctx)
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2 — a partial result must never replace the stored set", len(items))
	}
	for _, it := range items {
		if !it.FirstSeenAt.Equal(now) {
			t.Errorf("item %s FirstSeenAt = %v, want %v — the previous data must survive untouched (FR-1.4 AC3)",
				it.ExternalID, it.FirstSeenAt, now)
		}
	}
	states, err := store.SourceStates(ctx)
	if err != nil {
		t.Fatalf("SourceStates: %v", err)
	}
	if states["github"].LastError == "" {
		t.Error("the partial failure must be recorded as an error (FR-1.4 AC2)")
	}
	if !states["github"].LastSuccessAt.Equal(now) {
		t.Error("the error must not erase when the source last succeeded")
	}
}

// QS-1.7: one refresh at a time, enforced in the database so the guarantee survives a restart.
func TestConcurrentRunsAreRejected(t *testing.T) {
	store := newTestStore(t)
	block := make(chan struct{})
	r := refresh.New(store, []ports.SourceFetcher{
		&ports.FakeFetcher{SourceName: "github", Block: block},
	}, &ports.FixedClock{T: now}, nil, discardLogger())

	done := make(chan error, 1)
	go func() { _, err := r.Run(context.Background(), "cron"); done <- err }()
	waitUntilLeaseTaken(t, store)

	if _, err := r.Run(context.Background(), "user"); !errors.Is(err, refresh.ErrBusy) {
		t.Fatalf("second run err = %v, want ErrBusy", err)
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatalf("first run: %v", err)
	}

	// The finished run released its lease, so the next trigger gets through.
	if _, err := r.Run(context.Background(), "cron"); err != nil {
		t.Fatalf("run after the first finished: %v — the lease was not released", err)
	}
}

// QS-1.7 again, for the case the test above cannot see: two runs of the *same* trigger. The holder
// is what the lease is keyed by, and AcquireRefreshLease deliberately admits the current holder
// again (a run that lost its answer must be able to carry on), so two runs sharing a holder name
// would both believe they won and whichever finished first would delete the lease out from under
// the other. Trigger and start time alone do not separate them — a fixed clock, or two cron
// triggers landing in the same nanosecond, produce the same string — which is why the holder
// carries a random suffix. Without it this test acquires twice and no test notices.
func TestTwoRunsOfTheSameTriggerAtTheSameInstantStillExcludeEachOther(t *testing.T) {
	store := newTestStore(t)
	clock := &ports.FixedClock{T: now} // both runs read the same start time
	block := make(chan struct{})
	first := refresh.New(store, []ports.SourceFetcher{
		&ports.FakeFetcher{SourceName: "github", Block: block},
	}, clock, nil, discardLogger())

	done := make(chan error, 1)
	go func() { _, err := first.Run(context.Background(), "cron"); done <- err }()
	waitUntilLeaseTaken(t, store)

	second := refresh.New(store, []ports.SourceFetcher{fetcher("github", item("1"))},
		clock, nil, discardLogger())
	if _, err := second.Run(context.Background(), "cron"); !errors.Is(err, refresh.ErrBusy) {
		t.Errorf("second run of the same trigger err = %v, want ErrBusy — the two runs shared a lease holder", err)
	}

	close(block)
	if err := <-done; err != nil {
		t.Fatalf("first run: %v", err)
	}
}

// waitUntilLeaseTaken blocks until the refresh lease is held, or fails the test after a bounded
// wait so a regression fails instead of hanging the suite.
//
// It probes by trying to acquire the lease itself, under a holder name of its own and with a
// negative TTL: the lease it would write has already expired when it is written, so a probe that
// wins the race against the runner does not keep the runner out. A probe that loses is the signal
// we are waiting for — the runner holds a live lease.
func waitUntilLeaseTaken(t *testing.T, s *libsql.Store) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		free, err := s.AcquireRefreshLease(ctx, "lease-probe", now, -time.Second)
		if err != nil {
			t.Fatalf("probe AcquireRefreshLease: %v", err)
		}
		if !free {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the refresh lease was never taken; the run did not acquire it")
}

// cancelThenFetch cancels the run's context before delegating, which is what a Machine being
// stopped or a client hanging up looks like from between two sources.
type cancelThenFetch struct {
	*ports.FakeFetcher
	cancel context.CancelFunc
}

func (f *cancelThenFetch) Fetch(ctx context.Context) (ports.FetchResult, error) {
	f.cancel()
	return f.FakeFetcher.Fetch(ctx)
}

// FR-5.5 AC2: every source is its own transaction, so a run that dies half way through keeps what
// the earlier sources already stored — and still records why the later ones have no fresh data.
func TestCancelledRunLeavesEarlierSourcesStored(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	second := &cancelThenFetch{FakeFetcher: fetcher("todoist", task("t1")), cancel: cancel}
	r := refresh.New(store, []ports.SourceFetcher{
		fetcher("github", item("1")),
		second,
	}, &ports.FixedClock{T: now}, nil, discardLogger())

	rep, err := r.Run(ctx, "cron")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.OK {
		t.Error("report.OK must be false when a source was cancelled")
	}
	if len(rep.Sources) != 2 {
		t.Fatalf("len(rep.Sources) = %d, want 2 — the cancelled source must still be reported", len(rep.Sources))
	}
	if rep.Sources[1].Err == "" {
		t.Error("the cancelled source must report an error")
	}

	// The reads below use a fresh context: the run's is cancelled.
	read := context.Background()
	items := mustItems(t, store, read)
	if len(items) != 1 || items[0].Source != "github" {
		t.Fatalf("items = %+v, want only the first source's item — its transaction had committed", items)
	}
	states, err := store.SourceStates(read)
	if err != nil {
		t.Fatalf("SourceStates: %v", err)
	}
	if !states["github"].LastSuccessAt.Equal(now) {
		t.Errorf("github state = %+v, want the success recorded before the cancellation", states["github"])
	}
	if states["todoist"].LastError == "" {
		t.Error("the cancelled source must have its failure recorded, not silently skipped")
	}

	// A cancelled run must still free its lease and close out its run record, or one cancellation
	// would lock refreshes out for the whole lease TTL.
	free, err := store.AcquireRefreshLease(read, "after", now.Add(time.Second), time.Minute)
	if err != nil {
		t.Fatalf("AcquireRefreshLease: %v", err)
	}
	if !free {
		t.Error("the cancelled run did not release its lease (context.WithoutCancel)")
	}
	last, err := store.LastRun(read)
	if err != nil {
		t.Fatalf("LastRun: %v", err)
	}
	if last.FinishedAt.IsZero() || last.OK {
		t.Errorf("LastRun = %+v, want a finished run marked not ok", last)
	}
}

// advancingFetcher moves the test clock forward while it fetches, standing in for a source that
// takes real time.
type advancingFetcher struct {
	*ports.FakeFetcher
	clock *ports.FixedClock
	by    time.Duration
}

func (f *advancingFetcher) Fetch(ctx context.Context) (ports.FetchResult, error) {
	f.clock.Advance(f.by)
	return f.FakeFetcher.Fetch(ctx)
}

// QS-2.5: a run's duration is observable, and a finished run says when it finished.
func TestReportCarriesDuration(t *testing.T) {
	store, ctx := newTestStore(t), context.Background()
	clock := &ports.FixedClock{T: now}
	fetchers := []ports.SourceFetcher{
		&advancingFetcher{FakeFetcher: fetcher("github", item("1")), clock: clock, by: 3 * time.Second},
		&advancingFetcher{FakeFetcher: fetcher("todoist", task("t1")), clock: clock, by: 4 * time.Second},
	}
	r := refresh.New(store, fetchers, clock, nil, discardLogger())

	rep, err := r.Run(ctx, "cron")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := rep.EndedAt.Sub(rep.StartedAt), 7*time.Second; got != want {
		t.Errorf("run duration = %v, want %v", got, want)
	}
	if !rep.StartedAt.Equal(now) {
		t.Errorf("StartedAt = %v, want the clock's time at the start, %v", rep.StartedAt, now)
	}

	last, err := store.LastRun(ctx)
	if err != nil {
		t.Fatalf("LastRun: %v", err)
	}
	if last.FinishedAt.IsZero() {
		t.Fatal("the persisted run has no FinishedAt; a finished run must record when it ended")
	}
	if !last.StartedAt.Equal(rep.StartedAt) || !last.FinishedAt.Equal(rep.EndedAt) {
		t.Errorf("persisted run ran %v–%v, want %v–%v",
			last.StartedAt, last.FinishedAt, rep.StartedAt, rep.EndedAt)
	}
	if last.Detail == "" {
		t.Error("the persisted run has no detail; the per-source outcome must be recorded")
	}
}

// FR-5.5 AC1: the run stores builds and metrics too, and only for the source that returned them.
func TestRunStoresBuildsAndMetrics(t *testing.T) {
	store, ctx := newTestStore(t), context.Background()
	builds := &ports.FakeFetcher{SourceName: "github-builds", Result: ports.FetchResult{
		Builds: []domain.Build{{Repo: "org/repo", Workflow: "ci", Conclusion: "success",
			Status: "completed", RunURL: "https://example/run", FinishedAt: at("2026-08-17T09:00:00Z")}},
	}}
	metrics := &ports.FakeFetcher{SourceName: "plausible", Result: ports.FetchResult{
		Metrics: []domain.Metric{{Site: "example.com", WindowDays: 7, Visitors: 10, Pageviews: 20}},
	}}
	r := refresh.New(store, []ports.SourceFetcher{builds, metrics}, &ports.FixedClock{T: now}, nil, discardLogger())

	if _, err := r.Run(ctx, "cron"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := store.Builds(ctx)
	if err != nil {
		t.Fatalf("Builds: %v", err)
	}
	if len(got) != 1 || got[0].Repo != "org/repo" {
		t.Errorf("builds = %+v, want the fetched build", got)
	}
	ms, err := store.Metrics(ctx)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if len(ms) != 1 || ms[0].Site != "example.com" {
		t.Errorf("metrics = %+v, want the fetched metric", ms)
	}
}

// FR-6.1 AC3: announcing is a courtesy; a notifier that fails must not fail the refresh.
func TestNotifierFailureDoesNotFailTheRun(t *testing.T) {
	store, ctx := newTestStore(t), context.Background()
	n := &failingNotifier{}
	r := refresh.New(store, []ports.SourceFetcher{fetcher("github", item("1"))},
		&ports.FixedClock{T: now}, n, discardLogger())

	rep, err := r.Run(ctx, "cron")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.OK {
		t.Error("a failing notifier must not make the run fail (FR-6.1 AC3)")
	}
	if n.items != 1 {
		t.Errorf("notifier saw %d items, want the 1 refreshed item", n.items)
	}
}

// The partial-result rule's sibling: what was not stored must not be announced either. A fetcher
// may hand back items together with an error, and those items are deliberately not stored — so
// announcing them would tell the user about items that do not exist in the database, and mark them
// notified, which suppresses the real announcement when they finally arrive (FR-6.1 AC2).
func TestFailedSourceItemsAreNotAnnounced(t *testing.T) {
	store, ctx := newTestStore(t), context.Background()
	n := &recordingNotifier{}
	partial := &ports.FakeFetcher{
		SourceName: "github",
		Result:     ports.FetchResult{Items: []domain.Item{item("1"), item("2")}}, // fetched, not stored
		Err:        errors.New("list org/two: 500"),
	}
	r := refresh.New(store, []ports.SourceFetcher{partial, fetcher("todoist", task("t1"))},
		&ports.FixedClock{T: now}, n, discardLogger())

	if _, err := r.Run(ctx, "cron"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(n.seen) != 1 {
		t.Fatalf("notifier saw %d items, want only the healthy source's 1 — a failed source's items were never stored", len(n.seen))
	}
	if n.seen[0].Source != "todoist" {
		t.Errorf("notifier saw an item from %q, want only todoist", n.seen[0].Source)
	}
}

type recordingNotifier struct{ seen []domain.Item }

func (n *recordingNotifier) Notify(_ context.Context, items []domain.Item) error {
	n.seen = append(n.seen, items...)
	return nil
}

type failingNotifier struct{ items int }

func (n *failingNotifier) Notify(_ context.Context, items []domain.Item) error {
	n.items += len(items)
	return errors.New("smtp down")
}

func mustItems(t *testing.T, s *libsql.Store, ctx context.Context) []domain.Item {
	t.Helper()
	got, err := s.Items(ctx)
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	return got
}
