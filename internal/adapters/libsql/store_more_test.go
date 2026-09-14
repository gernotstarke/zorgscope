package libsql_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// The ports.Store contract: ReplaceItems returns how many items the source now holds — len(items)
// — and not how many of them are new. The refresh runner writes that number to
// source_state.item_count, so a second refresh of an unchanged source must still report its full
// size rather than zero. How many are new is a question about first_seen_at, answered by
// domain.CountNew.
func TestReplaceItemsReturnsTheStoredCount(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	t1, t2 := at("2026-08-17T10:00:00Z"), at("2026-08-17T11:00:00Z")

	n, err := s.ReplaceItems(ctx, "github", []domain.Item{item("1", "a"), item("2", "b")}, t1)
	if err != nil {
		t.Fatalf("ReplaceItems: %v", err)
	}
	if n != 2 {
		t.Errorf("stored count on an empty store = %d, want 2", n)
	}

	// One item survives and one is new: the source still holds two, so the answer is still 2.
	n, err = s.ReplaceItems(ctx, "github", []domain.Item{item("1", "a"), item("3", "c")}, t2)
	if err != nil {
		t.Fatalf("ReplaceItems: %v", err)
	}
	if n != 2 {
		t.Errorf("stored count = %d, want 2: the count is what the source holds, not what is new", n)
	}
	if got := mustItems(t, s, ctx); len(got) != n {
		t.Errorf("ReplaceItems returned %d but the store holds %d items", n, len(got))
	}

	if n, err = s.ReplaceItems(ctx, "github", nil, t2); err != nil || n != 0 {
		t.Errorf("ReplaceItems(nil) = %d, %v; want 0, nil", n, err)
	}
}

// An empty fetch result empties the source — the degenerate case of deleteAbsent, where there is
// no id to build an IN list from.
func TestReplaceItemsWithNoItemsClearsTheSource(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	now := at("2026-08-17T10:00:00Z")

	td := item("t1", "task")
	td.Source = "todoist"
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
// direction: many rows written, then nearly all of them deleted at once. This is the path the
// chunked IN-delete takes, and the todoist row is here because it is also the only test that
// reaches that delete with another source present: the scoping test replaces github with an
// unchanged item set, so nothing is absent and no delete statement is issued at all (FR-5.5 AC1).
func TestReplaceItemsHandlesMoreItemsThanTheParameterLimit(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	t1, t2 := at("2026-08-17T10:00:00Z"), at("2026-08-17T11:00:00Z")

	td := item("t1", "task")
	td.Source = "todoist"
	if _, err := s.ReplaceItems(ctx, "todoist", []domain.Item{td}, t1); err != nil {
		t.Fatalf("ReplaceItems(todoist): %v", err)
	}

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

	if got := mustItems(t, s, ctx); len(got) != many+1 {
		t.Fatalf("len(items) = %d, want %d", len(got), many+1)
	}
	// 499 rows go away at once, which is past the 400-placeholder chunk size.
	if _, err := s.ReplaceItems(ctx, "github", items[:1], t2); err != nil {
		t.Fatalf("ReplaceItems(1 item): %v", err)
	}
	got := mustItems(t, s, ctx)
	if len(got) != 2 {
		t.Fatalf("len(items) = %d, want 2 after the bulk delete: one github row and the todoist row", len(got))
	}
	survived := false
	for _, it := range got {
		if it.Source == "todoist" && it.ExternalID == "t1" {
			survived = true
		}
	}
	if !survived {
		t.Errorf("the todoist row did not survive github's bulk delete (FR-5.5 AC1): %v", got)
	}
}

// Times are RFC 3339 UTC in SQL and time.Time in Go, and a local time is normalized to UTC on the
// round trip.
func TestItemTimesRoundTrip(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	now := at("2026-08-17T10:00:00Z")

	it := item("1", "issue")
	local := time.FixedZone("CEST", 2*60*60)
	it.CreatedAt = time.Date(2026, 8, 20, 14, 30, 0, 0, local)

	mustReplace(t, s, ctx, []domain.Item{it}, now)

	got := mustItems(t, s, ctx)[0]
	if !got.CreatedAt.Equal(it.CreatedAt) || !got.UpdatedAt.Equal(it.UpdatedAt) {
		t.Errorf("CreatedAt/UpdatedAt = %v/%v, want %v/%v",
			got.CreatedAt, got.UpdatedAt, it.CreatedAt, it.UpdatedAt)
	}
	if got.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt location = %v, want UTC — nothing may store local time", got.CreatedAt.Location())
	}
	if got.Kind != domain.KindIssue || got.Repo != "org/repo" || got.Number != 1 ||
		got.URL != "https://example/1" || got.Author != "someone" || got.State != "open" {
		t.Errorf("round-tripped item = %+v, does not match what was written", got)
	}
}

func TestUpsertBuildsRoundTripsAndUpdates(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	t1, t2 := at("2026-08-17T10:00:00Z"), at("2026-08-17T11:00:00Z")

	b := domain.Build{
		Repo: "org/repo", Workflow: "ci", WorkflowPath: ".github/workflows/ci.yml",
		Conclusion: "success", Status: "completed",
		RunURL: "https://example/run/1", FinishedAt: at("2026-08-17T09:55:00Z"),
		Badge: []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="88" height="20"/>`),
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
	// The workflow file survives the round trip: without it the details page cannot build a
	// badge for the repository (FR-2.3 AC5), and a column that is written but never read back is
	// the kind of omission a schema change makes silently.
	if got[0].WorkflowPath != b.WorkflowPath {
		t.Errorf("WorkflowPath = %q, want %q", got[0].WorkflowPath, b.WorkflowPath)
	}
	// The badge is bytes, not text, and survives the round trip unchanged: the details page draws
	// it from these bytes and makes no request of its own (FR-2.3 AC5).
	if !bytes.Equal(got[0].Badge, b.Badge) {
		t.Errorf("Badge = %q, want %q", got[0].Badge, b.Badge)
	}
}

// A build stored without a badge — no workflow file to address, or a fetch that did not arrive —
// reads back as no badge rather than as a scan failure. The column is NULL for every row written
// before it existed, which is the shape every stored build is in immediately after the migration.
func TestABuildWithoutABadgeRoundTrips(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	now := at("2026-08-17T10:00:00Z")

	b := domain.Build{Repo: "org/plain", Workflow: "ci", Status: "completed", Conclusion: "success"}
	if err := s.UpsertBuilds(ctx, []domain.Build{b}, now); err != nil {
		t.Fatalf("UpsertBuilds: %v", err)
	}
	got, err := s.Builds(ctx)
	if err != nil {
		t.Fatalf("Builds: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(builds) = %d, want 1", len(got))
	}
	if len(got[0].Badge) != 0 {
		t.Errorf("Badge = %q, want none", got[0].Badge)
	}
}

// UpsertBuilds has the same complement delete ReplaceItems has: a repository dropped from the
// configuration stops appearing in the incoming set, and its row must go with it. Without this,
// the build tile keeps showing a repository nobody watches any more, frozen at whatever its last
// run was, with nothing left to refresh it.
func TestUpsertBuildsDeletesRepositoriesNoLongerPresent(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	now := at("2026-08-17T10:00:00Z")

	build := func(repo string) domain.Build {
		return domain.Build{
			Repo: repo, Workflow: "ci", Conclusion: "success", Status: "completed",
			RunURL: "https://example/run/" + repo, FinishedAt: at("2026-08-17T09:55:00Z"),
		}
	}

	if err := s.UpsertBuilds(ctx, []domain.Build{build("org/kept"), build("org/dropped")}, now); err != nil {
		t.Fatalf("first UpsertBuilds: %v", err)
	}
	if err := s.UpsertBuilds(ctx, []domain.Build{build("org/kept")}, now); err != nil {
		t.Fatalf("second UpsertBuilds: %v", err)
	}

	got, err := s.Builds(ctx)
	if err != nil {
		t.Fatalf("Builds: %v", err)
	}
	if len(got) != 1 || got[0].Repo != "org/kept" {
		t.Fatalf("builds = %+v, want only org/kept — org/dropped left the incoming set and must "+
			"not survive it", got)
	}
}

// The degenerate case of the same rule: an empty incoming set means no repository has a build any
// more, so no build row may remain.
func TestUpsertBuildsWithNoBuildsClearsTheTable(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	now := at("2026-08-17T10:00:00Z")

	b := domain.Build{Repo: "org/repo", Workflow: "ci", Conclusion: "success", Status: "completed"}
	if err := s.UpsertBuilds(ctx, []domain.Build{b}, now); err != nil {
		t.Fatalf("UpsertBuilds: %v", err)
	}
	if err := s.UpsertBuilds(ctx, nil, now); err != nil {
		t.Fatalf("UpsertBuilds(nil): %v", err)
	}

	got, err := s.Builds(ctx)
	if err != nil {
		t.Fatalf("Builds: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("builds = %+v, want none", got)
	}
}

// The build rows must not be collateral damage of an item refresh, nor items of a build refresh:
// the two sets are replaced independently (FR-5.5 AC1).
func TestUpsertBuildsLeavesItemsAlone(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	now := at("2026-08-17T10:00:00Z")

	if _, err := s.ReplaceItems(ctx, "github", []domain.Item{item("1", "kept")}, now); err != nil {
		t.Fatalf("ReplaceItems: %v", err)
	}
	if err := s.UpsertBuilds(ctx, nil, now); err != nil {
		t.Fatalf("UpsertBuilds(nil): %v", err)
	}

	items, err := s.Items(ctx)
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1 — replacing builds must not touch items", len(items))
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
//
// This is not a race test: it proves that one connection pool sees another's lease, and that the
// holder checks behave. Exclusivity under a genuine tie rests on AcquireRefreshLease being a
// single INSERT … ON CONFLICT … WHERE statement — the database decides, not this test.
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

// LastSuccessfulRun is the store side of FR-1.1 AC3: the header needs the run that actually
// worked, and LastRun cannot reach it once a later attempt has failed or is still open.
//
// The three answers it has to get right are all here: "never" as a zero value and not an error
// (the convention LastVisit and LastRun follow), an open run passed over because a run that is
// still running is not a successful one, and — the case that motivated the method — an earlier
// success found again behind a failed latest run.
func TestLastSuccessfulRunSkipsFailedAndOpenRuns(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	start := at("2026-08-17T10:00:00Z")

	never, err := s.LastSuccessfulRun(ctx)
	if err != nil {
		t.Fatalf("LastSuccessfulRun on an empty store: %v", err)
	}
	if never.ID != 0 || !never.StartedAt.IsZero() || !never.FinishedAt.IsZero() {
		t.Errorf("LastSuccessfulRun on an empty store = %+v, want the zero run with a nil error", never)
	}

	good, err := s.StartRun(ctx, "cron", start)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := s.FinishRun(ctx, good, start.Add(10*time.Second), true, "github: 12"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	bad, err := s.StartRun(ctx, "cron", start.Add(time.Minute))
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := s.FinishRun(ctx, bad, start.Add(70*time.Second), false, "github: 502"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	// A third run finished as successful but at the zero time, which this package stores as the
	// empty string like every other zero time. It has an outcome and no time to put in the
	// header, so it is not an answer this method may give: FinishRun records the time it is
	// handed, and the row it leaves behind is the reason ok = 1 is not the whole condition.
	zeroTimed, err := s.StartRun(ctx, "cron", start.Add(90*time.Second))
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := s.FinishRun(ctx, zeroTimed, time.Time{}, true, "no finishing time"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	// And a fourth run that is still open, as one is during every refresh: it has neither an
	// outcome nor a finishing time yet.
	if _, err := s.StartRun(ctx, "user", start.Add(2*time.Minute)); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	last, err := s.LastRun(ctx)
	if err != nil {
		t.Fatalf("LastRun: %v", err)
	}
	if !last.Running() {
		t.Fatalf("LastRun = %+v, want the open run — the fixture is not the one this test needs", last)
	}

	got, err := s.LastSuccessfulRun(ctx)
	if err != nil {
		t.Fatalf("LastSuccessfulRun: %v", err)
	}
	if got.ID != good {
		t.Errorf("LastSuccessfulRun = run %d (%+v), want run %d: neither the failed run, nor the "+
			"open one, nor the one with no finishing time is a successful refresh",
			got.ID, got, good)
	}
	if got.FinishedAt.IsZero() {
		t.Error("LastSuccessfulRun answered with a run that has no finishing time; the header " +
			"has nothing to state (FR-1.1 AC3)")
	}
	if !got.OK || got.Trigger != "cron" || got.Detail != "github: 12" {
		t.Errorf("LastSuccessfulRun = %+v, want the whole row of the successful run", got)
	}
	if !got.StartedAt.Equal(start) || !got.FinishedAt.Equal(start.Add(10*time.Second)) {
		t.Errorf("LastSuccessfulRun times = %v..%v, want %v..%v",
			got.StartedAt, got.FinishedAt, start, start.Add(10*time.Second))
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
