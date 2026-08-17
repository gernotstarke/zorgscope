package domain

import "time"

// Metric is a Plausible analytics snapshot for a site over a rolling window, alongside the same
// window from the immediately preceding period for comparison.
type Metric struct {
	Site                        string
	WindowDays                  int
	Visitors, Pageviews         int
	PrevVisitors, PrevPageviews int
	FetchedAt                   time.Time
}

// VisitorChange reports the percentage change in visitors versus the previous period. When the
// previous period is zero, a jump from nothing is not a percentage, so known is false.
func (m Metric) VisitorChange() (percent float64, known bool) {
	return change(m.Visitors, m.PrevVisitors)
}

// PageviewChange reports the percentage change in pageviews versus the previous period. When the
// previous period is zero, a jump from nothing is not a percentage, so known is false.
func (m Metric) PageviewChange() (percent float64, known bool) {
	return change(m.Pageviews, m.PrevPageviews)
}

// change computes the percentage change from prev to cur, reporting known as false when prev is
// zero — there is no baseline to measure a percentage against.
func change(cur, prev int) (percent float64, known bool) {
	if prev == 0 {
		return 0, false
	}
	return float64(cur-prev) / float64(prev) * 100, true
}
