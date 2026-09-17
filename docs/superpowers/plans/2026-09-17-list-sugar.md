# List Sugar Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the list view the arc42 family's colours and a little more information per item: the status site's rainbow band on every page, a stripe in the site's colour on each repository group, the items' labels as chips with a fixed palette, and items untouched for 90 days marked quiet.

**Architecture:** Labels are one more field on the GraphQL nodes the adapter already fetches (no new request), carried on `domain.Item` and turned into chips in the web layer, where a fixed name-to-key map decides the colour class. The site colour reaches a group as the same `hue-<key>` class the tiles use, drawn as a left border. The quiet rule is one domain function. Everything visual is CSS in `app.css`; nothing inline (CSP).

**Tech Stack:** Go 1.26 module (host Go 1.27), `html/template`, hand-written CSS with `light-dark()` and `color-mix()`, `shurcooL/githubv4` for the query structs. Gate: `make check`, or its native equivalent while Docker is down.

**Spec:** `docs/superpowers/specs/2026-09-17-list-sugar-design.md`

## Global Constraints

- No inline `<script>`, `<style>` or `style=` attribute; `contentSecurityPolicy` unchanged; a colour reaches the page only as a CSS class (QS‑4.4).
- The request count per fetch does not change: `TestGraphQLRequestBudget` keeps counting exactly 20 for 10 repositories (QS‑3.5). Labels are a field, `labels(first: 10)`, on the existing nodes.
- Label keys, exactly: `bug`, `enhancement`, `documentation`, `question`, `help-wanted`, `in-progress`; everything else is `other`. Matching: lowercase, trim, runs of whitespace become one hyphen.
- Quiet threshold, exactly: `QuietAfter = 90 * 24 * time.Hour`; an item is quiet when `now.Sub(UpdatedAt) >= QuietAfter`; an item with a zero `UpdatedAt` is never quiet. Quiet changes no ordering.
- Rainbow band, exactly: `linear-gradient(90deg, #c22b47, #ffc95c, #2e9e67, #5fb49c, #1675b9, #374769, #682d63)`, 4 px high, on every page including sign-in and the wait page, same in both appearances.
- Group stripe: 4 px left border; light appearance `--tile-hue`, dark appearance `--tile-sig` mixed 30 % toward white; the repository name stays `--text`; every stripe ≥ 3:1 against `--bg` in both appearances (adjust only the mix percentage if a stripe fails, never a brand colour).
- Label palette tokens, exactly: `--label-bug: light-dark(#b3261e, #f2836f)`, `--label-enhancement: light-dark(#0e4f80, #7fb2e0)`, `--label-documentation: light-dark(#1b5648, #5fb49c)`, `--label-question: light-dark(#682d63, #c98ac1)`, `--label-help-wanted: light-dark(#186633, #6bc98a)`, `--label-in-progress: light-dark(#8a5300, #e0a03c)`; chip text (the label colour) ≥ 4.5:1 against the chip background (`--bg` with 10 % of the label colour mixed in) in both appearances.
- Dashboard budgets of QS‑2.3 unchanged: ≤ 150 kB HTML with the representative fixture, which now carries labels.
- No new configuration key, no new route, no new Make target, no database, no ticker.
- Every task ends with `go build ./... && go vet ./... && go test -race -timeout 120s ./...` green on the host and exactly one commit; Task 4 also ends with the full gate. If Docker is up, the gate is `make check`; if not, run natively: the three Go commands above with `-coverprofile=coverage.out` and `go tool cover -func=coverage.out | tail -1`; `go test -coverprofile=domain.out ./internal/domain/... && go tool cover -func=domain.out | tail -1` (≥ 90 %); `GOFLAGS=-mod=mod go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.0 run ./...` (0 issues); `npx --yes markdownlint-cli2@0.23.2 "docs/**/*.md" "README.md"` (0 issues); `fly config validate --strict --app zorgscope --config deploy/fly.toml`. Never run a `docker` command when Docker is down (it hangs).
- Stage files explicitly. Never `git add -A`. Never commit `.agent/`, `.agents/`, `.claude/`, `_bmad/`, `.env`, `coverage.out` or `domain.out`.
- Commit messages name the requirement ids they touch and end with a blank line and `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`.
- Comments use British spelling (golangci `misspell` locale UK); `revive` runs: exported identifiers carry doc comments starting with their name.
- Branch: `feat/list-sugar` (exists; spec at `d0f9de0`, version bump at `1a48020`).

---

### Task 1: Labels through the adapter, the domain and the fake GitHub

Model tier: cheap (the code is complete below).

**Files:**

- Modify: `internal/domain/item.go` (the `Item` struct)
- Modify: `internal/adapters/github/issues.go` (`ghIssueNode`, `ghPRNode`, `toItem`, `toPRItem`; add `ghLabelNode`, `labelNames`)
- Modify: `internal/adapters/github/issues_test.go` (append a test)
- Modify: `internal/fakesources/fixtures.go` (`ghIssue`; add `ghLabels`, `ghLabel`)
- Modify: `internal/fakesources/github_graphql.go` (`paginate`)
- Modify: `internal/fakesources/testdata/github/repos/org-repo.json` (labels on four nodes)
- Modify: `internal/fakesources/github_graphql_test.go` (append two tests)

**Interfaces:**

- Consumes: nothing new.
- Produces: `domain.Item.Labels []string` (GitHub's order, nil when none); fixture `org/repo` labels: issue #1 `["bug", "Help Wanted"]`, issue #2 `["enhancement"]`, issue #3 `["documentation", "needs-triage"]`, pull request #10 `["in progress"]`; every fake node serialises `labels: {nodes: [...]}`, `[]` when none.

- [ ] **Step 1: Write the failing tests**

Append to `internal/adapters/github/issues_test.go`:

```go
// FR-1.10 AC3: an item carries its labels, as GitHub spells them and in GitHub's order, and an
// item without labels carries none. The fixture's org/repo #1 has two, #2 one, #3 two (one of
// them outside the palette — the adapter does not know the palette), and #10 one; #11 has none.
func TestFetchCarriesLabelsInGitHubsOrder(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	f := github.NewIssueFetcher(github.Config{
		Token: "x", BaseURL: srv.URL + "/graphql", Repos: []string{"org/repo"},
	}, srv.Client())
	items, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	byID := byKey(items)

	want := map[string][]string{
		"issue:org/repo#1": {"bug", "Help Wanted"},
		"issue:org/repo#2": {"enhancement"},
		"issue:org/repo#3": {"documentation", "needs-triage"},
		"pr:org/repo#10":   {"in progress"},
		"pr:org/repo#11":   nil,
	}
	for key, labels := range want {
		it, ok := byID[key]
		if !ok {
			t.Fatalf("%s missing from %d items", key, len(items))
		}
		if strings.Join(it.Labels, "|") != strings.Join(labels, "|") {
			t.Errorf("%s labels = %q, want %q", key, it.Labels, labels)
		}
		if labels == nil && it.Labels != nil {
			t.Errorf("%s has an empty non-nil label slice; an item without labels carries nil", key)
		}
	}
}
```

Append to `internal/fakesources/github_graphql_test.go`:

```go
// The fixture's labels round-trip through the GraphQL handler in the shape GitHub uses:
// labels.nodes[].name, in fixture order.
func TestGraphQLServesLabelsInFixtureOrder(t *testing.T) {
	srv := httptest.NewServer(NewServer())
	defer srv.Close()

	body := graphQL(t, srv.URL, `{"query":"{ repository(owner: $owner, name: $name) { issues { nodes { number labels { nodes { name } } } } } }","variables":{"owner":"org","name":"repo"}}`)
	var resp struct {
		Data struct {
			Repository struct {
				Issues struct {
					Nodes []struct {
						Number int `json:"number"`
						Labels struct {
							Nodes []struct {
								Name string `json:"name"`
							} `json:"nodes"`
						} `json:"labels"`
					} `json:"nodes"`
				} `json:"issues"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decoding: %v\n%s", err, body)
	}
	got := map[int][]string{}
	for _, n := range resp.Data.Repository.Issues.Nodes {
		var names []string
		for _, l := range n.Labels.Nodes {
			names = append(names, l.Name)
		}
		got[n.Number] = names
	}
	if strings.Join(got[1], "|") != "bug|Help Wanted" || strings.Join(got[3], "|") != "documentation|needs-triage" {
		t.Errorf("labels = %v, want #1 [bug Help Wanted] and #3 [documentation needs-triage]", got)
	}
}

// A node without labels serialises an empty connection, never a missing key or a null list:
// shurcooL's decoder is strict about the keys the query asked for.
func TestANodeWithoutLabelsServesAnEmptyConnection(t *testing.T) {
	srv := httptest.NewServer(NewServer())
	defer srv.Close()

	body := graphQL(t, srv.URL, `{"query":"{ repository(owner: $owner, name: $name) { pullRequests { nodes { number labels { nodes { name } } } } } }","variables":{"owner":"org","name":"bad"}}`)
	if !strings.Contains(string(body), `"labels":{"nodes":[]}`) {
		t.Errorf("a node without labels must serialise \"labels\":{\"nodes\":[]}; got %s", body)
	}
}
```

If `github_graphql_test.go` has no helper that POSTs a JSON body to `/graphql` and returns the response bytes, add one there:

```go
// graphQL posts body to the fake's /graphql and returns the response body.
func graphQL(t *testing.T, base, body string) []byte {
	t.Helper()
	resp, err := http.Post(base+"/graphql", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /graphql: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /graphql = %d: %s", resp.StatusCode, out)
	}
	return out
}
```

(check that file's imports: `encoding/json`, `io`, `net/http`, `net/http/httptest`, `strings`, `testing`.) The fake handler ignores the query text except for the connection names, so the query strings above only need to mention `issues` or `pullRequests`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/adapters/github/ -run TestFetchCarriesLabelsInGitHubsOrder 2>&1 | head -5; go test ./internal/fakesources/ -run 'TestGraphQLServesLabelsInFixtureOrder|TestANodeWithoutLabelsServesAnEmptyConnection' 2>&1 | head -5`

Expected: the adapter test fails to compile (`it.Labels undefined`); the fakes tests fail on the assertions (no `labels` key yet).

- [ ] **Step 3: The domain field**

In `internal/domain/item.go`, add to `Item` after `Author`:

```go
	// Labels are the item's GitHub labels as GitHub spells them, in GitHub's order — at most ten,
	// which covers every arc42 item there is. nil when the item has none. The domain carries the
	// names and nothing else; which of them get a colour is the page's business (FR-1.10 AC3).
	Labels []string
```

- [ ] **Step 4: The adapter**

In `internal/adapters/github/issues.go`:

Add after `ghPageInfo`:

```go
// ghLabelNode is one label of an issue or pull request: its name is all the page needs.
type ghLabelNode struct {
	Name githubv4.String
}

// ghLabelConnection is the labels connection of one node. first: 10 is the whole of any arc42
// item's labels today, and it is a field on a node the query already fetches — it costs points,
// not requests, so QS-3.5's count of 20 does not move (FR-1.10 AC3).
type ghLabelConnection struct {
	Nodes []ghLabelNode
}
```

Add to both `ghIssueNode` and `ghPRNode`, after `State` (and before `IsDraft` on the PR node):

```go
	Labels    ghLabelConnection `graphql:"labels(first: 10)"`
```

(`gofmt` will realign the field block.) In `toItem`, add `Labels: labelNames(n.Labels)` to the `domain.Item` literal. In `toPRItem`, add `Labels: n.Labels,` to the `ghIssueNode` literal it builds. Add after `toPRItem`:

```go
// labelNames is the names of a label connection, in GitHub's order; nil for none, so an item
// without labels carries nil rather than an empty slice.
func labelNames(c ghLabelConnection) []string {
	if len(c.Nodes) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.Nodes))
	for _, l := range c.Nodes {
		out = append(out, string(l.Name))
	}
	return out
}
```

- [ ] **Step 5: The fake GitHub**

In `internal/fakesources/fixtures.go`, add to `ghIssue` after `State`:

```go
	// Labels is the labels connection GitHub serialises as labels.nodes[].name. A fixture may
	// leave it out; paginate serves such a node with an empty connection rather than null.
	Labels ghLabels `json:"labels"`
```

and after `ghAuthor`:

```go
// ghLabels mirrors GitHub's labels connection: a page of nodes.
type ghLabels struct {
	Nodes []ghLabel `json:"nodes"`
}

// ghLabel is one label; the fake serves its name only.
type ghLabel struct {
	Name string `json:"name"`
}
```

In `internal/fakesources/github_graphql.go`, in `paginate`, replace

```go
	page := nodes[start:end]
	if page == nil {
		page = []ghIssue{}
	}
```

with

```go
	// A copy, so that filling in an empty label list never writes into the fixture. shurcooL's
	// decoder wants every key it asked for, so a node without labels is served with
	// labels: {nodes: []} rather than null.
	page := make([]ghIssue, 0, end-start)
	for _, n := range nodes[start:end] {
		if n.Labels.Nodes == nil {
			n.Labels.Nodes = []ghLabel{}
		}
		page = append(page, n)
	}
```

In `internal/fakesources/testdata/github/repos/org-repo.json`, add a `"labels"` member to four nodes (keep every other member as it is):

- issue number 1: `"labels": { "nodes": [ { "name": "bug" }, { "name": "Help Wanted" } ] }`
- issue number 2: `"labels": { "nodes": [ { "name": "enhancement" } ] }`
- issue number 3: `"labels": { "nodes": [ { "name": "documentation" }, { "name": "needs-triage" } ] }`
- pull request number 10: `"labels": { "nodes": [ { "name": "in progress" } ] }`

Pull request 11 and the other fixture files get no labels.

- [ ] **Step 6: Run the tests**

Run: `go test -race ./internal/adapters/github/ ./internal/fakesources/ ./internal/domain/ 2>&1 | tail -4`

Expected: all `ok`, including `TestGraphQLRequestBudget` (still 20) and `TestOnlyThePullRequestQueryAsksForIsDraft`.

- [ ] **Step 7: Whole-module check and commit**

Run: `go build ./... && go vet ./... && go test -race -timeout 120s ./...`

```bash
git add internal/domain/item.go internal/adapters/github/issues.go internal/adapters/github/issues_test.go internal/fakesources/fixtures.go internal/fakesources/github_graphql.go internal/fakesources/testdata/github/repos/org-repo.json internal/fakesources/github_graphql_test.go
git commit -m "feat(github,domain,fakes): items carry their labels, fetched as a field on the nodes already asked for (FR-1.10, QS-3.5)

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 2: Label chips and quiet items in the row

Model tier: standard.

**Files:**

- Modify: `internal/domain/item.go` (add `QuietAfter`, `IsQuiet`)
- Modify: `internal/domain/item_test.go` (append a test)
- Modify: `internal/web/dashboard.go` (`itemView`, `newItemView`; add `labelView`, `labelKeys`, `labelOther`, `labelKey`, `labelViews`)
- Modify: `internal/web/templates/fragments/items.html` (lines 20, 24, 35)
- Modify: `internal/web/static/app.css` (append a section)
- Modify: `internal/web/dashboard_test.go` (`representativeItems`; append tests)
- Modify: `internal/web/contrast_test.go` (append a test)

**Interfaces:**

- Consumes: `domain.Item.Labels` (Task 1).
- Produces: `domain.QuietAfter`, `(domain.Item).IsQuiet(now time.Time) bool`; `itemView.Labels []labelView`, `itemView.Quiet bool`; `labelKeys []string`, `labelKey(name string) string`; CSS classes `.label`, `.label-<key>`, `.label-other`, `.is-quiet`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/domain/item_test.go`:

```go
// FR-1.10 AC4: quiet is 90 days without an update, measured to the second; an item whose update
// time is unknown is never quiet — unknown is not idle.
func TestIsQuietAtTheBoundary(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		updated time.Time
		want    bool
	}{
		{"just under 90 days", now.Add(-QuietAfter + time.Second), false},
		{"exactly 90 days", now.Add(-QuietAfter), true},
		{"a year", now.AddDate(-1, 0, 0), true},
		{"unknown", time.Time{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (Item{UpdatedAt: c.updated}).IsQuiet(now); got != c.want {
				t.Errorf("IsQuiet = %v, want %v", got, c.want)
			}
		})
	}
}
```

(`item_test.go` is package `domain` or `domain_test`; match its existing form and, if the latter, qualify `domain.Item` and `domain.QuietAfter`.)

Append to `internal/web/dashboard_test.go`:

```go
// FR-1.10 AC3: labels become chips after the title, in GitHub's order; six names get their own
// class, matched without regard to case and with spaces as hyphens; anything else is "other"; an
// item without labels draws no chip at all.
func TestLabelsRenderAsChipsWithTheFixedPalette(t *testing.T) {
	labelled := ghItem(1, "Labelled", testNow.Add(-time.Hour))
	labelled.Labels = []string{"bug", "Help Wanted", "in progress", "needs-triage", "Documentation"}
	plain := ghItem(2, "Plain", testNow.Add(-time.Hour))
	body := getAuthed(t, dashHandler(t, &fakeSource{items: []domain.Item{labelled, plain}}), "/").Body.String()

	want := `<span class="label label-bug">bug</span>` +
		`<span class="label label-help-wanted">Help Wanted</span>` +
		`<span class="label label-in-progress">in progress</span>` +
		`<span class="label label-other">needs-triage</span>` +
		`<span class="label label-documentation">Documentation</span>`
	if !strings.Contains(strings.Join(strings.Fields(body), ""), strings.Join(strings.Fields(want), "")) {
		t.Errorf("the labelled row does not carry the chips in order; row:\n%s", firstLineContaining(body, "Labelled"))
	}
	if n := strings.Count(body, `class="label `); n != 5 {
		t.Errorf("page has %d chips, want 5: the plain item must draw none", n)
	}
	for _, key := range labelKeys {
		if !strings.Contains(key, "-") && strings.Contains(key, " ") {
			t.Errorf("label key %q carries a space; keys are hyphenated", key)
		}
	}
}

// FR-1.10 AC3: the key is the normalised name when it is one of the six, "other" otherwise.
func TestLabelKeyNormalisesCaseAndSpaces(t *testing.T) {
	cases := map[string]string{
		"bug": "bug", "Bug": "bug", "  enhancement ": "enhancement", "Help Wanted": "help-wanted",
		"help-wanted": "help-wanted", "help   wanted": "help-wanted", "in progress": "in-progress",
		"documentation": "documentation", "question": "question", "content": "other", "": "other",
		"wontfix": "other",
	}
	for name, want := range cases {
		if got := labelKey(name); got != want {
			t.Errorf("labelKey(%q) = %q, want %q", name, got, want)
		}
	}
}

// FR-1.10 AC4: a quiet item is marked as such on the row and says the word in its meta line; a
// recently updated one is neither.
func TestQuietItemsAreDimmedAndSayQuiet(t *testing.T) {
	quiet := ghItem(1, "Old thing", testNow.Add(-100*24*time.Hour))
	live := ghItem(2, "Fresh thing", testNow.Add(-time.Hour))
	body := getAuthed(t, dashHandler(t, &fakeSource{items: []domain.Item{quiet, live}}), "/").Body.String()

	quietRow := openingTag(t, body, `<li class="item is-quiet"`)
	if quietRow == "" {
		t.Fatalf("no row carries is-quiet; the 100-day-old item must:\n%s", firstLineContaining(body, "Old thing"))
	}
	if !strings.Contains(body, "</time>, quiet</p>") {
		t.Error("the quiet item's meta line does not end with \", quiet\"")
	}
	if strings.Count(body, "is-quiet") != 1 || strings.Count(body, ", quiet</p>") != 1 {
		t.Errorf("quiet marks = %d rows / %d words, want exactly 1 each: the fresh item must carry none",
			strings.Count(body, "is-quiet"), strings.Count(body, ", quiet</p>"))
	}
}
```

`openingTag(t, body, prefix)` exists in `dashboard_test.go` (it fatals when the prefix is absent, which is fine here). In `representativeItems()`, give every fourth item labels so QS‑2.3's budget test measures the heavier row: inside the loop, after the item is built and before it is appended, add

```go
			if i%4 == 0 {
				it.Labels = []string{"bug", "help wanted"}
			}
```

(adapt the variable name to the loop's; `it` is illustrative).

Append to `internal/web/contrast_test.go`:

```go
// FR-1.10 AC3: chip text is the label colour on a chip tinted 10 % with the same colour over the
// page; it must reach 4.5:1 in both appearances, and every key the page can emit must have a
// token — a key without one would draw a chip in the inherited colour and nobody would notice.
func TestLabelChipsKeepTextReadable(t *testing.T) {
	raw, err := fs.ReadFile(embedded, "static/app.css")
	if err != nil {
		t.Fatalf("reading the embedded app.css: %v", err)
	}
	css := string(raw)
	bg, ok := lightDarkToken(t, css, "bg")
	if !ok {
		t.Fatal("no --bg token")
	}
	for _, key := range labelKeys {
		if !strings.Contains(css, ".label-"+key+" {") {
			t.Errorf("app.css defines no .label-%s rule", key)
		}
		colour, ok := lightDarkToken(t, css, "label-"+key)
		if !ok {
			continue
		}
		for i, name := range []string{"light", "dark"} {
			chip := mixSRGB(colour[i], bg[i], 0.10)
			if r := contrastRatio(colour[i], chip); r < 4.5 {
				t.Errorf("--label-%s on its chip (%s) = %.2f:1, want at least 4.5:1", key, name, r)
			}
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/domain/ -run TestIsQuietAtTheBoundary 2>&1 | head -3; go test ./internal/web/ -run 'TestLabelsRenderAsChips|TestLabelKeyNormalises|TestQuietItems|TestLabelChipsKeepTextReadable' 2>&1 | head -5`

Expected: compile errors (`QuietAfter`, `IsQuiet`, `labelKeys`, `labelKey` undefined).

- [ ] **Step 3: The domain rule**

Append to `internal/domain/item.go`:

```go
// QuietAfter is how long an item may go without an update before the page calls it quiet
// (FR-1.10 AC4). Three months: long enough that a maintainer's own pause does not trip it, short
// enough that a forgotten issue shows up before the year is out.
const QuietAfter = 90 * 24 * time.Hour

// IsQuiet reports whether nothing has happened to the item for QuietAfter or longer, measured
// from its last update to now. An item whose update time is unknown is never quiet: unknown is
// not idle.
func (i Item) IsQuiet(now time.Time) bool {
	return !i.UpdatedAt.IsZero() && now.Sub(i.UpdatedAt) >= QuietAfter
}
```

- [ ] **Step 4: The view**

In `internal/web/dashboard.go`, add to `itemView` after `New bool`:

```go
	// Labels are the item's chips, in GitHub's order (FR-1.10 AC3).
	Labels []labelView
	// Quiet marks an item nothing has touched for domain.QuietAfter (FR-1.10 AC4).
	Quiet bool
```

Add after the `itemView` type:

```go
// labelView is one chip: the name as GitHub spells it, and the key that picks its colour class.
type labelView struct {
	Name, Key string
}

// labelKeys are the label names that get a colour of their own, as the keys their CSS classes and
// tokens use: label-<key> and --label-<key>. They are the names the arc42 repositories actually
// use, normalised as labelKey normalises them. Any other label is labelOther (FR-1.10 AC3).
var labelKeys = []string{"bug", "enhancement", "documentation", "question", "help-wanted", "in-progress"}

// labelOther is the key of every label outside labelKeys: a neutral chip whose name does the work.
const labelOther = "other"

// labelKey normalises a label name — lowercase, trimmed, runs of whitespace as one hyphen — and
// returns it when it is one of labelKeys, labelOther otherwise. "Help Wanted", "help wanted" and
// "help-wanted" all land on the same chip, which is the point: the same label is spelled three
// ways across the arc42 repositories.
func labelKey(name string) string {
	key := strings.Join(strings.Fields(strings.ToLower(name)), "-")
	if slices.Contains(labelKeys, key) {
		return key
	}
	return labelOther
}

// labelViews turns label names into chips, keeping GitHub's order; nil for none.
func labelViews(names []string) []labelView {
	if len(names) == 0 {
		return nil
	}
	out := make([]labelView, 0, len(names))
	for _, name := range names {
		out = append(out, labelView{Name: name, Key: labelKey(name)})
	}
	return out
}
```

(add `"slices"` to the imports if it is not there.) In `newItemView`, add `Labels: labelViews(it.Labels),` and `Quiet: it.IsQuiet(now),` to the literal.

- [ ] **Step 5: The template and the stylesheet**

In `internal/web/templates/fragments/items.html`:

- line 20: `<li class="item{{if .New}} is-new{{end}}">` becomes `<li class="item{{if .New}} is-new{{end}}{{if .Quiet}} is-quiet{{end}}">`
- line 24: after the `<a class="item-title" …>{{.Title}}</a>` line, add on its own line:
  `{{range .Labels}}<span class="label label-{{.Key}}">{{.Name}}</span>{{end}}`
  with this comment above it:
  `{{/* The item's labels as chips (FR-1.10 AC3). Key picks the colour class; the six names the arc42 repositories use get a colour of their own, everything else is a plain chip. */}}`
- line 35: `{{if .Updated.Known}}, updated <time datetime="{{.Updated.Absolute}}">{{.Updated.Relative}}</time>{{end}}` becomes `{{if .Updated.Known}}, updated <time datetime="{{.Updated.Absolute}}">{{.Updated.Relative}}</time>{{end}}{{if .Quiet}}, quiet{{end}}`

Append to `internal/web/static/app.css`:

```css
/* ---------------------------------------------------------------- labels and quiet items (FR-1.10)

   Labels as chips. Six names the arc42 repositories actually use get a colour of their own;
   anything else is a plain outlined chip whose name does the work. The palette is fixed here
   rather than taken from GitHub: the same name carries different colours in different arc42
   repositories, and a colour can only reach the page as a class (QS-4.4). Each colour is chosen so
   the chip's text reaches 4.5:1 on its own tinted background in both appearances — the contrast
   test measures exactly that. */
:root {
  --label-bug: light-dark(#b3261e, #f2836f);
  --label-enhancement: light-dark(#0e4f80, #7fb2e0);
  --label-documentation: light-dark(#1b5648, #5fb49c);
  --label-question: light-dark(#682d63, #c98ac1);
  --label-help-wanted: light-dark(#186633, #6bc98a);
  --label-in-progress: light-dark(#8a5300, #e0a03c);
}

.label {
  padding: 0 0.4rem;
  border: 1px solid var(--border);
  border-radius: 999px;
  font-size: 0.72rem;
  font-weight: 500;
  color: var(--muted);
  white-space: nowrap;
}

.label-bug { --label: var(--label-bug); }
.label-enhancement { --label: var(--label-enhancement); }
.label-documentation { --label: var(--label-documentation); }
.label-question { --label: var(--label-question); }
.label-help-wanted { --label: var(--label-help-wanted); }
.label-in-progress { --label: var(--label-in-progress); }

/* Only the chips with a colour of their own are tinted; label-other keeps the plain outline. */
.label-bug, .label-enhancement, .label-documentation, .label-question, .label-help-wanted, .label-in-progress {
  color: var(--label);
  border-color: var(--label);
  background: color-mix(in srgb, var(--label) 10%, transparent);
}

/* Quiet: nothing has happened here for three months (FR-1.10 AC4). The title dims, and the meta
   line says the word, so the state is readable without the colour. */
.is-quiet .item-title { color: var(--muted); }
```

- [ ] **Step 6: Run the tests**

Run: `go test -race ./internal/domain/ ./internal/web/ 2>&1 | tail -3`

Expected: `ok` for both, `TestRenderedPageStaysInsideItsBudget` and `TestNoRenderedHTMLNeedsUnsafeInline` included. If `TestLabelChipsKeepTextReadable` reports a ratio under 4.5:1, darken the light value or lighten the dark value of that one token until it passes and say so in the report; never change a `--hue-*` token.

- [ ] **Step 7: Whole-module check and commit**

Run: `go build ./... && go vet ./... && go test -race -timeout 120s ./...`

```bash
git add internal/domain/item.go internal/domain/item_test.go internal/web/dashboard.go internal/web/templates/fragments/items.html internal/web/static/app.css internal/web/dashboard_test.go internal/web/contrast_test.go
git commit -m "feat(web,domain): labels as chips with a fixed six-name palette, and quiet items dimmed and worded after 90 days (FR-1.10)

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 3: The rainbow band and the site stripe on each group

Model tier: cheap (the code is complete below).

**Files:**

- Modify: `internal/web/templates/layout.html` (one element after `</header>`)
- Modify: `internal/web/static/app.css` (`.repo-group` rule; append `.rainbow`)
- Modify: `internal/web/dashboard.go` (`groupView`, `itemsView`)
- Modify: `internal/web/sites.go` (`siteSpecs` uses the new helper; add `hueForRepo`)
- Modify: `internal/web/templates/fragments/items.html` (line 12)
- Modify: `internal/web/dashboard_test.go` (append tests)
- Modify: `internal/web/contrast_test.go` (append a test)

**Interfaces:**

- Consumes: `tileHue(key string) string` and `config.GitHub.Sites` (exist in `sites.go`); CSS `.hue-<key>` classes setting `--tile-hue` and `--tile-sig`.
- Produces: `hueForRepo(gh config.GitHub, repo string) string`; `groupView.Hue string`; CSS `.rainbow`; `.repo-group` carries `hue-<key>`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/web/dashboard_test.go`:

```go
// FR-1.10 AC1: the arc42 rainbow band sits on every page — the list, the Sites view, the sign-in
// page and the wait page — as one element the stylesheet paints; it is never the only thing that
// tells pages apart, so it is hidden from assistive technology.
func TestEveryPageCarriesTheRainbowBand(t *testing.T) {
	const band = `<div class="rainbow" aria-hidden="true"></div>`
	h := dashHandler(t, &fakeSource{items: representativeItems()})
	c := signIn(t, h)
	pages := map[string]string{
		"GET /":      getAs(h, "/", c).Body.String(),
		"GET /sites": getAs(h, "/sites", c).Body.String(),
		"GET /login": get(h, "/login").Body.String(),
	}
	wh, release := coldServer(t)
	defer close(release)
	pages["GET / (waiting)"] = getAs(wh, "/", signIn(t, wh)).Body.String()

	for name, body := range pages {
		if strings.Count(body, band) != 1 {
			t.Errorf("%s carries the band %d times, want exactly once", name, strings.Count(body, band))
		}
	}
	if strings.Contains(getAs(h, "/items", c).Body.String(), band) {
		t.Error("the list fragment carries the band; it belongs to the layout, not the list")
	}
}

// FR-1.10 AC2: a group carries the hue of the site that claims its repository, slate when none
// does — the same rule the Other tile follows.
func TestGroupsCarryTheirSitesHue(t *testing.T) {
	claimed := ghItem(1, "Claimed", testNow.Add(-time.Hour))
	claimed.Repo = "org/claimed"
	unclaimed := ghItem(2, "Unclaimed", testNow.Add(-time.Hour))
	unclaimed.Repo = "org/unclaimed"
	src := &fakeSource{items: []domain.Item{claimed, unclaimed}}
	h := newTestServerWith(t, func(o *Options) {
		o.Config.GitHub.Repos = []string{"org/claimed", "org/unclaimed"}
		o.Config.GitHub.Sites = []config.Site{{Name: "claimed.example", URL: "https://claimed.example", Repo: "org/claimed", Hue: "plum"}}
		o.Cache = snapshot.New(src, time.Hour, o.Clock)
	}).Handler()
	body := getAuthed(t, h, "/").Body.String()

	if !strings.Contains(body, `<section class="repo-group hue-plum">`) {
		t.Errorf("the claimed group does not carry hue-plum:\n%s", firstLineContaining(body, "repo-group"))
	}
	if !strings.Contains(body, `<section class="repo-group hue-slate">`) {
		t.Error("the unclaimed group does not carry hue-slate")
	}
	if strings.Contains(body, `style="`) {
		t.Error("a colour reached the page as a style attribute (QS-4.4)")
	}
}
```

(`config` and `snapshot` are imported in `dashboard_test.go` already; check with `go vet`.) Append to `internal/web/contrast_test.go`:

```go
// FR-1.10 AC2: every site's stripe is visible against the page in both appearances — the hue in
// light, the signal colour lightened 30 % toward white in dark, at least 3:1 (the bar for a
// non-text element). Navy and plum as drawn on a tile are nearly invisible as a 4 px line on the
// dark page; the lightening is what this test guards. When a stripe fails, raise the mix
// percentage in app.css and here together; never change a brand colour.
func TestGroupStripesAreVisibleInBothAppearances(t *testing.T) {
	raw, err := fs.ReadFile(embedded, "static/app.css")
	if err != nil {
		t.Fatalf("reading the embedded app.css: %v", err)
	}
	css := string(raw)
	if !strings.Contains(css, "color-mix(in srgb, var(--tile-sig, var(--hue-slate-sig)) 70%, #ffffff)") {
		t.Fatal("the dark stripe is not the signal colour mixed 30 % toward white; this test measures that mix")
	}
	bg, ok := lightDarkToken(t, css, "bg")
	if !ok {
		t.Fatal("no --bg token")
	}
	white := rgb{255, 255, 255}
	for _, key := range config.HueKeys {
		hue, ok := hexToken(t, css, "hue-"+key)
		if !ok {
			continue
		}
		sig, ok := hexToken(t, css, "hue-"+key+"-sig")
		if !ok {
			continue
		}
		if r := contrastRatio(hue, bg[0]); r < 3 {
			t.Errorf("the %s stripe on the light page = %.2f:1, want at least 3:1", key, r)
		}
		if r := contrastRatio(mixSRGB(sig, white, 0.70), bg[1]); r < 3 {
			t.Errorf("the %s stripe on the dark page = %.2f:1, want at least 3:1", key, r)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/web/ -run 'TestEveryPageCarriesTheRainbowBand|TestGroupsCarryTheirSitesHue|TestGroupStripesAreVisibleInBothAppearances' 2>&1 | grep -E '^(---|FAIL|ok)'`

Expected: all three FAIL (no band, no hue class, no mix in the stylesheet).

- [ ] **Step 3: The band**

In `internal/web/templates/layout.html`, directly after the `</header>` that closes `<header class="topbar">`, add:

```html
{{/* The arc42 family's rainbow, as status.arc42.org draws it, on every page (FR-1.10 AC1). One
     element the stylesheet paints; hidden from assistive technology because it says nothing a
     reader needs. */}}
<div class="rainbow" aria-hidden="true"></div>
```

Append to `internal/web/static/app.css`:

```css
/* ---------------------------------------------------------------- the rainbow band (FR-1.10)

   The arc42 family's rainbow, as status.arc42.org draws it ($arc42-rainbow in its
   docs/_sass/_arc42-family.scss). It is the brand, not the theme, so it is the same in both
   appearances; it is written out as the status site writes it rather than assembled from the
   tile tokens, so the two stay identical by inspection. */
.rainbow {
  height: 4px;
  background: linear-gradient(90deg, #c22b47, #ffc95c, #2e9e67, #5fb49c, #1675b9, #374769, #682d63);
}
```

- [ ] **Step 4: The stripe**

In `internal/web/sites.go`, add after `tileHue`:

```go
// hueForRepo is the colour key of the site that claims repo, slate when no site does — the rule
// the Other tile follows, now shared with the list's groups (FR-1.10 AC2).
func hueForRepo(gh config.GitHub, repo string) string {
	for _, site := range gh.Sites {
		if site.Repo == repo {
			return tileHue(site.Hue)
		}
	}
	return "slate"
}
```

In `internal/web/dashboard.go`, add to `groupView` after `Repo string`:

```go
	// Hue is the site's colour key, drawn as the group's stripe (FR-1.10 AC2).
	Hue string
```

and in `itemsView`, add `Hue: hueForRepo(s.cfg.GitHub, g.Repo),` to the `groupView` literal.

In `internal/web/templates/fragments/items.html`, line 12: `<section class="repo-group">` becomes `<section class="repo-group hue-{{.Hue}}">`, with this comment above it:

```html
  {{/* The site's colour as a stripe down the group's edge (FR-1.10 AC2): the same hue-<key>
       class a tile carries, drawn here as a border rather than a band. */}}
```

In `internal/web/static/app.css`, replace the rule `.repo-group { margin-top: 1.5rem; }` and the comment above it with:

```css
/* One repository, one block. No card: the groups are read in sequence down a single column, and
   a border around each would turn a list into a stack of boxes to look past. The space above is
   what separates them; the site's colour is a stripe down the left edge (FR-1.10 AC2). The name
   stays in the page's text colour — the stripe is the colour's whole job here, and eight groups in
   eight coloured bands would be a poster, not a list. In the dark appearance the signal colour is
   lightened, because navy and plum as drawn on a tile are nearly invisible as a thin line on the
   dark page; the contrast test holds every stripe at 3:1 against the page in both appearances. */
.repo-group {
  margin-top: 1.5rem;
  padding-left: 0.75rem;
  border-left: 4px solid light-dark(var(--tile-hue, var(--hue-slate)), color-mix(in srgb, var(--tile-sig, var(--hue-slate-sig)) 70%, #ffffff));
}
```

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/web/ 2>&1 | tail -3`

Expected: `ok`. `TestNoRenderedHTMLNeedsUnsafeInline`, `TestStaticAssetsFitTheirBudgetOnTheWire` and the sites tests stay green (the sites view already carries `hue-` classes; nothing about the tiles changed).

- [ ] **Step 6: Whole-module check and commit**

Run: `go build ./... && go vet ./... && go test -race -timeout 120s ./...`

```bash
git add internal/web/templates/layout.html internal/web/static/app.css internal/web/dashboard.go internal/web/sites.go internal/web/templates/fragments/items.html internal/web/dashboard_test.go internal/web/contrast_test.go
git commit -m "feat(web): the arc42 rainbow band on every page and each list group striped in its site's colour (FR-1.10)

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 4: Requirements, glossary, concept page, and the gate

Model tier: cheap (every text is given below).

**Files:**

- Modify: `docs/requirements/04-functional-requirements.md` (E‑1 table: a row after FR‑1.9)
- Modify: `docs/requirements/06-glossary.md` (two rows after **Wait page**)
- Modify: `docs/concepts/configuration.md` (one sentence in the `github.sites` paragraph, around line 70–82)

- [ ] **Step 1: The requirement**

In `docs/requirements/04-functional-requirements.md`, insert after the FR‑1.9 row, one line (copy the non-breaking hyphen from the neighbouring ids):

```markdown
| FR‑1.10 | M | As the user I see the family's colours and each item's labels on the list, and I see what has gone quiet. | AC1 Every page carries the arc42 rainbow band under the top bar, the same in both appearances. AC2 Each repository group on the list carries a stripe in its site's colour, slate for a repository no site claims; the repository name stays in the page's text colour; every stripe keeps 3:1 against the page in both appearances. AC3 An item's labels are shown as chips after its title, in GitHub's order; bug, enhancement, documentation, question, help wanted and in progress — matched case-insensitively, spaces and hyphens alike — each have a fixed colour, any other label is a neutral chip; chip text keeps 4.5:1 against the chip in both appearances; a colour never reaches the page except as a class. AC4 An item not updated for 90 days is quiet: its title is dimmed and its meta line says "quiet"; the order of the list does not change. |
```

- [ ] **Step 2: The glossary**

In `docs/requirements/06-glossary.md`, after the **Wait page** row, add:

```markdown
| **Label chip** | A label of an item, drawn after its title on the list. Six names — bug, enhancement, documentation, question, help wanted, in progress — carry a fixed colour; every other label is a neutral chip. |
| **Quiet item** | An item not updated for 90 days: its title is dimmed and its meta line says "quiet". It keeps its place in the order. |
```

- [ ] **Step 3: The concept page**

In `docs/concepts/configuration.md`, at the end of the paragraph that begins "`github.sites` is what the Sites view draws", add the sentence: "The list borrows the same colours: each repository group is striped in the hue of the site that claims it, slate when none does (FR‑1.10). The label palette is not configured; it is fixed in the stylesheet." Re-wrap the paragraph at the file's width.

- [ ] **Step 4: The gate**

Run `make check` if Docker is up; otherwise the native equivalent from Global Constraints. Expected: everything green, markdownlint `0 issues`.

- [ ] **Step 5: Commit**

```bash
git add docs/requirements/04-functional-requirements.md docs/requirements/06-glossary.md docs/concepts/configuration.md
git commit -m "docs: FR-1.10 the list's colours, labels and quiet items; glossary and configuration concept (FR-1.10)

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 5: Look at it (Gernot)

Not a subagent task. `make backend` (or `make fakes` for the offline fixture, which now carries every chip kind), open the list in both appearances: the band under the top bar, a stripe per group, chips after titles, a quiet item dimmed. Then merge order: `feat/fast-first-view` first, then `feat/list-sugar`.
