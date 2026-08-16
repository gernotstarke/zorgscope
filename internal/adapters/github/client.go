// Package github talks to the GitHub GraphQL and REST APIs (ADR-0011).
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gernotstarke/zorgscope/internal/ports"
)

// TokenCredentialName is the name under which the token expiry is reported (FR-11.2).
const TokenCredentialName = "zorgscope GitHub token"

const expiryHeader = "GitHub-Authentication-Token-Expiration"

// Client is a minimal authenticated GitHub HTTP client shared by all fetchers.
type Client struct {
	http    *http.Client
	baseURL string
	token   string
	sink    ports.CredentialSink

	mu           sync.Mutex
	lastReported string
}

// NewClient creates a client. baseURL is "https://api.github.com" in production, the fake in tests.
// sink may be nil.
func NewClient(hc *http.Client, baseURL, token string, sink ports.CredentialSink) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{http: hc, baseURL: strings.TrimRight(baseURL, "/"), token: token, sink: sink}
}

// do performs a request, maps HTTP errors to port errors and decodes JSON into out (if non-nil).
func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) (http.Header, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "zorgscope (+https://github.com/gernotstarke/zorgscope)")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ports.ErrTransient, err)
	}
	defer func() { _ = resp.Body.Close() }()
	c.noteExpiry(resp.Header)
	if err := mapStatus(resp); err != nil {
		return resp.Header, err
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.Header, fmt.Errorf("%w: decode: %v", ports.ErrPermanent, err)
		}
	}
	return resp.Header, nil
}

func mapStatus(resp *http.Response) error {
	code := resp.StatusCode
	switch {
	case code >= 200 && code < 300:
		return nil
	case code == http.StatusUnauthorized:
		return fmt.Errorf("%w: github 401", ports.ErrAuth)
	case code == http.StatusForbidden || code == http.StatusTooManyRequests:
		if resp.Header.Get("X-RateLimit-Remaining") == "0" || code == http.StatusTooManyRequests {
			reset := time.Now().Add(15 * time.Minute)
			if v, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil && v > 0 {
				reset = time.Unix(v, 0)
			}
			return &ports.RateLimitedError{ResetAt: reset}
		}
		return fmt.Errorf("%w: github 403", ports.ErrAuth)
	case code == http.StatusNotFound:
		return fmt.Errorf("%w: github 404", ports.ErrPermanent)
	case code >= 500:
		return fmt.Errorf("%w: github %d", ports.ErrTransient, code)
	default:
		return fmt.Errorf("%w: github %d", ports.ErrPermanent, code)
	}
}

// noteExpiry reports the token expiration header to the sink once per distinct value.
func (c *Client) noteExpiry(h http.Header) {
	if c.sink == nil {
		return
	}
	v := h.Get(expiryHeader)
	if v == "" {
		return
	}
	c.mu.Lock()
	changed := v != c.lastReported
	c.lastReported = v
	c.mu.Unlock()
	if !changed {
		return
	}
	for _, layout := range []string{"2006-01-02 15:04:05 MST", "2006-01-02 15:04:05 -0700", time.RFC3339} {
		if t, err := time.Parse(layout, v); err == nil {
			t = t.UTC()
			c.sink.ReportCredential(TokenCredentialName, &t, "zorgscope")
			return
		}
	}
}

// graphql posts a query and decodes response.data into out.
func (c *Client) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if _, err := c.do(ctx, http.MethodPost, "/graphql", body, &resp); err != nil {
		return err
	}
	if len(resp.Errors) > 0 && (len(resp.Data) == 0 || string(resp.Data) == "null") {
		msgs := make([]string, 0, len(resp.Errors))
		for _, e := range resp.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("%w: graphql: %s", ports.ErrPermanent, strings.Join(msgs, "; "))
	}
	if err := json.Unmarshal(resp.Data, out); err != nil {
		return fmt.Errorf("%w: graphql decode: %v", ports.ErrPermanent, err)
	}
	return nil
}
