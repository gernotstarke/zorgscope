package plausible

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

var siteIDPattern = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)

// SiteFetcher fetches the dashboard metrics for one Plausible site.
type SiteFetcher struct {
	client *Client
	siteID string
}

// NewSiteFetcher validates a bare Plausible site ID and creates its fetcher.
func NewSiteFetcher(client *Client, siteID string) (*SiteFetcher, error) {
	if client == nil {
		return nil, fmt.Errorf("%w: Plausible client is required", ports.ErrPermanent)
	}
	if !siteIDPattern.MatchString(siteID) || strings.HasPrefix(siteID, ".") || strings.HasSuffix(siteID, ".") {
		return nil, fmt.Errorf("%w: Plausible site %q must be a bare hostname", ports.ErrPermanent, siteID)
	}
	return &SiteFetcher{client: client, siteID: siteID}, nil
}

// ID implements ports.SourceFetcher.
func (f *SiteFetcher) ID() string { return "plausible:" + f.siteID }

// Kind implements ports.SourceFetcher.
func (f *SiteFetcher) Kind() string { return ports.KindPlausibleSite }

// Fetch implements ports.SourceFetcher. Plausible v2 has no comparison include option, so the
// previous 7- and 30-day periods are queried explicitly and compared locally.
func (f *SiteFetcher) Fetch(ctx context.Context) ([]domain.Item, error) {
	instant := f.client.now()
	now := instant.UTC()
	reportingNow := instant.In(f.client.location)
	current7, previous7 := dateRanges(reportingNow, 7)
	current30, previous30 := dateRanges(reportingNow, 30)

	cur7, err := f.aggregate(ctx, current7, []string{"visitors"})
	if err != nil {
		return nil, err
	}
	prev7, err := f.aggregate(ctx, previous7, []string{"visitors"})
	if err != nil {
		return nil, err
	}
	cur30, err := f.aggregate(ctx, current30, []string{"visitors", "pageviews"})
	if err != nil {
		return nil, err
	}
	prev30, err := f.aggregate(ctx, previous30, []string{"visitors"})
	if err != nil {
		return nil, err
	}
	daily, err := f.daily(ctx, current30)
	if err != nil {
		return nil, err
	}
	topPages, err := f.topPages(ctx)
	if err != nil {
		return nil, err
	}

	payload := domain.MetricSeriesPayload{
		Visitors7d:       cur7[0],
		Visitors30d:      cur30[0],
		Pageviews30d:     cur30[1],
		DeltaVisitors7d:  delta(cur7[0], prev7[0]),
		DeltaVisitors30d: delta(cur30[0], prev30[0]),
		Daily:            daily,
		TopPages:         topPages,
	}
	item := domain.Item{
		ID:        domain.ItemID{SourceID: f.ID(), ExternalID: "summary"},
		Kind:      domain.KindMetricSeries,
		Title:     f.siteID,
		URL:       strings.TrimRight(f.client.baseURL, "/") + "/" + url.PathEscape(f.siteID),
		CreatedAt: now,
		UpdatedAt: now,
		Payload:   domain.MustPayload(payload),
	}
	return []domain.Item{item}, nil
}

func dateRanges(now time.Time, days int) (current, previous []string) {
	end := dateOnly(now)
	startTime := now.AddDate(0, 0, -(days - 1))
	start := dateOnly(startTime)
	previousEnd := dateOnly(startTime.AddDate(0, 0, -1))
	previousStart := dateOnly(startTime.AddDate(0, 0, -days))
	return []string{start, end}, []string{previousStart, previousEnd}
}

func dateOnly(t time.Time) string { return t.Format("2006-01-02") }

func (f *SiteFetcher) aggregate(ctx context.Context, dateRange []string, metrics []string) ([]int, error) {
	resp, err := f.client.do(ctx, query{SiteID: f.siteID, Metrics: metrics, DateRange: dateRange})
	if err != nil {
		return nil, err
	}
	if len(resp.Results) != 1 || len(resp.Results[0].Metrics) != len(metrics) {
		return nil, fmt.Errorf("%w: Plausible aggregate returned an unexpected result shape", ports.ErrPermanent)
	}
	values := make([]int, len(metrics))
	for i := range metrics {
		values[i], err = integerMetric(resp.Results[0].Metrics[i])
		if err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (f *SiteFetcher) daily(ctx context.Context, dateRange []string) ([]int, error) {
	resp, err := f.client.do(ctx, query{
		SiteID: f.siteID, Metrics: []string{"visitors"}, DateRange: dateRange,
		Dimensions: []string{"time:day"}, Include: map[string]bool{"time_labels": true},
	})
	if err != nil {
		return nil, err
	}
	byDay := make(map[string]int, len(resp.Results))
	for _, row := range resp.Results {
		if len(row.Dimensions) != 1 || len(row.Metrics) != 1 {
			return nil, fmt.Errorf("%w: Plausible timeseries returned an unexpected result shape", ports.ErrPermanent)
		}
		value, err := integerMetric(row.Metrics[0])
		if err != nil {
			return nil, err
		}
		byDay[dayLabel(row.Dimensions[0])] = value
	}
	labels := resp.Meta.TimeLabels
	if len(labels) == 0 {
		labels = enumerateDates(dateRange[0], dateRange[1])
	}
	values := make([]int, 0, len(labels))
	for _, label := range labels {
		values = append(values, byDay[dayLabel(label)])
	}
	return values, nil
}

func (f *SiteFetcher) topPages(ctx context.Context) ([]domain.MetricPage, error) {
	resp, err := f.client.do(ctx, query{
		SiteID: f.siteID, Metrics: []string{"visitors"}, DateRange: "7d",
		Dimensions: []string{"event:page"}, OrderBy: [][]string{{"visitors", "desc"}},
		Pagination: &pagination{Limit: 10},
	})
	if err != nil {
		return nil, err
	}
	pages := make([]domain.MetricPage, 0, len(resp.Results))
	for _, row := range resp.Results {
		if len(row.Dimensions) != 1 || len(row.Metrics) != 1 {
			return nil, fmt.Errorf("%w: Plausible top-pages query returned an unexpected result shape", ports.ErrPermanent)
		}
		visitors, err := integerMetric(row.Metrics[0])
		if err != nil {
			return nil, err
		}
		pages = append(pages, domain.MetricPage{Path: row.Dimensions[0], Visitors: visitors})
	}
	return pages, nil
}

func integerMetric(raw json.RawMessage) (int, error) {
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value != math.Trunc(value) || value > float64(math.MaxInt) {
		return 0, fmt.Errorf("%w: Plausible returned a non-integer metric", ports.ErrPermanent)
	}
	return int(value), nil
}

func delta(current, previous int) float64 {
	if previous == 0 {
		return 0
	}
	return float64(current-previous) / float64(previous)
}

func dayLabel(s string) string {
	if len(s) >= len("2006-01-02") {
		return s[:len("2006-01-02")]
	}
	return s
}

func enumerateDates(first, last string) []string {
	start, err1 := time.Parse("2006-01-02", first)
	end, err2 := time.Parse("2006-01-02", last)
	if err1 != nil || err2 != nil || end.Before(start) {
		return nil
	}
	var out []string
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		out = append(out, dateOnly(d))
	}
	return out
}
