// Package plausible implements ports.SourceFetcher for Plausible Analytics: visitors and
// pageviews over rolling 7- and 30-day windows, each compared against the immediately preceding
// period (FR-3.1). Plausible's stats API is a handful of JSON requests, so this package is
// written against net/http and encoding/json rather than a client library (no new dependency).
package plausible

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// defaultBaseURL is used when Config.BaseURL is empty.
const defaultBaseURL = "https://plausible.io"

// windows are the two comparison periods FR-3.1 requires, and the Plausible "period" query value
// each maps to. Order matters: it fixes the order metrics are returned in for a given site.
var windows = []struct {
	days   int
	period string
}{
	{7, "7d"},
	{30, "30d"},
}

// Config configures access to Plausible for Fetcher.
type Config struct {
	APIKey  string
	BaseURL string   // "" -> https://plausible.io
	Sites   []string // Plausible site IDs, e.g. "example.org"
}

// Fetcher fetches visitors and pageviews for the sites in Config, over Plausible's stats API
// (FR-3.1).
type Fetcher struct {
	apiKey  string
	baseURL string
	sites   []string
	hc      *http.Client
}

// New builds a Fetcher from cfg. hc supplies the transport (as in this package's tests, which
// pass an httptest server's client). When cfg.BaseURL is non-empty, requests go to that URL
// (used to point at a fake); otherwise they go to Plausible's public API.
func New(cfg Config, hc *http.Client) *Fetcher {
	base := cfg.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	return &Fetcher{apiKey: cfg.APIKey, baseURL: base, sites: cfg.Sites, hc: hc}
}

// Name identifies this fetcher's source.
func (f *Fetcher) Name() string { return "plausible" }

// Fetch retrieves a 7-day and a 30-day metric, each with a comparison to the preceding period,
// for every site in f.sites. A failure fetching one site or one window does not lose metrics
// already fetched from the others (QS-1.4): every per-site, per-window error is collected,
// joined with errors.Join, and returned alongside every metric successfully fetched — never
// returned early on the first failure. Sites are visited in the order configured, and for each
// site the 7-day window is fetched before the 30-day window, so the result order is deterministic
// (Task 13 pairs metrics by that order).
func (f *Fetcher) Fetch(ctx context.Context) (ports.FetchResult, error) {
	var metrics []domain.Metric
	var errs []error

	for _, site := range f.sites {
		for _, w := range windows {
			m, err := f.fetchWindow(ctx, site, w.days, w.period)
			if err != nil {
				errs = append(errs, fmt.Errorf("plausible %s (%dd): %w", site, w.days, err))
				continue
			}
			metrics = append(metrics, m)
		}
	}

	return ports.FetchResult{Metrics: metrics}, errors.Join(errs...)
}

// aggregateResponse is shaped exactly as
// GET /api/v1/stats/aggregate?metrics=visitors,pageviews&compare=previous_period responds.
type aggregateResponse struct {
	Results struct {
		Visitors  metricValue `json:"visitors"`
		Pageviews metricValue `json:"pageviews"`
	} `json:"results"`
}

// metricValue is one metric's current value and its comparison-period value.
type metricValue struct {
	Value           int `json:"value"`
	ComparisonValue int `json:"comparison_value"`
}

// fetchWindow fetches one site's aggregate visitors and pageviews for period (days used only to
// populate the returned Metric), with the comparison to the immediately preceding period.
//
// A non-200 response is treated as an error without decoding the body as JSON — a 500 with an
// HTML body must never be silently decoded into a zero-valued Metric, since a zero visitor count
// rendered as real data is worse than a visible error. The response body is always read and
// closed, whichever path is taken, so the underlying connection can be reused. No error returned
// from here names the API key, the Authorization header, or a request dump (QS-4.3); the caller
// adds the site and window.
func (f *Fetcher) fetchWindow(ctx context.Context, site string, days int, period string) (domain.Metric, error) {
	q := url.Values{}
	q.Set("site_id", site)
	q.Set("period", period)
	q.Set("metrics", "visitors,pageviews")
	q.Set("compare", "previous_period")
	reqURL := f.baseURL + "/api/v1/stats/aggregate?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return domain.Metric{}, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.apiKey)

	resp, err := f.hc.Do(req)
	if err != nil {
		return domain.Metric{}, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return domain.Metric{}, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var body aggregateResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return domain.Metric{}, fmt.Errorf("decoding response: %w", err)
	}

	// FetchedAt is deliberately left zero: Store.UpsertMetrics stamps every row with the run's
	// `now` and never reads the value carried here, so anything set would be overwritten before it
	// could be seen. Filling it in would mean calling time.Now outside ports.SystemClock — the one
	// place in this system allowed to read the wall clock — to produce a value nothing uses.
	return domain.Metric{
		Site:          site,
		WindowDays:    days,
		Visitors:      body.Results.Visitors.Value,
		Pageviews:     body.Results.Pageviews.Value,
		PrevVisitors:  body.Results.Visitors.ComparisonValue,
		PrevPageviews: body.Results.Pageviews.ComparisonValue,
	}, nil
}
