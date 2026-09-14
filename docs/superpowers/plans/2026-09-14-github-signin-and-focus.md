# GitHub sign-in and a GitHub-only dashboard — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the shared-token sign-in with "sign in with GitHub" gated on push access to `gernotstarke/zorgscope`, remove Plausible and Todoist from backend and client, and turn the front page into one filtered list of open issues and pull requests grouped by repository.

**Architecture:** The domain (`internal/domain`, stdlib only) gains a pure `Filter` and assembles `RepoGroup`s instead of tiles. The web layer gets two OAuth routes and a `/items` fragment route; the permission check is an adapter behind a new `ports.AccessChecker` port so the web package never talks to GitHub itself. Everything Plausible and Todoist is deleted, with one migration cleaning the database. The fake sources server grows the three GitHub endpoints the sign-in needs.

**Tech Stack:** Go 1.26, `golang.org/x/oauth2` (already a dependency) for the code exchange, `html/template` + vendored htmx, libSQL migrations, Docker + make.

**Spec:** [`docs/superpowers/specs/2026-09-14-github-signin-and-focus-design.md`](../specs/2026-09-14-github-signin-and-focus-design.md)

## Global Constraints

- `internal/domain` imports only the standard library (QS‑5.1); `.golangci.yml` enforces it. Domain statement coverage must stay ≥ 90 %.
- The external dependency set does not grow. `golang.org/x/oauth2` is already required; use it for the exchange.
- No secret value — client secret, OAuth code, state, access token, `GITHUB_TOKEN`, `REFRESH_SECRET` — may be logged, rendered or written to the repository (QS‑4.3). `web.Redact` and the canary test `TestNoResponseEverContainsASecret` must cover every field of `config.Secrets`.
- Every route is declared in `routes()` in `internal/web/server.go` with its `authKind`; `TestEveryRouteIsEitherDeliberatelyPublicOrRefusesAnonymousAccess` pins the public list.
- Comparisons of secrets use `crypto/subtle.ConstantTimeCompare` (QS‑4.2).
- The CSP in `internal/web/server.go` stays without `unsafe-inline` and without an external host (QS‑4.4). No inline script, no inline style, in any template.
- Times are `time.Time` in Go and RFC 3339 UTC strings in SQL.
- The Makefile has exactly six targets: `help`, `backend`, `client`, `fakes`, `check`, `clean`. Do not add targets.
- Quick local loop: the host has Go installed, so `go test ./...` and `go vet ./...` work directly (store tests skip themselves without `TEST_TURSO_URL`). **Every task ends with `make check` passing** — that is the Docker run CI mirrors, and it includes lint and the database tests. `make check` needs Docker running; its last step validates `deploy/fly.toml` and needs either `FLY_API_TOKEN` in the environment or a `~/.fly` login — if neither is available, run every other line of the `check` recipe by hand and say so in the task report.
- Commit messages reference requirement ids, e.g. `feat(web): sign in with GitHub (FR-8.3)`, and end with the attribution trailer the session was given.
- Read `.impeccable.md` before touching a template or `app.css`.

---

## File structure

| Path | Responsibility after this plan |
|------|--------------------------------|
| `internal/domain/item.go` | `Item` (issue or PR only), `Kind`, `IsNew`, `SortItems`, `CountNew` |
| `internal/domain/filter.go` | **new** — `Filter`, `Filter.Match`, `Filter.Empty` |
| `internal/domain/dashboard.go` | `DashboardInput`, `Dashboard`, `RepoGroup`, `SourceHealth`, `BuildDashboard` (groups, no tiles) |
| `internal/domain/problem.go` | problems page model; `sourceOrder` shrinks |
| `internal/domain/metric.go` | **deleted** |
| `internal/ports/ports.go` | `FetchResult` without metrics; `Store` without metrics; **new** `AccessChecker` |
| `internal/adapters/libsql/migrations/0005_github_only.sql` | **new** — drops metrics, Todoist rows, task-only columns |
| `internal/adapters/libsql/store.go` | store without `UpsertMetrics`/`Metrics`, without `due_at`/`priority` |
| `internal/adapters/github/access.go` | **new** — `AccessChecker` over `GET /repos/{owner}/{repo}` |
| `internal/adapters/plausible/`, `internal/adapters/todoist/` | **deleted** |
| `internal/fakesources/oauth.go` | **new** — authorize, token, repository endpoints and the `oauth-user` control |
| `internal/fakesources/plausible.go`, `todoist.go`, `testdata/todoist/` | **deleted** |
| `internal/fakesources/server.go` | routes: GitHub only, plus `GET /` index |
| `internal/config/config.go` | no Plausible/Todoist; `GitHub.AuthRepo`; `Secrets.OAuthClientID`, `OAuthClientSecret`; `GitHub.OAuthBaseURL` |
| `internal/web/auth.go` | session codec keyed from the client secret, middleware, rate limiter (unchanged shape) |
| `internal/web/signin.go` | **new** — `/login`, `/auth/github`, `/auth/callback` |
| `internal/web/dashboard.go` | `/`, `/items`, `/seen`, `/problems`, `/builds`; filter parsing; view models |
| `internal/web/templates/dashboard.html` | filter form + `{{template "items" .}}` |
| `internal/web/templates/fragments/items.html` | **new** — the list section (health line, groups) |
| `internal/web/templates/fragments/{counts,alert,build-status}.html` | moved from `tiles/` |
| `internal/web/templates/tiles/` | **deleted** |
| `internal/web/templates/login.html` | one link button |
| `internal/web/static/app.css` | filter bar and group styles; tile grid styles removed |
| `cmd/zorgscope/main.go` | wiring: GitHub fetchers, access checker, Slack |
| `config/zorgscope.yaml` | `github.auth_repo`; no `plausible:`/`todoist:` |
| `deploy/env.example`, `Makefile` | OAuth pair replaces `ZORGSCOPE_TOKEN` |
| `docs/requirements/*.md`, `docs/decisions/0009-*.md`, `docs/concepts/*.md`, `README.md` | Task 7 |

Task order is a dependency order. Tasks 1→2→3→4 must be sequential (each compiles only after the previous). Task 5 is independent of 1–4. Task 6 needs 3 and 5. Task 7 (docs) only touches Markdown and can run in parallel with 4–6 in a worktree. Task 8 is last.

---

### Task 1: Domain — filter, repository groups, no tiles

**Files:**
- Create: `internal/domain/filter.go`, `internal/domain/filter_test.go`
- Modify: `internal/domain/item.go`, `internal/domain/dashboard.go`, `internal/domain/problem.go`
- Delete: `internal/domain/metric.go`, `internal/domain/metric_test.go`, `internal/domain/tile_limit_internal_test.go`
- Test: `internal/domain/dashboard_test.go`, `internal/domain/problem_test.go`, `internal/domain/item_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces (later tasks rely on these exact names):

```go
// item.go
const (
	KindIssue Kind = "issue"
	KindPR    Kind = "pr"
)
// Item loses DueAt and Priority. Repo is always "owner/name".

// filter.go
type Filter struct {
	Repo         string
	Kind         Kind
	CreatedSince time.Time
	Text         string
}
func (f Filter) Match(it Item) bool
func (f Filter) Empty() bool

// dashboard.go
type SourceHealth struct {
	Disabled bool
	Stale    bool
	Error    string
	LastOKAt time.Time
}
type RepoGroup struct {
	Repo     string
	Items    []Item
	NewCount int
	Total    int
}
type DashboardInput struct {
	Now, LastVisitAt          time.Time
	LastRun, LastSuccessfulRun RefreshRun
	StaleAfter                time.Duration
	Items                     []Item
	Builds                    []Build
	States                    map[string]SourceState
	Disabled                  []string
	Repos                     []string
	Filter                    Filter        // new
}
type Dashboard struct {
	GeneratedAt, LastVisitAt time.Time
	LastRun                  RunOutcome
	LastRunAt, LastSuccessAt time.Time
	LastRunDetail            string
	NewTotal, Total, Shown   int
	Filter                   Filter
	Groups                   []RepoGroup
	Source                   SourceHealth
	Builds                   BuildStatus
	Problems                 []Problem
	ProblemCount             int
}
func BuildDashboard(in DashboardInput) Dashboard
```

- [ ] **Step 1: Write the failing filter tests**

`internal/domain/filter_test.go`:

```go
package domain_test

import (
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

func TestFilterMatchesEveryAxis(t *testing.T) {
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	it := domain.Item{Repo: "arc42/arc42.org-site", Kind: domain.KindPR, Title: "Fix the Header", Summary: "broken on mobile", CreatedAt: created}

	cases := []struct {
		name string
		f    domain.Filter
		want bool
	}{
		{"empty matches", domain.Filter{}, true},
		{"repo equal", domain.Filter{Repo: "arc42/arc42.org-site"}, true},
		{"repo other", domain.Filter{Repo: "arc42/arc42.de-site"}, false},
		{"kind equal", domain.Filter{Kind: domain.KindPR}, true},
		{"kind other", domain.Filter{Kind: domain.KindIssue}, false},
		{"since before created", domain.Filter{CreatedSince: created.Add(-time.Hour)}, true},
		{"since at created", domain.Filter{CreatedSince: created}, true},
		{"since after created", domain.Filter{CreatedSince: created.Add(time.Hour)}, false},
		{"text in title, case-insensitive", domain.Filter{Text: "header"}, true},
		{"text in summary", domain.Filter{Text: "MOBILE"}, true},
		{"text absent", domain.Filter{Text: "footer"}, false},
		{"text is trimmed", domain.Filter{Text: "  header "}, true},
		{"all axes together", domain.Filter{Repo: "arc42/arc42.org-site", Kind: domain.KindPR, CreatedSince: created, Text: "fix"}, true},
		{"one axis fails the whole filter", domain.Filter{Repo: "arc42/arc42.org-site", Kind: domain.KindIssue}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.Match(it); got != tc.want {
				t.Fatalf("Match() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFilterEmpty(t *testing.T) {
	if !(domain.Filter{}).Empty() {
		t.Fatal("zero filter should be empty")
	}
	if (domain.Filter{Text: " "}).Empty() == false {
		t.Fatal("whitespace-only text is still empty")
	}
	if (domain.Filter{Kind: domain.KindIssue}).Empty() {
		t.Fatal("a kind makes the filter non-empty")
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/domain/ -run 'TestFilter' -v`
Expected: compile error, `undefined: domain.Filter`.

- [ ] **Step 3: Implement `filter.go`**

```go
package domain

import (
	"strings"
	"time"
)

// Filter narrows the dashboard's list. Every field is optional; the zero Filter matches every
// item. The fields are ANDed: an item must pass every axis that is set.
type Filter struct {
	Repo         string    // "" means every repository
	Kind         Kind      // "" means issues and pull requests alike
	CreatedSince time.Time // zero means no lower bound; an item created exactly then passes
	Text         string    // "" (after trimming) means no text filter
}

// Match reports whether it passes every axis of f. Text is matched case-insensitively against
// the title and the summary, as a substring: the box is for "the thing about the header", not
// for a query language.
func (f Filter) Match(it Item) bool {
	if f.Repo != "" && it.Repo != f.Repo {
		return false
	}
	if f.Kind != "" && it.Kind != f.Kind {
		return false
	}
	if !f.CreatedSince.IsZero() && it.CreatedAt.Before(f.CreatedSince) {
		return false
	}
	if text := strings.TrimSpace(f.Text); text != "" {
		needle := strings.ToLower(text)
		if !strings.Contains(strings.ToLower(it.Title), needle) &&
			!strings.Contains(strings.ToLower(it.Summary), needle) {
			return false
		}
	}
	return true
}

// Empty reports whether f narrows nothing.
func (f Filter) Empty() bool {
	return f.Repo == "" && f.Kind == "" && f.CreatedSince.IsZero() && strings.TrimSpace(f.Text) == ""
}
```

- [ ] **Step 4: Run the filter tests; they pass**

Run: `go test ./internal/domain/ -run 'TestFilter' -v` — expected PASS.

- [ ] **Step 5: Strip the task-only parts of `item.go`**

Remove `KindTask`, the `DueAt` and `Priority` fields, and rewrite the `Repo` comment to one line: `// Repo is the repository the item belongs to, "owner/name".` Update the package comment's "three shapes" to "two". Delete `metric.go` and `metric_test.go`. In `item_test.go`, delete any case that used `KindTask`, `DueAt` or `Priority`.

- [ ] **Step 6: Write the failing dashboard tests**

Replace the tile-based tests in `internal/domain/dashboard_test.go` with these (keep the tests about `lastRun` outcomes and the header times, adapting field names). Delete `tile_limit_internal_test.go`.

```go
func TestBuildDashboardGroupsByRepositoryInConfigurationOrder(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	items := []domain.Item{
		{Source: "github", ExternalID: "1", Repo: "arc42/b", Kind: domain.KindIssue, Title: "b1", CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now.Add(-time.Hour), FirstSeenAt: now.Add(-time.Hour)},
		{Source: "github", ExternalID: "2", Repo: "arc42/a", Kind: domain.KindPR, Title: "a1", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour), FirstSeenAt: now.Add(-5 * time.Hour)},
		{Source: "github", ExternalID: "3", Repo: "arc42/a", Kind: domain.KindIssue, Title: "a2", CreatedAt: now.Add(-9 * time.Hour), UpdatedAt: now.Add(-time.Minute), FirstSeenAt: now.Add(-time.Minute)},
		{Source: "github", ExternalID: "4", Repo: "gone/repo", Kind: domain.KindIssue, Title: "orphan", CreatedAt: now, UpdatedAt: now, FirstSeenAt: now.Add(-5 * time.Hour)},
	}
	d := domain.BuildDashboard(domain.DashboardInput{
		Now: now, LastVisitAt: now.Add(-2 * time.Hour), Items: items,
		Repos: []string{"arc42/a", "arc42/b"},
	})

	if got := repoNames(d.Groups); !slices.Equal(got, []string{"arc42/a", "arc42/b", "gone/repo"}) {
		t.Fatalf("group order = %v", got)
	}
	a := d.Groups[0]
	if a.Total != 2 || a.NewCount != 1 || len(a.Items) != 2 {
		t.Fatalf("arc42/a: total %d new %d shown %d", a.Total, a.NewCount, len(a.Items))
	}
	if a.Items[0].ExternalID != "3" { // new first, then most recently updated
		t.Fatalf("arc42/a first item = %s, want the new one", a.Items[0].ExternalID)
	}
	if d.NewTotal != 2 || d.Total != 4 || d.Shown != 4 {
		t.Fatalf("NewTotal %d Total %d Shown %d", d.NewTotal, d.Total, d.Shown)
	}
}

func TestBuildDashboardAppliesTheFilterButCountsNewUnfiltered(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	items := []domain.Item{
		{Source: "github", ExternalID: "1", Repo: "arc42/a", Kind: domain.KindIssue, Title: "issue", CreatedAt: now, UpdatedAt: now, FirstSeenAt: now},
		{Source: "github", ExternalID: "2", Repo: "arc42/a", Kind: domain.KindPR, Title: "pull", CreatedAt: now, UpdatedAt: now, FirstSeenAt: now},
		{Source: "github", ExternalID: "3", Repo: "arc42/b", Kind: domain.KindPR, Title: "pull", CreatedAt: now, UpdatedAt: now, FirstSeenAt: now.Add(-3 * time.Hour)},
	}
	d := domain.BuildDashboard(domain.DashboardInput{
		Now: now, LastVisitAt: now.Add(-time.Hour), Items: items,
		Repos:  []string{"arc42/a", "arc42/b"},
		Filter: domain.Filter{Kind: domain.KindPR},
	})
	if d.NewTotal != 2 {
		t.Fatalf("NewTotal = %d; the badge must not follow the filter", d.NewTotal)
	}
	if d.Total != 3 || d.Shown != 2 {
		t.Fatalf("Total %d Shown %d", d.Total, d.Shown)
	}
	if len(d.Groups) != 2 || len(d.Groups[0].Items) != 1 || d.Groups[0].Total != 2 {
		t.Fatalf("groups = %+v", d.Groups)
	}
	if d.Filter.Kind != domain.KindPR {
		t.Fatal("the applied filter is echoed back")
	}
}

func TestBuildDashboardOmitsARepositoryWithNoMatch(t *testing.T) {
	now := time.Now()
	items := []domain.Item{
		{Source: "github", ExternalID: "1", Repo: "arc42/a", Kind: domain.KindIssue, Title: "x", CreatedAt: now, UpdatedAt: now, FirstSeenAt: now},
	}
	d := domain.BuildDashboard(domain.DashboardInput{Now: now, Items: items, Repos: []string{"arc42/a", "arc42/b"}, Filter: domain.Filter{Text: "nothing"}})
	if len(d.Groups) != 0 || d.Shown != 0 {
		t.Fatalf("groups = %+v", d.Groups)
	}
}

func TestBuildDashboardReportsTheSourcesHealth(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	t.Run("disabled wins", func(t *testing.T) {
		d := domain.BuildDashboard(domain.DashboardInput{Now: now, Disabled: []string{"github"}, StaleAfter: time.Hour})
		if !d.Source.Disabled || d.Source.Stale || d.Source.Error != "" {
			t.Fatalf("Source = %+v", d.Source)
		}
	})
	t.Run("failing carries the error and the last success", func(t *testing.T) {
		ok := now.Add(-30 * time.Minute)
		d := domain.BuildDashboard(domain.DashboardInput{Now: now, StaleAfter: time.Hour, States: map[string]domain.SourceState{
			"github": {Source: "github", LastSuccessAt: ok, LastError: "boom", LastErrorAt: now},
		}})
		if d.Source.Error != "boom" || !d.Source.LastOKAt.Equal(ok) || d.Source.Stale {
			t.Fatalf("Source = %+v", d.Source)
		}
	})
	t.Run("stale", func(t *testing.T) {
		d := domain.BuildDashboard(domain.DashboardInput{Now: now, StaleAfter: time.Hour, States: map[string]domain.SourceState{
			"github": {Source: "github", LastSuccessAt: now.Add(-2 * time.Hour)},
		}})
		if !d.Source.Stale || d.Source.Error != "" {
			t.Fatalf("Source = %+v", d.Source)
		}
	})
}

func TestBuildDashboardNeverMutatesItsInput(t *testing.T) {
	now := time.Now()
	items := []domain.Item{
		{Source: "github", ExternalID: "old", Repo: "r", UpdatedAt: now.Add(-time.Hour), FirstSeenAt: now.Add(-time.Hour)},
		{Source: "github", ExternalID: "new", Repo: "r", UpdatedAt: now, FirstSeenAt: now},
	}
	_ = domain.BuildDashboard(domain.DashboardInput{Now: now, LastVisitAt: now.Add(-time.Minute), Items: items})
	if items[0].ExternalID != "old" {
		t.Fatal("BuildDashboard sorted the caller's slice")
	}
}

func repoNames(gs []domain.RepoGroup) []string {
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.Repo)
	}
	return out
}
```

- [ ] **Step 7: Rewrite `dashboard.go`**

Delete `tileSource`, `tileTitle`, `tileOrder`, `githubTileLimit`, the Plausible window constants, `Tile`, `SiteMetrics`, `buildTile`, `firstN`, `sortTasks`, `siteMetrics`, and `DashboardInput.Metrics`. Set `sourceOrder = []string{"github", "builds"}` (check `problem.go`: `sourceProblem` takes a tile name and maps it through the deleted `tileSource`; replace that lookup with a local `map[string]string{"github": "github", "builds": "github-builds"}` named `problemSource` in `problem.go`, and delete the `sites` and `tasks` display names there). Add the types from the Interfaces block and this assembly:

```go
// githubSource is the fetcher name behind every item on the page.
const githubSource = "github"

func BuildDashboard(in DashboardInput) Dashboard {
	disabled := make(map[string]bool, len(in.Disabled))
	for _, s := range in.Disabled {
		disabled[s] = true
	}
	outcome, at := lastRun(in.LastRun)
	d := Dashboard{
		GeneratedAt:   in.Now,
		LastVisitAt:   in.LastVisitAt,
		LastRun:       outcome,
		LastRunAt:     at,
		LastRunDetail: in.LastRun.Detail,
		LastSuccessAt: in.LastSuccessfulRun.FinishedAt,
		Filter:        in.Filter,
	}

	items := itemsBySource(in.Items, githubSource)
	d.Total = len(items)
	// Counted before filtering: the tab title and the summary say what is new, not what is
	// visible, and a filter must never make the badge lie.
	d.NewTotal = CountNew(items, in.LastVisitAt)
	d.Groups = groupByRepo(items, in.Repos, in.Filter, in.LastVisitAt)
	for _, g := range d.Groups {
		d.Shown += len(g.Items)
	}

	d.Source = sourceHealth(in, disabled)
	d.Builds = buildStatus(in, disabled)
	d.Problems = buildProblems(in, disabled)
	for _, p := range d.Problems {
		if p.Wrong() {
			d.ProblemCount++
		}
	}
	return d
}

// groupByRepo assembles one group per repository that has at least one item passing f, in the
// order the repositories are configured; repositories that still hold items but are no longer
// configured follow, in first-seen order. Total and NewCount are per repository before filtering.
func groupByRepo(items []Item, repos []string, f Filter, lastVisit time.Time) []RepoGroup {
	order := append([]string(nil), repos...)
	known := make(map[string]bool, len(repos))
	for _, r := range repos {
		known[r] = true
	}
	byRepo := make(map[string]*RepoGroup)
	for _, it := range items {
		g, ok := byRepo[it.Repo]
		if !ok {
			g = &RepoGroup{Repo: it.Repo}
			byRepo[it.Repo] = g
			if !known[it.Repo] {
				known[it.Repo] = true
				order = append(order, it.Repo)
			}
		}
		g.Total++
		if it.IsNew(lastVisit) {
			g.NewCount++
		}
		if f.Match(it) {
			g.Items = append(g.Items, it)
		}
	}
	out := make([]RepoGroup, 0, len(byRepo))
	for _, repo := range order {
		g, ok := byRepo[repo]
		if !ok || len(g.Items) == 0 {
			continue
		}
		SortItems(g.Items, lastVisit)
		out = append(out, *g)
	}
	return out
}

// sourceHealth reduces the GitHub source's state to what the page says above the list. Disabled
// is checked first (FR-8.2 AC2): a source that never runs is neither stale nor failing.
func sourceHealth(in DashboardInput, disabled map[string]bool) SourceHealth {
	if disabled[githubSource] {
		return SourceHealth{Disabled: true}
	}
	state := in.States[githubSource]
	h := SourceHealth{Stale: state.Stale(in.Now, in.StaleAfter), LastOKAt: state.LastSuccessAt}
	if state.Failing() {
		h.Error = state.LastError
	}
	return h
}
```

Keep `itemsBySource` (it allocates a fresh slice, which is what keeps the caller's slice unsorted; `groupByRepo` appends into per-group slices, so `SortItems` never touches the input). Keep `lastRun`.

- [ ] **Step 8: Fix `problem_test.go` and run the whole domain package with coverage**

Delete problem tests that name `sites`, `tasks`, `plausible` or `todoist`. Then:

Run: `go test -coverprofile=/tmp/domain.out ./internal/domain/... && go tool cover -func=/tmp/domain.out | tail -1`
Expected: PASS, total ≥ 90 %.

- [ ] **Step 9: Commit**

The rest of the module does not compile yet (ports and store still name `Metric`); that is Task 2. Commit the domain alone:

```bash
git add internal/domain
git commit -m "feat(domain): a filter and repository groups replace the tiles (FR-1.1, FR-1.2)"
```

---

### Task 2: Ports, store and migration 0005

**Files:**
- Create: `internal/adapters/libsql/migrations/0005_github_only.sql`
- Modify: `internal/ports/ports.go`, `internal/ports/fake.go`, `internal/adapters/libsql/store.go`, `internal/refresh/runner.go`
- Test: `internal/adapters/libsql/store_test.go`, `internal/adapters/libsql/store_more_test.go`, `internal/adapters/libsql/migrate_test.go` (create if absent), `internal/refresh/runner_test.go`, `internal/refresh/stubstore_test.go`, `internal/refresh/detail_internal_test.go`

**Interfaces:**
- Consumes: Task 1's `domain.Item` without `DueAt`/`Priority`; no `domain.Metric`.
- Produces:

```go
// ports.go
type FetchResult struct {
	Items      []domain.Item
	Builds     []domain.Build
	OwnsBuilds bool
}
// Store loses UpsertMetrics and Metrics. Everything else unchanged.

// AccessChecker decides who may sign in (FR-8.3). token is the visitor's own OAuth access token;
// it is used for one request and never stored.
type AccessChecker interface {
	HasPushAccess(ctx context.Context, token string) (bool, error)
}
```

- [ ] **Step 1: Write the migration**

`internal/adapters/libsql/migrations/0005_github_only.sql`:

```sql
-- 0005_github_only: Plausible and Todoist are gone (design 2026-09-14 §3). The metrics table held
-- Plausible's numbers and nothing else; Todoist's items and both sources' health rows go with
-- them; the two columns only a task ever filled leave the items table. GitHub rows are untouched,
-- first_seen_at included — that column is the product (ADR-0006) and no clean-up may rewrite it.
DROP TABLE IF EXISTS metrics;
DELETE FROM items WHERE source = 'todoist';
DELETE FROM source_state WHERE source IN ('plausible', 'todoist');
DELETE FROM notified WHERE key LIKE 'todoist|%';
ALTER TABLE items DROP COLUMN due_at;
ALTER TABLE items DROP COLUMN priority;
```

Check the `notified` key format in `store.go` (`MarkNotified` / the runner's key builder) and adjust the `LIKE` pattern to the real separator; if keys never carried the source, drop that line.

- [ ] **Step 2: Write the failing migration test**

In `internal/adapters/libsql/migrate_test.go` (create it, following the `TEST_TURSO_URL` skip pattern the store tests use — copy their `openTestStore` helper):

```go
func TestMigration0005RemovesTodoistAndMetricsAndKeepsGitHub(t *testing.T) {
	s := openTestStore(t) // migrates to the latest version and truncates
	ctx := context.Background()
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	// Apply 0001..0004 only by re-creating the old shape by hand: the migrated store has no
	// metrics table any more, so the test recreates it and the old columns exactly as 0001 did,
	// seeds them, then applies 0005 again through the same code path Migrate uses.
	mustExec(t, s, `CREATE TABLE metrics (site TEXT, window_days INTEGER, visitors INTEGER, pageviews INTEGER, prev_visitors INTEGER, prev_pageviews INTEGER, fetched_at TEXT, PRIMARY KEY (site, window_days))`)
	mustExec(t, s, `INSERT INTO metrics VALUES ('arc42.org', 7, 1, 1, 1, 1, '2026-09-01T00:00:00Z')`)
	mustExec(t, s, `ALTER TABLE items ADD COLUMN due_at TEXT`)
	mustExec(t, s, `ALTER TABLE items ADD COLUMN priority INTEGER`)
	mustExec(t, s, `INSERT INTO items (source, external_id, kind, repo, title, first_seen_at, last_fetched_at) VALUES ('todoist','t1','task','Inbox','a task','2026-09-01T00:00:00Z','2026-09-01T00:00:00Z')`)
	mustExec(t, s, `INSERT INTO items (source, external_id, kind, repo, title, first_seen_at, last_fetched_at) VALUES ('github','g1','issue','org/repo','an issue','2026-08-01T00:00:00Z','2026-09-01T00:00:00Z')`)
	mustExec(t, s, `INSERT INTO source_state (source) VALUES ('plausible'), ('todoist'), ('github')`)
	mustExec(t, s, `DELETE FROM schema_migrations WHERE version = 5`)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	items, err := s.Items(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ExternalID != "g1" || !items[0].FirstSeenAt.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("items after 0005 = %+v", items)
	}
	states, err := s.SourceStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := states["todoist"]; ok {
		t.Fatal("todoist source_state survived")
	}
	if _, ok := states["github"]; !ok {
		t.Fatal("github source_state was deleted")
	}
	if _, err := s.DB().ExecContext(ctx, `SELECT 1 FROM metrics`); err == nil {
		t.Fatal("metrics table survived")
	}
	_ = now
}
```

If `*Store` exposes no `DB()` accessor, add an unexported test helper in a `_test.go` file of package `libsql` (`func (s *Store) exec(...)`) instead of exporting one; `mustExec` wraps it and calls `t.Fatal` on error.

- [ ] **Step 3: Run it to see it fail**

Run: `make check` is too slow for the loop; instead start the database and run the package directly:

```sh
docker compose -f deploy/compose.yml up -d db
docker run --rm -t --network=container:$(docker compose -f deploy/compose.yml ps -q db) \
  -v "$PWD":/src -w /src -v zorgscope-gomod:/go/pkg/mod -v zorgscope-gocache:/root/.cache/go-build \
  golang:1.26 sh -c 'TEST_TURSO_URL=http://localhost:8080 go test ./internal/adapters/libsql/ -run TestMigration0005 -v'
```

Expected: fails (compile error until Step 4, then the assertion on the metrics table until the migration file exists — it exists from Step 1, so after Step 4 this passes).

- [ ] **Step 4: Remove metrics from ports, store, fake and runner**

- `ports.go`: `FetchResult` per the Interfaces block; delete `UpsertMetrics` and `Metrics` from `Store`; add `AccessChecker`. Update `SourceFetcher`'s comment ("GitHub, Plausible or Todoist" → "one upstream source").
- `ports/fake.go`: drop any `Metrics` field from `FakeFetcher`.
- `store.go`: delete `UpsertMetrics`, `Metrics`; remove `"metrics"` from `TruncateAll`; remove `due_at` and `priority` from every SELECT/INSERT/scan (`ReplaceItems`, `Items`, `upsertItemSQL`), and the `DueAt`/`Priority` fields they fed.
- `runner.go`: delete the `UpsertMetrics` call and the metrics branch; fix the log line and the comments that name metrics; `detail` and its test lose the Todoist example.
- `refresh/stubstore_test.go`, `runner_test.go`, `store_test.go`, `store_more_test.go`: delete metrics and task cases. `go vet ./...` must be clean for these packages; `cmd/zorgscope`, `internal/web` and the two doomed adapters still fail to compile until Task 3 — that is expected.

- [ ] **Step 5: Run the store and refresh tests**

Same command as Step 3 with `./internal/adapters/libsql/... ./internal/refresh/... ./internal/ports/...` and no `-run`. Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/ports internal/adapters/libsql internal/refresh
git commit -m "feat(store): migration 0005 drops metrics and task rows; ports lose metrics, gain AccessChecker (FR-8.3)"
```

---

### Task 3: Remove Plausible and Todoist everywhere else; fakes index page

**Files:**
- Delete: `internal/adapters/plausible/`, `internal/adapters/todoist/`, `internal/fakesources/plausible.go`, `internal/fakesources/plausible_test.go`, `internal/fakesources/todoist.go`, `internal/fakesources/todoist_test.go`, `internal/fakesources/testdata/todoist/`
- Modify: `internal/fakesources/server.go`, `internal/fakesources/fixtures.go`, `internal/fakesources/control.go`, `internal/config/config.go`, `internal/config/testdata/valid.yaml`, `internal/config/config_test.go`, `config/zorgscope.yaml`, `deploy/env.example`, `Makefile`, `cmd/zorgscope/main.go`, `cmd/zorgscope/main_test.go`, `cmd/fakesources/main.go`, `internal/adapters/slack/slack.go`, `internal/adapters/slack/slack_test.go`
- Test: `internal/fakesources/server_test.go` (create if absent), `internal/config/config_test.go`

**Interfaces:**
- Consumes: Task 2's `ports.FetchResult`.
- Produces: `config.Config` without `Plausible`/`Todoist`; `config.Secrets{GitHubToken, SlackWebhook, AppToken, RefreshSecret, TursoURL, TursoAuthToken string}` (AppToken goes in Task 6); `Config.Enabled` knows only `"github"`; `fakesources.NewServer()` serving `GET /`.

- [ ] **Step 1: Write the failing fakes index test**

`internal/fakesources/server_test.go`:

```go
func TestTheRootListsWhatTheFakeServes(t *testing.T) {
	rec := httptest.NewRecorder()
	NewServer().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q", ct)
	}
	for _, path := range []string{"POST /graphql", "GET /repos/{owner}/{repo}/actions/runs", "POST /_control/reset"} {
		if !strings.Contains(rec.Body.String(), path) {
			t.Errorf("index does not mention %s", path)
		}
	}
	if strings.Contains(rec.Body.String(), "plausible") || strings.Contains(rec.Body.String(), "todoist") {
		t.Error("index still names a removed source")
	}
}

func TestAnUnknownPathIsStill404(t *testing.T) {
	rec := httptest.NewRecorder()
	NewServer().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nothing-here", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /nothing-here = %d", rec.Code)
	}
}
```

- [ ] **Step 2: Remove the two sources from the fakes and add the index**

In `server.go`: delete the `todoist`/`todoistProjects` fields, the three Plausible/Todoist handler registrations, and the words in the comments; `resetLocked` stops loading tasks/projects. In `fixtures.go`: delete the Todoist types, file constants and loading, and drop `testdata/todoist/*.json` from the `go:embed` line. In `control.go`: `controlSources = map[string]bool{"github": true}` and trim the comment. Replace the route table with a slice so the index can be generated from it:

```go
// routes is the whole table, so that GET / can list it: the first thing anyone does with a fake
// server is open it in a browser, and a 404 there says nothing.
var routes = []string{
	"POST /graphql",
	"GET /repos/{owner}/{repo}/actions/runs",
	"POST /_control/fail",
	"POST /_control/add-issue",
	"POST /_control/reset",
}

func NewServer() http.Handler {
	s := &server{fail: map[string]map[string]int{}}
	if err := s.resetLocked(); err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("POST /graphql", s.handleGraphQL)
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/runs", s.handleActionsRuns)
	mux.HandleFunc("POST /_control/fail", s.handleControlFail)
	mux.HandleFunc("POST /_control/add-issue", s.handleControlAddIssue)
	mux.HandleFunc("POST /_control/reset", s.handleControlReset)
	return mux
}

func (s *server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "zorgscope fake sources — a fixture-backed GitHub.\n\nServes:\n")
	for _, r := range routes {
		_, _ = io.WriteString(w, "  "+r+"\n")
	}
	_, _ = io.WriteString(w, "\nPoint the backend here with GITHUB_BASE_URL and GITHUB_OAUTH_BASE_URL in .env.\n")
}
```

(Task 5 appends its three routes to `routes` and the mux.) Keep the registrations and the slice in the same order so a reviewer can diff them.

- [ ] **Step 3: Run the fakes tests**

Run: `go test ./internal/fakesources/ -v` — expected PASS, and no test names a removed source.

- [ ] **Step 4: Strip config**

`config.go`: delete `Plausible`, `Todoist` types and fields, the two `fileConfig` sections, `PlausibleKey`, `TodoistToken`, `PLAUSIBLE_BASE_URL`, `TODOIST_BASE_URL`; `Enabled` keeps only the `github` case. `Login` stays for now (FR‑2.4 may use it). `config/testdata/valid.yaml` and `config_test.go` lose those sections and assertions; `TestLoadValid` asserts `Enabled("github")` true and `Enabled("todoist")` false (unknown source). `config/zorgscope.yaml` loses the `plausible:` and `todoist:` blocks. `deploy/env.example` loses `PLAUSIBLE_API_KEY`, `TODOIST_TOKEN`, and the two commented base URLs. Run `go test ./internal/config/` — PASS.

- [ ] **Step 5: Rewire `cmd/zorgscope/main.go`, Slack, and the Makefile**

- `main.go`: delete the two imports and the two `if cfg.Enabled(...)` blocks in `buildFetchers`; `loc` is now unused there — pass only what is used, and delete `loc` if nothing else needs it (the web layer will need the timezone in Task 4; it reads `cfg.Timezone` itself). Fix `main_test.go` accordingly.
- `slack.go` `label`: delete the `KindTask` case; `slack_test.go` loses any task case.
- `cmd/fakesources/main.go`: the package comment names GitHub only.
- `Makefile`: nothing to remove for the sources (already six targets); leave it.
- Grep the tree: `grep -rni "plausible\|todoist" --include=*.go --include=*.yaml --include=*.yml --include=*.example --include=Makefile . | grep -v docs/ | grep -v _bmad` must return nothing.

- [ ] **Step 6: Build, vet and run the whole module locally**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: `internal/web` still fails to compile (it references tiles and `Metrics`); every other package passes. If `internal/web` is the only failure, proceed — Task 4 owns it. If the web failure blocks `go test ./...` from reporting the others, run the others by path.

- [ ] **Step 7: Commit**

```bash
git add -A internal/adapters internal/fakesources internal/config config deploy/env.example cmd
git commit -m "refactor: remove Plausible and Todoist from backend, fakes and config (design 2026-09-14 §3)"
```

---

### Task 4: The front page — one filtered list grouped by repository

**Files:**
- Create: `internal/web/templates/fragments/items.html`, `internal/web/filter.go`, `internal/web/filter_test.go`
- Move: `internal/web/templates/tiles/{counts,alert,build-status}.html` → `internal/web/templates/fragments/`
- Delete: `internal/web/templates/tiles/{tile,github,sites,tasks}.html`
- Modify: `internal/web/server.go` (`tileGlob`, `routes()`, `pageFiles` unchanged), `internal/web/dashboard.go`, `internal/web/templates/dashboard.html`, `internal/web/static/app.css`, `internal/web/auth_test.go` (`fakeStore`), `internal/web/dashboard_test.go`, `internal/web/chrome_test.go`, `internal/web/refresh_test.go`
- Test: `internal/web/filter_test.go`, `internal/web/dashboard_test.go`

**Interfaces:**
- Consumes: Task 1's `domain.Filter`, `domain.RepoGroup`, `domain.SourceHealth`, `Dashboard.Groups/Shown/Total`.
- Produces: routes `GET /{$}` and `GET /items` (fragment, `authSessionFragment`); `parseFilter(q url.Values, loc *time.Location) domain.Filter`; the `items` template with the id `items` on its root element; `Server.loc *time.Location`.

- [ ] **Step 1: Write the failing filter-parsing test**

`internal/web/filter_test.go`:

```go
package web

import (
	"net/url"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

func TestParseFilterReadsEveryParameter(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	f := parseFilter(url.Values{"repo": {"arc42/a"}, "kind": {"pr"}, "since": {"2026-09-01"}, "q": {" header "}}, berlin)
	want := domain.Filter{Repo: "arc42/a", Kind: domain.KindPR, CreatedSince: time.Date(2026, 9, 1, 0, 0, 0, 0, berlin), Text: "header"}
	if f.Repo != want.Repo || f.Kind != want.Kind || !f.CreatedSince.Equal(want.CreatedSince) || f.Text != want.Text {
		t.Fatalf("parseFilter = %+v, want %+v", f, want)
	}
}

func TestParseFilterIgnoresWhatItCannotRead(t *testing.T) {
	f := parseFilter(url.Values{"kind": {"task"}, "since": {"yesterday"}}, time.UTC)
	if !f.Empty() {
		t.Fatalf("unreadable values should be ignored, got %+v", f)
	}
}

func TestParseFilterAcceptsIssueAndPRAndAll(t *testing.T) {
	for in, want := range map[string]domain.Kind{"issue": domain.KindIssue, "pr": domain.KindPR, "": ""} {
		if got := parseFilter(url.Values{"kind": {in}}, time.UTC).Kind; got != want {
			t.Errorf("kind %q parsed as %q", in, got)
		}
	}
}
```

- [ ] **Step 2: Implement `filter.go`**

```go
package web

import (
	"net/url"
	"strings"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// parseFilter reads the four filter parameters. A value it cannot read is ignored rather than
// refused: a stale bookmark with a typo in it should show the unfiltered page, not an error.
func parseFilter(q url.Values, loc *time.Location) domain.Filter {
	f := domain.Filter{
		Repo: strings.TrimSpace(q.Get("repo")),
		Text: strings.TrimSpace(q.Get("q")),
	}
	switch q.Get("kind") {
	case "issue":
		f.Kind = domain.KindIssue
	case "pr":
		f.Kind = domain.KindPR
	}
	if since := q.Get("since"); since != "" {
		if t, err := time.ParseInLocation("2006-01-02", since, loc); err == nil {
			f.CreatedSince = t
		}
	}
	return f
}
```

Run: `go test ./internal/web/ -run TestParseFilter` — it will not compile until the rest of the package does; keep going and run it at Step 7.

- [ ] **Step 3: Templates**

Move the three shared fragments to `templates/fragments/` and change `tileGlob` to `"templates/fragments/*.html"`; rename the `tiles` field/template set to `fragments` throughout `server.go`/`dashboard.go`. Delete `templates/tiles/`. Write `templates/fragments/items.html`:

```html
{{/* The list section. It is one template for the page and for GET /items, so a poll or a filter
     change can never draw something the page would not have drawn (FR-1.6 AC1). The root carries
     the id htmx targets and the poll: every N seconds it re-fetches itself with the filter it was
     drawn with, so leaving the tab open keeps it current and never counts as a visit
     (FR-1.6 AC2). */}}
{{define "items"}}
<section class="items-section" id="items"
         hx-get="/items{{.Query}}" hx-trigger="every {{.PollSecs}}s" hx-swap="outerHTML">
  <p class="source-line">
    {{if .Source.Disabled}}<span class="notice notice-disabled" role="status">Disabled — {{.DisabledReason}}.</span>
    {{else if .Source.Error}}<span class="notice notice-error" role="alert">Failing — {{.Source.Error}} (last success {{if .Source.LastOK.Known}}<time datetime="{{.Source.LastOK.Absolute}}">{{.Source.LastOK.Relative}}</time>{{else}}never{{end}})</span>
    {{else if .Source.Stale}}<span class="notice notice-stale" role="status">Stale — nothing has been fetched recently.</span>
    {{else if .Source.LastOK.Known}}<span class="source-age">Fetched <time datetime="{{.Source.LastOK.Absolute}}">{{.Source.LastOK.Relative}}</time></span>
    {{else}}<span class="source-age">Never fetched</span>{{end}}
    <span class="shown-count">{{.ShownLine}}</span>
  </p>
  {{range .Groups}}
  <section class="repo-group">
    <h2 class="repo-title"><a href="https://github.com/{{.Repo}}" target="_blank" rel="noopener noreferrer">{{.Repo}}</a>
      {{if gt .NewCount 0}}<span class="tile-count">{{.NewCount}} new</span>{{end}}
      <span class="repo-count">{{.CountLine}}</span></h2>
    <ul class="items">
      {{range .Items}}
      <li class="item{{if .New}} is-new{{end}}">
        <p class="item-line">
          {{if .New}}<span class="badge badge-new">NEW</span>{{end}}
          {{if .Number}}<span class="item-number">#{{.Number}}</span>{{end}}
          <a class="item-title" href="{{.URL}}" target="_blank" rel="noopener noreferrer">{{.Title}}</a>
        </p>
        {{with .Summary}}<p class="item-summary">{{.}}</p>{{end}}
        <p class="item-meta">{{.Kind}}{{if .Author}} · {{.Author}}{{end}}</p>
        <p class="item-meta">{{if .Created.Known}}opened <time datetime="{{.Created.Absolute}}">{{.Created.Relative}}</time>{{else}}opened at an unknown time{{end}}{{if .Updated.Known}}, updated <time datetime="{{.Updated.Absolute}}">{{.Updated.Relative}}</time>{{end}}</p>
      </li>
      {{end}}
    </ul>
  </section>
  {{else}}
  <p class="empty">{{if .Filtered}}Nothing matches this filter.{{else}}Nothing open.{{end}}</p>
  {{end}}
</section>
{{end}}
```

The `<a>` to github.com is a link, not a fetched resource, so the CSP is unaffected. `dashboard.html` replaces the `.tiles` div with the filter form and `{{template "items" .Items}}`:

```html
<form class="filter" method="get" action="/"
      hx-get="/items" hx-trigger="change, keyup changed delay:400ms from:[name=q], search"
      hx-target="#items" hx-swap="outerHTML" hx-push-url="true">
  <label>Repository
    <select name="repo">
      <option value=""{{if eq .Filter.Repo ""}} selected{{end}}>all</option>
      {{range .Repos}}<option value="{{.}}"{{if eq . $.Filter.Repo}} selected{{end}}>{{.}}</option>{{end}}
    </select></label>
  <fieldset class="filter-kind"><legend>Kind</legend>
    <label><input type="radio" name="kind" value=""{{if eq .Filter.Kind ""}} checked{{end}}> all</label>
    <label><input type="radio" name="kind" value="issue"{{if eq .Filter.Kind "issue"}} checked{{end}}> issues</label>
    <label><input type="radio" name="kind" value="pr"{{if eq .Filter.Kind "pr"}} checked{{end}}> pull requests</label>
  </fieldset>
  <label>Created since <input type="date" name="since" value="{{.Filter.Since}}"></label>
  <label>Search <input type="search" name="q" value="{{.Filter.Text}}" placeholder="title or description"></label>
  <noscript><button type="submit">Apply</button></noscript>
</form>
{{template "items" .Items}}
```

`hx-push-url` with `hx-get="/items"` would push `/items?...` into the address bar; set `hx-push-url` to the page instead: htmx accepts a URL, so compute it server-side — put `hx-push-url="/{{.Items.Query}}"`? That is static per render; instead keep `hx-push-url="true"` and add `hx-replace-url` no. Simplest correct approach: add `hx-get="/items"` **and** `hx-push-url="/"` — htmx pushes `/` + the form's parameters, because for a `GET` form htmx appends the parameters to the push URL as it does to the request URL. Verify in the test at Step 6 by asserting the attribute values, and by hand in Step 9.

- [ ] **Step 4: Handlers and views in `dashboard.go`**

- Add `loc *time.Location` to `Server` (loaded in `New` from `o.Config.Timezone`, defaulting to UTC when empty so the existing tests keep working).
- `dashboard(ctx, f domain.Filter)` passes `Filter: f` into `DashboardInput`; delete the `Metrics` read.
- `handleDashboard` parses the filter from `r.URL.Query()`.
- Replace `handleTile` with `handleItems`: parse the filter, assemble, execute `"items"` with the view, then the same four out-of-band blocks.
- `disabledSources` loses the two branches; `tileCredential` and `disabledReason` become a single `githubDisabledReason()`.
- View models: delete `tileData`, `siteView`, `windowView`, `changeView`, `orderedSites`, `newSiteView`, `newWindowView`, `newChangeView`, `priorityLabel`, and `kindLabel`'s task case. Add:

```go
type itemsView struct {
	Query          template.URL // "?repo=…&kind=…" or "", the filter as a query string
	PollSecs       int
	Source         sourceView
	DisabledReason string
	Groups         []groupView
	Filtered       bool
	ShownLine      string // "12 of 40 open" / "40 open"
}
type sourceView struct {
	Disabled, Stale bool
	Error           string
	LastOK          timeView
}
type groupView struct {
	Repo      string
	NewCount  int
	CountLine string // "3 of 7" when filtered, "7" otherwise
	Items     []itemView
}
type filterView struct {
	Repo, Text, Since string // Since is "YYYY-MM-DD" or ""
	Kind              string // "", "issue", "pr"
}
```

`dashboardView` gains `Filter filterView`, `Repos []string` (from `s.cfg.GitHub.Repos`) and `Items itemsView`; drop `Tiles`. `queryString(f domain.Filter) string` builds the canonical `?repo=&kind=&since=&q=` with only the set parameters, using `url.Values.Encode()`.

- Route table: replace `{http.MethodGet, "/tile/{source}", authSessionFragment, s.handleTile, "/tile/github"}` with `{http.MethodGet, "/items", authSessionFragment, s.handleItems, ""}`.

- [ ] **Step 5: CSS**

In `app.css`: delete `.tiles`, `.tile*`, `.sites`, `.site*`, `.window*`, `.change*`, task/priority rules. Add `.filter` (a wrapping flex row, gap `0.75rem`, controls on one baseline, radio group inline), `.items-section`, `.source-line` (muted, small, the count right-aligned via `margin-left:auto`), `.repo-group` (a `margin-top: 1.5rem` block; no card chrome), `.repo-title` (font-size 1rem, weight 600, the count muted and small), keeping `.items`, `.item*`, `.badge-new`, `.tile-count` (rename to `.new-count` and update the template) as they are. Both themes must be covered by the existing custom properties; add no new colours. Keep `prefers-reduced-motion` untouched.

- [ ] **Step 6: Rewrite the affected web tests**

- `auth_test.go`: `fakeStore` loses `UpsertMetrics`/`Metrics`; `testOptions` unchanged otherwise.
- `dashboard_test.go` and `chrome_test.go`: every test that requested `/tile/github` requests `/items`; tests that asserted tile headings assert the repository heading; `TestTheGitHubTileShowsNumbersDescriptionsAndTheRemainder` becomes `TestTheListShowsNumbersDescriptionsAndCounts` asserting `#12`, the summary, and the `CountLine`; delete tests about sites/tasks tiles. Add:

```go
func TestTheFrontPageFiltersByQueryString(t *testing.T) {
	h := dashHandler(t, representativeStore()) // existing helper; its store must hold ≥2 repos, both kinds
	c := signedInCookie(t, h)                  // existing helper name may differ — reuse what auth_test uses
	all := getAs(h, "/", c).Body.String()
	prs := getAs(h, "/?kind=pr", c).Body.String()
	if strings.Count(prs, `class="item`) >= strings.Count(all, `class="item`) {
		t.Fatal("kind=pr did not narrow the list")
	}
	if !strings.Contains(prs, `value="pr" checked`) {
		t.Fatal("the filter form does not echo the applied kind")
	}
	none := getAs(h, "/?q=zzz-no-such-text", c).Body.String()
	if !strings.Contains(none, "Nothing matches this filter.") {
		t.Fatal("an empty filtered result should say so")
	}
}

func TestTheItemsFragmentCarriesTheOutOfBandBlocks(t *testing.T) {
	h := dashHandler(t, representativeStore())
	c := signedInCookie(t, h)
	body := getAs(h, "/items?kind=issue", c).Body.String()
	for _, want := range []string{`id="items"`, `hx-get="/items?kind=issue"`, `<title>`, `id="dash-summary"`, `hx-swap-oob="true"`} {
		if !strings.Contains(body, want) {
			t.Errorf("fragment lacks %s", want)
		}
	}
	if strings.Contains(body, "<html") {
		t.Fatal("the fragment must not be a whole document")
	}
}

func TestTheFilterFormIsAPlainGetFormWithHtmxOnTop(t *testing.T) {
	h := dashHandler(t, representativeStore())
	body := getAs(h, "/", signedInCookie(t, h)).Body.String()
	for _, want := range []string{`<form class="filter" method="get" action="/"`, `hx-target="#items"`, `hx-push-url=`, `name="repo"`, `name="kind"`, `name="since"`, `name="q"`} {
		if !strings.Contains(body, want) {
			t.Errorf("form lacks %s", want)
		}
	}
}

func TestTheBadgeIgnoresTheFilter(t *testing.T) {
	h := dashHandler(t, representativeStore())
	c := signedInCookie(t, h)
	all := getAs(h, "/", c).Body.String()
	some := getAs(h, "/?q=zzz-no-such-text", c).Body.String()
	title := func(s string) string { i := strings.Index(s, "<title>"); j := strings.Index(s, "</title>"); return s[i:j] }
	if title(all) != title(some) {
		t.Fatalf("tab title changed with the filter: %q vs %q", title(all), title(some))
	}
}
```

Use the helper names that actually exist in the test files (`dashHandler`, `representativeStore`, and whatever signs a test client in); read them before writing.

- [ ] **Step 7: Run the web package**

Run: `go test ./internal/web/ 2>&1 | tail -30` — iterate until PASS. Then `go vet ./...` and `go test ./...` — PASS everywhere.

- [ ] **Step 8: `make check`**

Run: `make check` — expected green (the fly validation step may need a login; see Global Constraints).

- [ ] **Step 9: Look at it**

`make backend` in one terminal (the `.env` still needs `ZORGSCOPE_TOKEN` until Task 6), `make client` in another, sign in, and check: the groups render in configuration order, the filter narrows without a reload, the address bar follows the filter, and with JavaScript disabled the form still submits. Report what was seen.

- [ ] **Step 10: Commit**

```bash
git add -A internal/web
git commit -m "feat(web): one filtered list grouped by repository replaces the tiles (FR-1.1, FR-1.2, FR-1.6)"
```

---

### Task 5: Fake GitHub OAuth endpoints and the repository endpoint

**Files:**
- Create: `internal/fakesources/oauth.go`, `internal/fakesources/oauth_test.go`
- Modify: `internal/fakesources/server.go` (routes, `routes` slice, `server` struct, `resetLocked`)

**Interfaces:**
- Consumes: Task 3's `routes` slice and index.
- Produces, on the fake server:

```text
GET  /login/oauth/authorize?client_id=…&state=S      303 → {callback}?code=fake-code&state=S
                                                     where callback = the value of the fake's
                                                     `redirect_uri` parameter if present, else the
                                                     one set by POST /_control/oauth-callback?url=…
POST /login/oauth/access_token   form or JSON body with code=fake-code → 200 JSON
                                 {"access_token":"fake-token","token_type":"bearer","scope":""}
                                 any other code → 400 JSON {"error":"bad_verification_code"}
GET  /repos/{owner}/{repo}       needs Authorization: Bearer fake-token → 200 JSON
                                 {"full_name":"owner/repo","permissions":{…}} per the control below;
                                 missing/other token → 401
POST /_control/oauth-user?permission={admin|maintain|push|pull|none|absent}
                                 decides the permissions block of the next repository responses:
                                 admin → {admin:true,maintain:true,push:true,triage:true,pull:true}
                                 maintain → {admin:false,maintain:true,push:true,…}
                                 push → {push:true,pull:true,…false}
                                 pull → {pull:true, rest false}
                                 none → all false
                                 absent → no "permissions" key at all
                                 default after reset: push
POST /_control/oauth-callback?url=http://…/auth/callback
                                 sets where /login/oauth/authorize redirects to when the request
                                 carries no redirect_uri (the backend sends none, §2 of the spec);
                                 default after reset: "" → authorize answers 400
```

- [ ] **Step 1: Write the failing tests**

`internal/fakesources/oauth_test.go`:

```go
func TestAuthorizeRedirectsToTheConfiguredCallbackWithTheState(t *testing.T) {
	h := NewServer()
	post(t, h, "/_control/oauth-callback?url=http://app.test/auth/callback")
	rec := get(t, h, "/login/oauth/authorize?client_id=abc&state=xyz")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code = %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "http://app.test/auth/callback?code=fake-code&state=xyz" {
		t.Fatalf("Location = %q", got)
	}
}

func TestAuthorizeWithoutACallbackIs400(t *testing.T) {
	rec := get(t, NewServer(), "/login/oauth/authorize?client_id=abc&state=xyz")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestTokenExchangeAcceptsOnlyTheFakeCode(t *testing.T) {
	h := NewServer()
	ok := postForm(t, h, "/login/oauth/access_token", url.Values{"code": {"fake-code"}, "client_id": {"abc"}, "client_secret": {"s"}})
	if ok.Code != http.StatusOK || !strings.Contains(ok.Body.String(), `"access_token":"fake-token"`) {
		t.Fatalf("exchange = %d %s", ok.Code, ok.Body.String())
	}
	bad := postForm(t, h, "/login/oauth/access_token", url.Values{"code": {"stale"}})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad code = %d", bad.Code)
	}
}

func TestRepositoryPermissionsFollowTheControl(t *testing.T) {
	h := NewServer()
	for perm, want := range map[string]string{
		"admin": `"push":true`, "maintain": `"push":true`, "push": `"push":true`,
		"pull": `"push":false`, "none": `"pull":false`,
	} {
		post(t, h, "/_control/oauth-user?permission="+perm)
		rec := getWithBearer(t, h, "/repos/gernotstarke/zorgscope", "fake-token")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s", perm, rec.Code, rec.Body.String())
		}
	}
	post(t, h, "/_control/oauth-user?permission=absent")
	if body := getWithBearer(t, h, "/repos/gernotstarke/zorgscope", "fake-token").Body.String(); strings.Contains(body, "permissions") {
		t.Fatalf("absent still has a permissions block: %s", body)
	}
}

func TestRepositoryNeedsTheFakeToken(t *testing.T) {
	h := NewServer()
	if rec := getWithBearer(t, h, "/repos/gernotstarke/zorgscope", "other"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestResetRestoresPushAndClearsTheCallback(t *testing.T) {
	h := NewServer()
	post(t, h, "/_control/oauth-user?permission=none")
	post(t, h, "/_control/oauth-callback?url=http://app.test/cb")
	post(t, h, "/_control/reset")
	if rec := getWithBearer(t, h, "/repos/o/r", "fake-token"); !strings.Contains(rec.Body.String(), `"push":true`) {
		t.Fatal("reset did not restore push")
	}
	if rec := get(t, h, "/login/oauth/authorize?state=s"); rec.Code != http.StatusBadRequest {
		t.Fatal("reset did not clear the callback")
	}
}
```

Write the four small helpers (`get`, `post`, `postForm`, `getWithBearer`) in the test file if the package's existing tests do not already have equivalents; reuse them if they do.

- [ ] **Step 2: Implement `oauth.go`**

```go
package fakesources

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

const (
	fakeCode  = "fake-code"
	fakeToken = "fake-token"
)

// permissionSets are the permissions blocks GET /repos/{owner}/{repo} can answer with. GitHub
// reports five booleans and higher roles imply the lower ones; "absent" leaves the key out, which
// is the shape the backend must treat as no access.
var permissionSets = map[string]map[string]bool{
	"admin":    {"admin": true, "maintain": true, "push": true, "triage": true, "pull": true},
	"maintain": {"admin": false, "maintain": true, "push": true, "triage": true, "pull": true},
	"push":     {"admin": false, "maintain": false, "push": true, "triage": true, "pull": true},
	"pull":     {"admin": false, "maintain": false, "push": false, "triage": false, "pull": true},
	"none":     {"admin": false, "maintain": false, "push": false, "triage": false, "pull": false},
	"absent":   nil,
}

func (s *server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	callback := q.Get("redirect_uri")
	s.mu.Lock()
	if callback == "" {
		callback = s.oauthCallback
	}
	s.mu.Unlock()
	if callback == "" {
		http.Error(w, "no callback: POST /_control/oauth-callback?url=… first, or pass redirect_uri", http.StatusBadRequest)
		return
	}
	u, err := url.Parse(callback)
	if err != nil {
		http.Error(w, "bad callback", http.StatusBadRequest)
		return
	}
	v := u.Query()
	v.Set("code", fakeCode)
	v.Set("state", q.Get("state"))
	u.RawQuery = v.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

func (s *server) handleAccessToken(w http.ResponseWriter, r *http.Request) {
	code := ""
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var body struct{ Code string `json:"code"` }
		_ = json.NewDecoder(r.Body).Decode(&body)
		code = body.Code
	} else {
		_ = r.ParseForm()
		code = r.PostForm.Get("code")
	}
	if code != fakeCode {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_verification_code"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"access_token": fakeToken, "token_type": "bearer", "scope": ""})
}

func (s *server) handleRepository(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+fakeToken {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	s.mu.Lock()
	perms := permissionSets[s.oauthPermission]
	s.mu.Unlock()
	body := map[string]any{"full_name": r.PathValue("owner") + "/" + r.PathValue("repo")}
	if perms != nil {
		body["permissions"] = perms
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *server) handleControlOAuthUser(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("permission")
	if _, ok := permissionSets[p]; !ok {
		http.Error(w, "permission must be one of admin, maintain, push, pull, none, absent", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.oauthPermission = p
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (s *server) handleControlOAuthCallback(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.oauthCallback = r.URL.Query().Get("url")
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}
```

Add `oauthPermission string` and `oauthCallback string` to `server`; `resetLocked` sets `oauthPermission = "push"`, `oauthCallback = ""`. Register the five routes in `NewServer` and append them to `routes` in the same order. `writeJSON` exists in `util.go`.

Note on `GET /repos/{owner}/{repo}`: it must not shadow `GET /repos/{owner}/{repo}/actions/runs`; Go's mux picks the more specific pattern, so both coexist.

- [ ] **Step 3: Run the fakes tests**

Run: `go test ./internal/fakesources/ -v` — PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/fakesources
git commit -m "feat(fakes): GitHub OAuth authorize, token and repository endpoints (FR-9.2)"
```

---

### Task 6: Sign in with GitHub

**Files:**
- Create: `internal/web/signin.go`, `internal/web/signin_test.go`, `internal/adapters/github/access.go`, `internal/adapters/github/access_test.go`
- Modify: `internal/config/config.go`, `internal/config/config_test.go`, `internal/config/testdata/valid.yaml`, `config/zorgscope.yaml`, `deploy/env.example`, `Makefile` (the `backend` check), `internal/web/auth.go`, `internal/web/server.go` (`Options`, `Server`, `New`, `routes`, the CSP comment), `internal/web/templates/login.html`, `internal/web/auth_test.go`, `internal/web/chrome_test.go`, `cmd/zorgscope/main.go`, `cmd/zorgscope/main_test.go`
- Delete: `handleLoginSubmit` and the `POST /login` route

**Interfaces:**
- Consumes: `ports.AccessChecker` (Task 2), the fake endpoints (Task 5).
- Produces:

```go
// config
type GitHub struct {
	Login, AuthRepo, BaseURL, BadgeBaseURL, OAuthBaseURL string
	Repos []string
}
type Secrets struct {
	GitHubToken, SlackWebhook           string
	OAuthClientID, OAuthClientSecret    string
	RefreshSecret                       string
	TursoURL, TursoAuthToken            string
}
// Load: github.auth_repo required and owner/name; GITHUB_OAUTH_CLIENT_ID and
// GITHUB_OAUTH_CLIENT_SECRET required (non-empty); GITHUB_OAUTH_BASE_URL optional.

// adapters/github
func NewAccessChecker(apiBase, repo string, hc *http.Client) *AccessChecker
func (a *AccessChecker) HasPushAccess(ctx context.Context, token string) (bool, error)

// web
type Options struct { …; Access ports.AccessChecker; HTTPClient *http.Client }
// New refuses a nil Access, an empty OAuthClientID or OAuthClientSecret.
// routes: GET /login (public), GET /auth/github (public), GET /auth/callback (public).
```

- [ ] **Step 1: Config — failing test, then implementation**

Add to `config_test.go`:

```go
func TestLoadRequiresTheOAuthPairAndTheAuthRepo(t *testing.T) {
	base := map[string]string{"REFRESH_SECRET": strings.Repeat("r", 32), "GITHUB_OAUTH_CLIENT_ID": "id", "GITHUB_OAUTH_CLIENT_SECRET": "secret"}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	if _, err := Load("testdata/valid.yaml", env(base)); err != nil {
		t.Fatalf("valid: %v", err)
	}
	for _, missing := range []string{"GITHUB_OAUTH_CLIENT_ID", "GITHUB_OAUTH_CLIENT_SECRET"} {
		m := maps.Clone(base)
		delete(m, missing)
		if _, err := Load("testdata/valid.yaml", env(m)); err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("without %s: err = %v", missing, err)
		}
	}
	if _, err := Load("testdata/no-auth-repo.yaml", env(base)); err == nil || !strings.Contains(err.Error(), "github.auth_repo") {
		t.Errorf("without auth_repo: err = %v", err)
	}
}
```

Create `testdata/no-auth-repo.yaml` as a copy of `valid.yaml` without the field; add `auth_repo: gernotstarke/zorgscope` to `valid.yaml` and to `config/zorgscope.yaml` (under `github:`, with a one-line comment: "push access here admits a visitor (FR-8.3)"). In `config.go`: the fields per the Interfaces block; `AppToken` and its length check go; validation:

```go
if !repoPattern.MatchString(fc.GitHub.AuthRepo) {
	return Config{}, fmt.Errorf("github.auth_repo: %q is not in owner/name form", fc.GitHub.AuthRepo)
}
if cfg.Secrets.OAuthClientID == "" {
	return Config{}, errors.New("GITHUB_OAUTH_CLIENT_ID is not set")
}
if cfg.Secrets.OAuthClientSecret == "" {
	return Config{}, errors.New("GITHUB_OAUTH_CLIENT_SECRET is not set")
}
```

`TestErrorNeverContainsSecretValues` must now seed the two OAuth values as canaries too. Run `go test ./internal/config/` — PASS. Update `deploy/env.example` (replace the `ZORGSCOPE_TOKEN` block):

```env
# Sign in with GitHub (FR-8.3): the OAuth App registered for THIS environment. Register one App per
# environment, because an App has exactly one callback URL:
#   local:      callback http://localhost:8080/auth/callback
#   production: callback https://zorgscope.fly.dev/auth/callback  (set these as Fly secrets)
# The client secret also seeds the session cookie's signing key: rotating it signs everyone out.
GITHUB_OAUTH_CLIENT_ID=
GITHUB_OAUTH_CLIENT_SECRET=
# Optional: where the authorize and token endpoints live. Empty means https://github.com.
# GITHUB_OAUTH_BASE_URL=http://host.docker.internal:9090
```

Makefile `backend`: replace `need ZORGSCOPE_TOKEN` with `need GITHUB_OAUTH_CLIENT_ID; need GITHUB_OAUTH_CLIENT_SECRET;` and reword the two printf hints ("Fill in GITHUB_OAUTH_CLIENT_ID, GITHUB_OAUTH_CLIENT_SECRET and REFRESH_SECRET (openssl rand -base64 32)" / "The OAuth pair comes from the GitHub OAuth App; REFRESH_SECRET needs at least 32 characters").

- [ ] **Step 2: The access checker — failing test against the fake**

`internal/adapters/github/access_test.go` (package `github_test`, using `httptest.NewServer(fakesources.NewServer())`):

```go
func TestHasPushAccessReadsThePermissionsBlock(t *testing.T) {
	fake := httptest.NewServer(fakesources.NewServer())
	defer fake.Close()
	a := github.NewAccessChecker(fake.URL, "gernotstarke/zorgscope", fake.Client())
	for perm, want := range map[string]bool{"admin": true, "maintain": true, "push": true, "pull": false, "none": false, "absent": false} {
		control(t, fake.URL+"/_control/oauth-user?permission="+perm)
		got, err := a.HasPushAccess(context.Background(), "fake-token")
		if err != nil {
			t.Fatalf("%s: %v", perm, err)
		}
		if got != want {
			t.Errorf("%s: HasPushAccess = %v, want %v", perm, got, want)
		}
	}
}

func TestHasPushAccessFailsClosedOnAnErrorAndNeverQuotesTheToken(t *testing.T) {
	fake := httptest.NewServer(fakesources.NewServer())
	defer fake.Close()
	a := github.NewAccessChecker(fake.URL, "gernotstarke/zorgscope", fake.Client())
	got, err := a.HasPushAccess(context.Background(), "wrong-token-canary")
	if got || err == nil {
		t.Fatalf("got %v, err %v; a 401 must be an error and no access", got, err)
	}
	if strings.Contains(err.Error(), "wrong-token-canary") {
		t.Fatal("the error quotes the token")
	}
}
```

Implementation `access.go`:

```go
// AccessChecker answers FR-8.3's one question — may this visitor see the dashboard? — by asking
// GitHub what the visitor may do to the configured repository. It is a ports.AccessChecker.
type AccessChecker struct {
	apiBase string // "" means https://api.github.com
	repo    string // owner/name
	hc      *http.Client
}

func NewAccessChecker(apiBase, repo string, hc *http.Client) *AccessChecker {
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	return &AccessChecker{apiBase: strings.TrimRight(apiBase, "/"), repo: repo, hc: hc}
}

// HasPushAccess makes one request with the visitor's token and reads the permissions block GitHub
// returns for the authenticated user. Push or admin admits; a missing block, any other status
// and any transport error refuse — the check fails closed. The token is never part of an error.
func (a *AccessChecker) HasPushAccess(ctx context.Context, token string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.apiBase+"/repos/"+a.repo, nil)
	if err != nil {
		return false, fmt.Errorf("access check: building the request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := a.hc.Do(req)
	if err != nil {
		return false, errors.New("access check: request failed") // err may carry the URL; the URL never carries the token, but keep it terse
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("access check: GitHub answered %d", resp.StatusCode)
	}
	var body struct {
		Permissions *struct{ Admin, Push bool } `json:"permissions"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return false, fmt.Errorf("access check: reading the answer: %w", err)
	}
	if body.Permissions == nil {
		return false, nil
	}
	return body.Permissions.Push || body.Permissions.Admin, nil
}
```

Run `go test ./internal/adapters/github/ -run TestHasPushAccess -v` — PASS.

- [ ] **Step 3: The web flow — failing tests**

`internal/web/signin_test.go`. The test server needs an OAuth base URL pointing at an in-process fake, an `Access` stub, and an `HTTPClient`; extend `testOptions()` in `auth_test.go`:

```go
// in testOptions(): Secrets gets OAuthClientID: "test-client-id", OAuthClientSecret: testClientSecret
// GitHub gets AuthRepo: "gernotstarke/zorgscope"
// Options gets Access: &stubAccess{allow: map[string]bool{"fake-token": true}}
// and OAuthBaseURL is set per test by startFakeGitHub.

type stubAccess struct {
	allow map[string]bool
	err   error
}
func (s *stubAccess) HasPushAccess(_ context.Context, token string) (bool, error) {
	return s.allow[token], s.err
}

// startFakeGitHub starts the fixture server, points the options' OAuth base URL at it, and
// registers the app's callback with it. It returns the fake's URL.
func startFakeGitHub(t *testing.T, o *Options) string {
	t.Helper()
	fake := httptest.NewServer(fakesources.NewServer())
	t.Cleanup(fake.Close)
	o.Config.GitHub.OAuthBaseURL = fake.URL
	o.HTTPClient = fake.Client()
	resp, err := http.Post(fake.URL+"/_control/oauth-callback?url=http://zorgscope.test/auth/callback", "", nil)
	if err != nil { t.Fatal(err) }
	_ = resp.Body.Close()
	return fake.URL
}
```

The tests:

```go
func TestTheLoginPageOffersOneLinkToGitHub(t *testing.T) {
	body := get(newTestServer(t).Handler(), "/login").Body.String()
	if !strings.Contains(body, `href="/auth/github"`) || strings.Contains(body, `type="password"`) {
		t.Fatalf("login page = %s", body)
	}
}

func TestStartingSignInSetsAStateCookieAndRedirectsToGitHub(t *testing.T) {
	var o Options
	s := newTestServerWith(t, func(opt *Options) { startFakeGitHub(t, opt); o = *opt })
	rec := get(s.Handler(), "/auth/github")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code = %d", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if !strings.HasPrefix(loc.String(), o.Config.GitHub.OAuthBaseURL+"/login/oauth/authorize") {
		t.Fatalf("Location = %s", loc)
	}
	if loc.Query().Get("client_id") != "test-client-id" || loc.Query().Get("scope") != "" || loc.Query().Get("redirect_uri") != "" {
		t.Fatalf("query = %v", loc.Query())
	}
	c := cookieNamed(rec, stateCookieName)
	if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Value != loc.Query().Get("state") {
		t.Fatalf("state cookie = %+v", c)
	}
}

// signInThroughGitHub drives the whole flow against the fake and returns the callback response.
func signInThroughGitHub(t *testing.T, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	start := get(h, "/auth/github")
	state := cookieNamed(start, stateCookieName)
	// The fake would redirect the browser to /auth/callback?code=fake-code&state=…; drive that
	// request directly, with the state cookie the browser would carry.
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=fake-code&state="+url.QueryEscape(state.Value), nil)
	req.AddCookie(state)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestACollaboratorIsSignedIn(t *testing.T) {
	s := newTestServerWith(t, func(o *Options) { startFakeGitHub(t, o) })
	rec := signInThroughGitHub(t, s.Handler())
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("callback = %d %s", rec.Code, rec.Header().Get("Location"))
	}
	session := cookieNamed(rec, sessionCookieName)
	if session == nil || !s.session.valid(session.Value, testNow) {
		t.Fatal("no valid session cookie")
	}
	if c := cookieNamed(rec, stateCookieName); c == nil || c.MaxAge >= 0 {
		t.Fatal("the state cookie was not cleared")
	}
	if dash := getAs(s.Handler(), "/", session); dash.Code != http.StatusOK {
		t.Fatalf("dashboard with the new session = %d", dash.Code)
	}
}

func TestAStrangerIsRefusedWithoutASession(t *testing.T) {
	s := newTestServerWith(t, func(o *Options) {
		startFakeGitHub(t, o)
		o.Access = &stubAccess{allow: map[string]bool{}}
	})
	rec := signInThroughGitHub(t, s.Handler())
	if rec.Code != http.StatusForbidden || cookieNamed(rec, sessionCookieName) != nil {
		t.Fatalf("stranger: %d, cookie %v", rec.Code, cookieNamed(rec, sessionCookieName))
	}
	if !strings.Contains(rec.Body.String(), "collaborators of gernotstarke/zorgscope") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestAMismatchedStateIsRefused(t *testing.T) {
	s := newTestServerWith(t, func(o *Options) { startFakeGitHub(t, o) })
	start := get(s.Handler(), "/auth/github")
	state := cookieNamed(start, stateCookieName)
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=fake-code&state=forged", nil)
	req.AddCookie(state)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || cookieNamed(rec, sessionCookieName) != nil {
		t.Fatalf("forged state: %d", rec.Code)
	}
}

func TestACallbackWithoutAStateCookieIsRefused(t *testing.T) {
	s := newTestServerWith(t, func(o *Options) { startFakeGitHub(t, o) })
	rec := get(s.Handler(), "/auth/callback?code=fake-code&state=anything")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestAFailedExchangeIsRefused(t *testing.T) {
	s := newTestServerWith(t, func(o *Options) { startFakeGitHub(t, o) })
	start := get(s.Handler(), "/auth/github")
	state := cookieNamed(start, stateCookieName)
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=stale&state="+url.QueryEscape(state.Value), nil)
	req.AddCookie(state)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway || cookieNamed(rec, sessionCookieName) != nil {
		t.Fatalf("failed exchange: %d", rec.Code)
	}
}

func TestAnAccessCheckErrorFailsClosed(t *testing.T) {
	s := newTestServerWith(t, func(o *Options) {
		startFakeGitHub(t, o)
		o.Access = &stubAccess{err: errors.New("github is down")}
	})
	rec := signInThroughGitHub(t, s.Handler())
	if rec.Code != http.StatusBadGateway || cookieNamed(rec, sessionCookieName) != nil {
		t.Fatalf("access error: %d", rec.Code)
	}
}

func TestRefusedCallbacksAreRateLimited(t *testing.T) {
	s := newTestServerWith(t, func(o *Options) {
		startFakeGitHub(t, o)
		o.Access = &stubAccess{allow: map[string]bool{}}
	})
	for i := 0; i < signInAttempts; i++ {
		if rec := signInThroughGitHub(t, s.Handler()); rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d = %d", i, rec.Code)
		}
	}
	if rec := signInThroughGitHub(t, s.Handler()); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after the budget: %d", rec.Code)
	}
}

func TestRotatingTheClientSecretInvalidatesExistingSessions(t *testing.T) {
	old := newTestServerWith(t, func(o *Options) { startFakeGitHub(t, o) })
	session := cookieNamed(signInThroughGitHub(t, old.Handler()), sessionCookieName)
	rotated := newTestServerWith(t, func(o *Options) { o.Config.Secrets.OAuthClientSecret = testClientSecret + "-rotated" })
	if rec := getAs(rotated.Handler(), "/", session); rec.Code != http.StatusSeeOther {
		t.Fatalf("old session after rotation = %d, want a redirect to sign-in", rec.Code)
	}
}

func TestNoSignInResponseOrLogLineCarriesACodeStateTokenOrSecret(t *testing.T) {
	var logs bytes.Buffer
	s := newTestServerWith(t, func(o *Options) {
		startFakeGitHub(t, o)
		o.Log = slog.New(slog.NewTextHandler(&logs, nil))
		o.Access = &stubAccess{allow: map[string]bool{}}
	})
	start := get(s.Handler(), "/auth/github")
	state := cookieNamed(start, stateCookieName)
	rec := signInThroughGitHub(t, s.Handler())
	for _, secret := range []string{"fake-code", "fake-token", testClientSecret, state.Value} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("response carries %q", secret)
		}
		if strings.Contains(logs.String(), secret) {
			t.Errorf("log carries %q", secret)
		}
	}
}
```

Adapt `TestNoResponseEverContainsASecret` and `TestRedactScrubsEverySecretValue` (they iterate `config.Secrets` by reflection — check they now cover `OAuthClientID` and `OAuthClientSecret`; the id is not a secret but scrubbing it is harmless). Delete `TestSignInIssuesAHardenedCookie` (replaced by `TestACollaboratorIsSignedIn`), `TestChangingTheTokenInvalidatesExistingSessions` (replaced), `TestFailedSignInsAreRateLimited`, `TestFailedSignInsAreLoggedWithoutTheSubmittedValue`, `TestASignInLockoutRecoversAsTheClockAdvances` (rewrite it around refused callbacks if it is cheap; otherwise delete and note it). Every test that signed in by posting `token` now calls `signInThroughGitHub` — write a helper `signedInCookie(t, s *Server) *http.Cookie` that mints one directly with `s.session.mint(testNow.Add(time.Hour))` for tests that only need a session and are not about sign-in; that keeps the dashboard tests independent of the OAuth flow. `TestNewRejectsAMissingCredential` cases: "no client id", "no client secret", "no access checker", "no refresh secret".

- [ ] **Step 4: Implement `signin.go`, adjust `auth.go`, `server.go`, `login.html`, `main.go`**

`auth.go`: `sessionKeyContext = "zorgscope-session-v2"`; `newSessionCodec(secret string)` unchanged in shape; delete `handleLoginForm`'s sibling `handleLoginSubmit`; add:

```go
const (
	stateCookieName = "zorgscope_oauth_state"
	stateTTL        = 10 * time.Minute
)
```

`signin.go`:

```go
package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"golang.org/x/oauth2"
)

// oauthConfig is the client for the OAuth App this deployment was registered as. RedirectURL is
// deliberately empty: GitHub then sends the browser to the callback registered on the App, so the
// process never has to know or trust its own public host (design 2026-09-14 §2).
func (s *Server) oauthConfig() *oauth2.Config {
	base := s.cfg.GitHub.OAuthBaseURL
	if base == "" {
		base = "https://github.com"
	}
	return &oauth2.Config{
		ClientID:     s.cfg.Secrets.OAuthClientID,
		ClientSecret: s.cfg.Secrets.OAuthClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:   base + "/login/oauth/authorize",
			TokenURL:  base + "/login/oauth/access_token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
}

// handleLoginForm shows the sign-in page, or sends an already signed-in browser to the dashboard.
func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if s.signedIn(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "login.html", pageData{Title: "Sign in"})
}

// handleAuthStart begins the flow: a random state in a short-lived cookie, and a redirect to
// GitHub. It is a GET because the CSP's form-action would let Chrome block the redirect after a
// POST, and because all it does is set one cookie.
func (s *Server) handleAuthStart(w http.ResponseWriter, r *http.Request) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		s.fail(w, r, "starting sign-in", err)
		return
	}
	state := base64.RawURLEncoding.EncodeToString(raw[:])
	http.SetCookie(w, &http.Cookie{
		Name: stateCookieName, Value: state, Path: "/auth/callback",
		MaxAge: int(stateTTL.Seconds()), HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, s.oauthConfig().AuthCodeURL(state), http.StatusSeeOther)
}

// handleAuthCallback finishes the flow. Every refusal clears the state cookie, sets no session,
// counts against the sign-in rate limit and is logged without the code, the state or the token.
func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	clearState := func() {
		http.SetCookie(w, &http.Cookie{Name: stateCookieName, Path: "/auth/callback", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	}
	refuse := func(status int, why, msg string) {
		clearState()
		ip := s.clientIP(r)
		if !s.signIn.allow(ip, s.clock.Now()) {
			s.log.Warn("sign-in rate-limited", "ip", ip)
			s.render(w, r, http.StatusTooManyRequests, "login.html", pageData{Title: "Sign in", Error: "Too many attempts. Try again later."})
			return
		}
		s.log.Warn("sign-in refused", "ip", ip, "why", why)
		s.render(w, r, status, "login.html", pageData{Title: "Sign in", Error: msg})
	}

	c, err := r.Cookie(stateCookieName)
	q := r.URL.Query()
	if err != nil || c.Value == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(q.Get("state"))) != 1 {
		refuse(http.StatusBadRequest, "state mismatch", "That sign-in did not start here. Try again.")
		return
	}
	if q.Get("code") == "" {
		refuse(http.StatusBadRequest, "no code", "GitHub sent no code. Try again.")
		return
	}

	ctx := context.WithValue(r.Context(), oauth2.HTTPClient, s.httpClient)
	tok, err := s.oauthConfig().Exchange(ctx, q.Get("code"))
	if err != nil {
		refuse(http.StatusBadGateway, "exchange failed", "GitHub did not accept the sign-in. Try again.")
		return
	}
	ok, err := s.access.HasPushAccess(ctx, tok.AccessToken)
	if err != nil {
		refuse(http.StatusBadGateway, "access check failed", "GitHub could not be asked who you are. Try again.")
		return
	}
	if !ok {
		refuse(http.StatusForbidden, "no push access", "This dashboard is for collaborators of "+s.cfg.GitHub.AuthRepo+".")
		return
	}
	clearState()
	s.log.Info("sign-in accepted", "ip", s.clientIP(r))
	s.setSession(w, s.clock.Now())
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
```

Check what `s.fail` and `s.render` are called in this package and match them. `server.go`: `Options.Access ports.AccessChecker` and `Options.HTTPClient *http.Client`; `Server.access`, `Server.httpClient` (default `&http.Client{Timeout: 15 * time.Second}`); `New` checks `o.Access != nil`, `OAuthClientID != ""`, `OAuthClientSecret != ""` with errors naming the variable, never the value; `session: newSessionCodec(o.Config.Secrets.OAuthClientSecret)`; routes:

```go
{http.MethodGet, "/login", authPublic, s.handleLoginForm, ""},
{http.MethodGet, "/auth/github", authPublic, s.handleAuthStart, ""},
{http.MethodGet, "/auth/callback", authPublic, s.handleAuthCallback, ""},
```

Update the CSP comment (the form that submitted the token is gone; `form-action 'self'` stays for the filter and control forms). `login.html`:

```html
{{define "content"}}
<section class="signin">
  <h1>Sign in</h1>
  {{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
  <p>zorgscope is for the people who can push to its repository.</p>
  <a class="button" href="/auth/github">Sign in with GitHub</a>
</section>
{{end}}
```

Give `.button` the same look as `button` in `app.css`. `main.go`: build `github.NewAccessChecker(cfg.GitHub.BaseURL, cfg.GitHub.AuthRepo, hc)` and pass `Access:` and `HTTPClient: hc` to `web.New`. `main_test.go`: set the OAuth pair and `auth_repo` wherever it built a config.

- [ ] **Step 5: Run everything**

Run: `go test ./...` — PASS. Then `make check` — PASS (fly step per Global Constraints).

- [ ] **Step 6: Try it against the fake**

In `.env`: set `GITHUB_OAUTH_CLIENT_ID=local-fake`, `GITHUB_OAUTH_CLIENT_SECRET=local-fake-secret`, `GITHUB_OAUTH_BASE_URL=http://host.docker.internal:9090`, `GITHUB_BASE_URL=http://host.docker.internal:9090`. Run `make fakes`, then `curl -X POST 'http://localhost:9090/_control/oauth-callback?url=http://localhost:8080/auth/callback'`, then `make backend` and `make client`. Sign in: the browser goes to the fake, straight back, and lands on the dashboard. Then `curl -X POST 'http://localhost:9090/_control/oauth-user?permission=pull'`, open a private window, sign in again: refused with the collaborators sentence. Report both.

- [ ] **Step 7: Commit**

```bash
git add -A internal config deploy/env.example Makefile cmd
git commit -m "feat(web): sign in with GitHub, admitting push access to the repository (FR-8.3, QS-4.2, QS-4.3)"
```

---

### Task 7: Documentation

**Files:**
- Create: `docs/decisions/0009-github-sign-in-push-access.md`
- Modify: `docs/requirements/01-goals.md`, `02-stakeholders.md`, `03-constraints.md`, `04-functional-requirements.md`, `06-glossary.md`, `docs/decisions/0007-token-sign-in-derived-cookie.md`, `docs/decisions/README.md`, `docs/concepts/security-and-tokens.md`, `docs/concepts/configuration.md`, `docs/concepts/data-storage.md` (if it names metrics or tasks), `README.md`
- Test: `make check` (markdownlint + lychee), and `go test ./internal/web/ -run 'Docs|Doc'` (the rendered-docs tests, which check every internal link resolves and the index lists every page)

This task only touches Markdown and may run in a worktree in parallel with Tasks 4–6.

- [ ] **Step 1: Requirements**

- `01-goals.md`: vision sentence → "collects the open issues and pull requests of the arc42 sites' repositories on GitHub, marks what is new since the last look, and costs almost nothing to run." Rows G‑2 and G‑3: wrap the goal text in `~~…~~` and append " — *retired 2026‑09‑14, see [the design](../superpowers/specs/2026-09-14-github-signin-and-focus-design.md)*". Keep the ids.
- `02-stakeholders.md` S‑3: drop Plausible and Todoist. `03-constraints.md` C‑8: drop them from the free-tier list.
- `04-functional-requirements.md`:
  - FR‑1.1 AC1: "The dashboard is one list of the open issues and pull requests of the configured repositories, a section per repository in configuration order, new items first — plus a single build-status indicator, builds having their own page (FR‑2.3 AC4). AC5 The list can be narrowed by repository, by kind (issue or pull request), by creation date and by a text search over title and description; the filter is carried in the URL, works without JavaScript, and never changes the new count in the tab title or the summary line."
  - FR‑1.2 AC2: "Each repository section shows how many of its items are new."
  - FR‑1.4 AC1: "The page states the age of its data." AC3: "…never blanks the list…"
  - FR‑1.6 AC1: "The list refreshes its fragment by htmx polling at a configured interval, keeping the filter it was drawn with."
  - Delete E‑3 and E‑4 entirely; add one line under "Explicitly out of scope": "Site statistics (Plausible) and task lists (Todoist), removed 2026‑09‑14 after a month of not being looked at."
  - FR‑8.1 AC1: "The file lists GitHub repositories, the repository whose push access admits a visitor, the refresh interval and the notification settings."
  - FR‑8.2 AC1: "GitHub token, the OAuth App's client id and secret, Slack webhook and the refresh secret come from environment variables (Fly secrets in production)."
  - FR‑8.3: story "As the user I sign in with my GitHub account, once per browser." AC1 unchanged. AC2 "Signing in with GitHub through the registered OAuth App, with no scopes requested, issues a signed, HttpOnly, SameSite=Lax session cookie; the visitor's GitHub token is used for one permission check and never stored." AC3 "Only a GitHub user with push or admin permission on the configured repository is admitted; anyone else is refused with a page that says so and receives no session." AC4 "The cookie's signing key derives from the OAuth client secret, so rotating the secret invalidates every session." AC5 "Refused sign-ins are rate-limited and logged without the code, the state or the token."
  - FR‑9.1 AC3: "`make check` needs only Docker and make and runs what CI runs."
  - FR‑9.2 AC1: "A fake-sources server serves GitHub responses — issues, pull requests, workflow runs, and the OAuth endpoints a sign-in needs — from fixtures."
  - FR‑9.3: story "As the operator I deploy from CI, not from my machine." AC1 "A push to `main` deploys to Fly through GitHub Actions; `make check` validates `deploy/fly.toml` before it gets there." (drop AC2.)
- `06-glossary.md`: *Item* → "a GitHub issue or pull request"; *Source* → "GitHub is the one source; the builds fetcher is a second fetcher over the same credential"; *Fake sources* → GitHub only.

- [ ] **Step 2: Decisions**

Write `0009-github-sign-in-push-access.md` from `adr-template.md`, status accepted, date 2026‑09‑14, requirements FR‑8.2, FR‑8.3, QS‑4.2, QS‑4.3. Context: the shared token names nobody and is pasted by hand; GitHub already maintains exactly the set of people who should see the page. Options: (1) GitHub OAuth App, no scopes, one request for the visitor's permission on the repository, session key derived from the client secret — chosen; (2) keep the shared token; (3) a GitHub App with an installation. Decision outcome: option 1, because the admission rule lives where it is already maintained, the derivation property of ADR‑0007 survives unchanged, and the only new moving part is one HTTP request. Consequences: good — no secret to paste, more than one collaborator can sign in, revocation is a GitHub setting; bad — two OAuth Apps to register (one callback per App), a dependency on GitHub being up at sign-in; neutral — the session still carries no identity, and a stolen cookie still leaks a thirty-day session and nothing else. Name the risk from the spec's §8 about the `permissions` block. Mark `0007` "Status: superseded by [0009](0009-github-sign-in-push-access.md)" in its status line and in the index table (`superseded`). Add the 0009 row to `README.md`'s table and change its "written in Task 19" paragraph to say the records are kept current with the design specs.

- [ ] **Step 3: Concepts**

`security-and-tokens.md`: rewrite the credentials table (OAuth client secret / `GITHUB_OAUTH_CLIENT_SECRET` / sign-in and the session key; refresh secret unchanged; `GITHUB_TOKEN` and the visitor's own token in a third row: "used for one request, never stored"); the derivation block with `zorgscope-session-v2` and the client secret; the rate-limit paragraph now about refused callbacks; a "What the callback logs" paragraph (ip, reason, never code/state/token); rotation: regenerate the client secret on GitHub, set it as a Fly secret, everyone is signed out. Delete every "note on evidence" paragraph that says auth.go does not exist yet — it does. Delete the `make fly*` references (those targets are gone); say "set it as a Fly secret with flyctl". `configuration.md`: the YAML example with `auth_repo` and without the two sources; the environment table with the OAuth pair and `GITHUB_OAUTH_BASE_URL`; the `Enabled` snippet with only the github case; a new section "Registering the two OAuth Apps" with the table from the spec's §6; remove `make check-env`, `make fly-deploy`, `make db-shell` mentions (say `make backend` refuses to start on an incomplete `.env`; deploys happen from CI). `data-storage.md`: remove any metrics/tasks table description and mention migration 0005 in one sentence.

- [ ] **Step 4: README**

Feature bullets: keep the GitHub bullet, add "Filters — by repository, kind, creation date and text; every filtered view is a URL." Remove Sites and Tasks. Package table: `cmd/fakesources/` "Fixture-backed GitHub, including the OAuth endpoints"; `internal/adapters/` "GitHub, Slack, libSQL". Quick start: `cp deploy/env.example .env   # then fill in the GitHub OAuth pair, REFRESH_SECRET and GITHUB_TOKEN`.

- [ ] **Step 5: Verify**

Run: `make check` (markdownlint and lychee run inside it; if the Go steps are red because Tasks 4–6 are not merged into this worktree yet, run the two docs containers from the `check` recipe by hand) and `go test ./internal/web/ -run 'Doc'` once merged. Grep: `grep -rn "Plausible\|Todoist\|ZORGSCOPE_TOKEN\|make fly\|make db-\|make test\b\|make lint" README.md docs/requirements docs/concepts docs/decisions/README.md` must return nothing except the retired-goal rows and the out-of-scope line.

- [ ] **Step 6: Commit**

```bash
git add README.md docs
git commit -m "docs: GitHub sign-in (ADR-0009), retire Plausible and Todoist, describe the filtered list (FR-1.1, FR-8.3)"
```

---

### Task 8: Merge, verify end to end, and hand over

**Files:**
- Modify: `docs/superpowers/plans/HANDOVER.md` (append a dated section), memory.

- [ ] **Step 1: Merge Task 7's worktree branch** into the working branch (rebase or merge, no squash), resolve nothing that is not a conflict, and run `go test ./...` and `make check` — green.

- [ ] **Step 2: Real-GitHub verification (needs the operator's OAuth Apps)**

This step is for the operator, and this plan cannot complete it unattended. Append to `HANDOVER.md`:

```markdown
## 2026-09-14 — sign in with GitHub, GitHub-only dashboard

Implemented per docs/superpowers/specs/2026-09-14-github-signin-and-focus-design.md. Verified
against the fake GitHub in tests and by hand. **Not yet verified against real GitHub**, which
needs the two OAuth Apps (spec §6). To finish:

1. Register the local App (callback http://localhost:8080/auth/callback), put its id and secret in
   .env as GITHUB_OAUTH_CLIENT_ID / GITHUB_OAUTH_CLIENT_SECRET, leave GITHUB_OAUTH_BASE_URL and
   GITHUB_BASE_URL unset, `make backend`, `make client`, sign in. Expect the dashboard. Check the
   log line `sign-in accepted`.
2. In a private window, sign in as a GitHub account without push access (or temporarily remove
   your own collaborator status on a test repository named in github.auth_repo). Expect the
   "collaborators of …" refusal and the log line `sign-in refused … why=no push access`.
3. If step 1 is refused with `why=no push access` although the account can push, GitHub answered
   without a permissions block for an unscoped token: the fix is to request the `read:org` scope
   in Server.oauthConfig (spec §8). Record the outcome here.
4. Register the production App, set the pair as Fly secrets, unset ZORGSCOPE_TOKEN there, push
   to main.
```

- [ ] **Step 3: Update the project memory** (`~/.claude/projects/-Users-gernotstarke-projects-privat-zorgscope/memory/zorgscope-project.md`): scope is GitHub-only, sign-in is GitHub OAuth gated on push access, the six make targets, and the open item "real-GitHub sign-in not yet verified".

- [ ] **Step 4: Commit**

```bash
git add docs/superpowers/plans/HANDOVER.md
git commit -m "docs(handover): what remains to verify the GitHub sign-in against real GitHub"
```
