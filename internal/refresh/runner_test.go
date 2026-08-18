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
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/libsql"
	"github.com/gernotstarke/zorgscope/internal/adapters/slack"
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

func item(id string) domain.Item { return issueOf("github", id) }

// issueOf is item for a source other than "github": announcing is scoped by kind, not by source
// name, so a second issue source is the honest way to test the scoping.
func issueOf(source, id string) domain.Item {
	return domain.Item{
		Source: source, ExternalID: id, Kind: domain.KindIssue,
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
	// The healthy source is a second issue source rather than Todoist, because only issues and
	// pull requests are announced at all (see TestOnlyGitHubItemsAreAnnounced) — a task would make
	// the assertion below pass for the wrong reason.
	r := refresh.New(store, []ports.SourceFetcher{partial, fetcher("github-extra", issueOf("github-extra", "3"))},
		&ports.FixedClock{T: now}, n, discardLogger())

	if _, err := r.Run(ctx, "cron"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(n.seen) != 1 {
		t.Fatalf("notifier saw %d items, want only the healthy source's 1 — a failed source's items were never stored", len(n.seen))
	}
	if n.seen[0].Source != "github-extra" {
		t.Errorf("notifier saw an item from %q, want only github-extra", n.seen[0].Source)
	}
}

// FR-6.1 AC2: an item is announced at most once, across repeated runs and across restarts. The
// bookkeeping is a row in the notified table precisely because there is no process between two
// refreshes to remember anything — the Machine is stopped.
//
// The real Slack adapter is used rather than a stub: the dedup only holds if the runner asks the
// store first, sends only what came back, and marks exactly that, and a notifier that counted
// calls would let a runner that marked the wrong keys pass.
func TestNotifyIsSkippedForAlreadyNotifiedItems(t *testing.T) {
	store, ctx := newTestStore(t), context.Background()
	posts := postRecorder(t, http.StatusOK)
	n := slack.New(posts.srv.URL+"/services/T0/B0/secret", posts.srv.Client())
	fetchers := []ports.SourceFetcher{fetcher("github", item("1"), item("2"))}

	// Two runs over the same two items, as a cron trigger every few minutes produces.
	for range 2 {
		r := refresh.New(store, fetchers, &ports.FixedClock{T: now}, n, discardLogger())
		if _, err := r.Run(ctx, "cron"); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}

	if got := posts.count(); got != 2 {
		t.Fatalf("posted %d messages over two runs of the same two items, want 2 — "+
			"an item is announced at most once (FR-6.1 AC2)", got)
	}

	// A genuinely new item on a third run still gets through: the dedup must suppress repeats,
	// not the feature.
	r := refresh.New(store, []ports.SourceFetcher{fetcher("github", item("1"), item("2"), item("3"))},
		&ports.FixedClock{T: now}, n, discardLogger())
	if _, err := r.Run(ctx, "cron"); err != nil {
		t.Fatalf("third Run: %v", err)
	}
	if got := posts.count(); got != 3 {
		t.Errorf("posted %d messages after a third item appeared, want 3", got)
	}
}

// FR-6.1 AC3: a Slack failure is recorded and dropped. The refresh itself succeeded — the items
// are stored and the dashboard is correct — so a webhook that is down must not make the run look
// broken.
//
// It must also not mark anything: nothing was sent, so the announcement is still owed. The second
// run proves it, and is the reason the runner marks after sending rather than before.
func TestSlackFailureDoesNotFailTheRun(t *testing.T) {
	store, ctx := newTestStore(t), context.Background()
	posts := postRecorder(t, http.StatusInternalServerError)
	n := slack.New(posts.srv.URL+"/services/T0/B0/secret", posts.srv.Client())
	r := refresh.New(store, []ports.SourceFetcher{fetcher("github", item("1"))},
		&ports.FixedClock{T: now}, n, discardLogger())

	rep, err := r.Run(ctx, "cron")
	if err != nil {
		t.Fatalf("Run must not fail because the webhook did: %v", err)
	}
	if !rep.OK {
		t.Error("report.OK = false; a Slack failure is not a refresh failure (FR-6.1 AC3)")
	}
	if got := len(mustItems(t, store, ctx)); got != 1 {
		t.Errorf("stored %d items, want 1 — the refresh itself succeeded", got)
	}
	last, err := store.LastRun(ctx)
	if err != nil {
		t.Fatalf("LastRun: %v", err)
	}
	if !last.OK {
		t.Error("the recorded run is marked failed; a Slack failure must not reach the run record")
	}
	// FR-6.1 AC3 says the failure is *recorded*. A log line is not a record on a Machine that
	// scales to zero and whose logs nobody reads: a revoked webhook would stop the notifications
	// for weeks while every dashboard signal said the system was healthy.
	if !strings.Contains(last.Detail, "notify:") || !strings.Contains(last.Detail, "500") {
		t.Errorf("run detail = %q, want it to record why the announcement failed", last.Detail)
	}
	// QS-4.3: the detail is rendered on the dashboard, and this webhook answered by quoting
	// itself — so this is the whole path from a rejection to a rendered page.
	if strings.Contains(last.Detail, "secret") {
		t.Errorf("the webhook leaked into the run detail (QS-4.3): %q", last.Detail)
	}

	// Nothing was sent, so nothing may have been marked: the announcement is retried.
	if got := posts.count(); got != 1 {
		t.Fatalf("attempted %d posts, want 1", got)
	}
	rerun := refresh.New(store, []ports.SourceFetcher{fetcher("github", item("1"))},
		&ports.FixedClock{T: now}, n, discardLogger())
	if _, err := rerun.Run(ctx, "cron"); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if got := posts.count(); got != 2 {
		t.Errorf("the second run attempted %d posts in total, want 2 — a failed announcement "+
			"must not be marked as sent", got)
	}
}

// posted is an httptest server standing in for a Slack incoming webhook, recording what reaches
// it. It answers "ok" as Slack does for a delivered message, and quotes the URL it was called at
// when it rejects one — which real endpoints do, and which is what makes the scrubbing (QS-4.3)
// testable all the way to the run record.
type posted struct {
	srv *httptest.Server
	mu  sync.Mutex
	n   int
	msg []string
	// reject decides what the nth post (1-based) is answered with: a zero status accepts it.
	reject func(n int, body string) int
}

func (p *posted) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n
}

// messages returns every body posted so far, in order.
func (p *posted) messages() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.msg...)
}

// sent reports how many of the posted messages name text — one item's announcement is one message
// naming its title.
func (p *posted) sent(text string) int {
	got := 0
	for _, m := range p.messages() {
		if strings.Contains(m, text) {
			got++
		}
	}
	return got
}

// rejectWith installs the rejection policy under the server's own lock, so a test may change it
// between two runs without racing the handler.
func (p *posted) rejectWith(f func(n int, body string) int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reject = f
}

func postRecorder(t *testing.T, status int) *posted {
	t.Helper()
	p := &posted{}
	if status < 200 || status > 299 {
		p.reject = func(int, string) int { return status }
	}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		p.mu.Lock()
		p.n++
		n := p.n
		p.msg = append(p.msg, string(body))
		reject := p.reject
		p.mu.Unlock()

		if reject != nil {
			if got := reject(n, string(body)); got != 0 {
				w.WriteHeader(got)
				_, _ = io.WriteString(w, "no_service for http://"+req.Host+req.URL.Path)
				return
			}
		}
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(p.srv.Close)
	return p
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

// The failure this fix exists for: a batch that stops partway must leave the run further ahead
// than it started, or it never gets anywhere.
//
// Before, the runner sent the whole set in one call and marked nothing when that call failed. The
// two failures a first run actually meets are deterministic — Slack's incoming webhooks allow
// about one message a second, and the announcement step has a budget of its own — so the run
// failed at the same position every time: the same prefix was re-posted on every cron tick
// forever and the items behind it were never announced at all. Now each message is sent on its
// own and recorded as it goes, so the unannounced prefix shrinks on every run and the next run
// carries on where this one stopped (FR-6.1 AC1, AC2).
func TestAPartlySentBatchContinuesWhereItStopped(t *testing.T) {
	store, ctx := newTestStore(t), context.Background()
	posts := postRecorder(t, http.StatusOK)
	// The webhook takes one message and then rate-limits, exactly as it would on a first run over
	// a populated database.
	posts.rejectWith(func(n int, _ string) int {
		if n >= 2 {
			return http.StatusTooManyRequests
		}
		return 0
	})
	n := slack.New(posts.srv.URL+"/services/T0/B0/secret", posts.srv.Client())
	fetchers := []ports.SourceFetcher{fetcher("github", item("1"), item("2"), item("3"))}

	first := refresh.New(store, fetchers, &ports.FixedClock{T: now}, n, discardLogger())
	if _, err := first.Run(ctx, "cron"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := posts.count(); got != 2 {
		t.Fatalf("the first run attempted %d posts, want 2: one delivered, then it stops at the "+
			"first failure", got)
	}

	// The rate limit passes, as it does between two cron ticks.
	posts.rejectWith(nil)
	second := refresh.New(store, fetchers, &ports.FixedClock{T: now}, n, discardLogger())
	if _, err := second.Run(ctx, "cron"); err != nil {
		t.Fatalf("second Run: %v", err)
	}

	// issue 1 went out in the first run and must never be posted again (FR-6.1 AC2); issue 2 was
	// the one the rate limit hit, so it is attempted once per run until it lands; issue 3 was
	// behind the failure and is announced by the run after it (FR-6.1 AC1) — which is the whole
	// point: before, it never was.
	for _, want := range []struct {
		item string
		n    int
		why  string
	}{
		{"issue 1", 1, "a message that went out must not be posted again on the next run"},
		{"issue 2", 2, "the message the failure hit is retried, exactly once per run"},
		{"issue 3", 1, "the items behind the failure must be announced by the run after it"},
	} {
		if got := posts.sent(want.item); got != want.n {
			t.Errorf("%s was posted %d times, want %d: %s", want.item, got, want.n, want.why)
		}
	}

	// And it has converged: a third run over the same items posts nothing at all.
	before := posts.count()
	third := refresh.New(store, fetchers, &ports.FixedClock{T: now}, n, discardLogger())
	if _, err := third.Run(ctx, "cron"); err != nil {
		t.Fatalf("third Run: %v", err)
	}
	if got := posts.count(); got != before {
		t.Errorf("a third run posted %d more messages, want none: everything is announced", got-before)
	}
}

// The other half of converging: one message Slack will never accept must not stand at the head of
// the queue forever.
//
// A non-429 4xx is Slack refusing this payload, so retrying cannot change the answer. Leaving it
// unmarked would mean it leads the batch on every run, fails, and suppresses every item behind it
// for good — one bad item silently switching the feature off. It is therefore recorded as
// announced although it never went out: the deliberate exception to "mark only what was sent",
// and the failure is recorded in the run detail rather than swallowed.
func TestAPermanentlyRejectedMessageDoesNotBlockTheOnesBehindIt(t *testing.T) {
	store, ctx := newTestStore(t), context.Background()
	posts := postRecorder(t, http.StatusOK)
	posts.rejectWith(func(_ int, body string) int {
		if strings.Contains(body, "issue 1") {
			return http.StatusBadRequest // invalid_payload: this message, on every run
		}
		return 0
	})
	n := slack.New(posts.srv.URL+"/services/T0/B0/secret", posts.srv.Client())
	fetchers := []ports.SourceFetcher{fetcher("github", item("1"), item("2"))}

	first := refresh.New(store, fetchers, &ports.FixedClock{T: now}, n, discardLogger())
	rep, err := first.Run(ctx, "cron")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.OK {
		t.Error("a rejected announcement is not a failed refresh (FR-6.1 AC3)")
	}
	if rep.NotifyErr == "" {
		t.Error("the rejection was not recorded; a swallowed message must at least be visible")
	}

	second := refresh.New(store, fetchers, &ports.FixedClock{T: now}, n, discardLogger())
	if _, err := second.Run(ctx, "cron"); err != nil {
		t.Fatalf("second Run: %v", err)
	}

	if got := posts.sent("issue 1"); got != 1 {
		t.Errorf("the rejected message was attempted %d times, want 1: retrying it cannot help", got)
	}
	if got := posts.sent("issue 2"); got != 1 {
		t.Errorf("issue 2 was announced %d times, want 1 — a message Slack will never accept must "+
			"not suppress the items behind it", got)
	}
}

// FR-6.1 AC1 is about GitHub items: issues and pull requests. A Todoist task is something the user
// entered themselves, so announcing it back to them is noise — and it multiplies the volume of
// every first run.
func TestOnlyGitHubItemsAreAnnounced(t *testing.T) {
	store, ctx := newTestStore(t), context.Background()
	posts := postRecorder(t, http.StatusOK)
	n := slack.New(posts.srv.URL+"/services/T0/B0/secret", posts.srv.Client())
	r := refresh.New(store, []ports.SourceFetcher{
		fetcher("github", item("1")),
		fetcher("todoist", task("t1")),
	}, &ports.FixedClock{T: now}, n, discardLogger())

	if _, err := r.Run(ctx, "cron"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := posts.count(); got != 1 {
		t.Fatalf("posted %d messages, want 1 — the issue, not the task: %q", got, posts.messages())
	}
	if got := posts.sent("issue 1"); got != 1 {
		t.Errorf("the announced message was %q, want the GitHub issue", posts.messages())
	}
}

// The mirror of the poison-message test, and the reason the permanent class is narrow.
//
// A revoked or moved hook answers 404 no_service (or 403 action_prohibited) to *every* message, so
// treating it as permanent would record one more item as announced on every run and quietly drain
// the whole backlog into nothing while the operator was still working out that the URL needs
// rotating. An endpoint-level failure must therefore block rather than destroy: nothing is marked,
// the block is visible in the run record's detail on every run, and when a human rotates the URL
// the queue is still there.
func TestARevokedWebhookBlocksTheQueueInsteadOfDrainingIt(t *testing.T) {
	store, ctx := newTestStore(t), context.Background()
	posts := postRecorder(t, http.StatusNotFound) // no_service, for every message, on every run
	n := slack.New(posts.srv.URL+"/services/T0/B0/secret", posts.srv.Client())
	fetchers := []ports.SourceFetcher{fetcher("github", item("1"), item("2"))}

	for range 3 {
		r := refresh.New(store, fetchers, &ports.FixedClock{T: now}, n, discardLogger())
		rep, err := r.Run(ctx, "cron")
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !rep.OK {
			t.Error("a dead webhook is not a failed refresh (FR-6.1 AC3)")
		}
		if rep.NotifyErr == "" {
			t.Error("a blocked queue has to be visible: the failure was not recorded")
		}
	}
	// One failing POST per run, three runs — and the same item each time, because nothing was
	// marked. Anything less means an announcement was swallowed.
	if got := posts.count(); got != 3 {
		t.Fatalf("attempted %d posts over three runs, want 3 (one per run, always the first item): "+
			"%q", got, posts.messages())
	}
	if got := posts.sent("issue 1"); got != 3 {
		t.Errorf("issue 1 was attempted %d times, want 3 — an endpoint-level failure leaves the "+
			"item unannounced and still owed", got)
	}

	// The operator rotates the webhook. Every announcement the outage owed is still there.
	posts.rejectWith(nil)
	r := refresh.New(store, fetchers, &ports.FixedClock{T: now}, n, discardLogger())
	if _, err := r.Run(ctx, "cron"); err != nil {
		t.Fatalf("Run after the webhook was rotated: %v", err)
	}
	if got := posts.sent("issue 2"); got != 1 {
		t.Errorf("issue 2 was announced %d times, want 1 — it must survive the outage rather than "+
			"be marked announced while the hook was dead", got)
	}
	if got := posts.count(); got != 5 {
		t.Errorf("posted %d messages in total, want 5: three failed attempts and then both items",
			got)
	}
}

// QS-1.3's second half — "an item first seen after the click is new again" — across the one seam
// no per-task test could see: the runner's clock and POST /seen's.
//
// The scenario is the ordinary one, not a contrived race. A refresh may legitimately run for
// minutes (refreshCeiling is four), and the user is looking at the dashboard the whole time:
//
//	12:00:00  cron fires; the run takes the lease and starts fetching.
//	12:00:30  the user presses "mark all seen"; last_visit_at = 12:00:30.
//	12:01:00  the fetch returns and the items are stored.
//
// While the runner stamped first_seen_at with the run's *start*, every item stored after that
// click was born at 12:00:00 — behind a watermark of 12:00:30 — and therefore never carried a NEW
// badge, on that visit or any later one, because first_seen_at is never updated (FR-5.3 AC2). The
// dashboard's whole product is that badge, so the item was, in the only sense that matters,
// invisible.
//
// This is deliberately driven through the real store and the real domain rule: the stamp is
// written by the INSERT, the watermark by SetLastVisit, and the verdict by domain.Item.IsNew. A
// stub for any of the three would let the bug back in.
func TestAMarkAllSeenDuringARunDoesNotSwallowThatRunsNewItems(t *testing.T) {
	store, ctx := newTestStore(t), context.Background()
	clock := &ports.FixedClock{T: now}

	// The click lands in the middle of the fetch, exactly as it would from a browser: the
	// watermark is the wall-clock instant of the click, and the fetch goes on afterwards.
	click := now.Add(30 * time.Second)
	source := &fetcherDoing{
		name:  "github",
		items: []domain.Item{item("1"), item("2")},
		during: func() {
			clock.T = click
			if err := store.SetLastVisit(ctx, clock.Now()); err != nil {
				t.Errorf("SetLastVisit: %v", err)
			}
			clock.T = click.Add(30 * time.Second) // the fetch runs on for another half minute
		},
	}
	r := refresh.New(store, []ports.SourceFetcher{source}, clock, nil, discardLogger())

	if _, err := r.Run(ctx, "cron"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	lastVisit, err := store.LastVisit(ctx)
	if err != nil {
		t.Fatalf("LastVisit: %v", err)
	}
	if !lastVisit.Equal(click) {
		t.Fatalf("last visit = %v, want the click at %v — the fixture is wrong", lastVisit, click)
	}
	items := mustItems(t, store, ctx)
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	for _, it := range items {
		if !it.IsNew(lastVisit) {
			t.Errorf("item %s: first seen %v, last visit %v — an item stored after the click "+
				"must still be NEW (FR-1.2 AC1, QS-1.3). Stamping first_seen_at with the run's "+
				"start time hides every item a long run stores after a mark-all-seen, for good.",
				it.ExternalID, it.FirstSeenAt, lastVisit)
		}
	}
}

// fetcherDoing is a SourceFetcher that runs during while it is fetching — the stand-in for
// everything that can happen to the database in the minutes a real fetch takes. ports.FakeFetcher
// can block on a channel but cannot act, and acting is the whole point here.
type fetcherDoing struct {
	name   string
	items  []domain.Item
	during func()
}

func (f *fetcherDoing) Name() string { return f.name }

func (f *fetcherDoing) Fetch(ctx context.Context) (ports.FetchResult, error) {
	if err := ctx.Err(); err != nil {
		return ports.FetchResult{}, err
	}
	f.during()
	return ports.FetchResult{Items: f.items}, nil
}
