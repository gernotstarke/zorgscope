package todoist_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if overdue.Repo != "" || overdue.Number != 0 || overdue.Author != "" {
		t.Errorf("Repo/Number/Author must stay empty for a task: %+v", overdue)
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

// TestFetchAppliesConfiguredLocation is Ruling 3: a nil Config.Location must be treated as UTC,
// never as time.Local — but when a location is configured, the end-of-day boundary that decides
// what counts as "due today" is computed in that zone. Anchoring the clock a few hours into
// 2026-08-18 UTC keeps the fake's fixed fixture (dated for 2026-08-17T12:00:00Z) exercising the
// same two tasks, while proving the boundary shifts with the zone rather than staying pinned to
// UTC midnight.
func TestFetchAppliesConfiguredLocation(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}

	// 2026-08-18T04:00:00Z is 2026-08-17T21:00:00-07:00 — still August 17th in Los Angeles, so
	// "due today" (2026-08-17) must still be included, even though UTC has already rolled to the
	// 18th.
	clock := &ports.FixedClock{T: at(t, "2026-08-18T04:00:00Z")}
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	f := todoist.New(todoist.Config{
		Token: "x", BaseURL: srv.URL, Filter: "overdue | today", Location: loc,
	}, srv.Client(), clock)

	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("len(items) = %d, want 2 (overdue + due-today, evaluated in America/Los_Angeles)", len(res.Items))
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
