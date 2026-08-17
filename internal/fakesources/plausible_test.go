package fakesources_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gernotstarke/zorgscope/internal/fakesources"
)

type plausibleBody struct {
	Results struct {
		Visitors  plausibleMetric `json:"visitors"`
		Pageviews plausibleMetric `json:"pageviews"`
	} `json:"results"`
}

type plausibleMetric struct {
	Value           int `json:"value"`
	ComparisonValue int `json:"comparison_value"`
}

func fetchAggregate(t *testing.T, base, site, period string) plausibleBody {
	t.Helper()
	url := base + "/api/v1/stats/aggregate?site_id=" + site +
		"&period=" + period + "&metrics=visitors,pageviews&compare=previous_period"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body plausibleBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func TestPlausibleAggregateHasNonZeroComparisons(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	for _, period := range []string{"7d", "30d"} {
		body := fetchAggregate(t, srv.URL, "example.com", period)
		if body.Results.Visitors.Value == 0 || body.Results.Pageviews.Value == 0 {
			t.Errorf("period %s: zero current value: %+v", period, body)
		}
		if body.Results.Visitors.ComparisonValue == 0 {
			t.Errorf("period %s: visitors comparison_value is zero, want non-zero", period)
		}
		if body.Results.Pageviews.ComparisonValue == 0 {
			t.Errorf("period %s: pageviews comparison_value is zero, want non-zero", period)
		}
	}
}

func TestPlausibleAggregateVariesByPeriodAndSite(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	sevenDay := fetchAggregate(t, srv.URL, "example.com", "7d")
	thirtyDay := fetchAggregate(t, srv.URL, "example.com", "30d")
	if sevenDay.Results.Visitors.Value == thirtyDay.Results.Visitors.Value {
		t.Error("7d and 30d visitors should differ")
	}

	otherSite := fetchAggregate(t, srv.URL, "other.example", "7d")
	if otherSite.Results.Visitors.Value == sevenDay.Results.Visitors.Value {
		t.Error("different sites should produce different figures")
	}
}

func TestPlausibleFailControl(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	post(t, srv.URL+"/_control/fail?source=plausible&status=502")

	resp, err := http.Get(srv.URL + "/api/v1/stats/aggregate?site_id=example.com&period=7d")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}
