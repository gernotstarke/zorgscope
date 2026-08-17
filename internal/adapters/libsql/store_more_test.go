package libsql_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// The ports.Store contract: ReplaceItems returns how many items are new as of now, which is what
// the refresh service counts to decide whether to notify (FR-6.1).
func TestReplaceItemsCountsOnlyTheNewOnes(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	t1, t2 := at("2026-08-17T10:00:00Z"), at("2026-08-17T11:00:00Z")

	n, err := s.ReplaceItems(ctx, "github", []domain.Item{item("1", "a"), item("2", "b")}, t1)
	if err != nil {
		t.Fatalf("ReplaceItems: %v", err)
	}
	if n != 2 {
		t.Errorf("new count on an empty store = %d, want 2", n)
	}

	n, err = s.ReplaceItems(ctx, "github", []domain.Item{item("1", "a"), item("3", "c")}, t2)
	if err != nil {
		t.Fatalf("ReplaceItems: %v", err)
	}
	if n != 1 {
		t.Errorf("new count = %d, want 1: only item 3 had not been seen before", n)
	}
}

// An empty fetch result empties the source — the degenerate case of deleteAbsent, where there is
// no id to build an IN list from.
func TestReplaceItemsWithNoItemsClearsTheSource(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	now := at("2026-08-17T10:00:00Z")

	td := item("t1", "task")
	td.Source, td.Kind = "todoist", domain.KindTask
	mustReplace(t, s, ctx, []domain.Item{item("1", "a")}, now)
	if _, err := s.ReplaceItems(ctx, "todoist", []domain.Item{td}, now); err != nil {
		t.Fatalf("ReplaceItems(todoist): %v", err)
	}

	if _, err := s.ReplaceItems(ctx, "github", nil, now); err != nil {
		t.Fatalf("ReplaceItems(nil): %v", err)
	}
	got := mustItems(t, s, ctx)
	if len(got) != 1 || got[0].Source != "todoist" {
		t.Fatalf("items = %v, want only the todoist row: an empty github fetch must clear github alone", got)
	}
}

// A repository with hundreds of items must not run into SQLite's variable limit, in either
// direction: many rows written, then nearly all of them deleted at once.
func TestReplaceItemsHandlesMoreItemsThanTheParameterLimit(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	t1, t2 := at("2026-08-17T10:00:00Z"), at("2026-08-17T11:00:00Z")

	const many = 500
	items := make([]domain.Item, 0, many)
	for i := range many {
		items = append(items, item(fmt.Sprintf("%d", i), "bulk"))
	}

	start := time.Now()
	if _, err := s.ReplaceItems(ctx, "github", items, t1); err != nil {
		t.Fatalf("ReplaceItems(%d items): %v", many, err)
	}
	t.Logf("wrote %d items in %s (QS-2.5 budgets a whole refresh at 30s)", many, time.Since(start))

	if got := mustItems(t, s, ctx); len(got) != many {
		t.Fatalf("len(items) = %d, want %d", len(got), many)
	}
	// 499 rows go away at once, which is past the 400-placeholder chunk size.
	if _, err := s.ReplaceItems(ctx, "github", items[:1], t2); err != nil {
		t.Fatalf("ReplaceItems(1 item): %v", err)
	}
	if got := mustItems(t, s, ctx); len(got) != 1 {
		t.Fatalf("len(items) = %d, want 1 after the bulk delete", len(got))
	}
}

// Times are RFC 3339 UTC in SQL and time.Time in Go, and the zero time survives the trip: a
// GitHub item has no due date, and domain.Item.DueAt must come back zero rather than year one.
func TestItemTimesRoundTrip(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	now := at("2026-08-17T10:00:00Z")

	withDue := item("1", "issue")
	local := time.FixedZone("CEST", 2*60*60)
	withDue.DueAt = time.Date(2026, 8, 20, 14, 30, 0, 0, local)
	withoutDue := item("2", "issue")

	mustReplace(t, s, ctx, []domain.Item{withDue, withoutDue}, now)

	byID := map[string]domain.Item{}
	for _, it := range mustItems(t, s, ctx) {
		byID[it.ExternalID] = it
	}
	if got := byID["2"].DueAt; !got.IsZero() {
		t.Errorf("DueAt of an item without a due date = %v, want the zero time", got)
	}
	if got := byID["1"].DueAt; !got.Equal(withDue.DueAt) {
		t.Errorf("DueAt = %v, want %v — a local time must be stored as UTC and come back equal", got, withDue.DueAt)
	}
	if got := byID["1"].DueAt.Location(); got != time.UTC {
		t.Errorf("DueAt location = %v, want UTC — nothing may store local time", got)
	}
	if got := byID["1"]; !got.CreatedAt.Equal(withDue.CreatedAt) || !got.UpdatedAt.Equal(withDue.UpdatedAt) {
		t.Errorf("CreatedAt/UpdatedAt = %v/%v, want %v/%v",
			got.CreatedAt, got.UpdatedAt, withDue.CreatedAt, withDue.UpdatedAt)
	}
	if got := byID["1"]; got.Kind != domain.KindIssue || got.Repo != "org/repo" || got.Number != 1 ||
		got.URL != "https://example/1" || got.Author != "someone" || got.State != "open" {
		t.Errorf("round-tripped item = %+v, does not match what was written", got)
	}
}

func TestUpsertBuildsRoundTripsAndUpdates(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	t1, t2 := at("2026-08-17T10:00:00Z"), at("2026-08-17T11:00:00Z")

	b := domain.Build{
		Repo: "org/repo", Workflow: "ci", Conclusion: "success", Status: "completed",
		RunURL: "https://example/run/1", FinishedAt: at("2026-08-17T09:55:00Z"),
	}
	if err := s.UpsertBuilds(ctx, []domain.Build{b}, t1); err != nil {
		t.Fatalf("UpsertBuilds: %v", err)
	}
	b.Conclusion, b.FinishedAt = "failure", at("2026-08-17T10:55:00Z")
	if err := s.UpsertBuilds(ctx, []domain.Build{b}, t2); err != nil {
		t.Fatalf("UpsertBuilds: %v", err)
	}

	got, err := s.Builds(ctx)
	if err != nil {
		t.Fatalf("Builds: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(builds) = %d, want 1: builds are keyed by repository", len(got))
	}
	if got[0].Conclusion != "failure" || !got[0].FinishedAt.Equal(b.FinishedAt) {
		t.Errorf("build = %+v, want the second write's conclusion and finish time", got[0])
	}
	if !got[0].FetchedAt.Equal(t2) {
		t.Errorf("FetchedAt = %v, want %v", got[0].FetchedAt, t2)
	}
}

func TestUpsertMetricsRoundTripsAndUpdates(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	t1, t2 := at("2026-08-17T10:00:00Z"), at("2026-08-17T11:00:00Z")

	m := domain.Metric{Site: "example.org", WindowDays: 7, Visitors: 100, Pageviews: 250,
		PrevVisitors: 80, PrevPageviews: 200}
	other := domain.Metric{Site: "example.org", WindowDays: 30, Visitors: 400, Pageviews: 900}
	if err := s.UpsertMetrics(ctx, []domain.Metric{m, other}, t1); err != nil {
		t.Fatalf("UpsertMetrics: %v", err)
	}
	m.Visitors = 111
	if err := s.UpsertMetrics(ctx, []domain.Metric{m}, t2); err != nil {
		t.Fatalf("UpsertMetrics: %v", err)
	}

	got, err := s.Metrics(ctx)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(metrics) = %d, want 2: metrics are keyed by site and window", len(got))
	}
	if got[0].WindowDays != 7 || got[0].Visitors != 111 || got[0].PrevVisitors != 80 {
		t.Errorf("metric = %+v, want the 7-day window updated to 111 visitors", got[0])
	}
	if !got[0].FetchedAt.Equal(t2) {
		t.Errorf("FetchedAt = %v, want %v", got[0].FetchedAt, t2)
	}
}

// A success after an error must clear neither the other way round: the dashboard shows both "last
// good data" and "currently failing" (FR-1.4 AC2).
func TestRecordSourceOKAfterErrorKeepsBoth(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	bad, good := at("2026-08-17T10:00:00Z"), at("2026-08-17T11:00:00Z")

	if err := s.RecordSourceError(ctx, "plausible", bad, "429 rate limited"); err != nil {
		t.Fatalf("RecordSourceError: %v", err)
	}
	if err := s.RecordSourceOK(ctx, "plausible", good, 3); err != nil {
		t.Fatalf("RecordSourceOK: %v", err)
	}

	states, err := s.SourceStates(ctx)
	if err != nil {
		t.Fatalf("SourceStates: %v", err)
	}
	st := states["plausible"]
	if st.LastError != "429 rate limited" || !st.LastErrorAt.Equal(bad) {
		t.Errorf("error = %q at %v, want it preserved across the later success", st.LastError, st.LastErrorAt)
	}
	if !st.LastSuccessAt.Equal(good) || st.ItemCount != 3 {
		t.Errorf("success = %v with %d items, want %v with 3", st.LastSuccessAt, st.ItemCount, good)
	}
	if st.Failing() {
		t.Error("Failing() = true, want false: the success is newer than the error")
	}
}

// The lease is what stops two Fly machines refreshing at once (QS-1.7), so it has to hold across
// connections, not just within one *Store.
func TestRefreshLeaseHoldsAcrossConnections(t *testing.T) {
	a, ctx := newStore(t), context.Background()
	b := newStore(t) // a second connection; newStore truncates, so take the lease afterwards
	now := at("2026-08-17T10:00:00Z")

	ok, err := a.AcquireRefreshLease(ctx, "machine-a", now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("machine-a acquire = %v, %v; want true, nil", ok, err)
	}
	ok, err = b.AcquireRefreshLease(ctx, "machine-b", now.Add(time.Second), time.Minute)
	if err != nil {
		t.Fatalf("machine-b acquire: %v", err)
	}
	if ok {
		t.Fatal("machine-b took a lease machine-a holds (QS-1.7)")
	}

	// A holder may renew its own lease without waiting for it to expire.
	ok, err = a.AcquireRefreshLease(ctx, "machine-a", now.Add(30*time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatalf("machine-a renew = %v, %v; want true, nil", ok, err)
	}

	// Releasing someone else's lease must not free it.
	if err := b.ReleaseRefreshLease(ctx, "machine-b"); err != nil {
		t.Fatalf("machine-b release: %v", err)
	}
	ok, err = b.AcquireRefreshLease(ctx, "machine-b", now.Add(31*time.Second), time.Minute)
	if err != nil {
		t.Fatalf("machine-b acquire after bogus release: %v", err)
	}
	if ok {
		t.Error("machine-b released a lease machine-a holds, then took it")
	}

	if err := a.ReleaseRefreshLease(ctx, "machine-a"); err != nil {
		t.Fatalf("machine-a release: %v", err)
	}
	ok, err = b.AcquireRefreshLease(ctx, "machine-b", now.Add(32*time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatalf("machine-b acquire after a proper release = %v, %v; want true, nil", ok, err)
	}
}

func TestRunLifecycle(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	start, end := at("2026-08-17T10:00:00Z"), at("2026-08-17T10:00:12Z")

	empty, err := s.LastRun(ctx)
	if err != nil {
		t.Fatalf("LastRun on an empty store: %v", err)
	}
	if empty.ID != 0 || !empty.StartedAt.IsZero() {
		t.Errorf("LastRun on an empty store = %+v, want the zero run", empty)
	}

	first, err := s.StartRun(ctx, "cron", start)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	second, err := s.StartRun(ctx, "manual", start.Add(time.Minute))
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if second == first || second == 0 {
		t.Fatalf("StartRun ids = %d and %d, want distinct non-zero ids", first, second)
	}
	if err := s.FinishRun(ctx, second, end, false, "github: 502"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	got, err := s.LastRun(ctx)
	if err != nil {
		t.Fatalf("LastRun: %v", err)
	}
	if got.ID != second || got.Trigger != "manual" || got.OK || got.Detail != "github: 502" {
		t.Errorf("LastRun = %+v, want the manual run recorded as failed", got)
	}
	if !got.StartedAt.Equal(start.Add(time.Minute)) || !got.FinishedAt.Equal(end) {
		t.Errorf("LastRun times = %v..%v, want %v..%v", got.StartedAt, got.FinishedAt, start.Add(time.Minute), end)
	}
}

func TestMarkNotifiedIsIdempotentAndUnnotifiedHandlesEdges(t *testing.T) {
	s, ctx := newStore(t), context.Background()

	got, err := s.UnnotifiedKeys(ctx, nil)
	if err != nil {
		t.Fatalf("UnnotifiedKeys(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("UnnotifiedKeys(nil) = %v, want empty", got)
	}

	keys := []string{"github|1", "github|2"}
	if err := s.MarkNotified(ctx, keys, at("2026-08-17T10:00:00Z")); err != nil {
		t.Fatalf("MarkNotified: %v", err)
	}
	// Re-announcing the same keys must not fail on the primary key.
	if err := s.MarkNotified(ctx, keys, at("2026-08-17T11:00:00Z")); err != nil {
		t.Fatalf("MarkNotified again: %v", err)
	}
	got, err = s.UnnotifiedKeys(ctx, []string{"github|2", "github|3", "github|1"})
	if err != nil {
		t.Fatalf("UnnotifiedKeys: %v", err)
	}
	if len(got) != 1 || got[0] != "github|3" {
		t.Errorf("UnnotifiedKeys = %v, want [github|3]", got)
	}
}

// Migrate runs on every startup, so running it twice must be a no-op rather than an error, and it
// must not disturb the data already there.
func TestMigrateIsIdempotent(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	now := at("2026-08-17T10:00:00Z")

	mustReplace(t, s, ctx, []domain.Item{item("1", "a")}, now)
	for range 2 {
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("Migrate again: %v", err)
		}
	}
	got := mustItems(t, s, ctx)
	if len(got) != 1 || !got[0].FirstSeenAt.Equal(now) {
		t.Errorf("items after re-migrating = %v, want the row untouched", got)
	}
}
