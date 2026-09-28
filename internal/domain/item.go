// Package domain holds zorgscope's pure core: the types shared across every source, and the
// product's own rules — grouping the list by repository, filtering it, ranking a search and
// gathering the contributors. It imports nothing but the standard library (QS-5.1), so every
// function that needs the current time takes it as a parameter; nothing in here calls
// time.Now().
package domain

import (
	"sort"
	"strings"
	"time"
)

// Kind distinguishes the two shapes of item zorgscope tracks.
type Kind string

// The set of item kinds zorgscope understands. An alert is a Dependabot alert (FR-1.16): not a
// conversation anybody opened, but GitHub's own statement that a dependency is vulnerable.
const (
	KindIssue Kind = "issue"
	KindPR    Kind = "pr"
	KindAlert Kind = "alert"
)

// Severity is a Dependabot alert's severity, as GitHub grades it (FR-1.16). The zero value is
// SeverityLow; the order of the constants is the order of loudness.
type Severity int

// GitHub's four severities, from quietest to loudest.
const (
	SeverityLow Severity = iota
	SeverityMedium
	SeverityHigh
	SeverityCritical
)

// ParseSeverity reads GitHub's spelling of a severity (LOW, MODERATE, HIGH, CRITICAL, in any case;
// MEDIUM too). A severity this build does not know is High: an alert GitHub raised at a grade
// nobody here has heard of should err loud, not quiet.
func ParseSeverity(s string) Severity {
	switch strings.ToUpper(s) {
	case "LOW":
		return SeverityLow
	case "MODERATE", "MEDIUM":
		return SeverityMedium
	case "CRITICAL":
		return SeverityCritical
	default:
		return SeverityHigh
	}
}

// String is the severity as the page names it, in a class and in words, and so is fixed text.
func (s Severity) String() string {
	switch s {
	case SeverityLow:
		return "low"
	case SeverityMedium:
		return "medium"
	case SeverityCritical:
		return "critical"
	default:
		return "high"
	}
}

// Serious reports whether the severity is High or Critical: what needs the owner (FR-1.16 AC2)
// and what the page draws loud. Low and Medium are shown, but never counted as needing anyone —
// a mark that is always on stops being read (ADR-0012).
func (s Severity) Serious() bool { return s >= SeverityHigh }

// AlertFacts is what a Dependabot alert says beyond what every item carries (FR-1.16). It is
// borrowed text like Title, and is escaped, never trusted.
type AlertFacts struct {
	Severity Severity
	// Package and Ecosystem name the vulnerable dependency: "rubyzip", "RUBYGEMS".
	Package   string
	Ecosystem string
	// Manifest is the file GitHub found the dependency in: "Gemfile.lock".
	Manifest string
	// Vulnerable is the requirement the manifest states, "= 2.3.2"; PatchedIn the first version
	// that fixes it, "3.4.0", or "" when there is none yet.
	Vulnerable string
	PatchedIn  string
	// FixPR is the number of the open Dependabot pull request that fixes the alert; 0 when there is
	// none.
	FixPR int
}

// Item is a single tracked unit from GitHub: an issue or a pull request.
type Item struct {
	Kind Kind
	// Repo is the repository the item belongs to, "owner/name".
	Repo   string
	Number int
	Title  string
	// Summary is a short prefix of the item's own text — a GitHub issue or pull request body —
	// with its whitespace collapsed, or empty when the source has none. It is stored short
	// because it is displayed short: the dashboard renders it in small type under the title, so
	// that a list of numbers and headlines says what the items are actually about. It is
	// borrowed text like Title, and is escaped, never trusted.
	Summary string
	URL     string
	Author  string
	// Labels are the item's GitHub labels as GitHub spells them, in GitHub's order — at most ten,
	// which covers every arc42 item there is. nil when the item has none. The domain carries the
	// names and nothing else; which of them get a colour is the page's business (FR-1.10 AC3).
	Labels []string
	// Advisories are the CVE and GHSA identifiers the item cites, in its title or anywhere in its
	// body — deduplicated, in the order they were first seen, at most three. The adapter extracts
	// them before the body is cut to its summary, because Dependabot cites them in the release
	// notes, far past the part zorgscope keeps. nil when the item cites none. Like Title, it is
	// borrowed text and is escaped, never trusted.
	Advisories []string
	// ReviewRequested is the logins a pull request asks for a review from, in GitHub's order —
	// people only, since a team is not somebody who can be told "this needs you". nil for an issue
	// and for a pull request nobody was asked to review (FR-1.14).
	ReviewRequested []string
	// Alert is set for a KindAlert item and nil for every other: what the Dependabot alert says.
	Alert     *AlertFacts
	State     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SortItems orders items most recently updated first. The sort is stable, so items with equal
// update times keep their original relative order.
func SortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
}

// QuietAfter is the default threshold: how long an item may go without an update before the page
// calls it quiet (FR-1.10 AC4). Three months: long enough that a maintainer's own pause does not
// trip it, short enough that a forgotten issue shows up before the year is out. It is the value
// the web layer falls back to when the visitor has expressed no preference (FR-1.12 AC4), so the
// number keeps one home even though the threshold is now chosen per visitor.
const QuietAfter = 90 * 24 * time.Hour

// IsQuiet reports whether nothing has happened to the item for after or longer, measured from its
// last update to now. An item whose update time is unknown is never quiet: unknown is not idle.
//
// An after of zero or less means nothing is ever quiet. That is what the "never" setting asks
// for, and making it the zero value's meaning is deliberate: a caller that forgot to pass a
// threshold marks nothing rather than marking everything.
func (i Item) IsQuiet(now time.Time, after time.Duration) bool {
	return after > 0 && !i.UpdatedAt.IsZero() && now.Sub(i.UpdatedAt) >= after
}

// Tier is how loudly the page marks an item (FR-1.13). The zero value is TierNone, so an item
// nobody classified is simply not marked.
type Tier int

// The tiers, from quietest to loudest. Alert is the loudest: a pull request citing an advisory
// says someone is working on a vulnerability, an alert says GitHub has found one in a manifest.
const (
	TierNone Tier = iota
	TierDependency
	TierSecurity
	TierAlert
)

// String is the tier as the page names it in a class, and so is fixed text: never anything an
// upstream said.
func (t Tier) String() string {
	switch t {
	case TierAlert:
		return "alert"
	case TierSecurity:
		return "security"
	case TierDependency:
		return "dependency"
	default:
		return ""
	}
}

// dependencyBots are the logins of the bots whose pull requests update dependencies. They are
// compared without regard to case. A bot that is not listed here — Copilot, GitHub Actions — is
// deliberately not a dependency update: what marks an item is what it does, not what opened it.
var dependencyBots = []string{"dependabot", "renovate"}

// Tier classifies the item from evidence (FR-1.13 AC1).
//
// Security needs evidence of a vulnerability: a cited advisory, or a label a person applied. A
// Dependabot pull request that cites a CVE is therefore Security, not Dependency — it is the fix
// for a published vulnerability, which is the thing the red exists to say. One that cites nothing
// is maintenance, and gets the quiet mark, so that the red keeps meaning something: a mark that is
// always on is read as noise, which is what retired the NEW badge (ADR-0012).
//
// An alert is TierAlert whatever its severity, and is decided first: an alert always cites its
// advisory, which would otherwise make it Security.
func (i Item) Tier() Tier {
	if i.Kind == KindAlert {
		return TierAlert
	}
	if len(i.Advisories) > 0 || i.hasLabel("security") {
		return TierSecurity
	}
	if i.hasLabel("dependencies") {
		return TierDependency
	}
	for _, bot := range dependencyBots {
		if strings.EqualFold(i.Author, bot) {
			return TierDependency
		}
	}
	return TierNone
}

// hasLabel reports whether the item carries a label spelled name in any case. Labels keep
// GitHub's spelling, and repositories spell the same label differently.
func (i Item) hasLabel(name string) bool {
	for _, l := range i.Labels {
		if strings.EqualFold(l, name) {
			return true
		}
	}
	return false
}

// ShowsQuiet is IsQuiet, except that a Security item and an alert are never quiet (FR-1.13 AC3,
// FR-1.16 AC3). An old, unfixed vulnerability is exactly the item a reader most needs to see, and
// dimming it would hide it; an alert has no conversation to go quiet in the first place. The list
// and the search results both call this rather than IsQuiet, so that the override lives in one
// place and the two cannot disagree.
func (i Item) ShowsQuiet(now time.Time, after time.Duration) bool {
	return i.Tier() < TierSecurity && i.IsQuiet(now, after)
}

// Coverage is how much a fetch could say about one repository's Dependabot alerts (FR-1.16 AC5).
// The zero value is CoverageUnknown, so a repository nobody reported on claims nothing.
type Coverage int

// The coverages. Only CoverageOn lets "no alerts" mean "no vulnerabilities".
const (
	CoverageUnknown Coverage = iota
	// CoverageOn: alerts are enabled and were read.
	CoverageOn
	// CoverageOff: the repository has Dependabot alerts switched off.
	CoverageOff
	// CoverageUnavailable: GitHub would not say — the token lacks the scope, or its owner the access.
	CoverageUnavailable
)

// Fetched is what one fetch of every configured repository produced: the items, and per
// repository ("owner/name") what could be said about its alerts.
type Fetched struct {
	Items    []Item
	Coverage map[string]Coverage
}

// AgeBucket classifies how long ago something happened, relative to a display cutoff.
type AgeBucket string

// The set of age buckets a timestamp can fall into.
const (
	BucketDay   AgeBucket = "day"
	BucketWeek  AgeBucket = "week"
	BucketMonth AgeBucket = "month"
	BucketOlder AgeBucket = "older"
)

// Age classifies t relative to now into a bucket, using the boundaries 24 hours, 7 days and
// 30 days.
func Age(t, now time.Time) AgeBucket {
	age := now.Sub(t)
	switch {
	case age <= 24*time.Hour:
		return BucketDay
	case age <= 7*24*time.Hour:
		return BucketWeek
	case age <= 30*24*time.Hour:
		return BucketMonth
	default:
		return BucketOlder
	}
}
