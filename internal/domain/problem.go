package domain

import (
	"sort"
	"strings"
	"time"
)

// Severity is what a Problem entry means for the person reading it. It is a small closed set
// because the page renders it as a word next to a colour, never as a colour alone (FR-1.5 AC2).
type Severity string

// The four states an external interface can be reported in, ordered worst first — the order
// severityRank encodes and the problems page sorts by.
const (
	// SeverityError: the interface's last exchange failed and has not succeeded since. Something
	// on the dashboard is wrong right now.
	SeverityError Severity = "error"
	// SeverityWarning: nothing is failing, but the interface has not succeeded recently enough to
	// be trusted — a stale source, or announcements that could not be delivered without failing
	// the run they belonged to (FR-6.1 AC3).
	SeverityWarning Severity = "warning"
	// SeverityOff: the interface is not configured. That is a choice, not a fault, so it is
	// listed and never counted as something that went wrong.
	SeverityOff Severity = "off"
	// SeverityOK: the interface is healthy. Listed so that the page answers "is anything wrong?"
	// with the whole picture rather than with an empty list that could equally mean "nothing is
	// being watched".
	SeverityOK Severity = "ok"
)

// severityRank orders the problems page: worst first, healthy last. An unknown severity sorts
// last rather than first, so a value added without a rank cannot silently claim the top of the
// page.
var severityRank = map[Severity]int{
	SeverityError:   0,
	SeverityWarning: 1,
	SeverityOff:     2,
	SeverityOK:      3,
}

// NotifyDetailPrefix marks the announcement step's failure inside a refresh run's Detail string.
//
// It lives here, in the domain, because two packages have to agree on it and neither may import
// the other: internal/refresh writes the Detail, and this package reads it back to report Slack
// as one of the external interfaces on the problems page. Slack is the one interface with no
// source-state row of its own — it fetches nothing, so nothing records its health — and a run
// whose announcements were rejected still finishes OK by FR-6.1 AC3, which means the run record's
// Detail is the only place the failure is written down at all.
const NotifyDetailPrefix = "notify: "

// DetailSeparator is how a run's Detail joins its parts. Shared for the same reason as the prefix.
const DetailSeparator = "; "

// Problem is the health of one external interface, as the warning box and the details page report
// it (FR-1.4).
//
// Summary is written here, in words this repository chose. Detail is borrowed text — an upstream
// library's error message, which may quote a URL, a header or a token — and it must go through
// the web layer's Redact before it is rendered (QS-4.3). The two fields are separate precisely so
// that the sentence a person reads first is never the one carrying a secret.
type Problem struct {
	// Source is the interface's wire name ("github", "github-builds", "slack"), which is what
	// source health and configuration are keyed by.
	Source string
	// Title is its display name.
	Title    string
	Severity Severity
	// Summary is a fixed sentence saying what state the interface is in. Never upstream text.
	Summary string
	// Detail is the upstream error, unredacted. Renderers must scrub it (QS-4.3).
	Detail string
	// At is when the reported state was observed: the time of the error for a failing interface,
	// of the last success otherwise. Zero when neither has ever happened.
	At time.Time
	// LastOKAt is when this interface last succeeded, zero if it never has. It is shown beside a
	// failure because "failing since ten minutes" and "failing since March" are different
	// problems.
	LastOKAt time.Time
}

// Wrong reports whether this entry is something that went wrong — an error or a warning. An
// unconfigured interface is not: switching a source off is a decision, and a dashboard that
// nagged about it would train its reader to ignore the warning box.
func (p Problem) Wrong() bool {
	return p.Severity == SeverityError || p.Severity == SeverityWarning
}

// The fixed sentences the problems page shows. They are constants rather than literals at their
// use sites so that the page's vocabulary can be read in one place, and so that a test can assert
// on the sentence a state produces without restating it.
const (
	summaryFailing    = "the last exchange with this service failed"
	summaryNeverOK    = "this service has never answered successfully"
	summaryStale      = "no successful exchange recently enough to trust what is shown"
	summaryOff        = "not configured, so nothing is fetched from it"
	summaryOK         = "answering normally"
	summaryNotifyFail = "new items could not be announced"
	summaryNotifyOK   = "announcements are going out"
	summaryNotifyOff  = "announcements are switched off"
)

// problemSource maps each fetching interface's display name, as buildProblems walks sourceOrder,
// to its wire name — the vocabulary States and Disabled are keyed by. "builds" is the one name
// that differs from its wire name; "github" needs no translation.
var problemSource = map[string]string{
	"github": "github",
	"builds": "github-builds",
}

// problemTitle gives each fetching interface its display title.
var problemTitle = map[string]string{
	"github": "GitHub",
	"builds": "Builds",
}

// notifyTitle names the announcement interface on the problems page. Slack is the only notifier
// v1 has, and naming it is more useful to an operator than the abstraction would be.
const notifyTitle = "Notifications (Slack)"

// notifySource is the wire name the notifier is listed under. It is not a fetcher, so it never
// appears in States; it is here so that every row on the page has an identity.
const notifySource = "slack"

// buildProblems reports the health of every external interface the dashboard depends on, worst
// first (FR-1.4).
//
// Every configured interface gets a row, healthy ones included. That is deliberate: a page that
// listed only failures would answer "is anything wrong?" with an empty list, and an empty list
// is also what a dashboard with nothing configured, or one whose health records were never
// written, would show. Listing all of them makes the healthy answer positive rather than absent.
func buildProblems(in DashboardInput, disabled map[string]bool) []Problem {
	out := make([]Problem, 0, len(sourceOrder)+1)
	for _, name := range sourceOrder {
		out = append(out, sourceProblem(name, in, disabled))
	}
	out = append(out, notifyProblem(in, disabled))
	sortProblems(out)
	return out
}

// sortProblems orders entries worst first. The sort is stable, so within one severity the
// interfaces keep the order they were assembled in — sourceOrder, which is the order they appear
// in on the dashboard — and the two pages read the same way round.
func sortProblems(ps []Problem) {
	sort.SliceStable(ps, func(i, j int) bool {
		return rankOf(ps[i].Severity) < rankOf(ps[j].Severity)
	})
}

// rankOf is severityRank with an explicit answer for a severity that has none. Reading the map
// directly would give an unranked value rank zero — the same rank as an error — and put it above
// the thing that is actually broken. Anything unknown sorts last instead.
func rankOf(sev Severity) int {
	if r, ok := severityRank[sev]; ok {
		return r
	}
	return len(severityRank)
}

// sourceProblem reports one fetching interface.
//
// The order of the checks is the order sourceHealth and buildStatus use and for the same reason
// (FR-8.2 AC2): a source with no credential is not failing and not stale, it simply never ran,
// and reporting it as an error would send an operator hunting a fault that is a line of
// configuration.
func sourceProblem(name string, in DashboardInput, disabled map[string]bool) Problem {
	source := problemSource[name]
	state := in.States[source]
	p := Problem{
		Source:   source,
		Title:    problemTitle[name],
		LastOKAt: state.LastSuccessAt,
	}

	switch {
	case disabled[source]:
		p.Severity = SeverityOff
		p.Summary = summaryOff
	case state.Failing():
		p.Severity = SeverityError
		p.Summary = summaryFailing
		if state.LastSuccessAt.IsZero() {
			p.Summary = summaryNeverOK
		}
		p.Detail = state.LastError
		p.At = state.LastErrorAt
	case state.Stale(in.Now, in.StaleAfter):
		p.Severity = SeverityWarning
		p.Summary = summaryStale
		if state.LastSuccessAt.IsZero() {
			p.Summary = summaryNeverOK
		}
		p.At = state.LastSuccessAt
	default:
		p.Severity = SeverityOK
		p.Summary = summaryOK
		p.At = state.LastSuccessAt
	}
	return p
}

// notifyProblem reports the announcement interface, which is the one external service with no
// health record of its own.
//
// A rejected announcement is a warning and never an error, because it is not a failure of the
// run: FR-6.1 AC3 keeps a run OK when notification fails, so that a revoked webhook cannot stop
// the dashboard from refreshing. The cost of that choice is exactly this — the failure is
// invisible unless something says it out loud — which is what this row is for.
func notifyProblem(in DashboardInput, disabled map[string]bool) Problem {
	p := Problem{Source: notifySource, Title: notifyTitle}
	if disabled[notifySource] {
		p.Severity = SeverityOff
		p.Summary = summaryNotifyOff
		return p
	}
	if msg, ok := notifyFailure(in.LastRun.Detail); ok {
		p.Severity = SeverityWarning
		p.Summary = summaryNotifyFail
		p.Detail = msg
		p.At = in.LastRun.FinishedAt
		p.LastOKAt = in.LastSuccessfulRun.FinishedAt
		return p
	}
	p.Severity = SeverityOK
	p.Summary = summaryNotifyOK
	p.At = in.LastSuccessfulRun.FinishedAt
	p.LastOKAt = in.LastSuccessfulRun.FinishedAt
	return p
}

// notifyFailure extracts the announcement step's error from a run's Detail, if it recorded one.
//
// The notify part is written last, so the *last* occurrence of the marker is the one that starts
// it — a source's own error text is borrowed from upstream and could contain the marker's
// characters, and searching from the front would then cut the message at a stranger's say-so.
func notifyFailure(detail string) (string, bool) {
	if strings.HasPrefix(detail, NotifyDetailPrefix) {
		return detail[len(NotifyDetailPrefix):], true
	}
	i := strings.LastIndex(detail, DetailSeparator+NotifyDetailPrefix)
	if i < 0 {
		return "", false
	}
	return detail[i+len(DetailSeparator)+len(NotifyDetailPrefix):], true
}
