// Package plausible talks to Plausible's Stats API v2.
package plausible

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gernotstarke/zorgscope/internal/ports"
)

// Client is the small authenticated HTTP client shared by Plausible site fetchers.
type Client struct {
	http     *http.Client
	baseURL  string
	apiKey   string
	clock    ports.Clock
	location *time.Location
}

// NewClient creates a Plausible Stats API v2 client.
// location is the reporting-calendar timezone used to construct explicit comparison ranges; it
// defaults to UTC for compatibility with adapter tests and callers without resolved config.
func NewClient(hc *http.Client, baseURL, apiKey string, clock ports.Clock, locations ...*time.Location) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	location := time.UTC
	if len(locations) > 0 && locations[0] != nil {
		location = locations[0]
	}
	return &Client{http: hc, baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, clock: clock, location: location}
}

type query struct {
	SiteID     string          `json:"site_id"`
	Metrics    []string        `json:"metrics"`
	DateRange  any             `json:"date_range"`
	Dimensions []string        `json:"dimensions,omitempty"`
	OrderBy    [][]string      `json:"order_by,omitempty"`
	Include    map[string]bool `json:"include,omitempty"`
	Pagination *pagination     `json:"pagination,omitempty"`
}

type pagination struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

type queryResponse struct {
	Results []resultRow `json:"results"`
	Meta    struct {
		TimeLabels []string `json:"time_labels"`
	} `json:"meta"`
}

type resultRow struct {
	Dimensions []string          `json:"dimensions"`
	Metrics    []json.RawMessage `json:"metrics"`
}

func (c *Client) do(ctx context.Context, q query) (queryResponse, error) {
	var out queryResponse
	body, err := json.Marshal(q)
	if err != nil {
		return out, fmt.Errorf("%w: encode Plausible query: %v", ports.ErrPermanent, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v2/query", bytes.NewReader(body))
	if err != nil {
		return out, fmt.Errorf("%w: build Plausible request: %v", ports.ErrPermanent, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return out, fmt.Errorf("%w: Plausible request failed: %v", ports.ErrTransient, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := c.mapStatus(resp); err != nil {
		return out, err
	}
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("%w: decode Plausible response: %v", ports.ErrPermanent, err)
	}
	return out, nil
}

func (c *Client) mapStatus(resp *http.Response) error {
	switch code := resp.StatusCode; {
	case code >= 200 && code < 300:
		return nil
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return fmt.Errorf("%w: Plausible returned %d", ports.ErrAuth, code)
	case code == http.StatusTooManyRequests:
		reset := c.now().Add(15 * time.Minute)
		if v := resp.Header.Get("Retry-After"); v != "" {
			if seconds, err := strconv.Atoi(v); err == nil && seconds >= 0 {
				reset = c.now().Add(time.Duration(seconds) * time.Second)
			} else if at, err := http.ParseTime(v); err == nil {
				reset = at
			}
		}
		return &ports.RateLimitedError{ResetAt: reset}
	case code >= 500:
		return fmt.Errorf("%w: Plausible returned %d", ports.ErrTransient, code)
	default:
		return fmt.Errorf("%w: Plausible returned %d", ports.ErrPermanent, code)
	}
}

func (c *Client) now() time.Time {
	if c.clock == nil {
		return time.Time{}
	}
	return c.clock.Now()
}
