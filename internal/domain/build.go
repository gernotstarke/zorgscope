package domain

import (
	"path"
	"sort"
	"strings"
	"time"
)

// Build is the most recent CI run for a repository's workflow.
//
// Workflow is the run's display name — "pages build and deployment" — and WorkflowPath is the file
// GitHub ran, ".github/workflows/ci.yml". They are both kept because they answer different
// questions: the name is what a person reads, and the path is the only one of the two that can
// address the workflow anywhere else. See BadgeWorkflow.
type Build struct {
	Repo, Workflow, WorkflowPath, Conclusion, Status, RunURL string
	FinishedAt, FetchedAt                                    time.Time
	// Badge is the badge image for this workflow, as the SVG bytes a badge service returned, or
	// empty when there is none. It is fetched with the build and stored with it so that the page
	// showing it makes no request of its own — see BadgeWorkflow for which builds can have one,
	// and FR-2.3 AC5 for why it is stored rather than linked.
	//
	// It is borrowed markup and is never inlined into a page: it is rendered as an image, which a
	// browser draws in a sandbox with no script and no network of its own.
	Badge []byte
}

// workflowDir is where GitHub keeps the files a workflow can be addressed by.
const workflowDir = ".github/workflows/"

// BadgeWorkflow is the workflow file this build can be addressed by from outside — the name a
// shields.io badge URL takes as its last segment — or "" when there is none.
//
// Not every run has a file behind it. GitHub's built-in Pages deployment reports its path as
// "dynamic/pages/pages-build-deployment", which is not a file in the repository and which no
// external service can look up; shields answers such a request with a "repo or workflow not found"
// badge, which on a status page reads as a broken build rather than as an unaddressable one. So a
// badge is offered only for a real workflow file, and the rest say so in words instead.
//
// The shape is checked rather than trusted: the value comes from an upstream API and ends up in a
// URL path. Anything with a separator left in it after the directory prefix, or without a YAML
// extension, is refused.
func (b Build) BadgeWorkflow() string {
	if !strings.HasPrefix(b.WorkflowPath, workflowDir) {
		return ""
	}
	file := strings.TrimPrefix(b.WorkflowPath, workflowDir)
	if file == "" || strings.ContainsAny(file, `/\`) {
		return ""
	}
	switch strings.ToLower(path.Ext(file)) {
	case ".yml", ".yaml":
		return file
	}
	return ""
}

// Running reports whether a run is in progress right now. GitHub reports the newest run's status
// and the newest *completed* run's conclusion, so a running build sits on top of an older outcome
// and both are worth showing (FR-2.3 AC2).
func (b Build) Running() bool {
	return b.Status != "" && b.Status != "completed"
}

// BuildHealth is what a repository's last completed run says about it. Three states, because the
// front page shows one indicator and a person reading it has three useful reactions: nothing to
// do, something to look at, something is broken.
type BuildHealth string

// The states, worst first.
const (
	// BuildBroken: the last completed run failed. Something that used to build does not.
	BuildBroken BuildHealth = "broken"
	// BuildWarning: the last completed run neither succeeded nor failed — it was cancelled,
	// skipped, needs approval — or there is no completed run at all. Nothing is provably wrong
	// and nothing is provably right, which is its own thing worth knowing and is not the same as
	// green.
	BuildWarning BuildHealth = "warning"
	// BuildOK: the last completed run succeeded.
	BuildOK BuildHealth = "ok"
	// BuildUnknown: nothing is known, because nothing is being watched. Not a state a repository
	// is in — a state the *indicator* is in when there are no repositories at all, or the source
	// has no credential.
	BuildUnknown BuildHealth = "unknown"
)

// brokenConclusions are the GitHub Actions conclusions that mean the build is broken. Everything
// else that is not "success" is a warning: cancelled, skipped, neutral, action_required and stale
// all describe a run that did not prove anything, and calling them failures would light the
// indicator red for a workflow somebody cancelled by hand.
var brokenConclusions = map[string]bool{
	"failure":         true,
	"timed_out":       true,
	"startup_failure": true,
}

// Health classifies one repository's build.
//
// It is decided by the last *completed* run, never by a run in progress. A build that is running
// right now has not said anything yet, and treating it as a state of its own would make the
// indicator flicker to a fourth colour every time CI starts — while a repository whose last
// completed run failed is broken whether or not a new attempt is under way.
func (b Build) Health() BuildHealth {
	switch {
	case b.Conclusion == "success":
		return BuildOK
	case brokenConclusions[b.Conclusion]:
		return BuildBroken
	default:
		return BuildWarning
	}
}

// Silent reports whether nothing at all is known about this repository's CI: no run, no
// conclusion, no status, no URL. That is what a configured repository whose workflow has never
// fired looks like, and it is worth saying in those words rather than as "no completed run",
// which suggests there were runs.
func (b Build) Silent() bool {
	return b.Status == "" && b.Conclusion == "" && b.RunURL == "" && b.FinishedAt.IsZero()
}

// BuildStatus is everything the dashboard knows about builds: one health for the front page's
// indicator and every repository's row for the details page behind it (FR-2.3).
//
// The front page carries the indicator alone. A list of one row per repository is a report, and
// the page it would sit on is meant to answer "does anything need me right now" — which for
// builds is one colour and, when it is not green, a link.
type BuildStatus struct {
	// Health is the worst state any watched repository is in.
	Health BuildHealth
	// Broken, Warning and OK count the repositories in each state; Running counts those with a
	// run in progress, which cuts across the other three rather than being one of them.
	Broken, Warning, OK, Running int
	// Rows is every watched repository, worst first — including ones with no build at all, which
	// is a fact about the repository and not an absence to be hidden.
	Rows []Build
	// The source's own health, the same three facts every tile carries.
	Stale    bool
	Disabled bool
	Error    string
	LastOKAt time.Time
}

// Total is how many repositories are reported on.
func (s BuildStatus) Total() int { return len(s.Rows) }

// buildStatus assembles the build indicator and its rows.
//
// Every *configured* repository gets a row, whether or not a build was stored for it. A
// repository with no workflow run at all is not nothing: it is a repository whose CI has never
// reported, which is either deliberate or a workflow that has never fired, and the details page
// is where that question gets asked. UpsertBuilds only stores repositories that have a run, so
// without this the front page would be green while three repositories were silent.
//
// A stored repository that configuration no longer names is listed after the configured ones
// rather than hidden, for the same reason the sites tile does it: a row that is there says
// something, a row that vanished says nothing.
func buildStatus(in DashboardInput, disabled map[string]bool) BuildStatus {
	const source = "github-builds"
	s := BuildStatus{}

	stored := make(map[string]Build, len(in.Builds))
	for _, b := range in.Builds {
		stored[b.Repo] = b
	}

	seen := make(map[string]bool, len(in.Repos))
	for _, repo := range in.Repos {
		if seen[repo] {
			continue
		}
		seen[repo] = true
		b, ok := stored[repo]
		if !ok {
			b = Build{Repo: repo}
		}
		s.Rows = append(s.Rows, b)
	}
	for _, b := range in.Builds {
		if !seen[b.Repo] {
			seen[b.Repo] = true
			s.Rows = append(s.Rows, b)
		}
	}

	for _, b := range s.Rows {
		if b.Running() {
			s.Running++
		}
		switch b.Health() {
		case BuildBroken:
			s.Broken++
		case BuildWarning:
			s.Warning++
		case BuildOK:
			s.OK++
		}
	}

	sortBuilds(s.Rows)

	// Disabled is checked before anything else, exactly as it is for a tile (FR-8.2 AC2): a
	// source with no credential is not green and not broken, it is not running.
	if disabled[source] {
		s.Disabled = true
		s.Health = BuildUnknown
		return s
	}

	state := in.States[source]
	s.Stale = state.Stale(in.Now, in.StaleAfter)
	s.LastOKAt = state.LastSuccessAt
	if state.Failing() {
		s.Error = state.LastError
	}

	switch {
	case len(s.Rows) == 0:
		s.Health = BuildUnknown
	case s.Broken > 0:
		s.Health = BuildBroken
	case s.Warning > 0:
		s.Health = BuildWarning
	default:
		s.Health = BuildOK
	}
	return s
}

// buildRank orders the details page: the rows that need doing something about come first. An
// unranked health sorts last rather than first, so a state added without a rank cannot claim the
// top of the page from something that is actually broken.
var buildRank = map[BuildHealth]int{
	BuildBroken:  0,
	BuildWarning: 1,
	BuildOK:      2,
}

// sortBuilds orders rows worst first, then by repository name so that two runs of the page in the
// same state list them identically.
func sortBuilds(rows []Build) {
	sort.SliceStable(rows, func(i, j int) bool {
		ri, rj := buildRankOf(rows[i].Health()), buildRankOf(rows[j].Health())
		if ri != rj {
			return ri < rj
		}
		return rows[i].Repo < rows[j].Repo
	})
}

func buildRankOf(h BuildHealth) int {
	if r, ok := buildRank[h]; ok {
		return r
	}
	return len(buildRank)
}
