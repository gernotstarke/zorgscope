package domain_test

import (
	"testing"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

func TestVisitorChange(t *testing.T) {
	tests := []struct {
		name         string
		visitors     int
		prevVisitors int
		wantPercent  float64
		wantKnown    bool
	}{
		{"growth", 150, 100, 50, true},
		{"decline", 50, 100, -50, true},
		{"no change", 100, 100, 0, true},
		{"zero previous period", 10, 0, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := domain.Metric{Visitors: tc.visitors, PrevVisitors: tc.prevVisitors}
			gotPercent, gotKnown := m.VisitorChange()
			if gotKnown != tc.wantKnown {
				t.Fatalf("VisitorChange() known = %v, want %v", gotKnown, tc.wantKnown)
			}
			if gotKnown && gotPercent != tc.wantPercent {
				t.Errorf("VisitorChange() percent = %v, want %v", gotPercent, tc.wantPercent)
			}
		})
	}
}

func TestPageviewChange(t *testing.T) {
	tests := []struct {
		name          string
		pageviews     int
		prevPageviews int
		wantPercent   float64
		wantKnown     bool
	}{
		{"growth", 300, 200, 50, true},
		{"decline", 100, 200, -50, true},
		{"no change", 200, 200, 0, true},
		{"zero previous period", 5, 0, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := domain.Metric{Pageviews: tc.pageviews, PrevPageviews: tc.prevPageviews}
			gotPercent, gotKnown := m.PageviewChange()
			if gotKnown != tc.wantKnown {
				t.Fatalf("PageviewChange() known = %v, want %v", gotKnown, tc.wantKnown)
			}
			if gotKnown && gotPercent != tc.wantPercent {
				t.Errorf("PageviewChange() percent = %v, want %v", gotPercent, tc.wantPercent)
			}
		})
	}
}
