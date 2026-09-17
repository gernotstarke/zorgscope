# List sugar — design

Date: 2026-09-17. Status: approved by Gernot in conversation (what to add, the stripe over the band,
the fixed label palette, quiet items — his choices). Builds on the per-site tiles
(`2026-09-15-per-site-tiles-design.md`) and the fast first view (`2026-09-16-fast-first-view-design.md`)
and changes none of their decisions: no database, no ticker, colour only as CSS classes, the
20-request budget of QS‑3.5 untouched.

## 1. Goal

The Sites view has the arc42 colours; the list, the page looked at most, has none. This design gives
the list the family's colours and a little more information per item without a new page, a new
configuration key or a new request to GitHub: the status site's rainbow band under the top bar, each
repository group striped in its site's colour, the items' labels as chips, and items nobody has
touched for three months marked quiet.

## 2. Decisions (Gernot, 2026-09-17)

| Question | Decision |
|---|---|
| What first | The list's visual sugar (this design); a "Needs me" view — issues needing a reply, pull requests waiting for review, unreleased work, quiet issues — is a later spec of its own. |
| Site colour on a group | An accent stripe: a coloured left border, the repository name in the page's text colour. Not a band like the tiles, not a wash. |
| Label colour | A fixed palette by name — bug, enhancement, documentation, question, help wanted, in progress — every other name a neutral chip. Not GitHub's own colours (they differ between the arc42 repositories for the same name, and the Content-Security-Policy forbids painting them inline anyway). |
| Freshness | Only "quiet": an item not updated for 90 days is dimmed and says so. No "fresh within 24 hours" cue — NEW and "updated 3 hours ago" already carry that. |
| Delivery | Branch `feat/list-sugar` off `feat/fast-first-view` at `82a8695`; it merges after that branch. |

## 3. The rainbow band

One element directly under the top bar in `layout.html`, on every page, the sign-in page included —
as on status.arc42.org:

```html
<div class="rainbow" aria-hidden="true"></div>
```

```css
/* The arc42 family's rainbow, as status.arc42.org draws it (docs/_sass/_arc42-family.scss). It is
   the brand, not the theme, so it is the same in both appearances. */
.rainbow {
  height: 4px;
  background: linear-gradient(90deg, #c22b47, #ffc95c, #2e9e67, #5fb49c, #1675b9, #374769, #682d63);
}
```

Four of the seven stops are already the tile palette's `--hue-teal-sig`, `--hue-blue-sig`,
`--hue-navy-sig` and `--hue-plum`; the gradient is written out as the status site writes it rather
than assembled from tokens, so the two stay identical by inspection.

## 4. The site stripe on a repository group

`groupView` gains `Hue string`, resolved in `itemsView` from the configured sites: the hue of the
site whose `repo` is the group's repository, `slate` for a repository no site claims — the same rule
`siteSpecs` applies for the Other tile, expressed once as a small helper `hueForRepo(gh config.GitHub, repo string) string`
that both `sites.go` and `dashboard.go` use.

```html
<section class="repo-group hue-{{.Hue}}">
```

```css
/* The site's colour as a stripe down the group's left edge (FR-1.10 AC2). The name stays in the
   page's text colour: the stripe is the colour's whole job here, and a list of eight groups in
   eight coloured bands would be a poster, not a list. In the dark appearance the signal colour is
   lightened, because navy and plum as drawn on a tile are nearly invisible as a thin line on the
   dark page; the contrast test holds every stripe at 3:1 against the page in both appearances. */
.repo-group {
  margin-top: 1.5rem;
  padding-left: 0.75rem;
  border-left: 4px solid light-dark(var(--tile-hue, var(--hue-slate)), color-mix(in srgb, var(--tile-sig, var(--hue-slate-sig)) 70%, #ffffff));
}
```

`--tile-hue` and `--tile-sig` are what the existing `.hue-<key>` classes set, so no new token is
needed. Nothing else about the heading changes: name, NEW count and count line stay as they are.

## 5. Labels as chips

### 5.1 Fetching

Both node types in `internal/adapters/github/issues.go` gain

```go
Labels struct {
	Nodes []struct{ Name githubv4.String }
} `graphql:"labels(first: 5)"`
```

Five covers every arc42 item today (the most labelled has three). The connection is a field on nodes
the queries already fetch, so the request count — what QS‑3.5 measures — does not change;
`TestGraphQLRequestBudget` keeps counting 20. `toItem` copies the names, in GitHub's order, into
`domain.Item.Labels []string`; an item with no labels has a nil slice.

The fake GitHub (`internal/fakesources`) adds `Labels []ghLabel` with `Name` to its node type,
serialised as GitHub does (`labels: {nodes: [{name: …}]}`), and the fixtures for `org/repo` carry a
few: `bug`, `enhancement`, `documentation`, `Help Wanted` (mixed case, on purpose) and one name
outside the palette, so `make fakes` shows every chip kind.

### 5.2 Rendering

The row shows the chips after the title, on the same line:

```html
<a class="item-title" …>{{.Title}}</a>
{{range .Labels}}<span class="label label-{{.Key}}">{{.Name}}</span>{{end}}
```

`itemView` gains `Labels []labelView` with `Name` (as GitHub spells it) and `Key`. `labelKey(name)`
lowercases the name, trims it, and replaces runs of whitespace with one hyphen; when the result is one
of `bug`, `enhancement`, `documentation`, `question`, `help-wanted`, `in-progress` it is the key,
otherwise the key is `other`. The six keys are a fixed slice in `dashboard.go`, `labelKeys`, which
the tests and the contrast test both read.

```css
/* Labels as chips (FR-1.10 AC3). Six names the arc42 repositories actually use get a colour of
   their own; anything else is a plain outlined chip whose name does the work. The palette is
   fixed here rather than taken from GitHub: the same name carries different colours in different
   arc42 repositories, and a colour can only reach the page as a class (QS-4.4). */
:root {
  --label-bug: light-dark(#b3261e, #f2836f);
  --label-enhancement: light-dark(#0e4f80, #7fb2e0);
  --label-documentation: light-dark(#1b5648, #5fb49c);
  --label-question: light-dark(#682d63, #c98ac1);
  --label-help-wanted: light-dark(#1f7a3d, #6bc98a);
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
.label[class*="label-"]:not(.label-other) {
  color: var(--label);
  border-color: var(--label);
  background: color-mix(in srgb, var(--label) 10%, transparent);
}
```

The six colours are the page's existing semantic tones (`--danger`, `--accent`'s family, `--ok`,
`--warn`) and the tile palette's teal and plum, chosen so the chip text keeps 4.5:1 against its own
tinted background in both appearances; the contrast test measures exactly that, mixing each label
colour at 10 % over `--bg` and checking the label colour on the result.

## 6. Quiet items

An item whose `UpdatedAt` is 90 days or more before now is quiet. The rule is one function in the
domain, `Item.IsQuiet(now time.Time) bool`, with the threshold `QuietAfter = 90 * 24 * time.Hour`
beside it; `newItemView` sets `itemView.Quiet`. The row:

```html
<li class="item{{if .New}} is-new{{end}}{{if .Quiet}} is-quiet{{end}}">
…
<p class="item-meta">… updated <time …>{{.Updated.Relative}}</time>{{if .Quiet}}, quiet{{end}}</p>
```

```css
/* Quiet: nothing has happened here for three months (FR-1.10 AC4). The title dims, and the meta
   line says the word, so the state is readable without the colour. */
.is-quiet .item-title { color: var(--muted); }
```

A quiet item that is also NEW is impossible (NEW means created after the seen mark, which is never
90 days old on a page anyone looks at) and needs no rule. Quiet does not sort: the order stays NEW
first, then most recently updated, which already puts quiet items last.

## 7. Requirements and documentation

- `docs/requirements/04-functional-requirements.md`, E‑1: **FR‑1.10** (M) "As the user I see the
  family's colours and each item's labels on the list, and I see what has gone quiet."
  AC1 Every page carries the arc42 rainbow band under the top bar, the same in both appearances.
  AC2 Each repository group on the list carries a stripe in its site's colour, slate for a repository
  no site claims; the repository name stays in the page's text colour; every stripe keeps 3:1
  against the page in both appearances. AC3 An item's labels are shown as chips after its title, in
  GitHub's order; bug, enhancement, documentation, question, help wanted and in progress — matched
  case-insensitively, spaces and hyphens alike — each have a fixed colour, any other label is a
  neutral chip; chip text keeps 4.5:1 against the chip in both appearances; a colour never reaches
  the page except as a class. AC4 An item not updated for 90 days is quiet: its title is dimmed and
  its meta line says "quiet"; the order of the list does not change.
- `docs/requirements/05-quality-requirements.md`: QS‑2.3 unchanged in text; the representative
  fixture gains labels so the budget test measures the heavier row. QS‑3.5 unchanged: labels are a
  field, not a request, and the test still counts 20.
- `docs/requirements/06-glossary.md`: **Label chip** — a label of an item, drawn after its title;
  six names carry a fixed colour, the rest are neutral. **Quiet item** — an item not updated for 90
  days, dimmed and worded as such.
- `docs/concepts/configuration.md`: one sentence that the list's group colours come from
  `github.sites` like the tiles', and that the label palette is fixed in the stylesheet.
- No ADR: nothing here decides architecture.

## 8. Testing

Test-first, under the race detector as `make check` runs them.

- **Adapter**: `TestFetchCarriesLabelsInGitHubsOrder` (fixture labels arrive as names, in order);
  `TestGraphQLRequestBudget` unchanged and still 20.
- **Fake sources**: the fixture round-trips labels; a node without labels serialises an empty
  connection, not a missing key (the client's decoder is strict about keys it asks for).
- **Domain**: `TestIsQuietAtTheBoundary` (89 days 23 h is not quiet, exactly 90 days is);
  `Labels` survive `SortItems` and grouping (a copy-through test in `dashboard_test.go` of the domain).
- **Web**: `TestEveryPageCarriesTheRainbowBand` (`/`, `/sites`, `/login`, and the wait page);
  `TestGroupsCarryTheirSitesHue` (a configured site → its key; an unclaimed repository → `slate`);
  `TestLabelsRenderAsChipsWithTheFixedPalette` (six keys, `Help Wanted` → `label-help-wanted`,
  an unknown name → `label-other`, GitHub's order kept, no chip when there are none);
  `TestQuietItemsAreDimmedAndSayQuiet`; `TestNoRenderedHTMLNeedsUnsafeInline` unchanged and green;
  `TestRenderedPageStaysInsideItsBudget` with the labelled fixture.
- **Contrast** (`contrast_test.go`): `TestGroupStripesAreVisibleInBothAppearances` — every hue
  against `--bg` in light, every sig lightened 30 % toward white against `--bg` in dark, ≥ 3:1;
  `TestLabelChipsKeepTextReadable` — each label colour against its own chip background (`--bg`
  with 10 % of the label colour mixed in), ≥ 4.5:1 in both appearances, and every key in `labelKeys`
  has a token.

## 9. Delivery

Branch `feat/list-sugar` off `feat/fast-first-view` at `82a8695`; merges after it. Commits cite
FR‑1.10 and, for the adapter, QS‑3.5. Suggested order: adapter and fakes (labels); domain (labels,
quiet); the band and the stripe; the chips and quiet in the row; documents. `make check` green at
the end, or its native equivalent while Docker is down.

## 10. Out of scope, deliberately

- Label filtering in the search box: FR‑2.1 names title and summary; widening it is its own decision.
- GitHub's own label colours; a "fresh within 24 hours" cue; label chips on the tiles (a tile is
  for a glance and already has its colour).
- The "Needs me" view: issues needing a reply, pull requests waiting for review, unreleased work —
  the next spec.
