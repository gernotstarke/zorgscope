package domain_test

import (
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

func TestSourceStateStale(t *testing.T) {
	now := at("2026-08-17T12:00:00Z")
	after := time.Hour

	tests := []struct {
		name          string
		lastSuccessAt time.Time
		want          bool
	}{
		{"never succeeded is stale", time.Time{}, true},
		{"succeeded within the window is fresh", at("2026-08-17T11:30:00Z"), false},
		{"succeeded exactly at the window boundary is fresh", at("2026-08-17T11:00:00Z"), false},
		{"succeeded before the window is stale", at("2026-08-17T10:00:00Z"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := domain.SourceState{LastSuccessAt: tc.lastSuccessAt}
			if got := s.Stale(now, after); got != tc.want {
				t.Errorf("Stale() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSourceStateFailing(t *testing.T) {
	success := at("2026-08-17T10:00:00Z")

	tests := []struct {
		name        string
		lastError   string
		lastErrorAt time.Time
		want        bool
	}{
		{"no error at all", "", time.Time{}, false},
		{"error before the last success", "boom", at("2026-08-17T09:00:00Z"), false},
		{"error after the last success", "boom", at("2026-08-17T11:00:00Z"), true},
		{"error message empty even with a later timestamp", "", at("2026-08-17T11:00:00Z"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := domain.SourceState{LastSuccessAt: success, LastError: tc.lastError, LastErrorAt: tc.lastErrorAt}
			if got := s.Failing(); got != tc.want {
				t.Errorf("Failing() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRefreshRunIsAPlainRecord exercises RefreshRun as a struct literal so the field set stays
// covered; it carries no behaviour of its own.
func TestRefreshRunIsAPlainRecord(t *testing.T) {
	run := domain.RefreshRun{
		ID:         1,
		StartedAt:  at("2026-08-17T10:00:00Z"),
		FinishedAt: at("2026-08-17T10:00:05Z"),
		Trigger:    "manual",
		OK:         true,
		Detail:     "ok",
	}
	if run.ID != 1 || run.Trigger != "manual" || !run.OK {
		t.Fatalf("unexpected RefreshRun: %+v", run)
	}
}
