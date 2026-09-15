package libsql_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/libsql"
	"github.com/gernotstarke/zorgscope/internal/domain"
)

func newStore(t *testing.T) *libsql.Store {
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

func at(s string) time.Time {
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return ts
}

func item(id, title string) domain.Item {
	return domain.Item{
		Source: "github", ExternalID: id, Kind: domain.KindIssue,
		Repo: "org/repo", Number: 1, Title: title, URL: "https://example/1",
		Author: "someone", State: "open",
		CreatedAt: at("2026-08-01T00:00:00Z"), UpdatedAt: at("2026-08-10T00:00:00Z"),
	}
}

// QS-1.2: the whole point of the schema. first_seen_at is written once and never again.
func TestReplaceItemsPreservesFirstSeen(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	first, second := at("2026-08-17T10:00:00Z"), at("2026-08-17T11:00:00Z")

	if _, err := s.ReplaceItems(ctx, "github", []domain.Item{item("1", "original")}, first); err != nil {
		t.Fatalf("first ReplaceItems: %v", err)
	}
	if _, err := s.ReplaceItems(ctx, "github", []domain.Item{item("1", "edited")}, second); err != nil {
		t.Fatalf("second ReplaceItems: %v", err)
	}

	got, err := s.Items(ctx)
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(got))
	}
	if !got[0].FirstSeenAt.Equal(first) {
		t.Errorf("FirstSeenAt = %v, want %v — a later refresh must not move it (FR-5.3 AC2)", got[0].FirstSeenAt, first)
	}
	if got[0].Title != "edited" {
		t.Errorf("Title = %q, want %q — content must be updated", got[0].Title, "edited")
	}
}

// FR-5.3 AC3: gone and back again counts as new again.
func TestReplaceItemsDeletesAbsentAndReSeesReturning(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	t1, t2, t3 := at("2026-08-17T10:00:00Z"), at("2026-08-17T11:00:00Z"), at("2026-08-17T12:00:00Z")

	mustReplace(t, s, ctx, []domain.Item{item("1", "a"), item("2", "b")}, t1)
	mustReplace(t, s, ctx, []domain.Item{item("1", "a")}, t2)

	if got := mustItems(t, s, ctx); len(got) != 1 {
		t.Fatalf("len(items) = %d, want 1: the absent item must be deleted", len(got))
	}

	mustReplace(t, s, ctx, []domain.Item{item("1", "a"), item("2", "b")}, t3)

	for _, it := range mustItems(t, s, ctx) {
		if it.ExternalID == "2" && !it.FirstSeenAt.Equal(t3) {
			t.Errorf("returning item FirstSeenAt = %v, want %v (FR-5.3 AC3)", it.FirstSeenAt, t3)
		}
		if it.ExternalID == "1" && !it.FirstSeenAt.Equal(t1) {
			t.Errorf("surviving item FirstSeenAt = %v, want %v", it.FirstSeenAt, t1)
		}
	}
}

// FR-5.5 AC1: one source's write must not touch another source's rows.
func TestReplaceItemsIsScopedToItsSource(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	now := at("2026-08-17T10:00:00Z")

	gh := item("1", "gh")
	td := item("t1", "task")
	td.Source = "todoist"

	mustReplace(t, s, ctx, []domain.Item{gh}, now)
	if _, err := s.ReplaceItems(ctx, "todoist", []domain.Item{td}, now); err != nil {
		t.Fatalf("ReplaceItems(todoist): %v", err)
	}
	mustReplace(t, s, ctx, []domain.Item{gh}, now)

	if got := mustItems(t, s, ctx); len(got) != 2 {
		t.Fatalf("len(items) = %d, want 2: replacing github deleted todoist rows", len(got))
	}
}

// QS-1.3
func TestLastVisitRoundTrips(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	zero, err := s.LastVisit(ctx)
	if err != nil {
		t.Fatalf("LastVisit: %v", err)
	}
	if !zero.IsZero() {
		t.Errorf("LastVisit on an empty store = %v, want the zero time", zero)
	}
	want := at("2026-08-17T10:00:00Z")
	if err := s.SetLastVisit(ctx, want); err != nil {
		t.Fatalf("SetLastVisit: %v", err)
	}
	got, err := s.LastVisit(ctx)
	if err != nil {
		t.Fatalf("LastVisit: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("LastVisit = %v, want %v", got, want)
	}
}

// QS-1.7
func TestRefreshLeaseIsExclusiveAndExpires(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	now := at("2026-08-17T10:00:00Z")

	ok, err := s.AcquireRefreshLease(ctx, "a", now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("first acquire = %v, %v; want true, nil", ok, err)
	}
	ok, err = s.AcquireRefreshLease(ctx, "b", now.Add(time.Second), time.Minute)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if ok {
		t.Error("second holder acquired a live lease; a refresh must not run twice (QS-1.7)")
	}
	ok, err = s.AcquireRefreshLease(ctx, "b", now.Add(2*time.Minute), time.Minute)
	if err != nil || !ok {
		t.Fatalf("acquire after expiry = %v, %v; want true, nil — a crashed run must not lock forever", ok, err)
	}
	if err := s.ReleaseRefreshLease(ctx, "b"); err != nil {
		t.Fatalf("ReleaseRefreshLease: %v", err)
	}
}

func TestSourceStateRecordsSuccessAndError(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	ok, bad := at("2026-08-17T10:00:00Z"), at("2026-08-17T11:00:00Z")

	if err := s.RecordSourceOK(ctx, "github", ok, 7); err != nil {
		t.Fatalf("RecordSourceOK: %v", err)
	}
	if err := s.RecordSourceError(ctx, "github", bad, "boom"); err != nil {
		t.Fatalf("RecordSourceError: %v", err)
	}

	states, err := s.SourceStates(ctx)
	if err != nil {
		t.Fatalf("SourceStates: %v", err)
	}
	st := states["github"]
	if !st.LastSuccessAt.Equal(ok) {
		t.Errorf("LastSuccessAt = %v, want %v — an error must not erase the last success (FR-1.4 AC2)", st.LastSuccessAt, ok)
	}
	if st.LastError != "boom" || !st.LastErrorAt.Equal(bad) {
		t.Errorf("error = %q at %v, want %q at %v", st.LastError, st.LastErrorAt, "boom", bad)
	}
	if st.ItemCount != 7 {
		t.Errorf("ItemCount = %d, want 7", st.ItemCount)
	}
}

func TestUnnotifiedKeysFiltersWhatWasSent(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	if err := s.MarkNotified(ctx, []string{"github|1"}, at("2026-08-17T10:00:00Z")); err != nil {
		t.Fatalf("MarkNotified: %v", err)
	}
	got, err := s.UnnotifiedKeys(ctx, []string{"github|1", "github|2"})
	if err != nil {
		t.Fatalf("UnnotifiedKeys: %v", err)
	}
	if len(got) != 1 || got[0] != "github|2" {
		t.Errorf("UnnotifiedKeys = %v, want [github|2] (FR-6.1 AC2)", got)
	}
}

func mustReplace(t *testing.T, s *libsql.Store, ctx context.Context, items []domain.Item, now time.Time) {
	t.Helper()
	if _, err := s.ReplaceItems(ctx, "github", items, now); err != nil {
		t.Fatalf("ReplaceItems: %v", err)
	}
}

func mustItems(t *testing.T, s *libsql.Store, ctx context.Context) []domain.Item {
	t.Helper()
	got, err := s.Items(ctx)
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	return got
}
