# Per-Site Tiles Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a second view, `/sites`, that shows the open pull requests and issues of each arc42 site as a tile coloured from the arc42 brand registry, reachable through a List | Sites switch.

**Architecture:** A `github.sites` list in the YAML names each site, its one repository and a palette key. A pure domain function turns the snapshot's items and those sites into tiles (at most three pull requests and four issues each, counted before the cut). The web layer adds one route and one page, shares the list's header with it, and draws colour only through CSS classes, because the Content-Security-Policy forbids inline styles.

**Tech Stack:** Go 1.26 (host Go 1.27), `html/template`, hand-written CSS with `light-dark()` and `color-mix()`, `gopkg.in/yaml.v3`. Docker-only gate (`make check`).

**Spec:** `docs/superpowers/specs/2026-09-15-per-site-tiles-design.md`

## Global Constraints

- `internal/domain` imports the standard library and itself only (depguard rule in `.golangci.yml`).
- No inline `<script>`, `<style>` or `style=` attribute anywhere; the CSP in `internal/web/server.go` does not change.
- A colour reaches the page only as a `hue-<key>` class over `--hue-<key>` custom properties in `internal/web/static/app.css`; no hex value in YAML or templates.
- Palette keys, exactly: `navy`, `blue`, `plum`, `teal`, `umber`, `rose`, `slate`.
- Tile caps: `tileMaxPRs = 3`, `tileMaxIssues = 4`.
- Tile order: configuration order; the tile named `Other` (hue `slate`, no URL, no tag) comes last and exists only when some watched repository is claimed by no site.
- Tile wash: `--tile-wash-light: 7%`, `--tile-wash-dark: 12%` to start; text keeps ≥ 4.5:1 contrast; the brand colours are never altered, only the percentages.
- The list view's behaviour does not change; every existing test stays green and keeps its meaning.
- No secret value in any log, response or error message.
- No new Make target, no background ticker, no database.
- Every task ends with `go build ./... && go vet ./... && go test -race ./...` green on the host and exactly one commit; Task 6 also ends with `make check` green.
- Stage files explicitly. Never `git add -A`. Never commit `.agent/`, `.agents/`, `.claude/`, `_bmad/` or `.env`.
- Commit messages name the requirement ids they touch and end with a blank line and `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.
- Comments use British spelling (golangci `misspell` locale UK): colour, normalise.

---

### Task 1: `github.sites` configuration

Model tier: cheap (the code is complete below).

**Files:**

- Modify: `internal/config/config.go`
- Create: `internal/config/testdata/sites.yaml`
- Modify: `internal/config/config_test.go`
- Modify: `config/zorgscope.yaml`

**Interfaces:**

- Consumes: nothing new.
- Produces: `config.HueKeys []string`; `config.Site{Name, URL, Repo, Hue, Tag string}`; `config.GitHub.Sites []config.Site`.

- [ ] **Step 1: Write the fixture and the failing tests**

Create `internal/config/testdata/sites.yaml`:

```yaml
timezone: Europe/Berlin
github:
  auth_repo: gernotstarke/zorgscope
  repos: [org/one, org/two, org/three]
  sites:
    - name: one.example
      url: https://one.example
      repo: org/one
      hue: navy
    - name: two.example
      url: https://two.example
      repo: org/two
      hue: rose
      tag: DE
```

In `internal/config/config_test.go`, extend the import block to:

```go
import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
)
```

Append these tests to the end of `internal/config/config_test.go`:

```go
// FR-8.1 AC5: github.sites loads in configuration order with every field.
func TestLoadSites(t *testing.T) {
	cfg, err := config.Load("testdata/sites.yaml", env(fullEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []config.Site{
		{Name: "one.example", URL: "https://one.example", Repo: "org/one", Hue: "navy"},
		{Name: "two.example", URL: "https://two.example", Repo: "org/two", Hue: "rose", Tag: "DE"},
	}
	if !slices.Equal(cfg.GitHub.Sites, want) {
		t.Errorf("sites = %+v, want %+v", cfg.GitHub.Sites, want)
	}
}

// FR-8.1 AC5: github.sites is optional.
func TestLoadWithoutSitesHasNone(t *testing.T) {
	cfg, err := config.Load("testdata/valid.yaml", env(fullEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.GitHub.Sites) != 0 {
		t.Errorf("sites = %+v, want none", cfg.GitHub.Sites)
	}
}

// FR-8.1 AC5: every rule a site breaks aborts start-up with an error naming the field.
func TestLoadRejectsBadSites(t *testing.T) {
	const head = "timezone: Europe/Berlin\n" +
		"github:\n" +
		"  auth_repo: gernotstarke/zorgscope\n" +
		"  repos: [org/one, org/two]\n" +
		"  sites:\n"
	cases := []struct {
		name, sites, field string
	}{
		{"missing name",
			`    - {url: "https://one.example", repo: org/one, hue: navy}`,
			"github.sites[0].name"},
		{"duplicate name",
			`    - {name: one.example, url: "https://one.example", repo: org/one, hue: navy}` + "\n" +
				`    - {name: one.example, url: "https://two.example", repo: org/two, hue: navy}`,
			"github.sites[1].name"},
		{"http url",
			`    - {name: one.example, url: "http://one.example", repo: org/one, hue: navy}`,
			"github.sites[0].url"},
		{"relative url",
			`    - {name: one.example, url: "/one", repo: org/one, hue: navy}`,
			"github.sites[0].url"},
		{"repo not in owner/name form",
			`    - {name: one.example, url: "https://one.example", repo: one, hue: navy}`,
			"github.sites[0].repo"},
		{"repo not watched",
			`    - {name: one.example, url: "https://one.example", repo: org/three, hue: navy}`,
			"github.sites[0].repo"},
		{"repo claimed twice",
			`    - {name: one.example, url: "https://one.example", repo: org/one, hue: navy}` + "\n" +
				`    - {name: two.example, url: "https://two.example", repo: org/one, hue: rose}`,
			"github.sites[1].repo"},
		{"unknown hue",
			`    - {name: one.example, url: "https://one.example", repo: org/one, hue: lime}`,
			"github.sites[0].hue"},
		{"tag too long",
			`    - {name: one.example, url: "https://one.example", repo: org/one, hue: navy, tag: DEUT}`,
			"github.sites[0].tag"},
		{"unknown field",
			`    - {name: one.example, url: "https://one.example", repo: org/one, hue: navy, colour: navy}`,
			"colour"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "zorgscope.yaml")
			if err := os.WriteFile(path, []byte(head+tc.sites+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := config.Load(path, env(fullEnv()))
			if err == nil {
				t.Fatalf("want an error naming %s (FR-8.1 AC5)", tc.field)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("error %q must name %s (FR-8.1 AC5)", err, tc.field)
			}
		})
	}
}

// FR-1.8 AC1: the committed configuration names the seven arc42 sites, in the order their tiles
// are drawn.
func TestTheRealConfigNamesTheSevenSitesInOrder(t *testing.T) {
	cfg, err := config.Load("../../config/zorgscope.yaml", env(fullEnv()))
	if err != nil {
		t.Fatalf("Load(config/zorgscope.yaml): %v", err)
	}
	var names []string
	for _, s := range cfg.GitHub.Sites {
		names = append(names, s.Name)
	}
	want := []string{
		"arc42.org", "arc42.de", "quality.arc42.org", "docs.arc42.org",
		"faq.arc42.org", "examples.arc42.org", "trainings.arc42.org",
	}
	if !slices.Equal(names, want) {
		t.Errorf("sites = %v, want %v", names, want)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/config/ -run 'TestLoadSites|TestLoadWithoutSitesHasNone|TestLoadRejectsBadSites|TestTheRealConfigNamesTheSevenSitesInOrder'`
Expected: FAIL to compile — `undefined: config.Site` and `cfg.GitHub.Sites undefined`.

- [ ] **Step 3: Implement the configuration**

In `internal/config/config.go`, extend the import block to:

```go
import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	_ "time/tzdata" // the distroless runtime image (deploy/Dockerfile) ships no /usr/share/zoneinfo
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)
```

Below `const defaultCacheTTL = 5 * time.Minute`, add:

```go
// maxTagLen is the longest tag a site may carry. A tag sits beside the site's name in its tile's
// heading, where anything longer than a language code stops being a tag and starts being a name.
const maxTagLen = 3

// HueKeys are the site colours the stylesheet defines, each as a `hue-<key>` class over
// `--hue-<key>` tokens taken from the arc42 brand registry (per-site tiles design §6). A colour can
// reach the page only as one of these classes: the Content-Security-Policy forbids inline styles,
// so a hex value in the YAML would have nowhere to go.
var HueKeys = []string{"navy", "blue", "plum", "teal", "umber", "rose", "slate"}

// Site is one arc42 web property drawn as a tile on the Sites view (FR-1.8): its name, its address,
// the one watched repository behind it, its colour key, and an optional short tag that tells apart
// two sites sharing a colour.
type Site struct {
	Name, URL, Repo, Hue, Tag string
}
```

In `type GitHub struct`, directly after the `Repos    []string` line, add:

```go
	// Sites are the tiles of the Sites view, in the order they are drawn (FR-1.8). Each names one
	// repository of Repos; the repositories no site names share a tile of their own. Empty is valid:
	// the Sites view then shows that one shared tile.
	Sites []Site
```

Replace `type fileConfig struct { … }` with:

```go
// fileConfig mirrors the shape of the YAML file. CacheTTL is a string here so that a malformed
// value can be rejected with a field-naming error rather than failing YAML decoding generically.
type fileConfig struct {
	Timezone string `yaml:"timezone"`
	GitHub   struct {
		AuthRepo string     `yaml:"auth_repo"`
		CacheTTL string     `yaml:"cache_ttl"`
		Repos    []string   `yaml:"repos"`
		Sites    []fileSite `yaml:"sites"`
	} `yaml:"github"`
}

// fileSite is one entry of github.sites as the YAML file spells it.
type fileSite struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
	Repo string `yaml:"repo"`
	Hue  string `yaml:"hue"`
	Tag  string `yaml:"tag"`
}
```

In `Load`, directly after the `for i, repo := range fc.GitHub.Repos { … }` loop, add:

```go
	sites, err := loadSites(fc.GitHub.Sites, fc.GitHub.Repos)
	if err != nil {
		return Config{}, err
	}
```

In the `cfg := Config{ … }` literal, add `Sites: sites,` directly after `Repos: fc.GitHub.Repos,`, aligned with its neighbours.

Directly below `Load`, add:

```go
// loadSites validates github.sites against the watched repositories and turns it into Sites. Every
// error names the offending field, as `github.sites[2].hue`, and quotes no value that could be a
// secret — nothing in this list is one.
func loadSites(in []fileSite, repos []string) ([]Site, error) {
	watched := make(map[string]bool, len(repos))
	for _, r := range repos {
		watched[r] = true
	}
	names := make(map[string]bool, len(in))
	claimed := make(map[string]bool, len(in))
	out := make([]Site, 0, len(in))
	for i, s := range in {
		field := func(name string) string { return fmt.Sprintf("github.sites[%d].%s", i, name) }
		if s.Name == "" {
			return nil, errors.New(field("name") + ": is required")
		}
		if names[s.Name] {
			return nil, fmt.Errorf("%s: %q names a second site", field("name"), s.Name)
		}
		names[s.Name] = true
		if u, err := url.Parse(s.URL); err != nil || u.Scheme != "https" || u.Host == "" {
			return nil, errors.New(field("url") + ": must be an absolute https URL")
		}
		if !repoPattern.MatchString(s.Repo) {
			return nil, fmt.Errorf("%s: %q is not in owner/name form", field("repo"), s.Repo)
		}
		if !watched[s.Repo] {
			return nil, fmt.Errorf("%s: %q is not listed in github.repos", field("repo"), s.Repo)
		}
		if claimed[s.Repo] {
			return nil, fmt.Errorf("%s: %q is already claimed by another site", field("repo"), s.Repo)
		}
		claimed[s.Repo] = true
		if !slices.Contains(HueKeys, s.Hue) {
			return nil, fmt.Errorf("%s: %q is not one of %s", field("hue"), s.Hue, strings.Join(HueKeys, ", "))
		}
		if utf8.RuneCountInString(s.Tag) > maxTagLen {
			return nil, fmt.Errorf("%s: %q is longer than %d characters", field("tag"), s.Tag, maxTagLen)
		}
		out = append(out, Site{Name: s.Name, URL: s.URL, Repo: s.Repo, Hue: s.Hue, Tag: s.Tag})
	}
	return out, nil
}
```

Replace the whole of `config/zorgscope.yaml` with:

```yaml
# Non-secret configuration (FR-8.1). Secrets come from the environment only — see deploy/env.example.
# This file is baked into the image; CONFIG_PATH points at it.

timezone: Europe/Berlin

github:
  # Push access here admits a visitor (FR-8.3).
  auth_repo: gernotstarke/zorgscope
  # How old the fetched item list may be before a page view refetches it.
  cache_ttl: 5m
  # The repository names are the ones GitHub actually uses, which for the arc42 family means the
  # "-site" suffix on every property that has a website: arc42/quality.arc42.org does not exist,
  # arc42/quality.arc42.org-site does. A name that is merely wrong fetches nothing and reports the
  # repository as failing rather than as missing.
  #
  # QS-3.5 budgets 20 GraphQL queries per page view and the issue fetcher spends two per repository
  # (issues and pull requests paginate independently), so this list may hold at most ten entries.
  # It holds nine.
  repos:
    - arc42/arc42.org-site
    - arc42/arc42.de-site
    - arc42/arc42-template
    - arc42/docs.arc42.org-site
    - arc42/quality.arc42.org-site
    - arc42/faq.arc42.org-site
    - arc42/examples.arc42.org-site
    - arc42/trainings.arc42.org-site
    - gernotstarke/zorgscope
  # The Sites view (FR-1.8): one tile per entry, in this order. Each site names exactly one of the
  # repositories above; the ones no site names share a last tile called "Other". hue is a key of the
  # palette app.css defines from the arc42 brand registry (arc42/meta.arc42.org,
  # wiki/concepts/brand.md): navy, blue, plum, teal, umber, rose or slate. tag tells apart two sites
  # that share a hue, at most three characters.
  sites:
    - name: arc42.org
      url: https://arc42.org
      repo: arc42/arc42.org-site
      hue: navy
    - name: arc42.de
      url: https://arc42.de
      repo: arc42/arc42.de-site
      hue: navy
      tag: DE
    - name: quality.arc42.org
      url: https://quality.arc42.org
      repo: arc42/quality.arc42.org-site
      hue: plum
    - name: docs.arc42.org
      url: https://docs.arc42.org
      repo: arc42/docs.arc42.org-site
      hue: blue
    - name: faq.arc42.org
      url: https://faq.arc42.org
      repo: arc42/faq.arc42.org-site
      hue: teal
    - name: examples.arc42.org
      url: https://examples.arc42.org
      repo: arc42/examples.arc42.org-site
      hue: umber
    - name: trainings.arc42.org
      url: https://trainings.arc42.org
      repo: arc42/trainings.arc42.org-site
      hue: rose
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test ./internal/config/`
Expected: PASS (the new tests and every existing one, including `TestLoadRealConfigFile` and `TestErrorNeverContainsSecretValues`).

- [ ] **Step 5: Run the gate**

Run: `go build ./... && go vet ./... && go test -race ./...`
Expected: every package `ok`.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go internal/config/testdata/sites.yaml config/zorgscope.yaml
git status --short
git commit -m "feat(config): github.sites — each arc42 site with its repository and brand colour; trainings watched (FR-8.1, FR-1.8)

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 2: Tiles in the domain

Model tier: cheap (the code is complete below).

**Files:**

- Create: `internal/domain/site.go`
- Create: `internal/domain/site_test.go`

**Interfaces:**

- Consumes: `domain.Item`, `domain.KindPR`, `domain.SortItems(items []Item, lastVisit time.Time)`, `domain.CountNew(items []Item, lastVisit time.Time) int` (all existing, `internal/domain/item.go`).
- Produces:
  - `domain.SiteSpec{Name, URL, Hue, Tag string; Repos []string}`
  - `domain.SiteTilesInput{LastVisitAt time.Time; Items []Item; Sites []SiteSpec; MaxPRs, MaxIssues int}`
  - `domain.RepoCount{Repo string; PRs, Issues int}`
  - `domain.SiteTile{Spec SiteSpec; PRs, Issues []Item; PRTotal, IssueTotal, NewCount int; Counts []RepoCount; More bool}`
  - `domain.BuildSiteTiles(in SiteTilesInput) []SiteTile`

- [ ] **Step 1: Write the failing tests**

Create `internal/domain/site_test.go`:

```go
package domain_test

import (
	"slices"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// FR-1.8 AC2: a tile lists at most MaxPRs pull requests and MaxIssues issues, new first and then
// most recently updated, and counts its totals, per-repository counts and new count before the cut.
func TestBuildSiteTilesCutsEachKindAndCountsBeforeTheCut(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	seen := now.Add(-24 * time.Hour)
	var items []domain.Item
	for i := range 5 { // five pull requests; the last is new
		created := now.Add(-48 * time.Hour)
		if i == 4 {
			created = now.Add(-time.Hour)
		}
		items = append(items, domain.Item{
			Repo: "arc42/q", Kind: domain.KindPR, Number: 100 + i,
			CreatedAt: created, UpdatedAt: now.Add(-time.Duration(i) * time.Hour),
		})
	}
	for i := range 6 { // six issues; the first two are new
		created := now.Add(-48 * time.Hour)
		if i < 2 {
			created = now.Add(-2 * time.Hour)
		}
		items = append(items, domain.Item{
			Repo: "arc42/q", Kind: domain.KindIssue, Number: 200 + i,
			CreatedAt: created, UpdatedAt: now.Add(-time.Duration(10+i) * time.Hour),
		})
	}

	tiles := domain.BuildSiteTiles(domain.SiteTilesInput{
		LastVisitAt: seen, Items: items, MaxPRs: 3, MaxIssues: 4,
		Sites: []domain.SiteSpec{{Name: "quality.arc42.org", Hue: "plum", Repos: []string{"arc42/q"}}},
	})

	if len(tiles) != 1 {
		t.Fatalf("tiles = %d, want 1", len(tiles))
	}
	q := tiles[0]
	if len(q.PRs) != 3 || len(q.Issues) != 4 {
		t.Fatalf("listed %d PRs and %d issues, want 3 and 4", len(q.PRs), len(q.Issues))
	}
	if q.PRTotal != 5 || q.IssueTotal != 6 {
		t.Errorf("totals = %d PRs, %d issues, want 5 and 6: counted after the cut", q.PRTotal, q.IssueTotal)
	}
	if q.NewCount != 3 {
		t.Errorf("new count = %d, want 3: counted after the cut", q.NewCount)
	}
	if !q.More {
		t.Error("More = false, but the tile cut two items")
	}
	if got := []int{q.PRs[0].Number, q.PRs[1].Number, q.PRs[2].Number}; !slices.Equal(got, []int{104, 100, 101}) {
		t.Errorf("PR order = %v, want the new #104 first, then most recently updated", got)
	}
	if q.Issues[0].Number != 200 || q.Issues[1].Number != 201 {
		t.Errorf("issue order starts %d, %d, want the two new ones first", q.Issues[0].Number, q.Issues[1].Number)
	}
	if want := []domain.RepoCount{{Repo: "arc42/q", PRs: 5, Issues: 6}}; !slices.Equal(q.Counts, want) {
		t.Errorf("counts = %+v, want %+v", q.Counts, want)
	}
}

// FR-1.8 AC1: one tile per spec in spec order, a spec with nothing open still gets its tile, the
// Other tile gathers several repositories, and an item of a repository no spec names is ignored.
func TestBuildSiteTilesKeepsSpecOrderAndEmptyTiles(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	items := []domain.Item{
		{Repo: "arc42/b", Kind: domain.KindIssue, Number: 1, CreatedAt: now, UpdatedAt: now},
		{Repo: "arc42/template", Kind: domain.KindPR, Number: 2, CreatedAt: now, UpdatedAt: now},
		{Repo: "gernotstarke/zorgscope", Kind: domain.KindIssue, Number: 3, CreatedAt: now, UpdatedAt: now},
		{Repo: "nobody/watches-this", Kind: domain.KindIssue, Number: 4, CreatedAt: now, UpdatedAt: now},
	}

	tiles := domain.BuildSiteTiles(domain.SiteTilesInput{
		Items: items, MaxPRs: 3, MaxIssues: 4,
		Sites: []domain.SiteSpec{
			{Name: "a", Repos: []string{"arc42/a"}},
			{Name: "b", Repos: []string{"arc42/b"}},
			{Name: "Other", Hue: "slate", Repos: []string{"arc42/template", "gernotstarke/zorgscope"}},
		},
	})

	var names []string
	total := 0
	for _, tl := range tiles {
		names = append(names, tl.Spec.Name)
		total += tl.PRTotal + tl.IssueTotal
	}
	if !slices.Equal(names, []string{"a", "b", "Other"}) {
		t.Fatalf("tile order = %v, want spec order", names)
	}
	if total != 3 {
		t.Errorf("tiles hold %d items, want 3: an item of a repository no spec names was counted", total)
	}
	a := tiles[0]
	if a.PRTotal != 0 || a.IssueTotal != 0 || len(a.PRs) != 0 || len(a.Issues) != 0 || a.More {
		t.Errorf("empty tile = %+v, want a tile with nothing in it", a)
	}
	if tiles[1].More {
		t.Error("More = true on a tile that cut nothing")
	}
	other := tiles[2]
	if other.PRTotal != 1 || other.IssueTotal != 1 || other.NewCount != 0 {
		t.Errorf("Other = %d PRs, %d issues, %d new, want 1, 1, 0", other.PRTotal, other.IssueTotal, other.NewCount)
	}
	want := []domain.RepoCount{{Repo: "arc42/template", PRs: 1}, {Repo: "gernotstarke/zorgscope", Issues: 1}}
	if !slices.Equal(other.Counts, want) {
		t.Errorf("Other counts = %+v, want %+v", other.Counts, want)
	}
}

// The snapshot's item slice is shared by every concurrent page render, so building tiles must never
// reorder or modify it.
func TestBuildSiteTilesLeavesTheInputUntouched(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	items := []domain.Item{
		{Repo: "arc42/a", Kind: domain.KindIssue, Number: 1, UpdatedAt: now.Add(-time.Hour)},
		{Repo: "arc42/a", Kind: domain.KindIssue, Number: 2, UpdatedAt: now},
	}
	before := slices.Clone(items)

	domain.BuildSiteTiles(domain.SiteTilesInput{
		Items: items, MaxPRs: 3, MaxIssues: 1,
		Sites: []domain.SiteSpec{{Name: "a", Repos: []string{"arc42/a"}}},
	})

	if !slices.Equal(items, before) {
		t.Errorf("input = %+v, want it unchanged %+v", items, before)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/domain/ -run TestBuildSiteTiles`
Expected: FAIL to compile — `undefined: domain.BuildSiteTiles`, `undefined: domain.SiteSpec`.

- [ ] **Step 3: Implement the tiles**

Create `internal/domain/site.go`:

```go
package domain

import "time"

// SiteSpec is one tile's worth of configuration, as the web layer hands it to BuildSiteTiles: a
// configured site with its one repository, or the Other tile holding every watched repository no
// site claims (FR-1.8). Treating Other as just another spec is what lets one function build every
// tile.
type SiteSpec struct {
	Name, URL, Hue, Tag string
	Repos               []string
}

// SiteTilesInput is everything BuildSiteTiles needs.
type SiteTilesInput struct {
	// LastVisitAt is the visitor's seen-mark; zero means nothing is new.
	LastVisitAt time.Time
	// Items is every item the snapshot holds. It is shared with concurrent renders and is never
	// modified.
	Items []Item
	// Sites are the tiles to build, in the order they are drawn.
	Sites []SiteSpec
	// MaxPRs and MaxIssues are how many of each kind a tile lists. They are the web layer's
	// presentation numbers, passed in so that this package holds no opinion about page density.
	MaxPRs, MaxIssues int
}

// RepoCount is how many open pull requests and issues one repository of a tile holds, counted
// before the tile's cut.
type RepoCount struct {
	Repo        string
	PRs, Issues int
}

// SiteTile is one site's tile of the Sites view.
type SiteTile struct {
	Spec SiteSpec
	// PRs and Issues are sorted new first, then most recently updated, and cut to MaxPRs and
	// MaxIssues.
	PRs, Issues []Item
	// PRTotal, IssueTotal, NewCount and Counts are taken before the cut, so a tile never understates
	// what is open — the rule the list already follows for its filter (FR-1.2).
	PRTotal, IssueTotal int
	NewCount            int
	Counts              []RepoCount
	// More says the tile listed fewer items than it holds.
	More bool
}

// BuildSiteTiles builds one tile per spec, in spec order (FR-1.8). A spec with nothing open still
// gets its tile: the Sites view is a fixed map of the family, and a tile that disappeared would
// read as a site that had gone. Items of a repository no spec names are ignored. It is a pure
// function and never modifies in.Items.
func BuildSiteTiles(in SiteTilesInput) []SiteTile {
	// append onto nil slices allocates fresh backing arrays, so sorting a tile's lists below can
	// never reorder the caller's Items.
	byRepo := make(map[string][]Item)
	for _, it := range in.Items {
		byRepo[it.Repo] = append(byRepo[it.Repo], it)
	}

	tiles := make([]SiteTile, 0, len(in.Sites))
	for _, spec := range in.Sites {
		tile := SiteTile{Spec: spec, Counts: make([]RepoCount, 0, len(spec.Repos))}
		var prs, issues []Item
		for _, repo := range spec.Repos {
			count := RepoCount{Repo: repo}
			for _, it := range byRepo[repo] {
				switch it.Kind {
				case KindPR:
					prs = append(prs, it)
					count.PRs++
				default:
					issues = append(issues, it)
					count.Issues++
				}
			}
			tile.Counts = append(tile.Counts, count)
		}

		SortItems(prs, in.LastVisitAt)
		SortItems(issues, in.LastVisitAt)
		tile.PRTotal, tile.IssueTotal = len(prs), len(issues)
		tile.NewCount = CountNew(prs, in.LastVisitAt) + CountNew(issues, in.LastVisitAt)
		tile.PRs = prs[:min(len(prs), in.MaxPRs)]
		tile.Issues = issues[:min(len(issues), in.MaxIssues)]
		tile.More = tile.PRTotal > len(tile.PRs) || tile.IssueTotal > len(tile.Issues)
		tiles = append(tiles, tile)
	}
	return tiles
}
```

- [ ] **Step 4: Run the tests and the coverage gate**

Run: `go test ./internal/domain/ && go test -coverprofile=/tmp/domain.out ./internal/domain/ && go tool cover -func=/tmp/domain.out | tail -1`
Expected: PASS, and `total: (statements) 100.0%`.

- [ ] **Step 5: Run the gate**

Run: `go build ./... && go vet ./... && go test -race ./...`
Expected: every package `ok`.

- [ ] **Step 6: Commit**

```bash
git add internal/domain/site.go internal/domain/site_test.go
git status --short
git commit -m "feat(domain): one tile per site — the newest pull requests and issues, counted before the cut (FR-1.8)

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 3: One header for every page; the actions return where they were pressed

Model tier: standard (a refactor across Go and templates that must keep every list test green).

**Files:**

- Create: `internal/web/templates/fragments/header.html`
- Modify: `internal/web/templates/dashboard.html`
- Modify: `internal/web/dashboard.go`
- Modify: `internal/web/dashboard_test.go`

**Interfaces:**

- Consumes: `safeReturn(p string) string` (existing, `internal/web/theme.go`), `clockLabel`, `errorNotice` (existing, `internal/web/dashboard.go`).
- Produces:
  - `headerView{FetchedAt string; FetchedAtUnix int64; Error string; NewTotal int; Return string}`
  - `(s *Server) headerView(snap snapshot.Snapshot, newTotal int, r *http.Request) headerView`
  - `dashboardView` now embeds `headerView` (fields stay reachable as `view.NewTotal`, `.FetchedAt` in templates).
  - template `"header"`, executed with any value that embeds `headerView`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/web/dashboard_test.go`, just above the `// ---------------------------------------------------------------- helpers` line:

```go
// FR-1.8 AC4: "Mark all seen" and "Refresh" go back to the page they were pressed on — and only ever
// to a page of this site, since the return field is the browser's to send.
func TestSeenAndRefreshReturnToThePageTheyWerePressedOn(t *testing.T) {
	h := dashHandler(t, &fakeSource{})
	c := signIn(t, h)
	for _, action := range []string{"/seen", "/refresh"} {
		for _, tc := range []struct{ ret, want string }{
			{"", "/"},
			{"/sites", "/sites"},
			{"/?repo=org%2Frepo&kind=pr", "/?repo=org%2Frepo&kind=pr"},
			{"https://evil.example/", "/"},
			{"//evil.example/", "/"},
		} {
			t.Run(action+" return="+tc.ret, func(t *testing.T) {
				form := url.Values{}
				if tc.ret != "" {
					form.Set("return", tc.ret)
				}
				rec := postAs(h, action, form, c)
				if rec.Code != http.StatusSeeOther {
					t.Fatalf("POST %s = %d, want %d", action, rec.Code, http.StatusSeeOther)
				}
				if got := rec.Header().Get("Location"); got != tc.want {
					t.Errorf("POST %s with return %q redirects to %q, want %q", action, tc.ret, got, tc.want)
				}
			})
		}
	}
}

// FR-1.8 AC4: the header's two forms carry the page they sit on, query included, so a filtered list
// comes back filtered.
func TestHeaderFormsCarryTheCurrentPageAsTheirReturn(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: []domain.Item{ghItem(1, "Anything", testNow)}})
	body := getAuthed(t, h, "/?kind=pr").Body.String()
	const want = `<input type="hidden" name="return" value="/?kind=pr">`
	for _, action := range []string{`action="/seen"`, `action="/refresh"`} {
		if line := firstLineContaining(body, action); !strings.Contains(line, want) {
			t.Errorf("the %s form does not carry its page as return:\n%s", action, line)
		}
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/web/ -run 'TestSeenAndRefreshReturnToThePageTheyWerePressedOn|TestHeaderFormsCarryTheCurrentPageAsTheirReturn'`
Expected: FAIL — `redirects to "/", want "/sites"` (and the other non-root returns), and `the action="/seen" form does not carry its page as return`.

- [ ] **Step 3: Move the header into a template of its own**

Create `internal/web/templates/fragments/header.html`:

```html
{{/* The header both views share (FR-1.8 AC4): when the list was fetched, the fetch error when there
     is one, the NEW total, and the three actions. It is executed with a value that embeds
     headerView — dashboardView and sitesView both do — so the two pages cannot drift apart. */}}
{{define "header"}}
{{/* FR-1.1 AC4: when the snapshot was last fetched, in the configured timezone — "never" before
     the first fetch has returned anything. */}}
<p class="fetched-line">Fetched {{.FetchedAt}}</p>
{{/* FR-1.4: a failed fetch keeps showing the previous list rather than blanking the page, and
     says so — when GitHub went unreachable and, when there is one, how old the list on screen
     is. Redacted of every configured secret before it ever reaches this template (QS-4.3). */}}
{{with .Error}}<p class="notice notice-error" role="alert">{{.}}</p>{{end}}
<div class="dash-actions">
  {{/* FR-1.2 AC2: the total new count, stated once above the list — not the filtered count, so
       narrowing the list with the search box never makes the badge lie about what is new. */}}
  {{if gt .NewTotal 0}}<span class="badge badge-new">NEW {{.NewTotal}}</span>{{end}}
  {{/* Three plain form posts, so the page is fully usable with JavaScript disabled (FR-1.2 AC4
       for the first, FR-1.3 AC2 for the second; the third, logging out, needs no requirement of
       its own to justify the same shape).

       return names the page the form sits on, query included, so POST /seen and POST /refresh
       come back to it rather than always to the list (FR-1.8 AC4). The handlers pass it through
       safeReturn, which admits only a path of this site. */}}
  {{/* seen_at names the fetch this page actually shows, so POST /seen (handleSeen's seenAt) can
       stamp seen at that moment rather than at the click — an item that arrived in a fetch made
       after this page was drawn was never on the page being acknowledged. Omitted before the
       first fetch has ever returned anything, in which case the handler falls back to now. */}}
  <form class="action" method="post" action="/seen">{{if .FetchedAtUnix}}<input type="hidden" name="seen_at" value="{{.FetchedAtUnix}}">{{end}}<input type="hidden" name="return" value="{{.Return}}"><button type="submit">Mark all seen</button></form>
  <form class="action" method="post" action="/refresh"><input type="hidden" name="return" value="{{.Return}}"><button type="submit">Refresh</button></form>
  <form class="action" method="post" action="/logout"><button type="submit">Log out</button></form>
</div>
{{end}}
```

In `internal/web/templates/dashboard.html`, replace every line from `{{/* FR-1.1 AC4: when the snapshot was last fetched, in the configured timezone — "never" before` down to and including the `</div>` that closes `<div class="dash-actions">` with this single line:

```html
{{template "header" .}}
```

The file then begins:

```html
{{define "content"}}
{{with .Dashboard}}
{{template "header" .}}
{{/* The filter (FR-2.1). It is a plain GET form whose action is this page, so with JavaScript
```

- [ ] **Step 4: Split headerView out of dashboardView**

In `internal/web/dashboard.go`, replace the whole `type dashboardView struct { … }` declaration with:

```go
// headerView is what the header both pages share needs (templates/fragments/header.html).
type headerView struct {
	// FetchedAt is when the snapshot's items were fetched, in the configured timezone, as
	// "15:04" — or "never" before the first fetch has returned anything.
	FetchedAt string
	// FetchedAtUnix is the same moment as FetchedAt, in Unix seconds, and zero before the first
	// fetch has returned anything. The "Mark all seen" form carries it as a hidden field so
	// POST /seen can stamp seen at the fetch the visitor actually saw rather than at the click —
	// see handleSeen's seenAt. The template omits the field entirely when it is zero.
	FetchedAtUnix int64
	// Error is the notice shown when the most recent fetch failed: what went wrong, scrubbed of
	// every configured secret (QS-4.3), and when — empty when the last fetch succeeded. A failed
	// fetch never discards the previous items (see internal/snapshot), so the page below it is
	// still whatever was fetched last.
	Error string
	// NewTotal is counted over every item regardless of any filter. A filter that also moved the
	// NEW total would make the page lie about what is new the moment somebody typed into the
	// search box.
	NewTotal int
	// Return is the page the header sits on, as a path with its query — "/?kind=pr", "/sites" —
	// which the "Mark all seen" and "Refresh" forms carry so each action comes back to where it was
	// pressed (FR-1.8 AC4). It is what the browser asked for, so the handlers only ever use it
	// through safeReturn.
	Return string
}

// dashboardView is the whole list page.
type dashboardView struct {
	headerView
	// Total and Shown are counted at two different points: Total over every item regardless of
	// the filter, Shown over what the filter actually let through.
	Total, Shown int
	// Filter is the filter that was applied, echoed back so the page can render it as the
	// visitor left it, and Repos is what its repository list offers — configuration order,
	// followed by the applied filter's own repository when configuration no longer names it.
	Filter filterView
	Repos  []string
	// Items is the list itself, and is what both this page and GET /items execute the "items"
	// template with, so the two can never draw different markup.
	Items itemsView
}
```

Replace the whole `func (s *Server) dashboardView(…) dashboardView { … }` with:

```go
// headerView builds the shared header from the snapshot on screen, the visitor's NEW total and the
// request the page answers.
func (s *Server) headerView(snap snapshot.Snapshot, newTotal int, r *http.Request) headerView {
	var fetchedAtUnix int64
	if !snap.FetchedAt.IsZero() {
		fetchedAtUnix = snap.FetchedAt.Unix()
	}
	return headerView{
		FetchedAt:     clockLabel(snap.FetchedAt, s.loc),
		FetchedAtUnix: fetchedAtUnix,
		Error:         errorNotice(s.cfg.Secrets, snap, s.loc),
		NewTotal:      newTotal,
		Return:        r.URL.RequestURI(),
	}
}

// dashboardView turns the assembled domain dashboard and the snapshot it was built from into the
// page's presentation data.
func (s *Server) dashboardView(d domain.Dashboard, snap snapshot.Snapshot, now time.Time, r *http.Request) dashboardView {
	return dashboardView{
		headerView: s.headerView(snap, d.NewTotal, r),
		Total:      d.Total,
		Shown:      d.Shown,
		Filter:     newFilterView(d.Filter),
		Repos:      filterRepos(s.cfg.GitHub.Repos, d.Filter.Repo),
		Items:      s.itemsView(d, now),
	}
}
```

In `render`, change `view := s.dashboardView(d, snap, now)` to:

```go
	view := s.dashboardView(d, snap, now, r)
```

Replace `handleSeen`'s last line `http.Redirect(w, r, "/", http.StatusSeeOther)` with:

```go
	// safeReturn is the sanitiser between the form field and the Location header; see handleTheme
	// for why the annotation is a trailing comment.
	http.Redirect(w, r, safeReturn(r.FormValue("return")), http.StatusSeeOther) // #nosec G710 -- sanitised by safeReturn
```

Replace `handleRefresh`'s body with:

```go
	s.cache.Invalidate()
	http.Redirect(w, r, safeReturn(r.FormValue("return")), http.StatusSeeOther) // #nosec G710 -- sanitised by safeReturn
```

and its doc comment with:

```go
// handleRefresh throws the snapshot away so the redirected GET fetches (FR-1.3), and goes back to
// the page it was pressed on (FR-1.8 AC4).
```

In the doc comment of `seenAt`, change `which dashboard.html fills in` to `which the header template (templates/fragments/header.html) fills in`.

- [ ] **Step 5: Run the web tests**

Run: `go test ./internal/web/`
Expected: PASS — the two new tests and every existing one (among them `TestMarkSeenClearsTheNewBadgeOnTheNextGet`, which posts no return and still expects `/`).

- [ ] **Step 6: Run the gate**

Run: `go build ./... && go vet ./... && go test -race ./...`
Expected: every package `ok`.

- [ ] **Step 7: Commit**

```bash
git add internal/web/templates/fragments/header.html internal/web/templates/dashboard.html internal/web/dashboard.go internal/web/dashboard_test.go
git status --short
git commit -m "refactor(web): one header for every page; Mark all seen and Refresh return to the page they were pressed on (FR-1.8)

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 4: The Sites view and the List | Sites switch

Model tier: standard (new route, handler, templates and view types across several files).

**Files:**

- Create: `internal/web/sites.go`
- Create: `internal/web/templates/sites.html`
- Create: `internal/web/templates/fragments/viewswitch.html`
- Create: `internal/web/sites_test.go`
- Modify: `internal/web/server.go` (`pageFiles`, `routes`, `pageData`)
- Modify: `internal/web/templates/dashboard.html`
- Modify: `internal/web/auth_test.go` (`TestEveryProtectedRouteRefusesAnonymousAccess`)

**Interfaces:**

- Consumes: `config.HueKeys`, `config.Site`, `config.GitHub.Sites` (Task 1); `domain.SiteSpec`, `domain.SiteTilesInput`, `domain.SiteTile`, `domain.BuildSiteTiles` (Task 2); `headerView`, `(s *Server) headerView(snap, newTotal, r)`, template `"header"` (Task 3); existing `newTimeView`, `quantity`, `(s *Server) session`, `s.cache.Get`, `s.execute`.
- Produces: route `GET /sites` → `(s *Server) handleSites`; `sitesView{headerView; Tiles []tileView}`; `tileView{ID, Name, URL, Hue, Tag string; NewCount int; CountLine string; PRs, Issues []tileItemView; Links []tileLinkView}`; `tileItemView{Number int; Title, URL string; New bool; Updated timeView}`; `tileLinkView{Href, Label, Repo, Site string}`; `siteSpecs(gh config.GitHub) []domain.SiteSpec`; `tileHue(key string) string`; `pageData.Sites *sitesView`; templates `"viewswitch"` (argument `"list"` or `"sites"`) and `"tileitem"`; CSS class names used by Task 5: `view-switch`, `tiles`, `tile`, `hue-<key>`, `tile-title`, `tile-tag`, `tile-new`, `tile-count`, `tile-body`, `tile-kind`, `tile-items`, `tile-item`, `tile-updated`, `tile-empty`, `tile-more`, `visually-hidden`.

- [ ] **Step 1: Write the failing tests**

Create `internal/web/sites_test.go`:

```go
package web

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/snapshot"
)

// sitesHandler builds a server watching three sites and two repositories no site claims, backed
// by src.
func sitesHandler(t *testing.T, src ports.Source) http.Handler {
	t.Helper()
	return newTestServerWith(t, func(o *Options) {
		o.Config.GitHub.Repos = []string{"arc42/org", "arc42/de", "arc42/quality", "arc42/template", "gernotstarke/zorgscope"}
		o.Config.GitHub.Sites = []config.Site{
			{Name: "arc42.org", URL: "https://arc42.org", Repo: "arc42/org", Hue: "navy"},
			{Name: "arc42.de", URL: "https://arc42.de", Repo: "arc42/de", Hue: "navy", Tag: "DE"},
			{Name: "quality.arc42.org", URL: "https://quality.arc42.org", Repo: "arc42/quality", Hue: "plum"},
		}
		o.Cache = snapshot.New(src, time.Hour, o.Clock)
	}).Handler()
}

func siteItem(repo string, kind domain.Kind, number int, created time.Time) domain.Item {
	n := strconv.Itoa(number)
	return domain.Item{
		Kind: kind, Repo: repo, Number: number, Title: "Item " + n,
		URL:       "https://github.com/" + repo + "/issues/" + n,
		CreatedAt: created, UpdatedAt: created,
	}
}

// tileSection returns tile n's markup, from its opening section tag to its closing one.
func tileSection(t *testing.T, body string, n int) string {
	t.Helper()
	start := strings.Index(body, `aria-labelledby="tile-`+strconv.Itoa(n)+`-title"`)
	if start < 0 {
		t.Fatalf("no tile %d on the page", n)
	}
	end := strings.Index(body[start:], "</section>")
	if end < 0 {
		t.Fatalf("tile %d is never closed", n)
	}
	return body[start : start+end]
}

// FR-1.8 AC1: one tile per configured site in configuration order, then Other; a site with nothing
// open keeps its tile; the tag is written out; Other's heading is not a link.
func TestSitesListsATilePerSiteInConfigurationOrderThenOther(t *testing.T) {
	src := &fakeSource{items: []domain.Item{siteItem("arc42/template", domain.KindPR, 7, testNow)}}
	body := getAuthed(t, sitesHandler(t, src), "/sites").Body.String()

	last := -1
	for _, want := range []string{
		`class="tile hue-navy" aria-labelledby="tile-1-title"`,
		`class="tile hue-navy" aria-labelledby="tile-2-title"`,
		`class="tile hue-plum" aria-labelledby="tile-3-title"`,
		`class="tile hue-slate" aria-labelledby="tile-4-title"`,
	} {
		i := strings.Index(body, want)
		if i < 0 {
			t.Fatalf("no %s on the page", want)
		}
		if i < last {
			t.Errorf("%s is out of configuration order", want)
		}
		last = i
	}
	if strings.Count(body, `class="tile `) != 4 {
		t.Errorf("tiles = %d, want 4", strings.Count(body, `class="tile `))
	}
	if !strings.Contains(tileSection(t, body, 2), `<span class="tile-tag">DE</span>`) {
		t.Error("arc42.de carries no DE tag: colour would be the only thing telling it from arc42.org")
	}
	if !strings.Contains(tileSection(t, body, 3), `<a href="https://quality.arc42.org"`) {
		t.Error("a site's heading does not link its website")
	}
	if !regexp.MustCompile(`id="tile-4-title">\s*Other\s*<`).MatchString(body) {
		t.Error("the Other tile's heading is not the plain word Other")
	}
	if !strings.Contains(tileSection(t, body, 1), "no open pull requests") {
		t.Error("a site with nothing open lost its tile or its empty-state line")
	}
}

// FR-1.8 AC2 and AC3: a tile lists at most three pull requests and four issues, and only a tile that
// cut something links to the list filtered to its repository — on Other, one link per repository.
func TestSitesTileCutsAndLinksToTheFilteredList(t *testing.T) {
	var items []domain.Item
	for i := range 5 {
		items = append(items, siteItem("arc42/quality", domain.KindPR, 100+i, testNow.Add(-48*time.Hour)))
	}
	for i := range 6 {
		items = append(items, siteItem("arc42/quality", domain.KindIssue, 200+i, testNow.Add(-48*time.Hour)))
	}
	for i := range 4 {
		items = append(items, siteItem("arc42/template", domain.KindPR, 300+i, testNow.Add(-48*time.Hour)))
	}
	items = append(items, siteItem("gernotstarke/zorgscope", domain.KindIssue, 400, testNow.Add(-48*time.Hour)))
	body := getAuthed(t, sitesHandler(t, &fakeSource{items: items}), "/sites").Body.String()

	quality := tileSection(t, body, 3)
	if n := strings.Count(quality, `class="tile-item`); n != 7 {
		t.Errorf("quality tile lists %d items, want 3 pull requests and 4 issues", n)
	}
	if !strings.Contains(quality, "5 PRs · 6 issues") {
		t.Error("quality tile does not state its totals before the cut")
	}
	if !strings.Contains(quality, `href="/?repo=arc42%2Fquality"`) || !strings.Contains(quality, "all 5 PRs · 6 issues →") {
		t.Errorf("quality tile does not link to the filtered list:\n%s", quality)
	}
	if strings.Contains(tileSection(t, body, 1), "tile-more") {
		t.Error("a tile that cut nothing carries an all → link")
	}
	other := tileSection(t, body, 4)
	for _, want := range []string{
		`href="/?repo=arc42%2Ftemplate"`, "arc42/template: all 4 PRs · 0 issues →",
		`href="/?repo=gernotstarke%2Fzorgscope"`, "gernotstarke/zorgscope: all 0 PRs · 1 issue →",
	} {
		if !strings.Contains(other, want) {
			t.Errorf("the Other tile lacks %q:\n%s", want, other)
		}
	}
}

// FR-1.8 AC1 with FR-1.2: the Sites view counts NEW exactly as the list does, in the header, the tab
// title and the tile that holds the new item.
func TestSitesPageCountsNewLikeTheList(t *testing.T) {
	seen := testNow.Add(-2 * time.Hour)
	src := &fakeSource{items: []domain.Item{
		siteItem("arc42/quality", domain.KindIssue, 1, testNow.Add(-time.Hour)),
		siteItem("arc42/org", domain.KindPR, 2, testNow.Add(-3*time.Hour)),
	}}
	h := sitesHandler(t, src)
	c := mintSessionSeenAt(seen)

	body := getAs(h, "/sites", c).Body.String()
	if !strings.Contains(body, "<title>(1) Sites · zorgscope</title>") {
		t.Errorf("tab title = %s", firstLineContaining(body, "<title>"))
	}
	if !strings.Contains(body, "NEW 1") {
		t.Error("the Sites header does not carry the NEW total")
	}
	if !strings.Contains(tileSection(t, body, 3), "badge-new") {
		t.Error("the new item's tile does not mark it NEW")
	}
	if strings.Contains(tileSection(t, body, 1), "badge-new") {
		t.Error("an item created before the seen mark is marked NEW")
	}
	if list := getAs(h, "/", c).Body.String(); !strings.Contains(list, "NEW 1") {
		t.Error("the list and the Sites view disagree about what is new")
	}
}

// FR-1.8 AC5 and QS-4.4: colour arrives only as a class; nothing on the page carries a style
// attribute, which the Content-Security-Policy would refuse anyway.
func TestNoTileCarriesAStyleAttribute(t *testing.T) {
	src := &fakeSource{items: []domain.Item{siteItem("arc42/quality", domain.KindIssue, 1, testNow)}}
	body := getAuthed(t, sitesHandler(t, src), "/sites").Body.String()
	if strings.Contains(body, "style=") {
		t.Errorf("the page carries a style attribute: %s", firstLineContaining(body, "style="))
	}
}

// FR-1.8 AC4: both views carry the switch, marking the view being shown.
func TestViewSwitchMarksTheCurrentView(t *testing.T) {
	h := sitesHandler(t, &fakeSource{})
	c := signIn(t, h)
	for _, tc := range []struct{ path, list, sites string }{
		{"/", `<a href="/" aria-current="page">List</a>`, `<a href="/sites">Sites</a>`},
		{"/sites", `<a href="/">List</a>`, `<a href="/sites" aria-current="page">Sites</a>`},
	} {
		body := getAs(h, tc.path, c).Body.String()
		if !strings.Contains(body, tc.list) || !strings.Contains(body, tc.sites) {
			t.Errorf("GET %s: the switch does not mark the current view:\n%s",
				tc.path, firstLineContaining(body, "view-switch"))
		}
	}
}

// FR-1.8 AC1: without any configured site, the Sites view shows the one Other tile.
func TestSitesWithoutConfiguredSitesShowsOnlyOther(t *testing.T) {
	body := getAuthed(t, newTestServer(t).Handler(), "/sites").Body.String()
	if n := strings.Count(body, `class="tile `); n != 1 {
		t.Errorf("tiles = %d, want the one Other tile", n)
	}
	if !strings.Contains(body, `class="tile hue-slate" aria-labelledby="tile-1-title"`) {
		t.Error("the only tile is not the slate Other tile")
	}
}

// The template draws hue-<key> from whatever the Config says. config.Load refuses an unknown key;
// this is the second lock, for a Config built in code.
func TestTileHueFallsBackToSlateForAnUnknownKey(t *testing.T) {
	if got := tileHue("rose"); got != "rose" {
		t.Errorf("tileHue(rose) = %q", got)
	}
	if got := tileHue("lime"); got != "slate" {
		t.Errorf("tileHue(lime) = %q, want slate", got)
	}
}

// QS-2.3: the Sites view over the representative configuration stays inside the page budget.
func TestSitesPageStaysInsideItsBudget(t *testing.T) {
	src := &fakeSource{items: representativeItems()}
	h := newTestServerWith(t, func(o *Options) {
		o.Config.GitHub.Repos = representativeRepos()
		for i, repo := range representativeRepos()[:7] {
			n := strconv.Itoa(i)
			o.Config.GitHub.Sites = append(o.Config.GitHub.Sites, config.Site{
				Name: "site-" + n + ".example", URL: "https://site-" + n + ".example",
				Repo: repo, Hue: config.HueKeys[i%len(config.HueKeys)],
			})
		}
		o.Cache = snapshot.New(src, time.Hour, o.Clock)
	}).Handler()

	body := getAuthed(t, h, "/sites").Body.String()
	if n := strings.Count(body, `class="tile `); n != 8 {
		t.Fatalf("tiles = %d, want 7 sites and Other: the budget would be measured on the wrong page", n)
	}
	if n := len(body); n > 150*1024 {
		t.Errorf("Sites view is %d bytes, budget is 150 kB (QS-2.3)", n)
	} else {
		t.Logf("Sites view is %d bytes of the 150 kB budget (QS-2.3)", n)
	}
}
```

In `internal/web/auth_test.go`, in `TestEveryProtectedRouteRefusesAnonymousAccess`, add this line directly after `{http.MethodGet, "/", http.StatusSeeOther}, // FR-8.3 AC1: redirect, not 401`:

```go
		{http.MethodGet, "/sites", http.StatusSeeOther}, // a page, so a redirect like / (FR-8.3 AC1)
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/web/ -run 'Sites|Tile|ViewSwitch|TestEveryProtectedRouteRefusesAnonymousAccess'`
Expected: FAIL to compile — `undefined: tileHue`.

- [ ] **Step 3: Add the page, route and page data**

In `internal/web/server.go`:

Change `var pageFiles = []string{"login.html", "dashboard.html"}` to:

```go
var pageFiles = []string{"login.html", "dashboard.html", "sites.html"}
```

In `routes()`, directly after the `{http.MethodGet, "/{$}", authSessionPage, s.handleDashboard, "/"},` line, add:

```go
		// The Sites view (FR-1.8): the same snapshot and session as the list, drawn as one tile per
		// site. A page, so an anonymous visitor is redirected to sign in like at /.
		{http.MethodGet, "/sites", authSessionPage, s.handleSites, ""},
```

In `type pageData struct`, directly after the `Dashboard *dashboardView` field, add:

```go
	// Sites is set only by handleSites. sites.html reads it for the header and the tiles; every
	// other page leaves it nil.
	Sites *sitesView
```

Create `internal/web/sites.go`:

```go
package web

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
)

// tileMaxPRs and tileMaxIssues are how much of each kind a tile lists (FR-1.8 AC2): enough to say
// what is going on at a site, few enough that seven tiles still fit a screen. The list, one click
// away, shows the rest.
const (
	tileMaxPRs    = 3
	tileMaxIssues = 4
)

// otherTileName is the tile holding every watched repository no configured site claims, so the
// Sites view never hides something the list shows.
const otherTileName = "Other"

// handleSites renders the Sites view (FR-1.8). Like render, it reads only the snapshot and the
// session, and it is not a visit: only POST /seen moves the seen-mark.
func (s *Server) handleSites(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.session(r) // requireSession already admitted the request
	snap := s.cache.Get(r.Context())
	now := s.clock.Now()

	tiles := domain.BuildSiteTiles(domain.SiteTilesInput{
		LastVisitAt: sess.Seen, Items: snap.Items, Sites: siteSpecs(s.cfg.GitHub),
		MaxPRs: tileMaxPRs, MaxIssues: tileMaxIssues,
	})
	view := sitesView{
		headerView: s.headerView(snap, domain.CountNew(snap.Items, sess.Seen), r),
		Tiles:      make([]tileView, 0, len(tiles)),
	}
	for i, tile := range tiles {
		view.Tiles = append(view.Tiles, newTileView(i+1, tile, sess.Seen, now))
	}
	s.execute(w, r, http.StatusOK, "sites.html", pageData{
		Title:    "Sites",
		NewCount: view.NewTotal,
		Sites:    &view,
	})
}

// siteSpecs turns the configured sites into tile specs, in configuration order, followed by an
// Other tile for the watched repositories no site claims — omitted when every one is claimed.
func siteSpecs(gh config.GitHub) []domain.SiteSpec {
	specs := make([]domain.SiteSpec, 0, len(gh.Sites)+1)
	claimed := make(map[string]bool, len(gh.Sites))
	for _, site := range gh.Sites {
		specs = append(specs, domain.SiteSpec{
			Name: site.Name, URL: site.URL, Hue: tileHue(site.Hue), Tag: site.Tag,
			Repos: []string{site.Repo},
		})
		claimed[site.Repo] = true
	}
	var other []string
	for _, repo := range gh.Repos {
		if !claimed[repo] {
			other = append(other, repo)
		}
	}
	if len(other) > 0 {
		specs = append(specs, domain.SiteSpec{Name: otherTileName, Hue: "slate", Repos: other})
	}
	return specs
}

// tileHue is key when the stylesheet defines it, and slate otherwise. config.Load already refuses an
// unknown key; this is the second lock on the same door, for a Config built in code, so that nothing
// but a known class name can ever reach the template.
func tileHue(key string) string {
	if slices.Contains(config.HueKeys, key) {
		return key
	}
	return "slate"
}

// sitesView is the whole Sites page.
type sitesView struct {
	headerView
	Tiles []tileView
}

// tileView is one tile. Like every view type here, every string it prints is computed in Go.
type tileView struct {
	// ID names the tile's heading, which the section points at with aria-labelledby.
	ID string
	// Name is the site's name, and URL its address; URL is empty for Other, whose heading is not a
	// link.
	Name, URL string
	// Hue is a palette key the stylesheet defines, rendered as the hue-<key> class.
	Hue string
	// Tag tells apart two sites sharing a hue; empty for most.
	Tag      string
	NewCount int
	// CountLine is the tile's totals before the cut: "4 PRs · 11 issues".
	CountLine   string
	PRs, Issues []tileItemView
	// Links are the tile's "all →" links, empty unless the tile cut something.
	Links []tileLinkView
}

// tileItemView is one row of a tile: less than a list row, because a tile is for a glance.
type tileItemView struct {
	Number  int
	Title   string
	URL     string
	New     bool
	Updated timeView
}

// tileLinkView is one "all →" link to the list filtered to a repository.
type tileLinkView struct {
	// Href is the filtered list's address, its query escaped by url.Values.
	Href string
	// Label says what the list holds: "all 4 PRs · 11 issues".
	Label string
	// Repo names the repository on Other, where a tile has more than one; empty otherwise.
	Repo string
	// Site is the tile's name, added for screen readers, which read a link out of its tile.
	Site string
}

// newTileView renders tile number n.
func newTileView(n int, tile domain.SiteTile, seen, now time.Time) tileView {
	v := tileView{
		ID:        "tile-" + strconv.Itoa(n) + "-title",
		Name:      tile.Spec.Name,
		URL:       tile.Spec.URL,
		Hue:       tile.Spec.Hue,
		Tag:       tile.Spec.Tag,
		NewCount:  tile.NewCount,
		CountLine: kindsLine(tile.PRTotal, tile.IssueTotal),
		PRs:       tileItems(tile.PRs, seen, now),
		Issues:    tileItems(tile.Issues, seen, now),
	}
	if !tile.More {
		return v
	}
	for _, count := range tile.Counts {
		if count.PRs+count.Issues == 0 {
			continue // a repository with nothing open has no list to link to
		}
		link := tileLinkView{
			Href:  "/?" + url.Values{"repo": {count.Repo}}.Encode(),
			Label: "all " + kindsLine(count.PRs, count.Issues),
			Site:  tile.Spec.Name,
		}
		if len(tile.Counts) > 1 {
			link.Repo = count.Repo
		}
		v.Links = append(v.Links, link)
	}
	return v
}

// kindsLine is a count of both kinds: "1 PR · 11 issues".
func kindsLine(prs, issues int) string {
	return quantity(prs, "PR") + " · " + quantity(issues, "issue")
}

// tileItems renders a tile's rows.
func tileItems(items []domain.Item, seen, now time.Time) []tileItemView {
	out := make([]tileItemView, 0, len(items))
	for _, it := range items {
		out = append(out, tileItemView{
			Number:  it.Number,
			Title:   it.Title,
			URL:     it.URL,
			New:     it.IsNew(seen),
			Updated: newTimeView(it.UpdatedAt, now),
		})
	}
	return out
}
```

- [ ] **Step 4: Add the templates**

Create `internal/web/templates/fragments/viewswitch.html`:

```html
{{/* The List | Sites switch (FR-1.8 AC4): two links, not a script, with the view being shown
     marked for assistive technology as well as for the eye. It is executed with the name of the
     current view, "list" or "sites". */}}
{{define "viewswitch"}}
<nav class="view-switch" aria-label="View">
  <a href="/"{{if eq . "list"}} aria-current="page"{{end}}>List</a>
  <a href="/sites"{{if eq . "sites"}} aria-current="page"{{end}}>Sites</a>
</nav>
{{end}}
```

Create `internal/web/templates/sites.html`:

```html
{{define "content"}}
{{with .Sites}}
{{template "header" .}}
{{template "viewswitch" "sites"}}
{{/* One tile per site (FR-1.8). The colour is a class the stylesheet defines, never a style
     attribute (QS-4.4), and never the only thing that tells a site apart: its name is always
     written out, and arc42.de carries a tag (FR-1.8 AC5). */}}
<div class="tiles">
  {{range .Tiles}}
  <section class="tile hue-{{.Hue}}" aria-labelledby="{{.ID}}">
    <h2 class="tile-title" id="{{.ID}}">
      {{if .URL}}<a href="{{.URL}}" target="_blank" rel="noopener noreferrer">{{.Name}}</a>{{else}}{{.Name}}{{end}}
      {{with .Tag}}<span class="tile-tag">{{.}}</span>{{end}}
      {{if gt .NewCount 0}}<span class="tile-new">{{.NewCount}} new</span>{{end}}
      <span class="tile-count">{{.CountLine}}</span>
    </h2>
    <div class="tile-body">
      <h3 class="tile-kind">Pull requests</h3>
      {{if .PRs}}<ul class="tile-items">{{range .PRs}}{{template "tileitem" .}}{{end}}</ul>{{else}}<p class="tile-empty">no open pull requests</p>{{end}}
      <h3 class="tile-kind">Issues</h3>
      {{if .Issues}}<ul class="tile-items">{{range .Issues}}{{template "tileitem" .}}{{end}}</ul>{{else}}<p class="tile-empty">no open issues</p>{{end}}
      {{/* FR-1.8 AC3: only a tile that cut something links on, and it links to the list filtered
           to the repository — the list is the detail page. */}}
      {{if .Links}}<p class="tile-more">{{range .Links}}<a href="{{.Href}}">{{with .Repo}}{{.}}: {{end}}{{.Label}} →<span class="visually-hidden"> of {{.Site}}</span></a>{{end}}</p>{{end}}
    </div>
  </section>
  {{end}}
</div>
{{end}}
{{end}}

{{/* One row of a tile: the NEW badge, the number, the title linking to GitHub, and when it last
     moved. No summary and no author — a tile is for a glance, the list is for reading. The
     timestamp is guarded by Known exactly as the list guards its own. */}}
{{define "tileitem"}}
<li class="tile-item{{if .New}} is-new{{end}}">
  {{if .New}}<span class="badge badge-new">NEW</span>{{end}}
  {{if .Number}}<span class="item-number">#{{.Number}}</span>{{end}}
  <a class="item-title" href="{{.URL}}" target="_blank" rel="noopener noreferrer">{{.Title}}</a>
  <span class="tile-updated">{{if .Updated.Known}}updated <time datetime="{{.Updated.Absolute}}">{{.Updated.Relative}}</time>{{else}}updated at an unknown time{{end}}</span>
</li>
{{end}}
```

In `internal/web/templates/dashboard.html`, directly after the line `{{template "header" .}}`, add:

```html
{{template "viewswitch" "list"}}
```

- [ ] **Step 5: Run the web tests**

Run: `go test ./internal/web/`
Expected: PASS — the new tests, the route-table test `TestEveryRouteIsEitherDeliberatelyPublicOrRefusesAnonymousAccess` (it now probes `/sites` on its own), and every existing test.

- [ ] **Step 6: Run the gate**

Run: `go build ./... && go vet ./... && go test -race ./...`
Expected: every package `ok`.

- [ ] **Step 7: Commit**

```bash
git add internal/web/sites.go internal/web/sites_test.go internal/web/templates/sites.html internal/web/templates/fragments/viewswitch.html internal/web/templates/dashboard.html internal/web/server.go internal/web/auth_test.go
git status --short
git commit -m "feat(web): the Sites view — a tile per arc42 site and a List | Sites switch (FR-1.8)

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 5: Brand colours and the contrast test

Model tier: cheap (the code is complete below).

**Files:**

- Modify: `internal/web/static/app.css` (append one section)
- Create: `internal/web/contrast_test.go`

**Interfaces:**

- Consumes: `config.HueKeys` (Task 1); the class names Task 4 renders (listed in its Interfaces); the `--surface`, `--text`, `--muted`, `--accent` tokens already declared with `light-dark()` in `app.css`'s `:root`; the package variable `embedded` (`internal/web/server.go`).
- Produces: tokens `--hue-<key>`, `--hue-<key>-sig`, `--tile-wash-light`, `--tile-wash-dark`; classes `.hue-<key>` setting `--tile-hue` and `--tile-sig`.

- [ ] **Step 1: Write the failing test**

Create `internal/web/contrast_test.go`:

```go
package web

import (
	"io/fs"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/gernotstarke/zorgscope/internal/config"
)

// FR-1.8 AC5 and FR-1.5 AC2: every site colour keeps text readable. White on each heading band, and
// the page's text, muted and link colours on each tile's wash, reach at least 4.5:1 in both
// appearances. When one fails, lower that appearance's --tile-wash-* percentage in app.css; never
// change a brand colour.
func TestTileColoursKeepTextReadable(t *testing.T) {
	raw, err := fs.ReadFile(embedded, "static/app.css")
	if err != nil {
		t.Fatalf("reading the embedded app.css: %v", err)
	}
	css := string(raw)
	surface := lightDarkToken(t, css, "surface")
	foregrounds := map[string][2]rgb{
		"--text":   lightDarkToken(t, css, "text"),
		"--muted":  lightDarkToken(t, css, "muted"),
		"--accent": lightDarkToken(t, css, "accent"),
	}
	washLight := percentToken(t, css, "tile-wash-light")
	washDark := percentToken(t, css, "tile-wash-dark")
	white := rgb{255, 255, 255}

	for _, key := range config.HueKeys {
		if !strings.Contains(css, ".hue-"+key+" {") {
			t.Errorf("app.css defines no .hue-%s class", key)
		}
		band := hexToken(t, css, "hue-"+key)
		sig := hexToken(t, css, "hue-"+key+"-sig")
		if r := contrastRatio(white, band); r < 4.5 {
			t.Errorf("white on the %s band = %.2f:1, want at least 4.5:1", key, r)
		}
		light := mixSRGB(band, surface[0], washLight)
		dark := mixSRGB(sig, surface[1], washDark)
		for name, fg := range foregrounds {
			if r := contrastRatio(fg[0], light); r < 4.5 {
				t.Errorf("%s on the light %s wash = %.2f:1, want at least 4.5:1", name, key, r)
			}
			if r := contrastRatio(fg[1], dark); r < 4.5 {
				t.Errorf("%s on the dark %s wash = %.2f:1, want at least 4.5:1", name, key, r)
			}
		}
	}
}

// The colour maths above is only as good as its agreement with WCAG's own reference values.
func TestContrastRatioMatchesWCAGReferenceValues(t *testing.T) {
	if r := contrastRatio(rgb{0, 0, 0}, rgb{255, 255, 255}); math.Abs(r-21) > 0.01 {
		t.Errorf("black on white = %.2f:1, want 21:1", r)
	}
	// #767676 is the lightest grey that passes 4.5:1 on white.
	if r := contrastRatio(rgb{0x76, 0x76, 0x76}, rgb{255, 255, 255}); math.Abs(r-4.54) > 0.01 {
		t.Errorf("#767676 on white = %.2f:1, want 4.54:1", r)
	}
	if got := mixSRGB(rgb{0, 0, 0}, rgb{255, 255, 255}, 0.5); got != (rgb{127.5, 127.5, 127.5}) {
		t.Errorf("color-mix(in srgb, black 50%%, white) = %+v, want 127.5 per channel", got)
	}
}

// rgb is a colour's gamma-encoded sRGB channels, 0 to 255.
type rgb struct{ r, g, b float64 }

const hexColour = `#([0-9a-fA-F]{6})`

// hexToken reads `--name: #rrggbb;` from css.
func hexToken(t *testing.T, css, name string) rgb {
	t.Helper()
	m := regexp.MustCompile(`--` + regexp.QuoteMeta(name) + `:\s*` + hexColour + `\s*;`).FindStringSubmatch(css)
	if m == nil {
		t.Fatalf("app.css declares no --%s: #rrggbb;", name)
	}
	return parseHexColour(m[1])
}

// lightDarkToken reads `--name: light-dark(#light, #dark)` from css: the light and the dark value.
func lightDarkToken(t *testing.T, css, name string) [2]rgb {
	t.Helper()
	m := regexp.MustCompile(`--` + regexp.QuoteMeta(name) + `:\s*light-dark\(\s*` + hexColour + `\s*,\s*` + hexColour + `\s*\)`).FindStringSubmatch(css)
	if m == nil {
		t.Fatalf("app.css declares no --%s: light-dark(#rrggbb, #rrggbb)", name)
	}
	return [2]rgb{parseHexColour(m[1]), parseHexColour(m[2])}
}

// percentToken reads `--name: 7%;` from css as a fraction.
func percentToken(t *testing.T, css, name string) float64 {
	t.Helper()
	m := regexp.MustCompile(`--` + regexp.QuoteMeta(name) + `:\s*([0-9.]+)%\s*;`).FindStringSubmatch(css)
	if m == nil {
		t.Fatalf("app.css declares no --%s: N%%;", name)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatalf("--%s: %v", name, err)
	}
	return v / 100
}

func parseHexColour(h string) rgb {
	n, _ := strconv.ParseUint(h, 16, 32) // the regexp admits six hex digits only
	return rgb{float64(n >> 16 & 0xff), float64(n >> 8 & 0xff), float64(n & 0xff)}
}

// mixSRGB is CSS `color-mix(in srgb, a p, b)`: a linear interpolation of the gamma-encoded
// channels, p of a and the rest of b.
func mixSRGB(a, b rgb, p float64) rgb {
	return rgb{a.r*p + b.r*(1-p), a.g*p + b.g*(1-p), a.b*p + b.b*(1-p)}
}

// contrastRatio is the WCAG 2 contrast ratio of two colours, from 1 to 21.
func contrastRatio(a, b rgb) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// relativeLuminance is WCAG 2's relative luminance of an sRGB colour.
func relativeLuminance(c rgb) float64 {
	linear := func(v float64) float64 {
		s := v / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(c.r) + 0.7152*linear(c.g) + 0.0722*linear(c.b)
}
```

- [ ] **Step 2: Run the test to see it fail**

Run: `go test ./internal/web/ -run 'TestTileColoursKeepTextReadable|TestContrastRatioMatchesWCAGReferenceValues'`
Expected: `TestContrastRatioMatchesWCAGReferenceValues` PASS; `TestTileColoursKeepTextReadable` FAIL with `app.css declares no --tile-wash-light: N%;`.

- [ ] **Step 3: Add the colours and the tile styles**

Append to the end of `internal/web/static/app.css`:

```css
/* ---------------------------------------------------------------- the view switch (FR-1.8 AC4)

   Two links drawn as one control. The current view is filled with the accent, its text in the
   page background so it reads in both appearances. */
.view-switch {
  display: inline-flex;
  margin: 0 0 1.25rem;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  overflow: hidden;
}

.view-switch a {
  padding: 0.35rem 0.9rem;
  color: var(--text);
  font-size: 0.9rem;
  text-decoration: none;
}

.view-switch a + a { border-left: 1px solid var(--border); }
.view-switch a:hover { color: var(--accent); }
.view-switch a[aria-current="page"] { background: var(--accent); color: var(--bg); font-weight: 600; }

/* ---------------------------------------------------------------- the sites view (FR-1.8)

   One tile per arc42 site. The colours are the arc42 brand registry's
   (arc42/meta.arc42.org, wiki/concepts/brand.md): each band is a site's masthead fill, and each
   -sig is its signature hue, which tints the tile in the dark appearance, where a masthead fill
   would vanish into the page. They are plain values rather than light-dark() pairs, because a
   site's colour is its identity and stays the same in both appearances; examples owns no hue in
   the registry, only a ground, so its ground serves as both. The palette keys match
   config.HueKeys, and TestTileColoursKeepTextReadable holds every one of them to 4.5:1. */
:root {
  --hue-navy: #2b3a57;
  --hue-navy-sig: #374769;
  --hue-blue: #0e4f80;
  --hue-blue-sig: #1675b9;
  --hue-plum: #682d63;
  --hue-plum-sig: #682d63;
  --hue-teal: #1b5648;
  --hue-teal-sig: #5fb49c;
  --hue-umber: #3a332b;
  --hue-umber-sig: #3a332b;
  --hue-rose: #a04c5e;
  --hue-rose-sig: #a04c5e;
  --hue-slate: #414a56;
  --hue-slate-sig: #6f777d;

  /* How much of a site's colour washes over its tile. Lower one when the contrast test fails;
     raise one when a wash is too faint to see, as long as the test still passes. */
  --tile-wash-light: 7%;
  --tile-wash-dark: 12%;
}

.hue-navy { --tile-hue: var(--hue-navy); --tile-sig: var(--hue-navy-sig); }
.hue-blue { --tile-hue: var(--hue-blue); --tile-sig: var(--hue-blue-sig); }
.hue-plum { --tile-hue: var(--hue-plum); --tile-sig: var(--hue-plum-sig); }
.hue-teal { --tile-hue: var(--hue-teal); --tile-sig: var(--hue-teal-sig); }
.hue-umber { --tile-hue: var(--hue-umber); --tile-sig: var(--hue-umber-sig); }
.hue-rose { --tile-hue: var(--hue-rose); --tile-sig: var(--hue-rose-sig); }
.hue-slate { --tile-hue: var(--hue-slate); --tile-sig: var(--hue-slate-sig); }

/* One column on a phone, two or three on a desktop. */
.tiles {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(20rem, 1fr));
  gap: var(--gap);
}

.tile {
  display: flex;
  flex-direction: column;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  overflow: hidden;
  background: var(--surface);
}

/* The band: the site's masthead colour, white text on it in both appearances. */
.tile-title {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 0.5rem;
  margin: 0;
  padding: 0.6rem 0.8rem;
  background: var(--tile-hue, var(--hue-slate));
  color: #ffffff;
  font-size: 1rem;
  font-weight: 600;
}

.tile-title a { color: #ffffff; text-decoration: none; }
.tile-title a:hover { text-decoration: underline; }

.tile-tag {
  padding: 0 0.35rem;
  border: 1px solid #ffffff;
  border-radius: 4px;
  font-size: 0.7rem;
  letter-spacing: 0.06em;
}

.tile-new { font-size: 0.8rem; }
.tile-count { margin-left: auto; font-size: 0.8rem; font-variant-numeric: tabular-nums; }

.tile-body { flex: 1; padding: 0.4rem 0.8rem 0.7rem; }

/* The wash exists only where the browser can compute it; everywhere else the body stays the plain
   surface the rule above gives the whole tile. light-dark() picks the light or the dark mix by the
   document's colour scheme, like every other colour on the page. */
@supports (color: light-dark(#000000, #ffffff)) and (color: color-mix(in srgb, #000000 50%, #ffffff)) {
  .tile-body {
    background: light-dark(
      color-mix(in srgb, var(--tile-hue) var(--tile-wash-light), var(--surface)),
      color-mix(in srgb, var(--tile-sig) var(--tile-wash-dark), var(--surface))
    );
  }
}

.tile-kind {
  margin: 0.5rem 0 0.2rem;
  color: var(--muted);
  font-size: 0.72rem;
  font-weight: 700;
  letter-spacing: 0.06em;
  text-transform: uppercase;
}

.tile-items { list-style: none; margin: 0; padding: 0; }

.tile-item {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 0.35rem;
  padding: 0.2rem 0;
  font-size: 0.9rem;
}

.tile-updated { width: 100%; color: var(--muted); font-size: 0.75rem; }
.tile-empty { margin: 0; color: var(--muted); font-size: 0.85rem; }

.tile-more {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 0.25rem 0.75rem;
  margin: 0.6rem 0 0;
  font-size: 0.85rem;
}

.tile-more a { color: var(--accent); }

/* Read aloud, not drawn: the site name a link says to a screen reader. */
.visually-hidden {
  position: absolute;
  width: 1px;
  height: 1px;
  margin: -1px;
  padding: 0;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
  border: 0;
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/web/`
Expected: PASS, including `TestTileColoursKeepTextReadable` and `TestStaticAssetsFitTheirBudgetOnTheWire`. If the contrast test fails for a wash, lower that appearance's `--tile-wash-light` or `--tile-wash-dark` in whole percent until it passes, never below 3%, and record the value in the commit message; never change a `--hue-*` value.

- [ ] **Step 5: Run the gate**

Run: `go build ./... && go vet ./... && go test -race ./...`
Expected: every package `ok`.

- [ ] **Step 6: Commit**

```bash
git add internal/web/static/app.css internal/web/contrast_test.go
git status --short
git commit -m "style(web): arc42 brand colours on the tiles, with a contrast test for both appearances (FR-1.8, FR-1.5)

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 6: Requirements, configuration docs, glossary, and the full gate

Model tier: cheap (the text is complete below).

**Files:**

- Modify: `docs/requirements/04-functional-requirements.md`
- Modify: `docs/requirements/05-quality-requirements.md`
- Modify: `docs/requirements/06-glossary.md`
- Modify: `docs/concepts/configuration.md`

**Interfaces:**

- Consumes: the test names from Tasks 1–5 (`TestSitesPageStaysInsideItsBudget` is cited below).
- Produces: nothing code depends on.

- [ ] **Step 1: FR‑1.8 and FR‑8.1 AC5**

In `docs/requirements/04-functional-requirements.md`, directly after the row starting `| FR‑1.5 | M |`, add this row (one line; the ids use a non-breaking hyphen, as every row in the file does):

```markdown
| FR‑1.8 | M | As the user I see each site's open pull requests and issues at a glance. | AC1 `/sites` shows one tile per site configured in `github.sites`, in configuration order, and — when some watched repository is claimed by no site — a last tile called Other holding those repositories; a site with nothing open keeps its tile. AC2 A tile lists at most three pull requests and four issues, new first, then most recently updated; its totals and new count are taken before that cut. AC3 When a tile has cut something, it links to the list filtered to its repository — on Other, one link per repository. AC4 Both views carry a List \| Sites switch marking the view being shown, and "Mark all seen" and "Refresh" return to the view they were pressed on. AC5 Each tile carries its site's colour from the arc42 brand registry, text on and inside a tile keeps a contrast of at least 4.5:1 in both appearances, and colour is never the only way a site or a new item is told apart. |
```

In the row starting `| FR‑8.1 | M |`, replace

```text
 AC4 No secret value is ever logged, returned in a response, or otherwise rendered. |
```

with (the leading space is part of both):

```markdown
 AC4 No secret value is ever logged, returned in a response, or otherwise rendered. AC5 `github.sites` is optional; a site with an empty or duplicate name, an address that is not an absolute `https` URL, a repository not in `owner/name` form, not listed in `github.repos` or claimed by a second site, a colour outside the fixed palette, or a tag longer than three characters aborts start-up with a message naming the offending field. |
```

- [ ] **Step 2: QS‑2.3 covers the Sites view**

In `docs/requirements/05-quality-requirements.md`, in the row starting `| QS‑2.3 |`, replace

```text
| The dashboard is rendered, unfiltered |
```

with

```text
| The dashboard or the Sites view is rendered, unfiltered |
```

and replace

```text
assert both halves of the budget against the rendered fixture. |
```

with

```text
assert both halves of the budget against the rendered fixture, and `TestSitesPageStaysInsideItsBudget` (`internal/web/sites_test.go`) holds the Sites view to the same 150 kB. |
```

- [ ] **Step 3: Glossary**

In `docs/requirements/06-glossary.md`, directly after the row starting `| **New** |`, add:

```markdown
| **Site** | One arc42 web property configured in `github.sites`: a name, an `https` address, the one watched repository behind it, a colour from the fixed palette, and an optional short tag. |
| **Tile** | A site's panel on the Sites view (`/sites`): its colour band, at most three pull requests and four issues, its totals, and a link to the filtered list when it holds more than it shows. |
| **Other tile** | The last tile of the Sites view, holding every watched repository no site claims, so the Sites view never hides something the list shows. |
```

- [ ] **Step 4: Configuration concept**

In `docs/concepts/configuration.md`:

Replace the three lines

```text
  repos:
    - arc42/arc42.org-site
    - arc42/arc42.de-site
    # … eight in total
```

with

```text
  repos:
    - arc42/arc42.org-site
    - arc42/arc42.de-site
    # … nine in total
  sites:                              # the Sites view: one tile per entry, in this order
    - name: quality.arc42.org
      url: https://quality.arc42.org
      repo: arc42/quality.arc42.org-site
      hue: plum
    # … seven in total
```

In the table, replace

```text
| `timezone`, `github.auth_repo`, `github.cache_ttl`, `github.repos` |
```

with

```text
| `timezone`, `github.auth_repo`, `github.cache_ttl`, `github.repos`, `github.sites` |
```

Directly before the heading line

```text
## `.env` for local development
```

add:

```markdown
## Sites and their colours

`github.sites` is what the Sites view draws (FR‑1.8): one tile per entry, in the order written. Each
site names exactly one repository that `github.repos` watches; the repositories no site names share a
last tile called Other, so the Sites view never hides something the list shows. The list is optional —
without it the Sites view is the one Other tile.

`hue` is not a colour but a key into a fixed palette: `navy`, `blue`, `plum`, `teal`, `umber`, `rose`,
`slate`. The colours behind the keys live in `internal/web/static/app.css` as `--hue-<key>` custom
properties, taken from the arc42 brand registry (`arc42/meta.arc42.org`, `wiki/concepts/brand.md`),
because the Content-Security-Policy forbids inline styles and a colour can therefore reach the page
only as a class the stylesheet defines. Choosing among the keys is a YAML edit; adding a colour means a
new token and a new key in `config.HueKeys`, and `TestTileColoursKeepTextReadable` holds every key to a
contrast of 4.5:1 in both appearances. `tag` (at most three characters) tells apart two sites that share
a colour, as arc42.de does beside arc42.org.

`Load` refuses a site with an empty or duplicate name, an address that is not an absolute `https` URL,
a repository not in `owner/name` form, not watched or claimed twice, an unknown `hue`, or a longer
`tag`, and names the field — `github.sites[2].hue` — as it does for every other setting.
```

- [ ] **Step 5: Lint the docs**

Run: `docker run --rm -v "$PWD":/work -w /work davidanson/markdownlint-cli2:latest "docs/**/*.md" "README.md"`
Expected: `Summary: 0 issues`.

- [ ] **Step 6: Run the full gate**

Run: `make check`
Expected: vet ok, `0 issues.` from golangci-lint, race tests ok, domain coverage 100 %, markdownlint 0 issues, `Configuration is valid`, exit 0.

- [ ] **Step 7: Commit**

```bash
git add docs/requirements/04-functional-requirements.md docs/requirements/05-quality-requirements.md docs/requirements/06-glossary.md docs/concepts/configuration.md
git status --short
git commit -m "docs: FR-1.8 the Sites view, github.sites and its palette, glossary (FR-1.8, FR-8.1, QS-2.3)

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

## Self-review notes

- **Spec coverage.** §3 configuration → Task 1. §4 domain → Task 2; the Other spec is built in Task 4's `siteSpecs`. §5.1 route and caps → Task 4. §5.2 shared header and return → Task 3. §5.3 switch → Task 4. §5.4 tile markup → Task 4; grid → Task 5. §6.1 tokens, §6.2 styling → Task 5. §6.3 accessibility → Tasks 4 (tag, screen-reader suffix, hue class only from known keys via `tileHue`) and 5. §7 requirements and docs → Task 6. §8 testing → spread over Tasks 1–5; `make check` → Task 6. §9 delivery → the branch already exists; nothing is pushed.
- **Deviations from the spec's wording, deliberate.**
  - §6.2 says the NEW badge "keeps its solid fill". The existing `.badge-new` is outlined (accent border and text), and this plan leaves it as it is; the contrast test covers its accent text on every wash instead.
  - The contrast test also checks `--accent`, not only `--text` and `--muted`, because the tile's "all →" links and the NEW badge use it on the wash.
  - The view switch's current segment uses `var(--bg)` for its text, not white: white on the dark appearance's light accent would fail contrast.
- **Names used across tasks.** `config.HueKeys`, `config.Site` (T1 → T4, T5). `domain.SiteSpec`, `SiteTilesInput`, `RepoCount`, `SiteTile`, `BuildSiteTiles` (T2 → T4). `headerView`, `(s *Server) headerView(snap, newTotal, r)`, template `"header"` (T3 → T4). `sitesView`, `tileView`, template `"viewswitch"`, CSS class names (T4 → T5). `TestSitesPageStaysInsideItsBudget` (T4 → T6).
