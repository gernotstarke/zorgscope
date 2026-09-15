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
//go:embed testdata/github/repos/*.json
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

// githubRepoFiles maps a "owner/name" repository to the fixture file holding its issues and pull
// requests. A repository requested via GraphQL but absent here is served as empty — no issues, no
// pull requests — rather than an error, so an adapter test that misspells a repo name gets an
// empty result instead of a fake-server crash.
var githubRepoFiles = map[string]string{
	"org/repo":  "testdata/github/repos/org-repo.json",
	"org/paged": "testdata/github/repos/org-paged.json",
	"org/bad":   "testdata/github/repos/org-bad.json",
}

// loadFixtures reads every embedded fixture document fresh and returns a new, independent copy of
// the pristine state. It is used to build the server's initial state — calling it again always
// yields the same starting point.
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

	return fx, nil
}

// fixtures is one pristine copy of every embedded fixture document. It exists so that adding a
// fixture kind does not add another positional return value to loadFixtures and another silent
// opportunity to swap two of them at the call site.
type fixtures struct {
	repos map[string]*ghRepoFixture
}

// readFixture reads the embedded file at path and decodes it into v.
func readFixture(path string, v any) error {
	data, err := fixturesFS.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
