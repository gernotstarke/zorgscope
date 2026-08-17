package fakesources

import (
	"hash/fnv"
	"net/http"
)

// plausibleAggregateResponse is shaped exactly as
// GET /api/v1/stats/aggregate?metrics=visitors,pageviews&compare=previous_period responds.
type plausibleAggregateResponse struct {
	Results struct {
		Visitors  plausibleMetric `json:"visitors"`
		Pageviews plausibleMetric `json:"pageviews"`
	} `json:"results"`
}

// plausibleMetric is one metric's current value and its comparison-period value.
type plausibleMetric struct {
	Value           int `json:"value"`
	ComparisonValue int `json:"comparison_value"`
}

// handlePlausibleAggregate serves GET /api/v1/stats/aggregate. It works for any site_id — Task
// 9's tests are free to configure whatever site names they like — by deriving deterministic,
// non-zero figures from a hash of site_id and period, so every comparison_value is guaranteed
// non-zero (Task 9 asserts a comparison is present).
func (s *server) handlePlausibleAggregate(w http.ResponseWriter, r *http.Request) {
	site := r.URL.Query().Get("site_id")
	period := r.URL.Query().Get("period")

	s.mu.Lock()
	defer s.mu.Unlock()

	if status, fail := s.shouldFailLocked("plausible", site); fail {
		w.WriteHeader(status)
		return
	}

	visitors, prevVisitors := plausibleFigures(site, period, "visitors")
	pageviews, prevPageviews := plausibleFigures(site, period, "pageviews")

	var resp plausibleAggregateResponse
	resp.Results.Visitors = plausibleMetric{Value: visitors, ComparisonValue: prevVisitors}
	resp.Results.Pageviews = plausibleMetric{Value: pageviews, ComparisonValue: prevPageviews}

	writeJSON(w, http.StatusOK, resp)
}

// plausibleFigures deterministically derives a (current, comparison) pair from site, period and
// metric, always returning two distinct, non-zero values.
func plausibleFigures(site, period, metric string) (value, comparison int) {
	h := fnvHash(site + "|" + period + "|" + metric)
	value = 500 + int(h%4500) // 500..4999
	delta := 25 + int(h%475)  // 25..499, always non-zero
	if h%2 == 0 {
		comparison = value - delta
	} else {
		comparison = value + delta
	}
	if comparison <= 0 {
		comparison = value + delta // never let a comparison read as zero (or negative)
	}
	return value, comparison
}

// fnvHash returns a stable, deterministic hash of s.
func fnvHash(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s)) // hash.Hash.Write never returns an error
	return h.Sum32()
}
