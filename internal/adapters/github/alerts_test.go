package github_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/github"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/fakesources"
)

// countingFake serves the fake GitHub and counts GraphQL requests, and how many of them asked for
// alerts.
type countingFake struct {
	mu            sync.Mutex
	requests      int
	alertRequests int
	srv           *httptest.Server
}

func newCountingFake(t *testing.T) *countingFake {
	t.Helper()
	c := &countingFake{}
	fake := fakesources.NewServer()
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			body, _ := io.ReadAll(r.Body)
			c.mu.Lock()
			c.requests++
			if strings.Contains(string(body), "vulnerabilityAlerts") {
				c.alertRequests++
			}
			c.mu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		fake.ServeHTTP(w, r)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *countingFake) counts() (requests, alertRequests int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, a := c.requests, c.alertRequests
	c.requests, c.alertRequests = 0, 0
	return r, a
}

func (c *countingFake) fetcher(repos ...string) *github.IssueFetcher {
	return github.NewIssueFetcher(github.Config{Token: "x", BaseURL: c.srv.URL + "/graphql", Repos: repos}, c.srv.Client())
}

// FR-1.16 AC1: an alert arrives as an item of kind Alert, every field mapped, after the repository's
// pull requests.
func TestAlertsAreFetchedAsItems(t *testing.T) {
	c := newCountingFake(t)
	got, err := c.fetcher("org/alerts").Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Coverage["org/alerts"] != domain.CoverageOn {
		t.Errorf("coverage = %v, want on", got.Coverage["org/alerts"])
	}
	if len(got.Items) != 3 || got.Items[0].Kind != domain.KindPR {
		t.Fatalf("items = %+v, want the pull request then two alerts", got.Items)
	}

	high := got.Items[1]
	want := domain.Item{
		Kind:       domain.KindAlert,
		Repo:       "org/alerts",
		Number:     62,
		Title:      "rubyzip path traversal vulnerability",
		Summary:    "rubyzip 2.3.2 → 3.4.0 · Gemfile.lock",
		Advisories: []string{"GHSA-47m2-wp7j-p9vc", "CVE-2026-85396"},
		URL:        "https://github.com/org/alerts/security/dependabot/62",
		Author:     "dependabot",
		State:      "OPEN",
		CreatedAt:  time.Date(2026, 9, 26, 6, 24, 52, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 9, 26, 6, 24, 52, 0, time.UTC),
	}
	facts := domain.AlertFacts{
		Severity: domain.SeverityHigh, Package: "rubyzip", Ecosystem: "RUBYGEMS",
		Manifest: "Gemfile.lock", Vulnerable: "= 2.3.2", PatchedIn: "3.4.0", FixPR: 30,
	}
	if high.Alert == nil || *high.Alert != facts {
		t.Errorf("Alert = %+v, want %+v", high.Alert, facts)
	}
	high.Alert = nil
	if high.Kind != want.Kind || high.Repo != want.Repo || high.Number != want.Number ||
		high.Title != want.Title || high.Summary != want.Summary || high.URL != want.URL ||
		high.Author != want.Author || high.State != want.State ||
		!high.CreatedAt.Equal(want.CreatedAt) || !high.UpdatedAt.Equal(want.UpdatedAt) ||
		strings.Join(high.Advisories, ",") != strings.Join(want.Advisories, ",") {
		t.Errorf("alert = %+v\nwant    %+v", high, want)
	}

	low := got.Items[2]
	if low.Alert == nil || low.Alert.Severity != domain.SeverityLow || low.Alert.FixPR != 0 || low.Alert.PatchedIn != "" {
		t.Errorf("low alert facts = %+v", low.Alert)
	}
	if low.Summary != "json 2.20.0 · no patched version · Gemfile.lock" {
		t.Errorf("low alert summary = %q", low.Summary)
	}
}

// FR-1.16 AC5: a repository with alerts switched off says so, and still brings its pull requests.
func TestAlertsSwitchedOffAreReportedOff(t *testing.T) {
	c := newCountingFake(t)
	got, err := c.fetcher("org/alerts-off").Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Coverage["org/alerts-off"] != domain.CoverageOff {
		t.Errorf("coverage = %v, want off", got.Coverage["org/alerts-off"])
	}
	if len(got.Items) != 1 || got.Items[0].Kind != domain.KindPR {
		t.Errorf("items = %+v, want the one pull request", got.Items)
	}
}

// FR-1.16 AC6, QS-1.4: GitHub refusing the alerts — the field alone, or the whole query — never costs
// the repository its pull requests. The alerts are reported unavailable, and once GitHub has
// refused, the process stops asking.
func TestRefusedAlertsKeepThePullRequests(t *testing.T) {
	for _, repo := range []string{"org/alerts-forbidden", "org/alerts-scope"} {
		t.Run(repo, func(t *testing.T) {
			c := newCountingFake(t)
			f := c.fetcher(repo)

			got, err := f.Fetch(context.Background())
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if got.Coverage[repo] != domain.CoverageUnavailable {
				t.Errorf("coverage = %v, want unavailable", got.Coverage[repo])
			}
			if len(got.Items) != 1 || got.Items[0].Kind != domain.KindPR {
				t.Errorf("items = %+v, want the one pull request", got.Items)
			}
			if req, _ := c.counts(); req != 3 {
				t.Errorf("first fetch made %d requests, want 3: issues, the refused page, the page again", req)
			}

			got, err = f.Fetch(context.Background())
			if err != nil {
				t.Fatalf("second Fetch: %v", err)
			}
			if got.Coverage[repo] != domain.CoverageUnavailable || len(got.Items) != 1 {
				t.Errorf("second fetch = %+v", got)
			}
			if req, alerts := c.counts(); req != 2 || alerts != 0 {
				t.Errorf("second fetch made %d requests, %d asking for alerts; want 2 and 0", req, alerts)
			}
		})
	}
}

// Alerts are asked for on the first page of pull requests only: a repository whose pull requests
// run to a second page does not ask twice.
func TestAlertsAreAskedForOnTheFirstPageOnly(t *testing.T) {
	c := newCountingFake(t)
	if _, err := c.fetcher("org/paged").Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, alerts := c.counts(); alerts != 1 {
		t.Errorf("%d requests asked for alerts, want 1", alerts)
	}
}

// QS-3.5 with a token GitHub refuses: at most one extra request per repository on the first fetch,
// and the ordinary 20 from then on.
func TestARefusedTokenCostsOneRequestPerRepositoryOnce(t *testing.T) {
	c := newCountingFake(t)
	repos := make([]string, 10)
	for i := range repos {
		repos[i] = "org/alerts-scope"
	}
	f := c.fetcher(repos...)
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if req, _ := c.counts(); req > 30 {
		t.Errorf("first fetch made %d requests, want at most 30", req)
	}
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if req, _ := c.counts(); req != 20 {
		t.Errorf("second fetch made %d requests, want 20", req)
	}
}
