# iSAQB group Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Watch the iSAQB repositories on the List, the Sites tiles and the header, and give the
Radar an arc42 | iSAQB switch that shows one group's sites as sectors and condenses every other
group into one sector each.

**Architecture:** A site entry in `config/zorgscope.yaml` gains `group` (default `arc42`) and
`alerts` (default `true`). The fetcher keeps fetching one snapshot of every repository but skips the
nested Dependabot alert connection for `alerts: false` repositories. The radar builder takes a group
name and derives its sectors from it; the handler reads `?group=`. Five orange hue keys join the
palette.

**Tech Stack:** Go 1.26, `html/template`, inline SVG, `shurcooL/githubv4`, Docker-only toolchain
(`make check`).

**Spec:** `docs/superpowers/specs/2026-09-29-isaqb-group-design.md`

## Global Constraints

- Version **2.2.0** (`internal/version/version.go`), README changelog row dated 2026-09-29.
- No inline `style` attribute, no inline script (QS‑4.4); every colour is a `hue-<key>` class.
- At most **15** repositories in `github.repos`; at most **2** GraphQL requests per repository per
  fetch (QS‑3.5, new wording: ≤ 30 per fetch).
- `group`: letters, digits, `.`, `-`; 1–12 characters; absent = `arc42`.
- `alerts: false` repositories send no `vulnerabilityAlerts` field and no REST probe; they get no
  `Coverage` entry (so the Security tile neither lists them as off nor as unavailable).
- New hue keys: `apricot`, `orange`, `tangerine`, `rust`, `copper`; white on each band ≥ 4.5:1 and
  the existing wash checks in `internal/web/contrast_test.go` pass.
- Go runs only in Docker. Single package test:
  `docker run --rm -t -v "$PWD":/src -w /src -v zorgscope-gomod:/go/pkg/mod -v zorgscope-gocache:/root/.cache/go-build golang:1.26 go test ./internal/<pkg>/... -run <Name> -v`
  (abbreviated below as `GOTEST ./internal/<pkg>/... -run <Name>`). Full gate: `make check`.
- Commit messages follow the repo style (`feat(web): … (FR-1.15)`) and end with
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. **A Site built in Go code without `Group`** (every existing test, `tenSites()`) must behave as
   the `arc42` group — the zero value may never create a group named "". Pinned in Task 1
   (`GroupName`) and Task 4 (single-group radar unchanged).
2. **Two spellings of one group** (`iSAQB` on one site, `isaqb` on another) would draw two buttons
   and split the group. Rejected at load time; pinned in Task 1.
3. **`?group=` with junk** (`?group=`, `?group=<script>`, `?group=ISAQB`) — case-insensitive match,
   anything else falls back to the first group, and the value never reaches the page. Pinned in
   Task 4.
4. **An alerts-off repository and the refusal fallback**: the plain query path in
   `firstPullRequestPage` sets `alertsRefused` today; taking it for an alerts-off repository must not
   switch alerts off for every other repository. Pinned in Task 3.
5. **Orange next to amber**: an orange blip must still read as "not Dependency" — Dependency is the
   striped `--warn` ring; checked by eye in Task 5, both appearances.

---

### Task 1: Config — `group`, `alerts`, the 15-repository cap

**Files:**
- Modify: `internal/config/config.go` (Site, fileSite, Load, loadSites, new helpers)
- Modify: `internal/config/config_test.go`
- Create: `internal/config/testdata/groups.yaml`

**Interfaces:**
- Produces:
  - `const config.DefaultGroup = "arc42"`, `const config.MaxRepos = 15`
  - `config.Site{Name, URL, Repo, Hue, Tag, Group string; NoAlerts bool}` — `Group` is set by
    `Load` (never empty after `Load`), `NoAlerts` is `true` iff the YAML says `alerts: false`.
  - `func (s config.Site) GroupName() string` — `s.Group`, or `DefaultGroup` when empty.
  - `func (g config.GitHub) Groups() []string` — distinct `GroupName()`s in order of first
    appearance among `g.Sites`; `nil` when there are no sites.
  - `func (g config.GitHub) ReposWithoutAlerts() []string` — repos of sites with `NoAlerts`, in
    site order.

- [ ] **Step 1: Write the fixture** `internal/config/testdata/groups.yaml`:

```yaml
timezone: Europe/Berlin
github:
  auth_repo: gernotstarke/zorgscope
  repos: [org/one, isaqb-org/two, isaqb-org/three]
  sites:
    - name: one.example
      url: https://one.example
      repo: org/one
      hue: navy
    - name: two
      url: https://github.com/isaqb-org/two
      repo: isaqb-org/two
      hue: orange
      group: iSAQB
      alerts: false
    - name: three
      url: https://github.com/isaqb-org/three
      repo: isaqb-org/three
      hue: apricot
      group: iSAQB
```

(`apricot`/`orange` are introduced in Task 2. Until then, use `hue: rose` and `hue: plum` here and
switch them in Task 2 Step 5.)

- [ ] **Step 2: Write the failing tests** in `config_test.go`:

```go
// Spec §3: group defaults to arc42, alerts to true; the groups are listed in first-appearance order.
func TestLoadGroups(t *testing.T) {
	cfg, err := config.Load("testdata/groups.yaml", env(fullEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var groups []string
	for _, s := range cfg.GitHub.Sites {
		groups = append(groups, s.Group)
	}
	if want := []string{"arc42", "iSAQB", "iSAQB"}; !slices.Equal(groups, want) {
		t.Errorf("site groups = %v, want %v", groups, want)
	}
	if got, want := cfg.GitHub.Groups(), []string{"arc42", "iSAQB"}; !slices.Equal(got, want) {
		t.Errorf("Groups() = %v, want %v", got, want)
	}
	if got, want := cfg.GitHub.ReposWithoutAlerts(), []string{"isaqb-org/two"}; !slices.Equal(got, want) {
		t.Errorf("ReposWithoutAlerts() = %v, want %v", got, want)
	}
}

// A Site built in code, with no Group, is in the default group: the zero value never names a group.
func TestSiteWithoutGroupIsInTheDefaultGroup(t *testing.T) {
	if got := (config.Site{}).GroupName(); got != config.DefaultGroup {
		t.Errorf("GroupName() = %q, want %q", got, config.DefaultGroup)
	}
	gh := config.GitHub{Sites: []config.Site{{Name: "a", Repo: "o/a"}, {Name: "b", Repo: "o/b", Group: "arc42"}}}
	if got := gh.Groups(); !slices.Equal(got, []string{"arc42"}) {
		t.Errorf("Groups() = %v, want [arc42]", got)
	}
	if got := (config.GitHub{}).Groups(); got != nil {
		t.Errorf("Groups() without sites = %v, want nil", got)
	}
}

// QS-3.5: sixteen repositories are one too many.
func TestLoadRejectsMoreThanFifteenRepos(t *testing.T) {
	repos := make([]string, 16)
	for i := range repos {
		repos[i] = fmt.Sprintf("org/r%d", i)
	}
	yaml := "timezone: Europe/Berlin\ngithub:\n  auth_repo: gernotstarke/zorgscope\n  repos: [" +
		strings.Join(repos, ", ") + "]\n"
	path := filepath.Join(t.TempDir(), "zorgscope.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load(path, env(fullEnv()))
	if err == nil || !strings.Contains(err.Error(), "github.repos") || !strings.Contains(err.Error(), "15") {
		t.Fatalf("err = %v, want one naming github.repos and the cap of 15", err)
	}
}
```

Add to the `cases` table of `TestLoadRejectsBadSites`:

```go
		{"group too long",
			`    - {name: one.example, url: "https://one.example", repo: org/one, hue: navy, group: abcdefghijklm}`,
			"github.sites[0].group"},
		{"group with a space",
			`    - {name: one.example, url: "https://one.example", repo: org/one, hue: navy, group: "i SAQB"}`,
			"github.sites[0].group"},
		{"group spelled two ways",
			`    - {name: one.example, url: "https://one.example", repo: org/one, hue: navy, group: iSAQB}` + "\n" +
				`    - {name: two.example, url: "https://two.example", repo: org/two, hue: rose, group: isaqb}`,
			"github.sites[1].group"},
```

Update `TestLoadSites`' `want` (Load now fills `Group`):

```go
	want := []config.Site{
		{Name: "one.example", URL: "https://one.example", Repo: "org/one", Hue: "navy", Group: "arc42"},
		{Name: "two.example", URL: "https://two.example", Repo: "org/two", Hue: "rose", Tag: "DE", Group: "arc42"},
	}
```

Add `"fmt"` to the imports if missing.

- [ ] **Step 3: Run to verify failure**

Run: `GOTEST ./internal/config/... -run 'TestLoadGroups|TestSiteWithoutGroup|TestLoadRejectsMoreThanFifteen|TestLoadRejectsBadSites|TestLoadSites' -v`
Expected: compile failure — `GroupName`, `Groups`, `ReposWithoutAlerts`, `DefaultGroup` undefined.

- [ ] **Step 4: Implement** in `config.go`:

```go
// DefaultGroup is the group of a site that names none (spec §3).
const DefaultGroup = "arc42"

// MaxRepos is QS-3.5's ceiling on github.repos: two GraphQL requests per repository, so at most 30
// per fetch.
const MaxRepos = 15

// groupPattern is a group's display name: short enough for a radar button.
var groupPattern = regexp.MustCompile(`^[A-Za-z0-9.-]{1,12}$`)
```

`Site` becomes:

```go
// Site is one web property or repository drawn as a tile on the Sites view (FR-1.8): its name, its
// address, the one watched repository behind it, its colour key, an optional short tag that tells
// apart two sites sharing a colour, the group whose radar draws it as a sector of its own, and
// whether its repository is asked for Dependabot alerts.
type Site struct {
	Name, URL, Repo, Hue, Tag string
	// Group is set by Load, DefaultGroup when the file names none. A Site built in code may leave it
	// empty; GroupName reads it.
	Group string
	// NoAlerts is `alerts: false`: the repository is fetched without its Dependabot alerts (FR-1.16).
	// It is a negative so that the zero value asks for alerts, as the file's default does.
	NoAlerts bool
}

// GroupName is the site's group, DefaultGroup when none is set.
func (s Site) GroupName() string {
	if s.Group == "" {
		return DefaultGroup
	}
	return s.Group
}

// Groups are the sites' groups in order of first appearance, nil without sites. The radar draws one
// switch button per group (FR-1.15).
func (g GitHub) Groups() []string {
	var out []string
	for _, s := range g.Sites {
		if name := s.GroupName(); !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// ReposWithoutAlerts are the repositories whose site says `alerts: false`, in site order.
func (g GitHub) ReposWithoutAlerts() []string {
	var out []string
	for _, s := range g.Sites {
		if s.NoAlerts {
			out = append(out, s.Repo)
		}
	}
	return out
}
```

`fileSite` gains:

```go
	Group  string `yaml:"group"`
	Alerts *bool  `yaml:"alerts"`
```

In `Load`, right after the `len(fc.GitHub.Repos) == 0` check:

```go
	if len(fc.GitHub.Repos) > MaxRepos {
		return Config{}, fmt.Errorf("github.repos: %d repositories, at most %d (QS-3.5)", len(fc.GitHub.Repos), MaxRepos)
	}
```

In `loadSites`, add `groups := map[string]string{} // lower-case -> first spelling` before the loop;
after the tag check replace `out = append(out, Site(s))` with:

```go
		group := s.Group
		if group == "" {
			group = DefaultGroup
		}
		if !groupPattern.MatchString(group) {
			return nil, fmt.Errorf("%s: %q must be 1 to 12 letters, digits, dots or hyphens", field("group"), group)
		}
		if first, ok := groups[strings.ToLower(group)]; ok && first != group {
			return nil, fmt.Errorf("%s: %q is spelled %q on an earlier site", field("group"), group, first)
		}
		groups[strings.ToLower(group)] = group
		out = append(out, Site{
			Name: s.Name, URL: s.URL, Repo: s.Repo, Hue: s.Hue, Tag: s.Tag,
			Group: group, NoAlerts: s.Alerts != nil && !*s.Alerts,
		})
```

Also update the `Sites` doc comment on `GitHub` to mention groups.

- [ ] **Step 5: Run to verify pass**

Run: `GOTEST ./internal/config/... -v`
Expected: PASS (all config tests, including `TestLoadRealConfigFile`).

- [ ] **Step 6: Commit**

```bash
git add internal/config
git commit -m "feat(config): sites carry a group and may switch alerts off; at most 15 repositories (FR-1.15, QS-3.5)"
```

---

### Task 2: Five orange hues

**Files:**
- Modify: `internal/config/config.go` (`HueKeys`)
- Modify: `internal/web/static/app.css` (hue tokens and classes, the palette comment)
- Test: `internal/web/contrast_test.go` (unchanged — it iterates `config.HueKeys`)

**Interfaces:**
- Produces: hue keys `apricot`, `orange`, `tangerine`, `rust`, `copper`; classes `.hue-<key>`.

- [ ] **Step 1: Add the keys (the failing test is the existing contrast suite)**

```go
var HueKeys = []string{"navy", "blue", "plum", "teal", "umber", "rose", "slate",
	"apricot", "orange", "tangerine", "rust", "copper"}
```

Update the comment above: the first seven come from the arc42 brand registry, the five oranges are
the iSAQB group's (spec §6).

- [ ] **Step 2: Run to verify failure**

Run: `GOTEST ./internal/web/... -run 'TestTileColoursKeepTextReadable|TestHueClassBindsItsOwnTokens' -v`
Expected: FAIL — "app.css defines no .hue-apricot class" (and the other four).

- [ ] **Step 3: Add the tokens and classes** to `app.css`, inside the same `:root` block after
`--hue-slate-sig`:

```css
  /* The iSAQB group (spec 2026-09-29 §6): five oranges, deep enough for white text on the band,
     red-leaning rather than yellow so none reads as the amber Dependency tape (--warn). */
  --hue-apricot: #a8531a;
  --hue-apricot-sig: #f0a35c;
  --hue-orange: #b34700;
  --hue-orange-sig: #f7862b;
  --hue-tangerine: #a33c0b;
  --hue-tangerine-sig: #ec6a2e;
  --hue-rust: #8e3413;
  --hue-rust-sig: #d0582c;
  --hue-copper: #7d4524;
  --hue-copper-sig: #c27a4a;
```

and after `.hue-slate { … }`:

```css
.hue-apricot { --tile-hue: var(--hue-apricot); --tile-sig: var(--hue-apricot-sig); }
.hue-orange { --tile-hue: var(--hue-orange); --tile-sig: var(--hue-orange-sig); }
.hue-tangerine { --tile-hue: var(--hue-tangerine); --tile-sig: var(--hue-tangerine-sig); }
.hue-rust { --tile-hue: var(--hue-rust); --tile-sig: var(--hue-rust-sig); }
.hue-copper { --tile-hue: var(--hue-copper); --tile-sig: var(--hue-copper-sig); }
```

Extend the section comment ("One tile per arc42 site …") by one sentence: the five oranges are the
iSAQB group's, not from the arc42 registry.

- [ ] **Step 4: Run to verify pass**

Run: `GOTEST ./internal/web/... -run 'TestTileColoursKeepTextReadable|TestHueClassBindsItsOwnTokens' -v`
Expected: PASS. If a wash check fails for one tone, darken that `-sig` value by ~10 % lightness
and rerun; never lower `--tile-wash-*` for the sake of one hue.

- [ ] **Step 5: Switch the Task 1 fixture** `internal/config/testdata/groups.yaml` to `hue: orange`
and `hue: apricot` (if Task 1 used rose/plum), rerun `GOTEST ./internal/config/...` → PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/config internal/web/static/app.css
git commit -m "feat(web): five orange hues for the iSAQB group (FR-1.8)"
```

---

### Task 3: Fetcher — no alerts where a site says so; the budget at 15

**Files:**
- Modify: `internal/adapters/github/issues.go` (`Config`, `IssueFetcher`, `NewIssueFetcher`,
  `firstPullRequestPage`, the `Fetch` doc comment's "ten repositories … twenty requests")
- Modify: `internal/adapters/github/alerts_test.go`, `internal/adapters/github/cost_test.go`
- Modify: `cmd/zorgscope/main.go` (`githubConfig`), `cmd/zorgscope/main_test.go`

**Interfaces:**
- Consumes: `config.GitHub.ReposWithoutAlerts()` (Task 1).
- Produces: `github.Config.NoAlerts []string` ("owner/name").

- [ ] **Step 1: Write the failing tests.** In `alerts_test.go`, give `countingFake` a second
constructor argument path by adding:

```go
func (c *countingFake) fetcherWithout(noAlerts []string, repos ...string) *github.IssueFetcher {
	return github.NewIssueFetcher(github.Config{
		Token: "x", BaseURL: c.srv.URL + "/graphql", RESTBaseURL: c.srv.URL, Repos: repos, NoAlerts: noAlerts,
	}, c.srv.Client())
}

// Spec §5: a repository whose site says `alerts: false` is fetched in two requests, neither asking
// for alerts, with no REST probe and no coverage entry — and its plain query does not switch alerts
// off for the repositories that do ask.
func TestAlertsOffRepositoryAsksNothingAboutAlerts(t *testing.T) {
	c := newCountingFake(t)
	f := c.fetcherWithout([]string{"org/alerts-clean"}, "org/alerts-clean", "org/alerts")
	got, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	req, alertReq := c.counts()
	if req != 4 {
		t.Errorf("%d GraphQL requests, want 4 (two per repository)", req)
	}
	if alertReq != 1 {
		t.Errorf("%d requests asked for alerts, want 1 (org/alerts only)", alertReq)
	}
	if probes := c.probeCount(); probes != 0 {
		t.Errorf("%d REST probes, want 0", probes)
	}
	if cov, ok := got.Coverage["org/alerts-clean"]; ok {
		t.Errorf("org/alerts-clean has coverage %v, want no entry", cov)
	}
	if got.Coverage["org/alerts"] != domain.CoverageOn {
		t.Errorf("org/alerts coverage = %v, want on", got.Coverage["org/alerts"])
	}
	// The next fetch still asks org/alerts: nothing was mistaken for a refusal.
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if _, alertReq := c.counts(); alertReq != 1 {
		t.Errorf("second fetch: %d requests asked for alerts, want 1", alertReq)
	}
}
```

In `cost_test.go` change the budget and add a maximum-configuration test:

```go
// graphQLBudget is QS-3.5's ceiling: ≤ 2 requests per repository and ≤ 15 repositories, so ≤ 30 per
// fetch. A fetch happens only on a stale page view (ADR-0011), at most every 5 minutes: 12 × 30 =
// 360 of GitHub's 5,000 an hour, about 7 %, and 30 requests in flight at once.
const graphQLBudget = 30
```

Replace `representativeRepos` usage by a helper that takes the count, keep the existing test at 10
(its exact-count assertion `2 * len(repos)` stays), and add:

```go
func reposN(n int) []string {
	repos := []string{"org/repo"}
	for i := 2; i <= n; i++ {
		repos = append(repos, fmt.Sprintf("org/repo-%d", i))
	}
	return repos
}

// QS-3.5 at the largest configuration Load admits: 15 repositories, 30 requests, no headroom.
func TestGraphQLRequestBudgetAtFifteen(t *testing.T) {
	// same body as TestGraphQLRequestBudget with repos := reposN(config.MaxRepos)
}
```

(Extract the body of `TestGraphQLRequestBudget` into `assertBudget(t *testing.T, repos []string)` so
both tests call it; `representativeRepos()` becomes `reposN(10)`. Import
`github.com/gernotstarke/zorgscope/internal/config` for `config.MaxRepos`.)

In `cmd/zorgscope/main_test.go`, extend the `githubConfig` test with a config whose
`GitHub.Sites` holds one `NoAlerts: true` site and assert `got.NoAlerts` equals
`[]string{<that repo>}`.

- [ ] **Step 2: Run to verify failure**

Run: `GOTEST ./internal/adapters/github/... ./cmd/... -v`
Expected: compile failure — `github.Config` has no field `NoAlerts`.

- [ ] **Step 3: Implement.** `issues.go`:

```go
type Config struct {
	...
	Repos       []string // "owner/name"
	// NoAlerts are the repositories never asked for Dependabot alerts (spec 2026-09-29 §5): their
	// pull request query leaves the alert connection out and no REST probe follows.
	NoAlerts []string
}
```

`IssueFetcher` gains `noAlerts map[string]bool`; `NewIssueFetcher` fills it from `cfg.NoAlerts`.

`firstPullRequestPage`, at the top:

```go
	vars := prVars(owner, name, nil)
	if f.noAlerts[owner+"/"+name] {
		var q pullRequestsQuery
		if err := f.client.Query(ctx, &q, vars); err != nil {
			return ghPRConnection{}, nil, domain.CoverageUnknown, err
		}
		// Not asked is not refused: alertsRefused stays as it was, and no coverage is claimed.
		return q.Repository.PullRequests, nil, domain.CoverageUnknown, nil
	}
```

Fix the `Fetch` comment: "QS-3.5 caps the configuration at fifteen repositories and therefore at
thirty requests in flight."

`cmd/zorgscope/main.go` `githubConfig`:

```go
	gh := github.Config{
		Token:    cfg.Secrets.GitHubToken,
		Repos:    cfg.GitHub.Repos,
		NoAlerts: cfg.GitHub.ReposWithoutAlerts(),
	}
```

- [ ] **Step 4: Run to verify pass**

Run: `GOTEST ./internal/adapters/github/... ./cmd/... -v`
Expected: PASS, including `TestGraphQLRequestBudgetAtFifteen` and the unchanged
`TestARefusedTokenCostsOneRequestPerRepositoryOnce`.

- [ ] **Step 5: Commit**

```bash
git add internal/adapters/github cmd/zorgscope
git commit -m "feat(github): a repository whose site says alerts: false is not asked for them; budget 30 at 15 repositories (FR-1.16, QS-3.5)"
```

---

### Task 4: Radar — one group's sites, the others condensed, the switch

**Files:**
- Modify: `internal/web/radar.go` (`radarView`, `handleRadar`, `buildRadar`, new `radarSpecs`,
  `radarGroup`)
- Modify: `internal/web/templates/radar.html`
- Modify: `internal/web/static/app.css` (the switch's placement)
- Modify: `internal/web/radar_test.go`

**Interfaces:**
- Consumes: `config.Site.GroupName()`, `config.GitHub.Groups()` (Task 1); `hueForRepo`,
  `siteSpecs`, `tileHue`, `otherTileName`, `unclaimedHue` (existing, `sites.go`).
- Produces:
  - `func radarGroup(gh config.GitHub, asked string) string` — the configured group matching
    `asked` case-insensitively, else `gh.Groups()[0]`, else `""` without sites.
  - `func radarSpecs(gh config.GitHub, group string) []domain.SiteSpec` — the shown group's sites,
    then one condensed spec per other group (`Name` = group, `Hue` = its first site's hue, `Repos` =
    all its sites' repos), then Other for unclaimed repositories. With one group it equals
    `siteSpecs(gh)`.
  - `func buildRadar(items []domain.Item, gh config.GitHub, group string, now time.Time) radarView`
  - `radarView.Groups []radarGroupLink`, `type radarGroupLink struct{ Name, URL string; Current bool }`
    — empty when fewer than two groups.

- [ ] **Step 1: Write the failing tests** in `radar_test.go`. Update every existing
`buildRadar(items, gh, now)` call to `buildRadar(items, gh, "", now)` (empty = first group).

```go
// radarGroups is two arc42 sites, two iSAQB sites and one repository no site claims.
var radarGroups = config.GitHub{
	Owner: "gernotstarke",
	Repos: []string{"arc42/org", "arc42/quality", "isaqb-org/cf", "isaqb-org/glossary", "arc42/stray"},
	Sites: []config.Site{
		{Name: "arc42.org", Repo: "arc42/org", Hue: "navy"},
		{Name: "quality", Repo: "arc42/quality", Hue: "plum"},
		{Name: "curriculum-foundation", Repo: "isaqb-org/cf", Hue: "orange", Group: "iSAQB"},
		{Name: "glossary", Repo: "isaqb-org/glossary", Hue: "apricot", Group: "iSAQB"},
	},
}

func radarGroupItems() []domain.Item {
	return []domain.Item{
		{Kind: domain.KindIssue, Repo: "arc42/org", Number: 1, URL: "a1", UpdatedAt: testNow},
		{Kind: domain.KindIssue, Repo: "arc42/quality", Number: 2, URL: "a2", UpdatedAt: testNow},
		{Kind: domain.KindIssue, Repo: "isaqb-org/cf", Number: 3, URL: "i1", UpdatedAt: testNow},
		{Kind: domain.KindPR, Repo: "isaqb-org/glossary", Number: 4, URL: "i2", UpdatedAt: testNow},
		{Kind: domain.KindIssue, Repo: "arc42/stray", Number: 5, URL: "s1", UpdatedAt: testNow},
	}
}

func sectorNames(v radarView) []string {
	var out []string
	for _, s := range v.Sectors {
		out = append(out, s.Name)
	}
	return out
}

// Spec §4: the arc42 view draws its own sites, the iSAQB group as one sector, then Other; every
// item is drawn, and a condensed blip keeps its own site's colour.
func TestRadarArc42ViewCondensesISAQB(t *testing.T) {
	v := buildRadar(radarGroupItems(), radarGroups, "arc42", testNow)
	if got, want := sectorNames(v), []string{"arc42.org", "quality", "iSAQB", otherTileName}; !slices.Equal(got, want) {
		t.Fatalf("sectors = %v, want %v", got, want)
	}
	if v.Sectors[2].Hue != "orange" {
		t.Errorf("iSAQB sector hue = %q, want orange (its first site's)", v.Sectors[2].Hue)
	}
	for url, hue := range map[string]string{"i1": "hue-orange", "i2": "hue-apricot", "a1": "hue-navy"} {
		b := blipFor(t, v, url)
		if b.Hue != hue {
			t.Errorf("%s: hue %q, want %q", url, b.Hue, hue)
		}
	}
	for _, url := range []string{"i1", "i2"} {
		if b := bearingOf(blipFor(t, v, url)); b < 180 || b > 270 {
			t.Errorf("%s: bearing %.1f outside the iSAQB sector [180, 270]", url, b)
		}
	}
	if v.Total != 5 {
		t.Errorf("Total = %d, want 5: nothing the list shows is missing (QG-1)", v.Total)
	}
}

// Spec §4: the iSAQB view mirrors it.
func TestRadarISAQBViewCondensesArc42(t *testing.T) {
	v := buildRadar(radarGroupItems(), radarGroups, "iSAQB", testNow)
	if got, want := sectorNames(v), []string{"curriculum-foundation", "glossary", "arc42", otherTileName}; !slices.Equal(got, want) {
		t.Fatalf("sectors = %v, want %v", got, want)
	}
	if b := blipFor(t, v, "a2"); b.Hue != "hue-plum" {
		t.Errorf("a2 hue = %q, want hue-plum", b.Hue)
	}
	if v.Total != 5 {
		t.Errorf("Total = %d, want 5", v.Total)
	}
}

// Review focus 3: ?group= matches case-insensitively and falls back to the first group.
func TestRadarGroupFromTheQuery(t *testing.T) {
	for asked, want := range map[string]string{
		"": "arc42", "isaqb": "iSAQB", "ISAQB": "iSAQB", "arc42": "arc42",
		"nope": "arc42", "<script>": "arc42",
	} {
		if got := radarGroup(radarGroups, asked); got != want {
			t.Errorf("radarGroup(%q) = %q, want %q", asked, got, want)
		}
	}
	if got := radarGroup(config.GitHub{}, "x"); got != "" {
		t.Errorf("without sites radarGroup = %q, want empty", got)
	}
}

// Review focus 1: with one group there is no switch and the sectors are the Sites view's.
func TestRadarSingleGroupIsUnchanged(t *testing.T) {
	v := buildRadar(nil, tenSites(), "", testNow)
	if len(v.Groups) != 0 {
		t.Errorf("one group drew %d switch links, want none", len(v.Groups))
	}
	if got, want := len(v.Sectors), len(siteSpecs(tenSites())); got != want {
		t.Errorf("sectors = %d, want %d", got, want)
	}
}

// Spec §4: the switch is a pair of plain links, the shown group marked, and the query never echoed.
func TestRadarPageDrawsTheGroupSwitch(t *testing.T) {
	h := radarGroupsHandler(t, &fakeSource{items: radarGroupItems()})
	body := getAuthed(t, h, "/radar?group=ISAQB").Body.String()
	for _, want := range []string{
		`<a href="/radar?group=arc42">arc42</a>`,
		`<a href="/radar?group=iSAQB" aria-current="page">iSAQB</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("radar page lacks %s", want)
		}
	}
	// The Refresh form carries the request URI URL-encoded (chromeView.Return), so only the decoded,
	// HTML-escaped form would betray the value reaching the switch.
	junk := getAuthed(t, h, "/radar?group=%3Cscript%3E").Body.String()
	if strings.Contains(junk, "&lt;script&gt;") {
		t.Error("the asked-for group reached the page")
	}
}

// radarGroupsHandler serves the radar over radarGroups' configuration.
func radarGroupsHandler(t *testing.T, src ports.Source) http.Handler {
	t.Helper()
	s, c := newColdServerWith(t, func(o *Options) {
		o.Config.GitHub.Repos = radarGroups.Repos
		o.Config.GitHub.Sites = radarGroups.Sites
		o.Cache = snapshot.New(src, time.Hour, o.Clock)
	})
	warmCache(t, c)
	return s.Handler()
}
```

In the switch test use `h := radarGroupsHandler(t, &fakeSource{items: radarGroupItems()})`. Add
`"slices"`, `"net/http"`, `ports` and `snapshot` imports as the compiler asks.

- [ ] **Step 2: Run to verify failure**

Run: `GOTEST ./internal/web/... -run 'TestRadar' -v`
Expected: compile failure — `radarGroup`, `radarView.Groups`, 4-argument `buildRadar` undefined.

- [ ] **Step 3: Implement** in `radar.go`:

```go
// radarGroupLink is one button of the radar's group switch (FR-1.15).
type radarGroupLink struct {
	Name, URL string
	Current   bool
}
```

`radarView` gains `Groups []radarGroupLink` (doc: "the group switch; empty with one group").

```go
// radarGroup is the configured group asked matches, ignoring case, else the first group. The asked
// text itself never reaches the page: only a configured name is returned.
func radarGroup(gh config.GitHub, asked string) string {
	groups := gh.Groups()
	for _, g := range groups {
		if strings.EqualFold(g, asked) {
			return g
		}
	}
	if len(groups) == 0 {
		return ""
	}
	return groups[0]
}

// radarSpecs are the radar's sectors for group: one per site of the group, in configuration order,
// then one per other group holding all its sites' repositories, coloured by its first site, then
// Other for the repositories no site claims (spec 2026-09-29 §4). It walks gh.Sites rather than
// siteSpecs, which does not carry the group.
func radarSpecs(gh config.GitHub, group string) []domain.SiteSpec {
	var specs []domain.SiteSpec
	idx := map[string]int{} // other group -> its index in specs
	claimed := map[string]bool{}
	var others []domain.SiteSpec
	for _, site := range gh.Sites {
		claimed[site.Repo] = true
		g := site.GroupName()
		if g == group || group == "" {
			specs = append(specs, domain.SiteSpec{
				Name: site.Name, URL: site.URL, Hue: tileHue(site.Hue), Tag: site.Tag,
				Repos: []string{site.Repo},
			})
			continue
		}
		i, ok := idx[g]
		if !ok {
			i = len(others)
			idx[g] = i
			others = append(others, domain.SiteSpec{Name: g, Hue: tileHue(site.Hue)})
		}
		others[i].Repos = append(others[i].Repos, site.Repo)
	}
	specs = append(specs, others...)
	var unclaimed []string
	for _, repo := range gh.Repos {
		if !claimed[repo] {
			unclaimed = append(unclaimed, repo)
		}
	}
	if len(unclaimed) > 0 {
		specs = append(specs, domain.SiteSpec{Name: otherTileName, Hue: unclaimedHue, Repos: unclaimed})
	}
	return specs
}
```

(`group == ""` happens only for a Config without sites, where `radarGroup` has nothing to
resolve to.)

`buildRadar(items, gh, group, now)`:
- first line: `group = radarGroup(gh, group)` then `specs := radarSpecs(gh, group)` instead of
  `siteSpecs(gh)`;
- the blip hue: replace `specs[sector].Hue` with a hue that prefers the item's own site:

```go
		hue := specs[sector].Hue
		if h := hueForRepo(gh, it.Repo); h != unclaimedHue {
			hue = h
		}
		b := newRadarBlip(it, gh, now, float64(sector)*width, width, hue)
```

- after the sectors, the switch:

```go
	if groups := gh.Groups(); len(groups) > 1 {
		for _, g := range groups {
			v.Groups = append(v.Groups, radarGroupLink{
				Name: g, URL: "/radar?group=" + url.QueryEscape(g), Current: g == group,
			})
		}
	}
```

(import `net/url`; `strings` and `slices` as needed.)

`handleRadar`: `view := buildRadar(snap.Items, s.cfg.GitHub, r.URL.Query().Get("group"), s.clock.Now())`.

`radar.html`, directly after `<h1 class="visually-hidden" …>`:

```html
{{with .Groups}}<nav class="view-switch radar-groups" aria-label="Radar group">{{range .}}<a href="{{.URL}}"{{if .Current}} aria-current="page"{{end}}>{{.Name}}</a>{{end}}</nav>{{end}}
```

`app.css`, after `.radar { margin-top: 0.5rem; }`:

```css
/* The radar's group switch (FR-1.15): the top bar's view switch, one row above the scope. */
.radar-groups { margin-bottom: 0.5rem; }
```

- [ ] **Step 4: Run to verify pass**

Run: `GOTEST ./internal/web/... -v`
Expected: PASS — the new tests, every existing radar test (collisions, budget, no style attribute),
and the Sites/list tests untouched.

- [ ] **Step 5: Commit**

```bash
git add internal/web
git commit -m "feat(web): the radar switches between groups, each other group condensed into one sector (FR-1.15)"
```

---

### Task 5: Shipped config, requirements, ADR, version 2.2.0, the full gate

**Files:**
- Modify: `config/zorgscope.yaml`
- Modify: `internal/config/config_test.go` (`TestTheRealConfigNamesTheTenSitesInOrder` →
  twelve sites)
- Modify: `docs/requirements/04-functional-requirements.md` (FR‑1.8 AC5, FR‑1.15 AC2 + new AC,
  FR‑1.16), `docs/requirements/05-quality-requirements.md` (QS‑3.5)
- Create: `docs/decisions/0016-site-groups-one-snapshot-a-radar-per-group.md`; add it to
  `docs/decisions/README.md`'s index
- Modify: `docs/concepts/configuration.md`, `internal/version/version.go`, `README.md` (changelog)
- Modify: `docs/superpowers/specs/2026-09-29-isaqb-group-design.md` (Status line)

- [ ] **Step 1: Failing test.** Rename to `TestTheRealConfigNamesTheTwelveSitesInOrder`, append
`"curriculum-foundation", "glossary"` to `want`, and add:

```go
	if got := cfg.GitHub.Groups(); !slices.Equal(got, []string{"arc42", "iSAQB"}) {
		t.Errorf("groups = %v, want [arc42 iSAQB]", got)
	}
	if got := cfg.GitHub.ReposWithoutAlerts(); !slices.Equal(got, []string{"isaqb-org/curriculum-foundation", "isaqb-org/glossary"}) {
		t.Errorf("repos without alerts = %v", got)
	}
```

Run: `GOTEST ./internal/config/... -run TheRealConfig -v` → FAIL.

- [ ] **Step 2: Config.** In `config/zorgscope.yaml` append to `repos`:

```yaml
    - isaqb-org/curriculum-foundation
    - isaqb-org/glossary
```

and to `sites`:

```yaml
    - name: curriculum-foundation
      url: https://github.com/isaqb-org/curriculum-foundation
      repo: isaqb-org/curriculum-foundation
      hue: orange
      group: iSAQB
      alerts: false
    - name: glossary
      url: https://github.com/isaqb-org/glossary
      repo: isaqb-org/glossary
      hue: apricot
      group: iSAQB
      alerts: false
```

Rewrite the budget comment above `repos`: QS‑3.5 allows 2 requests per repository and at most 15
repositories (30 per fetch); twelve are listed, three left for req4arc, improve and adoc. Extend
the `sites` comment: `group` (default arc42) picks the radar view a site gets its own sector in;
`alerts: false` fetches issues and pull requests only; the orange keys are the iSAQB group's.

Run: `GOTEST ./internal/config/... -v` → PASS (`TestTheRealConfigSitesClaimEveryRepoInOrder` still
holds: sites and repos in the same order).

- [ ] **Step 3: Requirements.**
  - FR‑1.8 AC5: "carries its site's colour from the arc42 brand registry" → "carries its site's
    configured colour — the arc42 brand registry's for the arc42 group, oranges for the iSAQB
    group".
  - FR‑1.15 AC2: "one equal sector per configured site in configuration order, then Other" → "one
    equal sector per configured site of the shown group in configuration order, then one sector per
    other group holding all its repositories, then Other"; add **AC8**: "When the sites name more
    than one group, the radar carries one link per group above the scope, the shown one marked;
    `GET /radar?group=<g>` shows group g, matched ignoring case, and an unknown or missing group
    shows the first. A blip keeps its own site's colour inside a condensed sector."
  - FR‑1.16: append an AC: "A repository whose site says `alerts: false` is not asked for alerts,
    costs no REST probe, and is neither off nor unavailable on the Security tile."
  - QS‑3.5: stimulus "The largest configuration (15 repositories)"; response "queried at most 30
    times — 2 per repository …" keeping the nested-connection sentence; add the rationale sentence
    from spec §5 (12 × 30 = 360 of 5,000 an hour); measure: `TestGraphQLRequestBudget` (10 → 20)
    and `TestGraphQLRequestBudgetAtFifteen` (15 → 30).

- [ ] **Step 4: ADR‑0016**, following `0015`'s layout (Status: accepted (implemented in 2.2.0);
Date 2026-09-29; Requirements FR‑1.8, FR‑1.15, FR‑1.16, QS‑3.5). Context: the request quote from
spec §1. Decision: D1–D5 of spec §2. Considered options: (A) one snapshot, groups only in the
radar — chosen; (B) a separately fetched iSAQB snapshot only on the iSAQB radar — rejected, the
List and Sites need iSAQB too (D4); (C) batch repositories into aliased GraphQL queries — deferred,
makes the budget moot but rewrites the fetcher and per-repository paging. Consequences: QS‑3.5
raised to 30; `alerts: false` is explicit per site; three repositories of headroom.
Add a row for 0016 to `docs/decisions/README.md` in the existing format.

- [ ] **Step 5: `docs/concepts/configuration.md`** — document `group`, `alerts`, the 15-repository
cap and the five orange keys alongside the existing site keys (match that file's table/list style).

- [ ] **Step 6: Version.** `internal/version/version.go`: `const Version = "2.2.0"`. README changelog,
new top row:

```markdown
| 2.2.0 | 2026-09-29 | iSAQB repositories (curriculum-foundation, glossary) on the list, the tiles and the header, in five orange tones; the Radar switches between the arc42 and iSAQB groups, the other group condensed into one sector; at most 15 repositories, 30 requests per fetch (ADR-0016) |
```

Spec status line → "Status: implemented in 2.2.0."

- [ ] **Step 7: Full gate**

Run: `make check`
Expected: vet, lint, race tests, domain coverage, markdownlint all pass.

- [ ] **Step 8: By eye** (review focus 5). `make fakes` / `make backend` per README, open `/radar`
and `/radar?group=iSAQB` and `/sites` in light and dark: orange blips distinct from the amber
Dependency ring; tile titles readable; the switch sits above the scope and does not push it off
screen on a laptop height. Real GitHub: `isaqb-org` repositories fetch with the current token (no
error notice on the list).

- [ ] **Step 9: Commit**

```bash
git add config docs internal README.md
git commit -m "feat: the iSAQB group — two repositories, orange tiles, a radar per group; version 2.2.0 (FR-1.8, FR-1.15, FR-1.16, QS-3.5, ADR-0016)"
```
