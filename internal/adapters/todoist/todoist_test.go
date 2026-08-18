package todoist_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/todoist"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/fakesources"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// at parses an RFC 3339 timestamp, failing the test on a malformed literal rather than silently
// producing a zero time.
func at(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("at(%q): %v", s, err)
	}
	return tm
}

// newFetcher builds a Fetcher against a fresh fake server (internal/fakesources keeps mutable
// state, so each test gets its own instance rather than sharing one across the suite) with the
// fixed clock the fixture is dated against.
func newFetcher(t *testing.T, clock ports.Clock) (*todoist.Fetcher, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(fakesources.NewServer())
	f := todoist.New(todoist.Config{
		Token:   "x",
		BaseURL: srv.URL,
		Filter:  "overdue | today",
	}, srv.Client(), clock)
	return f, srv
}

// TestFetchReturnsOnlyOverdueAndDueToday is FR-4.1 AC3: the fixture holds one overdue task
// (2026-08-16), one due today (2026-08-17), one due next week (2026-08-24) and one with no due
// date at all; against the fixed clock only the first two may survive.
func TestFetchReturnsOnlyOverdueAndDueToday(t *testing.T) {
	clock := &ports.FixedClock{T: at(t, "2026-08-17T12:00:00Z")}
	f, srv := newFetcher(t, clock)
	defer srv.Close()

	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("len(items) = %d, want 2 — later and undated tasks are out of scope (FR-4.1 AC3)", len(res.Items))
	}
	for _, it := range res.Items {
		if it.Kind != domain.KindTask {
			t.Errorf("Kind = %q, want task", it.Kind)
		}
		if it.Source != "todoist" {
			t.Errorf("Source = %q, want todoist", it.Source)
		}
		if it.DueAt.IsZero() {
			t.Errorf("task %s has no DueAt", it.ExternalID)
		}
		if !strings.HasPrefix(it.ExternalID, "todoist:") {
			t.Errorf("ExternalID = %q, want todoist: prefix", it.ExternalID)
		}
		if !it.FirstSeenAt.IsZero() {
			t.Errorf("task %s: FirstSeenAt must be left zero — the store owns it", it.ExternalID)
		}
	}
}

// TestOverdueSortsBeforeDueToday is FR-4.1 AC2: items come back sorted by DueAt ascending, so the
// overdue task (2026-08-16) precedes the due-today task (2026-08-17) — the dashboard's most
// urgent items lead the list without any further sorting by the caller.
func TestOverdueSortsBeforeDueToday(t *testing.T) {
	clock := &ports.FixedClock{T: at(t, "2026-08-17T12:00:00Z")}
	f, srv := newFetcher(t, clock)
	defer srv.Close()

	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(res.Items))
	}

	overdue, dueToday := res.Items[0], res.Items[1]
	if overdue.ExternalID != "todoist:1001" {
		t.Errorf("Items[0].ExternalID = %q, want todoist:1001 (overdue)", overdue.ExternalID)
	}
	if dueToday.ExternalID != "todoist:1002" {
		t.Errorf("Items[1].ExternalID = %q, want todoist:1002 (due today)", dueToday.ExternalID)
	}
	if !overdue.DueAt.Before(dueToday.DueAt) {
		t.Errorf("overdue.DueAt (%s) must be before dueToday.DueAt (%s)", overdue.DueAt, dueToday.DueAt)
	}
}

// TestFetchMapsTaskFields pins the field mapping (FR-4.1): id, content, url and priority map
// straight across, the two due shapes (a floating date vs. a full instant) both resolve to a
// non-zero UTC DueAt, and Repo/Number/Author stay empty since Todoist tasks have none of those.
func TestFetchMapsTaskFields(t *testing.T) {
	clock := &ports.FixedClock{T: at(t, "2026-08-17T12:00:00Z")}
	f, srv := newFetcher(t, clock)
	defer srv.Close()

	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	byID := map[string]domain.Item{}
	for _, it := range res.Items {
		byID[it.ExternalID] = it
	}

	overdue, ok := byID["todoist:1001"]
	if !ok {
		t.Fatal("missing todoist:1001 (has due.datetime)")
	}
	if overdue.Title != "Renew hosting certificate" {
		t.Errorf("Title = %q", overdue.Title)
	}
	if overdue.URL != "https://todoist.com/showTask?id=1001" {
		t.Errorf("URL = %q", overdue.URL)
	}
	if overdue.Priority != 4 {
		t.Errorf("Priority = %d, want 4", overdue.Priority)
	}
	// due.datetime is "2026-08-16T17:00:00Z" — used as-is, converted to UTC.
	if !overdue.DueAt.Equal(at(t, "2026-08-16T17:00:00Z")) {
		t.Errorf("DueAt = %s, want 2026-08-16T17:00:00Z", overdue.DueAt)
	}
	if overdue.Number != 0 || overdue.Author != "" {
		t.Errorf("Number/Author must stay empty for a task: %+v", overdue)
	}
	// Repo carries the project name for a task — see toItem (FR-4.1 AC1).
	if overdue.Repo != "Website" {
		t.Errorf("Repo = %q, want the project name Website", overdue.Repo)
	}

	dueToday, ok := byID["todoist:1002"]
	if !ok {
		t.Fatal("missing todoist:1002 (has only due.date)")
	}
	// due.date is the floating date "2026-08-17" with no time component — it must be interpreted
	// as the end of that day in the configured location (UTC here, since Config.Location is nil),
	// not midnight, or a task due "today" would read as already past by the morning.
	if !dueToday.DueAt.Equal(time.Date(2026, 8, 17, 23, 59, 59, 999999999, time.UTC)) {
		t.Errorf("DueAt = %s, want end of 2026-08-17 UTC", dueToday.DueAt)
	}
}

// countingFetcher builds a Fetcher against a fresh fake server whose requests are counted by
// path, so a test can assert how many upstream calls one Fetch costs.
func countingFetcher(t *testing.T, clock ports.Clock) (*todoist.Fetcher, *httptest.Server, func(path string) int) {
	t.Helper()

	var mu sync.Mutex
	counts := map[string]int{}
	fake := fakesources.NewServer()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		counts[r.URL.Path]++
		mu.Unlock()
		fake.ServeHTTP(w, r)
	}))

	f := todoist.New(todoist.Config{
		Token:   "x",
		BaseURL: srv.URL,
		Filter:  "overdue | today",
	}, srv.Client(), clock)

	return f, srv, func(path string) int {
		mu.Lock()
		defer mu.Unlock()
		return counts[path]
	}
}

// TestFetchCarriesProjectName is FR-4.1 AC1: a task is shown with content, project, due date and
// priority, so the project name has to reach the item. It is carried in Repo — the item's
// container field — because Todoist tasks never used that column and adding a dedicated one would
// mean a schema migration for a single string (see toItem).
//
// The two surviving fixture tasks deliberately belong to different projects (1001 to 5001
// "Website", 1002 to 5002 "Planning"), so an implementation that resolves one project and applies
// its name to every task fails here.
func TestFetchCarriesProjectName(t *testing.T) {
	clock := &ports.FixedClock{T: at(t, "2026-08-17T12:00:00Z")}
	f, srv := newFetcher(t, clock)
	defer srv.Close()

	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	want := map[string]string{"todoist:1001": "Website", "todoist:1002": "Planning"}
	got := map[string]string{}
	for _, it := range res.Items {
		got[it.ExternalID] = it.Repo
	}
	for id, name := range want {
		if got[id] != name {
			t.Errorf("item %s: project = %q, want %q (FR-4.1 AC1)", id, got[id], name)
		}
	}
}

// QS-2.5, QS-3.1: resolving project names must cost one request for all of them, not one per
// task. Two tasks survive the filter here; a per-task implementation would make two projects
// calls and still pass every assertion about the names.
func TestProjectsAreFetchedOnceForAllTasks(t *testing.T) {
	clock := &ports.FixedClock{T: at(t, "2026-08-17T12:00:00Z")}
	f, srv, count := countingFetcher(t, clock)
	defer srv.Close()

	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(res.Items))
	}
	if n := count("/rest/v2/projects"); n != 1 {
		t.Errorf("GET /rest/v2/projects called %d times, want exactly 1 for the whole fetch", n)
	}
	if n := count("/rest/v2/tasks"); n != 1 {
		t.Errorf("GET /rest/v2/tasks called %d times, want 1", n)
	}
}

// A fetch that keeps no task needs no project names, so it must not spend a request on them —
// the projects call is an extra cost on the refresh budget (QS-2.5) and buys nothing here. The
// clock is set years BEFORE every fixture due date — not after, which would make all of them
// overdue and keep them — so nothing survives the filter.
func TestNoProjectsRequestWhenNoTaskSurvives(t *testing.T) {
	clock := &ports.FixedClock{T: at(t, "2020-01-01T12:00:00Z")}
	f, srv, count := countingFetcher(t, clock)
	defer srv.Close()

	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Items) != 0 {
		t.Fatalf("len(items) = %d, want 0 — every fixture task is due well after 2020", len(res.Items))
	}
	if n := count("/rest/v2/projects"); n != 0 {
		t.Errorf("GET /rest/v2/projects called %d times, want 0 when no task survives", n)
	}
}

// QS-1.4 and the partial-result rule: the project lookup is a second call, and a second call must
// not be able to destroy the first. When only /rest/v2/projects fails, Fetch still returns every
// task, with a nil error — a non-nil error would make the runner discard the whole healthy fetch
// (see ports.SourceFetcher), blanking the tile over a missing label. The tasks simply carry no
// project name.
func TestProjectLookupFailureKeepsTasks(t *testing.T) {
	clock := &ports.FixedClock{T: at(t, "2026-08-17T12:00:00Z")}
	f, srv := newFetcher(t, clock)
	defer srv.Close()
	post(t, srv.URL+"/_control/fail?source=todoist&repo=projects&status=500")

	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch must not fail when only the project lookup does: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("len(items) = %d, want 2 — a failed project lookup must not lose tasks", len(res.Items))
	}
	for _, it := range res.Items {
		if it.Repo != "" {
			t.Errorf("item %s: project = %q, want empty when the lookup failed", it.ExternalID, it.Repo)
		}
	}
}

// todoistServer serves body verbatim (a hand-written JSON tasks array) at the exact path Fetch
// requests, ignoring query parameters. It exists so tests that need to control a task's exact due
// value can do so directly, without reshaping internal/fakesources' fixture — that fixture is a
// contract several other tasks depend on and is not this package's to rewrite for one test.
func todoistServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v2/tasks" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

// TestFetchLocationDiscriminatesDueTodayBoundary is Ruling 3: a nil Config.Location must be
// treated as UTC, never as time.Local, and when a location IS configured the end-of-day boundary
// must be computed in that zone rather than in UTC.
//
// A prior version of this test used clock=2026-08-18T04:00:00Z / Location=America/Los_Angeles
// with the shared fixture and asserted len(items)==2 — but an implementation that silently
// ignores Location and always uses UTC returns the same 2 items on that fixture, so the test
// could not have caught a regression back to UTC-only. This version picks a due instant that the
// two implementations genuinely disagree on:
//
// clock.Now() = 2026-08-17T12:00:00Z, Location = Asia/Tokyo (UTC+9). "Today" in Tokyo is still
// August 17th (12:00 UTC = 21:00 JST on the 17th). End of August 17th in Tokyo local time is
// 2026-08-17T23:59:59.999999999+09:00, which is 2026-08-17T14:59:59.999999999Z — so the Tokyo
// boundary (start of Aug 18 JST) falls at 2026-08-17T15:00:00Z. A task due at
// 2026-08-17T18:00:00Z is therefore:
//   - correct (location-aware) implementation: 18:00Z is AFTER the Tokyo boundary (15:00Z) ->
//     DROPPED, 0 items.
//   - buggy (UTC-only) implementation: "today" in UTC is also the 17th, so its boundary is start
//     of Aug 18 UTC (2026-08-18T00:00:00Z); 18:00Z is BEFORE that -> KEPT, 1 item.
//
// A test asserting 0 items therefore fails under the UTC-only implementation and passes only
// under one that actually threads Location through the boundary calculation.
func TestFetchLocationDiscriminatesDueTodayBoundary(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}

	const body = `[{"id":"9001","content":"discriminating task","priority":1,
		"url":"https://todoist.com/showTask?id=9001",
		"due":{"date":"2026-08-17","datetime":"2026-08-17T18:00:00Z"}}]`
	srv := todoistServer(t, body)
	defer srv.Close()

	f := todoist.New(todoist.Config{
		Token: "x", BaseURL: srv.URL, Filter: "overdue | today", Location: loc,
	}, srv.Client(), &ports.FixedClock{T: at(t, "2026-08-17T12:00:00Z")})

	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Items) != 0 {
		t.Fatalf("len(items) = %d, want 0 — a task due 2026-08-17T18:00:00Z is past the Tokyo "+
			"end-of-day boundary (2026-08-17T15:00:00Z) and must be dropped; an implementation "+
			"that ignores Location and compares against the UTC boundary instead would wrongly "+
			"keep it", len(res.Items))
	}
}

// TestFetchLocationNilDefaultsToUTC is the mirror of the test above: the very same due instant
// (2026-08-17T18:00:00Z), with the same clock, but Config.Location left nil. Nil must default to
// UTC (never time.Local — Ruling 3), whose boundary for "today" (2026-08-17, since clock.Now() is
// 12:00 UTC on the 17th) is 2026-08-18T00:00:00Z. 18:00Z is before that, so the task must be
// KEPT — the opposite outcome of the Tokyo case above, confirming the boundary genuinely moves
// with Location rather than the two tests coincidentally agreeing.
func TestFetchLocationNilDefaultsToUTC(t *testing.T) {
	const body = `[{"id":"9001","content":"discriminating task","priority":1,
		"url":"https://todoist.com/showTask?id=9001",
		"due":{"date":"2026-08-17","datetime":"2026-08-17T18:00:00Z"}}]`
	srv := todoistServer(t, body)
	defer srv.Close()

	f := todoist.New(todoist.Config{
		Token: "x", BaseURL: srv.URL, Filter: "overdue | today",
	}, srv.Client(), &ports.FixedClock{T: at(t, "2026-08-17T12:00:00Z")})

	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("len(items) = %d, want 1 — with Location nil (UTC), a task due 2026-08-17T18:00:00Z "+
			"is before the UTC end-of-day boundary (2026-08-18T00:00:00Z) and must be kept", len(res.Items))
	}
}

// TestFetchTieBreaksEqualDueAtByExternalID covers Fetch's sort comparator when two tasks share an
// identical DueAt: sort.Slice is not a stable sort, so a comparator that only compared DueAt
// (dropping the ExternalID tie-break) would let the two tasks' relative order vary from run to
// run — on a dashboard that reads as items moving around for no reason between refreshes. The
// fixture below serves task "9" before task "3" in the JSON array, specifically so that surviving
// in ExternalID order (todoist:3, then todoist:9) cannot be explained by "the sort just preserved
// input order" — it can only happen if the ExternalID tie-break actually ran.
func TestFetchTieBreaksEqualDueAtByExternalID(t *testing.T) {
	const body = `[
		{"id":"9","content":"second by id","priority":1,
		 "url":"https://todoist.com/showTask?id=9",
		 "due":{"date":"2026-08-17","datetime":"2026-08-17T10:00:00Z"}},
		{"id":"3","content":"first by id","priority":1,
		 "url":"https://todoist.com/showTask?id=3",
		 "due":{"date":"2026-08-17","datetime":"2026-08-17T10:00:00Z"}}
	]`
	srv := todoistServer(t, body)
	defer srv.Close()

	f := todoist.New(todoist.Config{
		Token: "x", BaseURL: srv.URL, Filter: "overdue | today",
	}, srv.Client(), &ports.FixedClock{T: at(t, "2026-08-17T12:00:00Z")})

	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(res.Items))
	}
	if !res.Items[0].DueAt.Equal(res.Items[1].DueAt) {
		t.Fatalf("test setup broken: DueAt values differ (%s vs %s), want identical", res.Items[0].DueAt, res.Items[1].DueAt)
	}
	if res.Items[0].ExternalID != "todoist:3" || res.Items[1].ExternalID != "todoist:9" {
		t.Errorf("ExternalID order = [%s, %s], want [todoist:3, todoist:9] — equal DueAt must "+
			"tie-break on ExternalID ascending, not on input/fetch order", res.Items[0].ExternalID, res.Items[1].ExternalID)
	}
}

// TestFetchNonOKStatusIsError is QS-4.3-adjacent (FR-4.1's non-negotiable half): a 500 must never
// decode into an empty, healthy-looking task list, and the error must name neither the token nor
// the Authorization header.
func TestFetchNonOKStatusIsError(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()
	post(t, srv.URL+"/_control/fail?source=todoist&status=500")

	f := todoist.New(todoist.Config{
		Token: "s3cr3t-token", BaseURL: srv.URL, Filter: "overdue | today",
	}, srv.Client(), &ports.FixedClock{T: at(t, "2026-08-17T12:00:00Z")})

	res, err := f.Fetch(context.Background())
	if err == nil {
		t.Fatal("want an error on a non-200 response")
	}
	if len(res.Items) != 0 {
		t.Errorf("len(items) = %d, want 0 on error — a failed fetch must not read as an empty, healthy dashboard", len(res.Items))
	}
	if strings.Contains(err.Error(), "s3cr3t-token") {
		t.Errorf("error %q must not contain the token (QS-4.3)", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "bearer") {
		t.Errorf("error %q must not contain the Authorization scheme (QS-4.3)", err)
	}
}

func TestName(t *testing.T) {
	f := todoist.New(todoist.Config{}, http.DefaultClient, &ports.FixedClock{})
	if got := f.Name(); got != "todoist" {
		t.Errorf("Name() = %q, want todoist", got)
	}
}

// post posts an empty body to url and fails the test on error or a non-2xx status.
func post(t *testing.T, url string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		t.Fatalf("post %s: status = %d, want < 300", url, resp.StatusCode)
	}
}
