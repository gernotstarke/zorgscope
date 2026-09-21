# Security Highlight Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Dependabot and security issues and pull requests stand out on the list and the search results: a Security tier (cites a CVE/GHSA, or labelled `security`) gets a red chip, a red rule and a header count; a Dependency tier (Dependabot/Renovate, or labelled `dependencies`) gets a quiet chip.

**Architecture:** The adapter extracts CVE/GHSA identifiers from the whole body before it is cut to 300 bytes, and stores them as `Item.Advisories`. The domain owns the rule, `Item.Tier()`, plus `Item.ShowsQuiet` (a Security item is never quiet) and `Dashboard.Security`. The web layer draws one shared `{{define "item-tier"}}` partial on the list and the search results. No new request to GitHub.

**Tech Stack:** Go standard library only in `internal/domain` (QS-5.1) — `regexp` is standard library; `html/template`; no new dependencies; Docker-only toolchain via `make`.

**Spec:** `docs/superpowers/specs/2026-09-21-security-highlight-design.md`

## Global Constraints

- **Branch:** `feat/security-highlight`, already created off `main` at `1cec874`. Do not rebase or merge anything.
- **No new request to GitHub.** QS-3.5 budgets 20 GraphQL queries and the configuration spends exactly 20. Nothing in this plan changes a query; the body is already fetched whole.
- **No new dependencies.** `go.mod` untouched. No new static asset file, no JavaScript.
- **CSP:** `script-src 'self'; style-src 'self'` with no `'unsafe-inline'`. No inline `<script>`, no `style=` attribute. An inline `<svg>` in markup is fine — the cogwheel already is one.
- **Colour is never the only signal.** Every tier carries a word. The shield and the rule are decorative (`aria-hidden` on the SVG).
- **Borrowed text is escaped.** Titles, labels and advisory identifiers are upstream text; nothing becomes `template.HTML`.
- **Domain purity:** `internal/domain` imports only the standard library and never calls `time.Now()`.
- **Budgets and contrast that must stay green, unmodified:** `TestRenderedPageStaysInsideItsBudget`, `TestStaticAssetsFitTheirBudgetOnTheWire`, `TestWaitPageStaysInsideItsBudget`, the `contrast_test.go` suite, and the QS-4.1 route-table test.
- **Run tests with:**
  `docker run --rm -t -v "$PWD":/src -w /src -v zorgscope-gomod:/go/pkg/mod -v zorgscope-gocache:/root/.cache/go-build golang:1.26 go test ./internal/domain/ -timeout 60s`
  and the same for `./internal/adapters/github/`, `./internal/fakesources/` and `./internal/web/`. Format with the same image and `gofmt -w internal/`. The full gate is `make check`; let it run to completion.
- **gofmt:** golangci-lint runs gofmt as a formatter and `make check` fails on misformatting. The code blocks below favour readability; run gofmt before every commit.
- **Attribution:** every commit message ends with `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.
- **Prose style:** comments explain *why*, in full sentences, British spelling (`colour`, `behaviour`, `recognises`). Match the density of the file being edited.

---

## File Structure

| File | Responsibility | Task |
|---|---|---|
| `internal/domain/item.go` | `Advisories` field; `Tier`; `Tier()`; `ShowsQuiet` | 1 |
| `internal/domain/tier_test.go` (new) | The tier and quiet-override tables | 1 |
| `internal/domain/dashboard.go` | `Dashboard.Security` | 1 |
| `internal/domain/dashboard_test.go` | The count ignores the filter | 1 |
| `internal/adapters/github/issues.go` | `advisoryIDs`; `toItem` sets `Advisories` | 2 |
| `internal/adapters/github/advisory_internal_test.go` (new) | Extraction table | 2 |
| `internal/fakesources/testdata/github/repos/org-deps.json` (new) | Three bot PRs | 2 |
| `internal/fakesources/fixtures.go` | Registers `org/deps` | 2 |
| `internal/adapters/github/issues_test.go` | A fetch of `org/deps` populates `Advisories` | 2 |
| `internal/web/dashboard.go` | `itemView` tier fields; `ShowsQuiet`; header count | 3 |
| `internal/web/search.go` | `hitView` tier fields; `ShowsQuiet` | 3 |
| `internal/web/tier.go` (new) | `tierView` built once from an item, for both views | 3 |
| `internal/web/templates/fragments/tier.html` (new) | `{{define "item-tier"}}` | 3 |
| `internal/web/templates/fragments/items.html` | Row class and chip | 3 |
| `internal/web/templates/search.html` | Row class and chip | 3 |
| `internal/web/static/app.css` | `.tier-*` and `.tier-chip` | 3 |
| `internal/web/tier_test.go` (new) | Rendering on `/`, `/items`, `/search` | 3 |
| `docs/requirements/04-functional-requirements.md` | FR-1.13 | 4 |
| `docs/decisions/0014-security-from-evidence-already-fetched.md` (new) | ADR-0014 | 4 |
| `docs/decisions/README.md` | Index row | 4 |
| `internal/version/version.go` | `0.6.0` → `0.7.0` | 4 |

---

### Task 1: The domain owns the rule

**Files:**

- Modify: `internal/domain/item.go`, `internal/domain/dashboard.go`, `internal/domain/dashboard_test.go`
- Create: `internal/domain/tier_test.go`

**Interfaces:**

- Produces, all in package `domain`:

```go
// on Item
Advisories []string

type Tier int
const (
	TierNone Tier = iota
	TierDependency
	TierSecurity
)
func (t Tier) String() string            // "", "dependency", "security"
func (i Item) Tier() Tier
func (i Item) ShowsQuiet(now time.Time, after time.Duration) bool

// on Dashboard
Security int
```

- [ ] **Step 1: Write the failing tests**

Create `internal/domain/tier_test.go`:

```go
package domain_test

import (
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// The tiers are drawn from evidence, not from who opened the item (FR-1.13 AC1). A bot is not by
// itself a security item: Copilot and GitHub Actions open pull requests too, and painting those
// red would teach the reader to ignore red.
func TestTierIsDrawnFromEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		item domain.Item
		want domain.Tier
	}{
		{"a cited CVE is security", domain.Item{Author: "someone", Advisories: []string{"CVE-2026-54904"}}, domain.TierSecurity},
		{"a security label is security", domain.Item{Author: "someone", Labels: []string{"Security"}}, domain.TierSecurity},
		{"a person's issue citing a CVE is security", domain.Item{Author: "gernotstarke", Advisories: []string{"GHSA-6wx8-w4f5-wwcr"}}, domain.TierSecurity},
		{"dependabot citing a CVE is security, not dependency", domain.Item{Author: "dependabot", Advisories: []string{"CVE-2026-33168"}}, domain.TierSecurity},
		{"dependabot with no advisory is dependency", domain.Item{Author: "dependabot"}, domain.TierDependency},
		{"the login is compared without regard to case", domain.Item{Author: "Dependabot"}, domain.TierDependency},
		{"renovate is dependency", domain.Item{Author: "renovate"}, domain.TierDependency},
		{"a dependencies label is dependency", domain.Item{Author: "someone", Labels: []string{"Dependencies"}}, domain.TierDependency},
		{"copilot is not security", domain.Item{Author: "copilot-swe-agent"}, domain.TierNone},
		{"github-actions is not security", domain.Item{Author: "github-actions"}, domain.TierNone},
		{"an ordinary issue is nothing", domain.Item{Author: "someone", Labels: []string{"bug"}}, domain.TierNone},
	} {
		if got := tc.item.Tier(); got != tc.want {
			t.Errorf("%s: Tier() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The tier's String is what the page uses as a class suffix, so it is fixed text and never
// anything upstream said.
func TestTierStringsAreFixed(t *testing.T) {
	for tier, want := range map[domain.Tier]string{
		domain.TierNone: "", domain.TierDependency: "dependency", domain.TierSecurity: "security",
	} {
		if got := tier.String(); got != want {
			t.Errorf("Tier(%d).String() = %q, want %q", tier, got, want)
		}
	}
}

// FR-1.13 AC3: an old, unfixed vulnerability is the item a reader most needs to see, so a Security
// item is never dimmed as quiet. Anything else behaves exactly as IsQuiet does.
func TestShowsQuietNeverHidesASecurityItem(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -400)

	security := domain.Item{UpdatedAt: old, Advisories: []string{"CVE-2026-54904"}}
	if security.ShowsQuiet(now, domain.QuietAfter) {
		t.Error("a year-old security item was marked quiet")
	}

	dependency := domain.Item{UpdatedAt: old, Author: "dependabot"}
	ordinary := domain.Item{UpdatedAt: old}
	for name, it := range map[string]domain.Item{"dependency": dependency, "ordinary": ordinary} {
		if got, want := it.ShowsQuiet(now, domain.QuietAfter), it.IsQuiet(now, domain.QuietAfter); got != want {
			t.Errorf("%s item: ShowsQuiet = %v, IsQuiet = %v; they must agree for anything but security", name, got, want)
		}
	}
}
```

Add to `internal/domain/dashboard_test.go`:

```go
// FR-1.13 AC4: the security count is taken over every item, whatever the filter. A visitor
// filtered to one repository still needs to know that another has an open vulnerability — the
// principle FR-2.1 AC3 already applies to Total.
func TestDashboardCountsSecurityOverEverything(t *testing.T) {
	items := []domain.Item{
		{Repo: "a/one", Number: 1, Advisories: []string{"CVE-2026-1111"}},
		{Repo: "a/two", Number: 2, Labels: []string{"security"}},
		{Repo: "a/two", Number: 3, Author: "dependabot"},
		{Repo: "a/two", Number: 4},
	}
	d := domain.BuildDashboard(domain.DashboardInput{
		Now:    time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC),
		Items:  items,
		Repos:  []string{"a/one", "a/two"},
		Filter: domain.Filter{Repo: "a/two"},
	})
	if d.Security != 2 {
		t.Errorf("Security = %d, want 2: one of them is in a repository the filter excludes", d.Security)
	}
}
```

If `dashboard_test.go` does not already import `time`, add it. If `domain.Filter` has no `Repo` field under that name, use the field the existing dashboard tests use to narrow by repository — read two of them first.

- [ ] **Step 2: Run them to verify they fail**

Run the domain tests. Expected: build failure — `undefined: domain.TierSecurity`, unknown field `Advisories`, and so on.

- [ ] **Step 3: Implement**

In `internal/domain/item.go`, add to the `Item` struct, after `Labels`:

```go
	// Advisories are the CVE and GHSA identifiers the item cites, in its title or anywhere in its
	// body — deduplicated, in the order they were first seen, at most three. The adapter extracts
	// them before the body is cut to its summary, because Dependabot cites them in the release
	// notes, far past the part zorgscope keeps. nil when the item cites none. Like Title, it is
	// borrowed text and is escaped, never trusted.
	Advisories []string
```

Then add, after `IsQuiet`:

```go
// Tier is how loudly the page marks an item (FR-1.13). The zero value is TierNone, so an item
// nobody classified is simply not marked.
type Tier int

// The tiers, from quietest to loudest.
const (
	TierNone Tier = iota
	TierDependency
	TierSecurity
)

// String is the tier as the page names it in a class, and so is fixed text: never anything an
// upstream said.
func (t Tier) String() string {
	switch t {
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
func (i Item) Tier() Tier {
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

// ShowsQuiet is IsQuiet, except that a Security item is never quiet (FR-1.13 AC3). An old, unfixed
// vulnerability is exactly the item a reader most needs to see, and dimming it would hide it.
// The list and the search results both call this rather than IsQuiet, so that the override lives
// in one place and the two cannot disagree.
func (i Item) ShowsQuiet(now time.Time, after time.Duration) bool {
	return i.Tier() != TierSecurity && i.IsQuiet(now, after)
}
```

Add `"strings"` to `item.go`'s imports.

In `internal/domain/dashboard.go`, add to the `Dashboard` struct after `Total, Shown int`:

```go
	// Security is how many open items are Security tier, counted over every item regardless of
	// the filter, like Total (FR-1.13 AC4). A visitor filtered to one repository still needs to
	// know that another has an open vulnerability.
	Security int
```

and in `BuildDashboard`, after `d.Total = len(in.Items)`:

```go
	for _, it := range in.Items {
		if it.Tier() == TierSecurity {
			d.Security++
		}
	}
```

- [ ] **Step 4: Run the domain tests**

Expected: PASS. Domain coverage must stay at or above 90 %.

- [ ] **Step 5: Commit**

```bash
git add internal/domain/
git commit -F - <<'EOF'
feat(domain): the security and dependency tiers, drawn from evidence (FR-1.13)

An item is Security when it cites a CVE or GHSA identifier or carries a security label, and
Dependency when Dependabot or Renovate opened it or it is labelled dependencies. A bot is not by
itself either: Copilot and GitHub Actions open pull requests too, and painting those red would
teach the reader to ignore red — the failure that retired the NEW badge (ADR-0012).

A Security item is never quiet, and the override lives here so that the list and the search
results cannot disagree about it. The count is taken over every item whatever the filter, as Total
already is.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

### Task 2: The adapter reads the advisories before cutting the body

**Files:**

- Modify: `internal/adapters/github/issues.go` (`toItem`, ~line 314)
- Create: `internal/adapters/github/advisory_internal_test.go`
- Create: `internal/fakesources/testdata/github/repos/org-deps.json`
- Modify: `internal/fakesources/fixtures.go` (`githubRepoFiles`, ~line 73)
- Modify: `internal/adapters/github/issues_test.go`

**Interfaces:**

- Consumes: `domain.Item.Advisories` from Task 1.
- Produces: `func advisoryIDs(texts ...string) []string` in package `github` (internal).

- [ ] **Step 1: Write the extraction test**

Create `internal/adapters/github/advisory_internal_test.go`:

```go
package github

import (
	"reflect"
	"strings"
	"testing"
)

// Dependabot cites advisories in its release notes, far past the 300 bytes zorgscope keeps as a
// summary. The scan must see the whole body, and these cases are shaped like the real thing.
func TestAdvisoryIDs(t *testing.T) {
	padding := strings.Repeat("Release notes and changelog text. ", 30) // well past 300 bytes

	for _, tc := range []struct {
		name  string
		texts []string
		want  []string
	}{
		{"nothing in an ordinary body", []string{"Fix a typo", "The link on the about page is broken."}, nil},
		{"a CVE far past the summary", []string{"Bump nokogiri", padding + "Fixes CVE-2026-54904."}, []string{"CVE-2026-54904"}},
		{"a GHSA, case normalised", []string{"", padding + "See ghsa-6WX8-w4f5-WWCR for details."}, []string{"GHSA-6wx8-w4f5-wwcr"}},
		{"a CVE in the title", []string{"Patch CVE-2026-1111", ""}, []string{"CVE-2026-1111"}},
		{"a lower-case CVE is stored upper-case", []string{"", "fixes cve-2026-2222"}, []string{"CVE-2026-2222"}},
		{"duplicates collapse, first-seen order kept", []string{"CVE-2026-3333", "CVE-2026-4444 and again CVE-2026-3333"}, []string{"CVE-2026-3333", "CVE-2026-4444"}},
		{"at most three", []string{"", "CVE-2026-0001 CVE-2026-0002 CVE-2026-0003 CVE-2026-0004"}, []string{"CVE-2026-0001", "CVE-2026-0002", "CVE-2026-0003"}},
		{"a CVE needs at least four digits after the year", []string{"", "CVE-2026-12 is not one"}, nil},
	} {
		if got := advisoryIDs(tc.texts...); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: advisoryIDs = %v, want %v", tc.name, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run `./internal/adapters/github/`. Expected: `undefined: advisoryIDs`.

- [ ] **Step 3: Implement the extraction**

In `internal/adapters/github/issues.go`, near `summarise`:

```go
// maxAdvisories bounds how many advisory identifiers an item keeps. One is enough to explain why
// a row is red, and three keeps the chip's title readable; a Dependabot group update can cite a
// dozen.
const maxAdvisories = 3

// advisoryPattern matches the two identifier schemes GitHub's own advisories use: CVE, with at
// least four digits after the year as the scheme requires, and GHSA, three groups of four.
var advisoryPattern = regexp.MustCompile(`(?i)\b(CVE-\d{4}-\d{4,}|GHSA(?:-[0-9a-z]{4}){3})\b`)

// advisoryIDs returns the CVE and GHSA identifiers the texts cite, in the order first seen,
// without duplicates, at most maxAdvisories (FR-1.13 AC1).
//
// It exists because of where Dependabot puts them: in the release notes it quotes, far past the
// 300 bytes zorgscope keeps as a summary. So it has to run on the whole body, before summarise
// cuts it — which costs nothing, because the body already arrives whole in the query that runs
// today (QS-3.5). The identifiers are normalised so that the same advisory spelled in two cases is
// one advisory: CVE upper-case, as the scheme writes it, and GHSA with a lower-case body, as GitHub
// writes it.
func advisoryIDs(texts ...string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, text := range texts {
		for _, m := range advisoryPattern.FindAllString(text, -1) {
			id := strings.ToUpper(m)
			if strings.HasPrefix(id, "GHSA-") {
				id = "GHSA-" + strings.ToLower(id[len("GHSA-"):])
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
			if len(out) == maxAdvisories {
				return out
			}
		}
	}
	return out
}
```

Add `"regexp"` to the imports (and `"strings"` if it is not already there).

In `toItem`, add the field beside `Summary`, so the scan sees the body before anything cuts it:

```go
		Summary:    summarise(string(n.BodyText)),
		Advisories: advisoryIDs(string(n.Title), string(n.BodyText)),
```

`toPRItem` already routes through `toItem`, passing `BodyText` along, so pull requests are covered by this one change.

- [ ] **Step 4: Run the extraction test**

Expected: PASS.

- [ ] **Step 5: Add the fixture repository**

Create `internal/fakesources/testdata/github/repos/org-deps.json`. Copy the node shape exactly from `org-repo.json` in the same directory — same keys, same date format, same `labels` and `author` shapes — and write three pull requests and no issues:

1. **A Dependabot security bump.** `author.login` `dependabot`; labels `dependencies` and `ruby`; title `Bump concurrent-ruby from 1.2.2 to 1.3.7`; a `bodyText` that begins `Bumps concurrent-ruby from 1.2.2 to 1.3.7.` followed by at least 400 bytes of release-note prose, and only *then* cites `CVE-2026-54904` and `GHSA-6wx8-w4f5-wwcr`. The advisories must sit past byte 300 — that is the whole point of the fixture.
2. **A routine Dependabot bump.** `author.login` `dependabot`; label `dependencies`; title `Bump uri from 1.0.3 to 1.0.4`; a body citing no advisory.
3. **A Copilot pull request.** `author.login` `copilot-swe-agent`; no labels; title `Fix broken include`; a body citing no advisory.

Register it in `internal/fakesources/fixtures.go`'s `githubRepoFiles`:

```go
	"org/deps":  "testdata/github/repos/org-deps.json",
```

It is a new repository rather than new nodes in `org/repo` on purpose: existing tests count `org/repo`'s items, and none of them should move.

- [ ] **Step 6: Prove it through the real decode path**

In `internal/adapters/github/issues_test.go`, add a test that fetches `org/deps` against the fake server exactly the way the existing tests fetch `org/repo` — read two of them first and use the same helpers — and asserts:

- the concurrent-ruby pull request's `Advisories` equal `[]string{"CVE-2026-54904", "GHSA-6wx8-w4f5-wwcr"}`;
- the uri and Copilot pull requests have nil `Advisories`;
- the concurrent-ruby `Summary` does **not** contain `CVE-2026-54904` — the test that proves the scan reached past the summary rather than reading it.

- [ ] **Step 7: Run the adapter and fakesources packages**

Expected: PASS in both, and every existing count of `org/repo` unchanged. If a fakesources test asserts the exact number of registered repositories, update it to include `org/deps` and say so in your report.

- [ ] **Step 8: Run the full gate and commit**

```bash
git add internal/adapters/github/ internal/fakesources/
git commit -F - <<'EOF'
feat(github): read the advisories a body cites before cutting it to a summary (FR-1.13)

Dependabot cites CVE and GHSA identifiers in the release notes it quotes, far past the 300 bytes
kept as a summary, so the scan runs on the whole body in toItem, before summarise cuts it. The body
already arrives whole in the query that runs today, so this costs no request (QS-3.5).

The org/deps fixture is a new repository rather than new nodes in org/repo, so that no existing
count moves; it proves the scan reaches past the summary through the real decode path.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

### Task 3: The page draws it, on the list and the search results alike

**Files:**

- Create: `internal/web/tier.go`, `internal/web/tier_test.go`, `internal/web/templates/fragments/tier.html`
- Modify: `internal/web/dashboard.go` (`itemView` ~line 287; `newItemView` ~line 501; `shownLine` ~line 436)
- Modify: `internal/web/search.go` (`hitView` ~line 37; the `hitView` literal ~line 72)
- Modify: `internal/web/templates/fragments/items.html`, `internal/web/templates/search.html`
- Modify: `internal/web/static/app.css`

**Interfaces:**

- Consumes: `domain.Item.Tier()`, `domain.Tier.String()`, `domain.Item.ShowsQuiet`, `domain.Item.Advisories`, `domain.Dashboard.Security` from Task 1.
- Produces: `type tierView struct { Name, Label, Evidence string }` and `func newTierView(it domain.Item) tierView`, embedded as a `Tier tierView` field in both `itemView` and `hitView`.

- [ ] **Step 1: Write the failing tests**

Create `internal/web/tier_test.go`:

```go
package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// tierItems are one item of each tier, the security one old enough to be quiet by default so that
// the quiet override is exercised on every page that draws it.
func tierItems() []domain.Item {
	security := ghItem(1, "Bump concurrent-ruby", testNow.AddDate(0, 0, -400))
	security.Author = "dependabot"
	security.Advisories = []string{"CVE-2026-54904", "GHSA-6wx8-w4f5-wwcr"}

	dependency := ghItem(2, "Bump uri", testNow.Add(-time.Hour))
	dependency.Author = "dependabot"

	plain := ghItem(3, "Fix broken include", testNow.Add(-time.Hour))
	plain.Author = "copilot-swe-agent"

	return []domain.Item{security, dependency, plain}
}

// FR-1.13 AC2: both tiers draw a word, a class and — for security — the evidence, on every page
// that draws an item. Search builds its rows separately from the list, so it is checked on its
// own: a chip that appeared on one and not the other is precisely the seam this guards.
func TestTiersAreDrawnOnEveryPageThatDrawsAnItem(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: tierItems()})
	c := signIn(t, h)

	for _, path := range []string{"/", "/items", "/search?q=bump"} {
		body := getAs(h, path, c).Body.String()
		for _, want := range []string{
			`tier-security`, `tier-dependency`,
			`>Security<`, `>Dependency<`,
			`title="cites CVE-2026-54904`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %s", path, want)
			}
		}
		// The Copilot pull request is not a dependency update and must not be drawn as one.
		if n := strings.Count(body, `>Dependency<`); n != 1 {
			t.Errorf("%s draws %d Dependency chips, want exactly 1", path, n)
		}
	}
}

// FR-1.13 AC3: the security item is 400 days old and would be quiet by default; it must not be,
// on the list or on the search results.
func TestASecurityItemIsNeverQuiet(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: tierItems()})
	c := signIn(t, h)

	for _, path := range []string{"/", "/search?q=concurrent"} {
		body := getAs(h, path, c).Body.String()
		if strings.Contains(body, "is-quiet") {
			t.Errorf("%s dims the security item as quiet", path)
		}
	}
}

// FR-1.13 AC4: the header counts security items, says nothing when there are none, and counts
// over everything rather than the filtered view.
func TestTheHeaderCountsSecurityItems(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: tierItems()})
	c := signIn(t, h)
	if body := getAs(h, "/", c).Body.String(); !strings.Contains(body, "· 1 security") {
		t.Error("the list header does not count the open security item")
	}

	// Filtered to pull requests, the list shows none of these items — ghItem makes issues — yet
	// the count still reports the security one, because it counts everything open.
	if body := getAs(h, "/?kind=pr", c).Body.String(); !strings.Contains(body, "· 1 security") {
		t.Error("the security count followed the filter; it must count everything open")
	}

	// With no security item the clause is absent, not "0 security". Asserting the exact header
	// text rather than the absence of the word keeps this from tripping on unrelated copy.
	quiet := dashHandler(t, &fakeSource{items: []domain.Item{ghItem(9, "Nothing to see", testNow)}})
	qc := signIn(t, quiet)
	if body := getAs(quiet, "/", qc).Body.String(); !strings.Contains(body, ">1 open<") {
		t.Error("the header is not exactly \"1 open\" when no security item is open")
	}
}

// Advisory identifiers are borrowed text. One that carried markup must reach the page escaped.
func TestEvidenceIsEscaped(t *testing.T) {
	it := ghItem(1, "Bump thing", testNow)
	it.Advisories = []string{`CVE-2026-1111"><script>`}
	h := dashHandler(t, &fakeSource{items: []domain.Item{it}})
	c := signIn(t, h)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), `"><script>`) {
		t.Error("an advisory identifier reached the page unescaped")
	}
}
```

`ghItem(n, title, updatedAt)` (in `dashboard_test.go`) returns a `domain.Item` that is an **issue** in `org/repo`, authored by `someone` — which is why `tierItems` sets `Author` explicitly and why the filter test uses `kind=pr` to exclude them all. `testNow` is in `auth_test.go`. The list's shown-count renders as `<span class="shown-count">…</span>`, so an unfiltered single item reads `>1 open<`.

- [ ] **Step 2: Run them to verify they fail**

Expected: FAIL — no `tier-security` anywhere.

- [ ] **Step 3: The shared view**

Create `internal/web/tier.go`:

```go
package web

import (
	"strings"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// tierView is how an item's tier is drawn (FR-1.13 AC2). It is built once, here, and embedded in
// both the list's row and the search results' row, because search builds its rows separately from
// the list — and a chip that appeared on one and not the other is exactly the kind of seam a
// separate builder invites.
type tierView struct {
	// Name is the tier as a class suffix — "security", "dependency" or "" — and is fixed text,
	// never anything an upstream said.
	Name string
	// Label is the word the chip carries. Colour is never the only signal.
	Label string
	// Evidence is the chip's title: why the row is marked. For a Security item it names the
	// advisories it cites, or says it is labelled security; for a Dependency item it is empty.
	Evidence string
}

func newTierView(it domain.Item) tierView {
	switch it.Tier() {
	case domain.TierSecurity:
		evidence := "labelled security"
		if len(it.Advisories) > 0 {
			evidence = "cites " + strings.Join(it.Advisories, ", ")
		}
		return tierView{Name: "security", Label: "Security", Evidence: evidence}
	case domain.TierDependency:
		return tierView{Name: "dependency", Label: "Dependency"}
	default:
		return tierView{}
	}
}
```

- [ ] **Step 4: Wire both row builders**

In `internal/web/dashboard.go`, add to `itemView`:

```go
	// Tier is how loudly the row is marked (FR-1.13), shared with the search results' rows.
	Tier tierView
```

and in `newItemView`, set `Tier: newTierView(it),` and change `Quiet: it.IsQuiet(now, quiet),` to `Quiet: it.ShowsQuiet(now, quiet),`.

In `internal/web/search.go`, add the same `Tier tierView` field to `hitView`, and in the `hitView` literal set `Tier: newTierView(h.Item),` and change `Quiet: h.Item.IsQuiet(now, quiet),` to `Quiet: h.Item.ShowsQuiet(now, quiet),`.

- [ ] **Step 5: The header count**

In `internal/web/dashboard.go`, change `shownLine` so both of its returns end with the security clause:

```go
// shownLine is the count above the list. It names the total whenever a filter is in force,
// because "12 open" and "12 of 150 open" are different news and only one of them is true. When
// any security item is open it says so, counted over everything whatever the filter
// (FR-1.13 AC4), and says nothing when there are none.
func shownLine(d domain.Dashboard) string {
	line := strconv.Itoa(d.Total) + " open"
	if !d.Filter.Empty() {
		line = strconv.Itoa(d.Shown) + " of " + strconv.Itoa(d.Total) + " open"
	}
	if d.Security > 0 {
		line += " · " + strconv.Itoa(d.Security) + " security"
	}
	return line
}
```

- [ ] **Step 6: The partial**

Create `internal/web/templates/fragments/tier.html`:

```html
{{/* The tier chip (FR-1.13 AC2), drawn by the list and by the search results alike. It is one
     definition in a fragment file because fragmentGlob parses every file here into every page and
     into the fragment set, so the list, GET /items and /search all reach the same markup and the
     three cannot drift.

     The word is what carries the meaning; the shield is decorative and hidden from assistive
     technology. The title names the evidence, so a red row always answers "why?". It is called
     with the tierView itself. */}}
{{define "item-tier"}}{{if .Name}}<span class="tier-chip tier-chip-{{.Name}}"{{with .Evidence}} title="{{.}}"{{end}}>{{if eq .Name "security"}}<svg class="tier-shield" viewBox="0 0 16 16" width="12" height="12" aria-hidden="true" focusable="false"><path d="M8 1.2 2.5 3.3v4.2c0 3.4 2.3 6.3 5.5 7.3 3.2-1 5.5-3.9 5.5-7.3V3.3z" fill="currentColor"/></svg>{{end}}{{.Label}}</span>{{end}}{{end}}
```

The chip's text is `{{.Label}}`, so the rendered markup contains `>Security<` and `>Dependency<` — the tests rely on that. Keep the label directly adjacent to the closing `</svg>` and `</span>` with no whitespace between.

- [ ] **Step 7: Draw it**

In `internal/web/templates/fragments/items.html`, extend the row's class and put the chip first on the item line:

```html
      <li class="item{{if .Quiet}} is-quiet{{end}}{{with .Tier.Name}} tier-{{.}}{{end}}">
        <p class="item-line">
          {{template "item-tier" .Tier}}
          {{if .Number}}<span class="item-number">#{{.Number}}</span>{{end}}
```

In `internal/web/templates/search.html`, the same for the hit row:

```html
    <li class="hit hue-{{.Hue}}{{if .Quiet}} is-quiet{{end}}{{with .Tier.Name}} tier-{{.}}{{end}}">
      <p class="item-line">
        {{template "item-tier" .Tier}}
        <span class="hit-kind">{{.Kind}}</span>
```

Leave everything else in both rows untouched.

- [ ] **Step 8: Style it**

In `internal/web/static/app.css`, beside the `.label` chip rules. The project's tokens are `--bg`, `--surface`, `--border`, `--text`, `--muted`, `--accent`, `--danger`, `--warn`, `--ok`, `--radius`, `--gap` — use those, invent none.

```css
/* The tier chips (FR-1.13). The word carries the meaning; the colour and the shield only make it
   easier to spot. Security is loud because it means a published vulnerability; Dependency is quiet
   because a routine update is maintenance, and a mark that is always loud is read as noise. */
.tier-chip {
  display: inline-flex;
  align-items: center;
  gap: 0.25em;
  margin-right: 0.35rem;
  padding: 0 0.4em;
  border: 1px solid currentColor;
  border-radius: 999px;
  font-size: 0.72rem;
  font-weight: 600;
  line-height: 1.5;
  vertical-align: 0.1em;
}
.tier-chip-security { color: var(--danger); }
.tier-chip-dependency { color: var(--muted); font-weight: 500; }
.tier-shield { flex: none; }

/* The row's left rule for a Security item, drawn inside the site's own stripe so that both read.
   The padding moves with it, so the text does not shift against its neighbours. */
.item.tier-security,
.hit.tier-security {
  border-left: 3px solid var(--danger);
  padding-left: 0.6rem;
}
```

Check how `.item` and `.hit` are already padded in `app.css`. If either already has a `padding-left` or a `border-left`, adjust this rule so the text of a Security row lines up with its neighbours rather than jumping sideways, and say what you did in your report.

`contrast_test.go` parses this file and asserts contrast ratios. `--danger` and `--muted` are both already used for text elsewhere; if a contrast assertion fails, use the tokens it already accepts rather than loosening it.

- [ ] **Step 9: Run everything**

Run `./internal/web/`, then `make check` to completion. Report the dashboard byte count from `TestRenderedPageStaysInsideItsBudget` and the static total from `TestStaticAssetsFitTheirBudgetOnTheWire`.

- [ ] **Step 10: Commit**

```bash
git add internal/web/
git commit -F - <<'EOF'
feat(web): security and dependency items stand out on the list and in search (FR-1.13)

A Security item carries a red chip naming the advisories it cites and a red rule down its edge; a
Dependency item a quiet chip. The chip is one partial in the fragments directory, which is parsed
into every page and into the fragment set, so the list, GET /items and the search results draw the
same markup — search builds its rows separately from the list, which is the seam a shared
definition closes.

A Security item is never dimmed as quiet, and the list header counts them over everything whatever
the filter, saying nothing when there are none.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

### Task 4: Requirements, the ADR and the version

**Files:**

- Modify: `docs/requirements/04-functional-requirements.md`
- Create: `docs/decisions/0014-security-from-evidence-already-fetched.md`
- Modify: `docs/decisions/README.md`
- Modify: `internal/version/version.go:20`

- [ ] **Step 1: FR-1.13**

Add a row to the E-1 table immediately after the FR‑1.12 row. **The ids in this file use U+2011 NON-BREAKING HYPHEN, not an ASCII hyphen** — copy `FR‑1.12` from the neighbouring row and edit the digit rather than typing the id, and do the same for every `FR‑`, `QS‑` and `ADR‑` reference in the cell. The acceptance criteria are one table cell on one line.

Priority `S`. Statement: *As the user I cannot miss a security item or a dependency update.*

Acceptance criteria:

`AC1 An item citing a CVE or GHSA identifier in its title or body, or labelled security in any case, is marked Security; otherwise an item authored by Dependabot or Renovate, or labelled dependencies in any case, is marked Dependency; nothing else is marked. AC2 A Security item carries a chip reading "Security" and a rule down its left edge, a Dependency item a chip reading "Dependency", on the list and on the search results alike; the chip's title names the evidence; colour is never the only signal. AC3 A Security item is never marked quiet (FR‑1.10 AC4). AC4 The list states how many Security items are open, over every open item whatever the filter, and says nothing when there are none. AC5 No request beyond those already made is sent to GitHub (QS‑3.5).`

- [ ] **Step 2: ADR-0014**

Create `docs/decisions/0014-security-from-evidence-already-fetched.md`. Follow the shape of `docs/decisions/0013-stale-while-revalidating.md` exactly — read it first. Title: `# 0014. Security from evidence already fetched: two tiers, no security API`. Status accepted, date 2026-09-21, requirements FR‑1.13, FR‑1.10, QS‑3.5.

Considered options — record all three:

- **A:** Highlight every bot-authored item. Rejected: `copilot-swe-agent` and `github-actions` open pull requests too, and would be painted red.
- **B:** Two tiers drawn from evidence the existing queries already fetch — a cited CVE/GHSA identifier or a `security` label for Security; Dependabot, Renovate or a `dependencies` label for Dependency. **Chosen.**
- **C:** Ask GitHub's Dependabot-alerts, code-scanning or secret-scanning APIs. Rejected: each is a new request in a budget that is spent exactly (QS‑3.5), each needs a `security_events` token scope the deployment does not have, and surfacing security alerts was rejected on 2026‑09‑17.

Consequences, in both directions:

- Good: no request, no scope, no budget; the rule is one pure function in the domain.
- Good: the red stays meaningful, because a routine bump is only a quiet Dependency mark — the lesson of the retired NEW badge (ADR‑0012).
- Bad: a vulnerability that Dependabot fixes in a pull request citing no identifier, and that nobody labels, is drawn as Dependency, not Security.
- Bad: a routine bump whose quoted release notes happen to cite an unrelated advisory is drawn as Security. On these repositories that errs towards visible, which is the safer mistake.
- Neutral: Dependabot's pull requests are short-lived and none is open at the time of writing, so the highlight is usually dormant.

Add the row to `docs/decisions/README.md`'s index table in the same format as its neighbours.

- [ ] **Step 3: The version**

`internal/version/version.go:20` → `const Version = "0.7.0"`.

- [ ] **Step 4: Verify and commit**

Run markdownlint (`docker run --rm -v "$PWD":/work -w /work davidanson/markdownlint-cli2:latest "docs/**/*.md" "README.md"`) — 0 issues — then `make check` to completion.

```bash
git add docs/ internal/version/version.go
git commit -F - <<'EOF'
docs: FR-1.13, ADR-0014, version 0.7.0 (FR-1.13)

FR-1.13 states what the security highlight owes the visitor. ADR-0014 records why the tiers are
drawn from evidence the existing queries already fetch rather than from GitHub's security APIs, and
why a bot is not by itself a security item.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

## Verification before calling this done

- [ ] `make check` clean.
- [ ] No Dependabot pull request is open on the configured repositories today, so the highlight cannot be seen live against the real configuration. The web tests render both tiers; say so plainly rather than claiming a browser check.
