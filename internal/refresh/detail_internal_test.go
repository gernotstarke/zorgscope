// This file tests detail from inside the package. What it asserts is a seam rather than a
// formatting detail: the run record's Detail string is written here and read back by the domain,
// and neither side can see the other's half of the agreement.
package refresh

import (
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// The seam between this package and the domain. detail writes the announcement failure into the
// run record; the dashboard reads it back out to report Slack as one of the external interfaces
// on its problems page — Slack being the only service with no health record of its own, because
// it fetches nothing. Neither package can see the other's half of that agreement, so it is
// asserted here, on a detail string produced by the real function and handed to the real reader.
//
// Without this, changing the marker in either place is a silent break: the run record still
// contains the failure, the page still renders, and the row simply reports Slack as healthy
// while the notifications are dead.
func TestTheRunDetailIsReadableByTheDashboard(t *testing.T) {
	const message = "slack rejected the message: 404 no_service"

	d := detail(Report{
		Sources:   []SourceReport{{Source: "github", Stored: 12}, {Source: "github-extra", Err: "401"}},
		NotifyErr: message,
	})

	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	dash := domain.BuildDashboard(domain.DashboardInput{
		Now:        now,
		StaleAfter: time.Hour,
		LastRun:    domain.RefreshRun{StartedAt: now.Add(-time.Minute), FinishedAt: now, OK: true, Detail: d},
	})

	var found bool
	for _, p := range dash.Problems {
		if p.Source != "slack" {
			continue
		}
		found = true
		if p.Severity != domain.SeverityWarning {
			t.Errorf("slack severity = %q, want %q", p.Severity, domain.SeverityWarning)
		}
		if p.Detail != message {
			t.Errorf("slack detail = %q, want the notifier's own message %q", p.Detail, message)
		}
	}
	if !found {
		t.Fatalf("the dashboard reported no entry for slack; detail was %q", d)
	}
}
