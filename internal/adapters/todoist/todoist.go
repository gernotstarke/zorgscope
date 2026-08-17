// Package todoist implements ports.SourceFetcher for Todoist: the tasks that are overdue or due
// today, and nothing else (FR-4.1). Todoist's REST v2 API is a single JSON request, so this
// package is written against net/http and encoding/json rather than a client library (no new
// dependency).
package todoist

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// defaultBaseURL is used when Config.BaseURL is empty.
const defaultBaseURL = "https://api.todoist.com"

// tasksPath is the REST v2 endpoint this package reads.
const tasksPath = "/rest/v2/tasks"

// Config configures access to Todoist for Fetcher.
type Config struct {
	Token   string
	BaseURL string // "" -> https://api.todoist.com
	Filter  string // Todoist filter string, e.g. "overdue | today"

	// Location is the timezone "due today" is evaluated in. A nil Location is treated as UTC —
	// never as time.Local: the process runs in a container whose zone is not necessarily the
	// user's, and "due today" computed in the wrong zone is wrong by up to a day at exactly the
	// hours the answer matters most. internal/config already loads and validates a "timezone"
	// setting into a *time.Location for the caller to pass here.
	Location *time.Location
}

// Fetcher fetches the Todoist tasks that are overdue or due today (FR-4.1).
type Fetcher struct {
	token   string
	baseURL string
	filter  string
	loc     *time.Location
	hc      *http.Client
	clock   ports.Clock
}

// New builds a Fetcher from cfg. hc supplies the transport (as in this package's tests, which
// pass an httptest server's client). When cfg.BaseURL is non-empty, requests go to that URL (used
// to point at a fake); otherwise they go to Todoist's public API. clock supplies "now" for the
// due-today boundary, so tests can fix it instead of depending on the real wall clock.
func New(cfg Config, hc *http.Client, clock ports.Clock) *Fetcher {
	base := cfg.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	loc := cfg.Location
	if loc == nil {
		loc = time.UTC
	}
	return &Fetcher{
		token:   cfg.Token,
		baseURL: base,
		filter:  cfg.Filter,
		loc:     loc,
		hc:      hc,
		clock:   clock,
	}
}

// Name identifies this fetcher's source.
func (f *Fetcher) Name() string { return "todoist" }

// Fetch retrieves every task Todoist's own filter matches, then filters again in Go against
// f.clock.Now() in f.loc: only tasks whose due instant falls before the end of today survive.
// Filtering twice exists because Todoist's filter strings are user-editable and easy to get
// subtly wrong (a stray character silently changes what "overdue | today" means), and a task that
// should not be on the dashboard is worse than one missing from it — the upstream filter is an
// optimisation that trims the response, the Go-side filter is the actual guarantee (FR-4.1 AC3).
// Tasks without a due date at all are out of scope and dropped. The surviving items are sorted by
// DueAt ascending, with ExternalID as a deterministic tie-break, so overdue items precede
// due-today items and the order is stable across fetches (FR-4.1 AC2).
func (f *Fetcher) Fetch(ctx context.Context) (ports.FetchResult, error) {
	tasks, err := f.fetchTasks(ctx)
	if err != nil {
		return ports.FetchResult{}, err
	}

	now := f.clock.Now().In(f.loc)
	y, m, d := now.Date()
	startOfTomorrow := time.Date(y, m, d+1, 0, 0, 0, 0, f.loc)

	var items []domain.Item
	for _, tk := range tasks {
		if tk.Due == nil {
			continue // no due date: out of scope (FR-4.1 AC3)
		}
		dueAt, err := tk.Due.instant(f.loc)
		if err != nil {
			return ports.FetchResult{}, fmt.Errorf("todoist: task %s: %w", tk.ID, err)
		}
		if !dueAt.Before(startOfTomorrow) {
			continue // due later than today: out of scope (FR-4.1 AC3)
		}
		items = append(items, tk.toItem(dueAt))
	}

	sort.Slice(items, func(i, j int) bool {
		if !items[i].DueAt.Equal(items[j].DueAt) {
			return items[i].DueAt.Before(items[j].DueAt)
		}
		return items[i].ExternalID < items[j].ExternalID
	})

	return ports.FetchResult{Items: items}, nil
}

// fetchTasks issues GET {baseURL}/rest/v2/tasks?filter={url-encoded filter} with the configured
// bearer token and decodes the response as a plain JSON array of tasks.
//
// A non-200 response is treated as an error without decoding the body as JSON — a 500 with an
// HTML body must never be silently decoded into an empty task list, since an empty dashboard
// reads as "nothing due", the worst failure this adapter can produce: it is indistinguishable
// from a healthy one. The response body is always closed, on every return path. No error returned
// from here names the token or the Authorization header (QS-4.3); it names only the filter or the
// request.
func (f *Fetcher) fetchTasks(ctx context.Context) ([]todoistTask, error) {
	q := url.Values{}
	q.Set("filter", f.filter)
	reqURL := f.baseURL + tasksPath + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("todoist: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.token)

	resp, err := f.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("todoist: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("todoist: unexpected status %d for filter %q", resp.StatusCode, f.filter)
	}

	var tasks []todoistTask
	if err := json.NewDecoder(resp.Body).Decode(&tasks); err != nil {
		return nil, fmt.Errorf("todoist: decoding response: %w", err)
	}
	return tasks, nil
}

// todoistTask is one element of Todoist REST v2's GET /rest/v2/tasks array.
type todoistTask struct {
	ID       string      `json:"id"`
	Content  string      `json:"content"`
	Priority int         `json:"priority"`
	URL      string      `json:"url"`
	Due      *todoistDue `json:"due"`
}

// todoistDue is Todoist's "due" object. Exactly one of the two shapes is meaningful at a time:
// Datetime, when set, is a full RFC 3339 instant and takes precedence; Date, when Datetime is
// empty, is a floating calendar date with no time component.
type todoistDue struct {
	Date     string `json:"date"`
	Datetime string `json:"datetime"`
}

// instant resolves d to the UTC instant Fetch compares against and stores as Item.DueAt.
//
// due.datetime is a full RFC 3339 instant — used as given, converted to UTC.
//
// due.date is a floating date like "2026-08-17" with no time of day. It is interpreted as the end
// of that day in loc, not its start: a task due "today" must not read as already overdue at
// 00:01, which is what comparing against midnight would do.
func (d *todoistDue) instant(loc *time.Location) (time.Time, error) {
	if d.Datetime != "" {
		t, err := time.Parse(time.RFC3339, d.Datetime)
		if err != nil {
			return time.Time{}, fmt.Errorf("parsing due.datetime: %w", err)
		}
		return t.UTC(), nil
	}

	day, err := time.ParseInLocation("2006-01-02", d.Date, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing due.date: %w", err)
	}
	y, m, dd := day.Date()
	endOfDay := time.Date(y, m, dd, 23, 59, 59, 999999999, loc)
	return endOfDay.UTC(), nil
}

// toItem maps t to a domain.Item, given the due instant already resolved by instant(). Repo,
// Number and Author stay empty: Todoist tasks have none of those. FirstSeenAt is left zero too —
// the store owns it, and an adapter writing it would break the one invariant the product depends
// on (FR-5.3).
func (t todoistTask) toItem(dueAt time.Time) domain.Item {
	return domain.Item{
		Source:     "todoist",
		ExternalID: "todoist:" + t.ID,
		Kind:       domain.KindTask,
		Title:      t.Content,
		URL:        t.URL,
		Priority:   t.Priority,
		DueAt:      dueAt,
	}
}
