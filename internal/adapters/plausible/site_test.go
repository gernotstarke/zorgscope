package plausible

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/clock"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

var testNow = time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

func TestSiteFetcherQueriesStatsAPIV2AndMapsMetricSeries(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/query" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-key" {
			t.Fatalf("Authorization = %q", got)
		}
		var q query
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			t.Fatal(err)
		}
		if q.SiteID != "arc42.org" {
			t.Fatalf("site_id = %q", q.SiteID)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case len(q.Dimensions) == 1 && q.Dimensions[0] == "time:day":
			labels := enumerateDates("2026-07-18", "2026-08-16")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []any{
					map[string]any{"dimensions": []string{"2026-07-18"}, "metrics": []int{3}},
					map[string]any{"dimensions": []string{"2026-08-16"}, "metrics": []int{9}},
				},
				"meta": map[string]any{"time_labels": labels},
			})
		case len(q.Dimensions) == 1 && q.Dimensions[0] == "event:page":
			if q.DateRange != "7d" || q.Pagination == nil || q.Pagination.Limit != 10 {
				t.Fatalf("top pages query = %+v", q)
			}
			_, _ = w.Write([]byte(`{"results":[{"dimensions":["/docs"],"metrics":[42]},{"dimensions":["/"],"metrics":[31]}],"meta":{}}`))
		default:
			rangeKey := fmt.Sprint(q.DateRange)
			var metrics []int
			switch rangeKey {
			case "[2026-08-10 2026-08-16]":
				metrics = []int{70}
			case "[2026-08-03 2026-08-09]":
				metrics = []int{50}
			case "[2026-07-18 2026-08-16]":
				metrics = []int{300, 600}
			case "[2026-06-18 2026-07-17]":
				metrics = []int{250}
			default:
				t.Fatalf("unexpected aggregate date range %v", q.DateRange)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []any{map[string]any{"dimensions": []string{}, "metrics": metrics}},
				"meta":    map[string]any{},
			})
		}
	}))
	t.Cleanup(srv.Close)

	client := NewClient(srv.Client(), srv.URL, "secret-key", clock.NewFake(testNow))
	fetcher, err := NewSiteFetcher(client, "arc42.org")
	if err != nil {
		t.Fatal(err)
	}
	if fetcher.ID() != "plausible:arc42.org" || fetcher.Kind() != ports.KindPlausibleSite {
		t.Fatalf("id/kind = %s/%s", fetcher.ID(), fetcher.Kind())
	}
	items, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if requests != 6 || len(items) != 1 {
		t.Fatalf("requests/items = %d/%d", requests, len(items))
	}
	item := items[0]
	if item.Kind != domain.KindMetricSeries || item.ID.ExternalID != "summary" || item.Title != "arc42.org" || !item.UpdatedAt.Equal(testNow) {
		t.Fatalf("item = %+v", item)
	}
	p, err := domain.DecodePayload[domain.MetricSeriesPayload](item)
	if err != nil {
		t.Fatal(err)
	}
	if p.Visitors7d != 70 || p.Visitors30d != 300 || p.Pageviews30d != 600 || p.DeltaVisitors7d != .4 || p.DeltaVisitors30d != .2 {
		t.Fatalf("payload aggregates = %+v", p)
	}
	if len(p.Daily) != 30 || p.Daily[0] != 3 || p.Daily[1] != 0 || p.Daily[29] != 9 {
		t.Fatalf("daily = %#v", p.Daily)
	}
	if len(p.TopPages) != 2 || p.TopPages[0].Path != "/docs" || p.TopPages[0].Visitors != 42 {
		t.Fatalf("top pages = %+v", p.TopPages)
	}
}

func TestSiteFetcherClassifiesHTTPAndResponseErrors(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		want        error
		rateLimited bool
	}{
		{name: "unauthorized", status: 401, want: ports.ErrAuth},
		{name: "forbidden", status: 403, want: ports.ErrAuth},
		{name: "rate limited", status: 429, rateLimited: true},
		{name: "server error", status: 502, want: ports.ErrTransient},
		{name: "bad request", status: 400, want: ports.ErrPermanent},
		{name: "malformed json", status: 200, body: `{`, want: ports.ErrPermanent},
		{name: "bad shape", status: 200, body: `{"results":[]}`, want: ports.ErrPermanent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.status == 429 {
					w.Header().Set("Retry-After", "90")
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			f, _ := NewSiteFetcher(NewClient(srv.Client(), srv.URL, "key", clock.NewFake(testNow)), "arc42.org")
			_, err := f.Fetch(context.Background())
			if tt.rateLimited {
				rl, ok := ports.AsRateLimited(err)
				if !ok || !rl.ResetAt.Equal(testNow.Add(90*time.Second)) {
					t.Fatalf("rate limit error = %v", err)
				}
			} else if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestNewSiteFetcherRejectsInvalidSite(t *testing.T) {
	c := NewClient(nil, "https://plausible.io", "key", clock.NewFake(testNow))
	for _, site := range []string{"", "https://arc42.org", "arc42.org/path", ".arc42.org"} {
		if _, err := NewSiteFetcher(c, site); !errors.Is(err, ports.ErrPermanent) {
			t.Fatalf("site %q: %v", site, err)
		}
	}
}
