package domain

import (
	"testing"
	"time"
)

// alert builds a Dependabot alert item of severity s in repo, fixed by fixPR (0 for none).
func alert(repo string, number int, s Severity, fixPR int, updated time.Time) Item {
	return Item{
		Kind: KindAlert, Repo: repo, Number: number, Author: "dependabot",
		Advisories: []string{"GHSA-47m2-wp7j-p9vc"},
		Alert:      &AlertFacts{Severity: s, Package: "rubyzip", FixPR: fixPR},
		CreatedAt:  updated, UpdatedAt: updated,
	}
}

// GitHub spells severities its own way; an unknown one errs loud (FR-1.16).
func TestParseSeverity(t *testing.T) {
	for in, want := range map[string]Severity{
		"LOW": SeverityLow, "moderate": SeverityMedium, "MEDIUM": SeverityMedium,
		"HIGH": SeverityHigh, "Critical": SeverityCritical, "": SeverityHigh, "SEVERE": SeverityHigh,
	} {
		if got := ParseSeverity(in); got != want {
			t.Errorf("ParseSeverity(%q) = %v, want %v", in, got, want)
		}
	}
	for s, want := range map[Severity]string{
		SeverityLow: "low", SeverityMedium: "medium", SeverityHigh: "high", SeverityCritical: "critical",
	} {
		if got := s.String(); got != want {
			t.Errorf("Severity(%d).String() = %q, want %q", s, got, want)
		}
	}
	if SeverityMedium.Serious() || !SeverityHigh.Serious() || !SeverityCritical.Serious() {
		t.Error("Serious must hold for High and Critical only")
	}
}

// An alert is TierAlert whatever its severity, although it always cites an advisory; the tier
// floor of the filter keeps it under tier=security (FR-1.16 AC7).
func TestAnAlertIsTierAlert(t *testing.T) {
	low := alert("o/r", 1, SeverityLow, 0, time.Time{})
	if got := low.Tier(); got != TierAlert {
		t.Fatalf("Tier() of a low alert = %v, want alert", got)
	}
	if TierAlert.String() != "alert" {
		t.Errorf("TierAlert.String() = %q", TierAlert.String())
	}
	if !(Filter{MinTier: TierSecurity}).Match(low) {
		t.Error("tier=security dropped an alert")
	}
	if (Filter{MinTier: TierAlert}).Match(Item{Advisories: []string{"CVE-2026-1"}}) {
		t.Error("tier=alert kept a pull request citing a CVE")
	}
	if !(Filter{Kind: KindAlert}).Match(low) || (Filter{Kind: KindAlert}).Match(Item{Kind: KindPR}) {
		t.Error("kind=alert does not narrow to alerts")
	}
}

// No alert is ever quiet, whatever its severity and age (FR-1.16 AC3).
func TestAnAlertIsNeverQuiet(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	old := alert("o/r", 1, SeverityLow, 0, now.AddDate(-1, 0, 0))
	if old.ShowsQuiet(now, QuietAfter) {
		t.Error("a year-old low alert was marked quiet")
	}
}

// High and Critical alerts need the owner ahead of everything and never go stale; Low and Medium
// need nobody (FR-1.16 AC2).
func TestAlertNeeds(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	ancient := now.AddDate(-2, 0, 0)
	for s, want := range map[Severity]Need{
		SeverityLow: NeedNone, SeverityMedium: NeedNone, SeverityHigh: NeedAlert, SeverityCritical: NeedAlert,
	} {
		if got := NeedFor(alert("o/r", 1, s, 0, now), "me"); got != want {
			t.Errorf("NeedFor(%v alert) = %v, want %v", s, got, want)
		}
	}
	if got := NeedFor(Item{Kind: KindAlert}, "me"); got != NeedAlert {
		t.Errorf("an alert without facts = %v, want alert (err loud)", got)
	}
	if (Needed{Item: alert("o/r", 1, SeverityHigh, 0, ancient), Need: NeedAlert}).Stale(now) {
		t.Error("a two-year-old high alert went stale")
	}
	if !NeedsNow(alert("o/r", 1, SeverityHigh, 0, ancient), "me", now) {
		t.Error("NeedsNow does not hold for an old high alert")
	}
	if NeedAlert.String() != "alert" {
		t.Errorf("NeedAlert.String() = %q", NeedAlert.String())
	}
}

// The band leads with alerts, and an alert whose fix is open absorbs its fix: one problem, one row.
// A Low alert's fix stays in the band on its own merits. The list is never touched (QG-1).
func TestBuildNeedsYouFoldsAnAlertsFixIntoIt(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	fix := Item{Kind: KindPR, Repo: "o/r", Number: 7, Author: "dependabot", Advisories: []string{"GHSA-47m2-wp7j-p9vc"}, UpdatedAt: now}
	lowFix := Item{Kind: KindPR, Repo: "o/r", Number: 8, Author: "dependabot", Advisories: []string{"GHSA-9hj4-r449-hfvc"}, UpdatedAt: now}
	other := Item{Kind: KindPR, Repo: "o/r", Number: 9, Author: "amy", UpdatedAt: now}
	items := []Item{
		other, fix, lowFix,
		alert("o/r", 62, SeverityHigh, 7, now.Add(-time.Hour)),
		alert("o/r", 64, SeverityLow, 8, now),
		alert("o/other", 1, SeverityCritical, 7, now.Add(-2*time.Hour)), // #7 of another repository
	}
	got := BuildNeedsYou(items, "me")
	type row struct {
		repo     string
		number   int
		need     Need
		fixReady int
	}
	want := []row{
		{"o/r", 62, NeedAlert, 7},
		{"o/other", 1, NeedAlert, 0},
		{"o/r", 8, NeedSecurity, 0},
		{"o/r", 9, NeedContribution, 0},
	}
	if len(got) != len(want) {
		t.Fatalf("band has %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := row{got[i].Item.Repo, got[i].Item.Number, got[i].Need, got[i].FixReady}
		if g != w {
			t.Errorf("row %d = %+v, want %+v", i, g, w)
		}
	}
}

// Alerts are counted over every item whatever the filter, and Serious counts High and Critical.
func TestDashboardCountsAlerts(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	d := BuildDashboard(DashboardInput{
		Now:    now,
		Repos:  []string{"o/r"},
		Filter: Filter{Kind: KindIssue},
		Items: []Item{
			alert("o/r", 1, SeverityHigh, 0, now),
			alert("o/r", 2, SeverityLow, 0, now),
			alert("o/r", 3, SeverityCritical, 0, now),
			{Kind: KindPR, Repo: "o/r", Advisories: []string{"CVE-2026-1"}},
		},
	})
	if d.Alerts != 3 || d.Serious != 2 || d.Security != 1 {
		t.Errorf("Alerts, Serious, Security = %d, %d, %d; want 3, 2, 1", d.Alerts, d.Serious, d.Security)
	}
}

// The reserved words "alert" and "alerts" narrow a search to alerts (FR-12.1).
func TestParseQueryAlertKeyword(t *testing.T) {
	for _, q := range []string{"alert rubyzip", "rubyzip alerts"} {
		got := ParseQuery(q)
		if got.Kind != KindAlert || len(got.Words) != 1 || got.Words[0] != "rubyzip" {
			t.Errorf("ParseQuery(%q) = %+v", q, got)
		}
	}
}

// Contributors are people: an alert is nobody's work.
func TestContributorsLeaveAlertsOut(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	got := BuildContributors([]Item{alert("o/r", 1, SeverityHigh, 0, now)}, []string{"o/r"})
	if got != nil {
		t.Errorf("BuildContributors listed an alert's author: %+v", got)
	}
}
