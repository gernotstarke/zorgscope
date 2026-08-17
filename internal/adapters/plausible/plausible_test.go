package plausible_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gernotstarke/zorgscope/internal/adapters/plausible"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/fakesources"
)

func TestFetchReturnsBothWindowsWithComparison(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	f := plausible.New(plausible.Config{
		APIKey: "x", BaseURL: srv.URL, Sites: []string{"example.org"},
	}, srv.Client())

	res, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Metrics) != 2 { // one site, 7 and 30 days
		t.Fatalf("len(metrics) = %d, want 2 (FR-3.1 AC1)", len(res.Metrics))
	}
	byWindow := map[int]domain.Metric{}
	for _, m := range res.Metrics {
		byWindow[m.WindowDays] = m
	}
	for _, w := range []int{7, 30} {
		m, ok := byWindow[w]
		if !ok {
			t.Fatalf("no metric for a %d-day window", w)
		}
		if m.Visitors == 0 || m.Pageviews == 0 {
			t.Errorf("%d-day window has no figures: %+v", w, m)
		}
		if m.PrevVisitors == 0 {
			t.Errorf("%d-day window has no comparison; FR-3.1 AC2 needs one", w)
		}
	}
}

// QS-1.4 / FR-1.4 AC2: one bad site must not lose the others, and the error must name the site
// so it can be shown on the dashboard.
func TestFetchErrorsNameTheSite(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()
	post(t, srv.URL+"/_control/fail?source=plausible&repo=example.org&status=500")

	f := plausible.New(plausible.Config{
		APIKey: "x", BaseURL: srv.URL, Sites: []string{"example.org", "example.com"},
	}, srv.Client())

	res, err := f.Fetch(context.Background())

	if err == nil {
		t.Fatal("want an error naming the failing site")
	}
	if !strings.Contains(err.Error(), "example.org") {
		t.Errorf("error %q must name the failing site", err)
	}
	if len(res.Metrics) == 0 {
		t.Error("metrics from the healthy site must still be returned (QS-1.4)")
	}
	for _, m := range res.Metrics {
		if m.Site != "example.com" {
			t.Errorf("only the healthy site's metrics should be returned, got %q", m.Site)
		}
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
