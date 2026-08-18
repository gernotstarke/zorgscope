package fakesources

import (
	"embed"
	"encoding/json"
	"fmt"
)

// fixturesFS embeds every fixture document so that the package works identically under `go test`
// (working directory: the package) and `go run ./cmd/fakesources` (working directory: the module
// root) — a relative path would only work for one of the two.
//
//go:embed testdata/github/repos/*.json testdata/github/runs/*.json testdata/todoist/*.json
var fixturesFS embed.FS

// ghIssue is a single GitHub issue or pull request, shaped exactly as shurcooL/githubv4
// unmarshals a GraphQL node: number, title, url, author.login, createdAt, updatedAt, state, and —
// for pull requests only — isDraft. Both issues and pull requests use this type; the two GraphQL
// connections differ only in which slice a node lives in, not in field shape.
//
// IsDraft carries omitempty for a reason that is not cosmetic. On real GitHub, isDraft exists on
// PullRequest and not on Issue, so an adapter's issues query cannot ask for it — and
// shurcooL/graphql's decoder is strict about response keys it has no struct field for. Emitting
// "isDraft": false into the issues connection would therefore break every issues query. omitempty
// drops the key wherever the flag is false, which is every issue and every ready pull request; a
// draft pull request is the only node that carries it.
type ghIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	// BodyText is the item's description with Markdown stripped, as GitHub's bodyText field
	// returns it. It carries omitempty so a fixture item written without one produces a node
	// with no bodyText key at all, which is what a real issue opened with an empty body looks
	// like — the adapter has to cope with its absence, and a fixture that always supplied it
	// would never show that it does.
	BodyText  string   `json:"bodyText,omitempty"`
	URL       string   `json:"url"`
	Author    ghAuthor `json:"author"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
	State     string   `json:"state"`
	IsDraft   bool     `json:"isDraft,omitempty"`
}

// ghAuthor is the author sub-object of a GraphQL issue or pull-request node.
type ghAuthor struct {
	Login string `json:"login"`
}

// ghRepoFixture is the pristine, un-paginated content of one fake GitHub repository: every open
// issue and every open pull request. The GraphQL handler slices these into pages at request time.
type ghRepoFixture struct {
	Issues       []ghIssue `json:"issues"`
	PullRequests []ghIssue `json:"pullRequests"`
}

// workflowRun is one entry of the GitHub Actions "list workflow runs" REST response.
type workflowRun struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	Status       string  `json:"status"`
	Conclusion   *string `json:"conclusion"`
	HTMLURL      string  `json:"html_url"`
	RunStartedAt string  `json:"run_started_at"`
	UpdatedAt    string  `json:"updated_at"`
}

// runsFixture is the pristine content of one fake repository's GitHub Actions run history.
type runsFixture struct {
	TotalCount   int           `json:"total_count"`
	WorkflowRuns []workflowRun `json:"workflow_runs"`
}

// todoistDue is Todoist's "due" sub-object. A task with no due date omits it entirely, which
// unmarshals to a nil pointer — that is how the fixture represents "no due date".
type todoistDue struct {
	Date     string `json:"date"`
	Datetime string `json:"datetime,omitempty"`
}

// todoistProject is a single project, shaped as Todoist's REST v2 GET /rest/v2/projects returns
// it. Only the two fields an adapter needs to resolve a task's project_id to a name are declared;
// the real response carries colour, order, view style and more.
type todoistProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// todoistTask is a single task, shaped as Todoist's REST v2 API returns it.
type todoistTask struct {
	ID        string      `json:"id"`
	Content   string      `json:"content"`
	ProjectID string      `json:"project_id"`
	Priority  int         `json:"priority"`
	URL       string      `json:"url"`
	Due       *todoistDue `json:"due"`
}

// githubRepoFiles maps a "owner/name" repository to the fixture file holding its issues and pull
// requests. A repository requested via GraphQL but absent here is served as empty — no issues, no
// pull requests — rather than an error, so an adapter test that misspells a repo name gets an
// empty result instead of a fake-server crash.
var githubRepoFiles = map[string]string{
	"org/repo":  "testdata/github/repos/org-repo.json",
	"org/paged": "testdata/github/repos/org-paged.json",
	"org/bad":   "testdata/github/repos/org-bad.json",
}

// githubRunsFiles maps a "owner/name" repository to its GitHub Actions runs fixture. It covers
// the three shapes Task 8 needs: a newest run that is completed with a conclusion (org/repo), a
// newest run that is in_progress over an older completed run (org/build-running), and a
// repository with no workflow runs at all (org/build-none). A repository absent here is served
// with an empty workflow_runs array, matching FR-2.3 AC3.
var githubRunsFiles = map[string]string{
	"org/repo":          "testdata/github/runs/org-repo.json",
	"org/build-running": "testdata/github/runs/org-build-running.json",
	"org/build-none":    "testdata/github/runs/org-build-none.json",
}

// todoistTasksFile holds the four fixture tasks: one overdue, one due today, one due next week,
// and one with no due date, dated against the fixed clock (2026-08-17T12:00:00Z) Task 10's tests
// use.
const todoistTasksFile = "testdata/todoist/tasks.json"

// todoistProjectsFile holds the two projects the fixture tasks belong to (5001 and 5002), so that
// an adapter resolving project_id to a project name has both a hit for every fixture task and two
// distinguishable names to assert on (FR-4.1 AC1).
const todoistProjectsFile = "testdata/todoist/projects.json"

// loadFixtures reads every embedded fixture document fresh and returns a new, independent copy of
// the pristine state. It is used both to build the server's initial state and to implement
// POST /_control/reset — calling it again always yields the same starting point, discarding any
// injected issues or failures from a previous call.
func loadFixtures() (fixtures, error) {
	var fx fixtures

	fx.repos = make(map[string]*ghRepoFixture, len(githubRepoFiles))
	for name, path := range githubRepoFiles {
		var repo ghRepoFixture
		if err := readFixture(path, &repo); err != nil {
			return fixtures{}, fmt.Errorf("loading github repo fixture %s: %w", name, err)
		}
		fx.repos[name] = &repo
	}

	fx.runs = make(map[string]*runsFixture, len(githubRunsFiles))
	for name, path := range githubRunsFiles {
		var runs runsFixture
		if err := readFixture(path, &runs); err != nil {
			return fixtures{}, fmt.Errorf("loading github runs fixture %s: %w", name, err)
		}
		fx.runs[name] = &runs
	}

	if err := readFixture(todoistTasksFile, &fx.tasks); err != nil {
		return fixtures{}, fmt.Errorf("loading todoist tasks fixture: %w", err)
	}
	if err := readFixture(todoistProjectsFile, &fx.projects); err != nil {
		return fixtures{}, fmt.Errorf("loading todoist projects fixture: %w", err)
	}

	return fx, nil
}

// fixtures is one pristine copy of every embedded fixture document. It exists so that adding a
// fixture kind does not add another positional return value to loadFixtures and another silent
// opportunity to swap two of them at the call site.
type fixtures struct {
	repos    map[string]*ghRepoFixture
	runs     map[string]*runsFixture
	tasks    []todoistTask
	projects []todoistProject
}

// readFixture reads the embedded file at path and decodes it into v.
func readFixture(path string, v any) error {
	data, err := fixturesFS.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
