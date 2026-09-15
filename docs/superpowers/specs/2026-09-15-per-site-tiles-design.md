# Per-site tiles — design

Date: 2026-09-15. Status: approved by Gernot in conversation, section by section (site model,
view switch, overflow, filters, site configuration, colour, tint, tests and delivery are his
choices). Builds on the stateless reset (`2026-09-15-stateless-reset-design.md`) and changes none of
its decisions.

## 1. Goal

A second view of the same data: the open pull requests and issues of each arc42 site, one tile per
site, coloured from the arc42 brand registry. The existing grouped list stays the place to find
something; the new view is the place to see, at a glance, what is going on per site.

## 2. Decisions (Gernot, 2026-09-15)

| Question | Decision |
|---|---|
| What is a site | Seven tiles: arc42.org, arc42.de, quality.arc42.org, docs.arc42.org, faq.arc42.org, examples.arc42.org, trainings.arc42.org. The trainings repository is added to the watched list. Watched repositories no site claims share one "Other" tile. |
| Telling the views apart | A two-part switch **List \| Sites** under the header, as two real links (`/` and `/sites`). |
| A tile with more than it shows | A link to the list filtered to that repository. No expand-in-place, no extra detail page. |
| Filters on the Sites view | None. The Sites view is an unfiltered overview; the list keeps every filter. |
| Where sites are defined | A `github.sites` list in `config/zorgscope.yaml`. |
| Colour | The brand registry's masthead colour as the tile's heading band, and a wash of the site's colour over the tile body in both appearances. |
| Tests | Test-first, including a contrast test over every colour in both appearances. |
| Delivery | Branch `feat/per-site-tiles` off the merged reset (`main` at `1c8eb70`). |

## 3. Configuration

`config/zorgscope.yaml` gains the trainings repository and an optional `sites` list:

```yaml
github:
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
  sites:                                  # tile order is list order
    - name: arc42.org
      url: https://arc42.org
      repo: arc42/arc42.org-site
      hue: navy
    - name: arc42.de
      url: https://arc42.de
      repo: arc42/arc42.de-site
      hue: navy
      tag: DE                             # arc42.de shares the hub navy; the tag tells them apart
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

Nine repositories cost 18 GraphQL queries per fetch, inside QS‑3.5's budget of 20; the list may
hold one more repository after this.

`config.Site{Name, URL, Repo, Hue, Tag string}` lives in `GitHub.Sites`. Loading refuses to start,
with an error naming the field (for example `github.sites[2].hue`), when:

- `name` is empty, or two sites share a name;
- `url` does not parse as an absolute `https` URL with a host;
- `repo` is not in owner/name form, is not listed in `github.repos`, or is claimed by a second site;
- `hue` is not one of `navy`, `blue`, `plum`, `teal`, `umber`, `rose`, `slate`;
- `tag` is longer than three characters.

`sites` may be absent. Then the Sites view shows the single "Other" tile holding every watched
repository, and nothing else needs a special case.

The palette keys are fixed in code and CSS, because the Content-Security-Policy forbids inline
styles: a colour can only reach the page as a class the stylesheet defines. Adding a hue means a
CSS token and a palette key; choosing among the existing ones is a YAML edit.

## 4. Domain

In `internal/domain`, standard library only, pure:

```go
type SiteSpec struct {
	Name, URL, Hue, Tag string
	Repos               []string // one for a configured site; every unclaimed repository for Other
}

type SiteTilesInput struct {
	LastVisitAt       time.Time
	Items             []Item
	Sites             []SiteSpec // Other, when present, is the last entry
	MaxPRs, MaxIssues int
}

type RepoCount struct {
	Repo        string
	PRs, Issues int
}

type SiteTile struct {
	Spec               SiteSpec
	PRs, Issues        []Item      // sorted, cut to MaxPRs and MaxIssues
	PRTotal, IssueTotal int        // before the cut
	NewCount           int         // before the cut
	Counts             []RepoCount // per repository of the spec, in spec order
	More               bool        // PRTotal > len(PRs) || IssueTotal > len(Issues)
}

func BuildSiteTiles(in SiteTilesInput) []SiteTile
```

- One tile per spec, in spec order. A spec with no open items still produces a tile: the Sites view
  is a fixed map of the family, and a tile that disappeared would read as a site that had gone.
  This differs deliberately from the list, which omits a repository with no items (FR‑1.1 AC1).
- Items are copied before sorting with `SortItems` (NEW first, then most recently updated), so the
  snapshot's shared slice is never modified.
- Totals, per-repository counts and `NewCount` are taken before the cut, so a tile never understates
  what is open, the rule the list already follows.
- Items of a repository no spec names are ignored.

The web layer builds the specs: one per configured site, then Other with every watched repository
no site claims, in `github.repos` order, with hue `slate`, no URL and no tag. Other is omitted when
every watched repository is claimed.

## 5. Web

### 5.1 Route

`GET /sites` → `handleSites`, guarded `authSessionPage` (an anonymous visitor is redirected to
`/login`), with `/sites` as its probe path so the every-protected-route test covers it. It reads the
same snapshot and the same session as the list, so NEW means exactly the same on both views. The
caps are constants in `internal/web`: `tileMaxPRs = 3`, `tileMaxIssues = 4`. Page title: "Sites".

### 5.2 Shared header

The fetched-at line, the error notice, the NEW badge and the three action forms move out of
`dashboard.html` into a `header` template both pages render. The list's behaviour does not change.

"Mark all seen" and "Refresh" each gain a hidden `return` field carrying the current page's path
(with its query on the list). `handleSeen` and `handleRefresh` redirect to
`safeReturn(r.FormValue("return"))`, the sanitiser the theme switch already uses: it accepts a path
of this site and falls back to `/` for anything else, including an absent field. Pressed on the
Sites view, both buttons therefore return to the Sites view. "Log out" still goes to `/login`.

### 5.3 View switch

A `viewswitch` template under the header on both pages:

```html
<nav class="view-switch" aria-label="View">
  <a href="/" aria-current="page">List</a>
  <a href="/sites">Sites</a>
</nav>
```

`aria-current="page"` sits on the view being shown. "List" links to the unfiltered list.

### 5.4 Tiles

`sites.html`, a grid of tiles:

- `<section class="tile hue-{key}" aria-labelledby="tile-{n}-title">`, where `{n}` is the tile's
  position and the heading carries `id="tile-{n}-title"`. The heading band holds the site name
  (a link to the site's URL; plain text for Other), the tag when set, "n new" when greater than
  zero, and "4 PRs · 11 issues".
- Two short lists, **Pull requests** and **Issues**. A row shows the NEW badge, `#number`, the title
  as a link to GitHub (`target="_blank" rel="noopener noreferrer"`), and "updated 3 days ago",
  guarded by `Known` exactly as the list guards its timestamps. Rows carry no summary and no author.
- An empty list reads "no open pull requests" or "no open issues".
- When `More` is set, the footer carries one link per entry in `Counts`:
  "all 4 PRs · 11 issues →" to `/?repo={repo}`, with the site name added for screen readers. On
  Other each link also names its repository.
- CSS grid, `repeat(auto-fill, minmax(20rem, 1fr))`: one column on a phone, two or three on a desktop.
- No htmx on this page.

## 6. Colour and accessibility

### 6.1 Tokens

Two tokens per palette key in `app.css`, taken from the brand registry
(`arc42/meta.arc42.org`, `wiki/concepts/brand.md`): the masthead band, and the site's signature hue
as the source of the dark-appearance wash.

| Key | Used by | Band `--hue-{key}` | Signature `--hue-{key}-sig` |
|---|---|---|---|
| `navy` | arc42.org, arc42.de | `#2b3a57` | `#374769` |
| `blue` | docs.arc42.org | `#0e4f80` | `#1675b9` |
| `plum` | quality.arc42.org | `#682d63` | `#682d63` |
| `teal` | faq.arc42.org | `#1b5648` | `#5fb49c` |
| `umber` | examples.arc42.org | `#3a332b` | `#3a332b` |
| `rose` | trainings.arc42.org | `#a04c5e` | `#a04c5e` |
| `slate` | Other | `#414a56` | `#6f777d` |

The values are plain, not `light-dark()` pairs: a site's colour is its identity and stays the same
in both appearances. examples owns no hue in the registry, only a ground; its ground serves as both.

### 6.2 Tile styling

- `.hue-{key}` sets `--tile-hue` and `--tile-sig` from the tokens.
- The heading band: `background: var(--tile-hue); color: #fff`.
- The tile body: `background: var(--tile-wash)` with
  `--tile-wash: light-dark(color-mix(in srgb, var(--tile-hue) var(--tile-wash-light), var(--surface)), color-mix(in srgb, var(--tile-sig) var(--tile-wash-dark), var(--surface)))`,
  starting at `--tile-wash-light: 7%` and `--tile-wash-dark: 12%`.
- The wash is declared only inside an `@supports` block requiring both `light-dark()` and
  `color-mix()`. Everywhere else the tile body is the plain `--surface`.
- Each tile has a 1px `--border`, so a dark band stays distinct from the dark page.
- The NEW badge keeps its solid fill. The view switch is app chrome and uses `--accent`, not a site
  colour.

### 6.3 Accessibility

Colour never carries meaning on its own (FR‑1.5 AC2): the site name is always written out, NEW is a
text badge, and arc42.de carries its tag. The template renders `hue-{key}` only from the keys the
configuration check admits, so no arbitrary class or style reaches the page.

## 7. Requirements and documentation

- **FR‑1.8** (M), "As the user I see each site's open pull requests and issues at a glance". The
  retired FR‑1.6 and FR‑1.7 are not reused.
  - AC1 `/sites` shows one tile per configured site in configuration order, and an Other tile for
    watched repositories no site claims.
  - AC2 A tile shows at most three pull requests and four issues, NEW first, then most recently
    updated; its totals and new count are taken before that cut.
  - AC3 When a tile cut something, it links to the list filtered to its repository.
  - AC4 Both views carry the List | Sites switch marking the current view, and "Mark all seen" and
    "Refresh" return to the view they were pressed on.
  - AC5 Each site's tile carries its brand colour, and colour is never the only way a site or a NEW
    item is told apart.
- **FR‑8.1** gains an AC for the `github.sites` rules in §3.
- **QS‑2.3**: the `/sites` page is added to the rendered-page budget test.
- `docs/concepts/configuration.md` documents `sites`, the palette keys and where the colours come
  from. The glossary gains *site*, *tile* and *Other tile*. No new ADR; this spec records the
  reasoning.

## 8. Testing

Test-first throughout.

- **Domain** (`BuildSiteTiles`): spec order; the 3/4 cut; totals, per-repository counts and new
  count before the cut; NEW first, then most recently updated; Other collecting several
  repositories; a tile with no items present; `More` false when nothing was cut; items of an
  unnamed repository ignored; the input slice unmodified. Domain coverage stays at 100 %.
- **Config**: `sites` optional; each rule in §3 fails with the field named; the committed
  `config/zorgscope.yaml` loads.
- **Web**:
  - `/sites` refuses anonymous access (covered by the every-protected-route test through its probe
    path).
  - Tiles render in configuration order with their `hue-*` class, Other last, and no element carries
    a `style` attribute.
  - A tile's "all →" link appears only when `More`, and targets `/?repo=…`; Other gets one link per
    repository.
  - `aria-current="page"` marks List on `/` and Sites on `/sites`.
  - `POST /seen` and `POST /refresh` with `return=/sites` redirect to `/sites`; an absent return goes
    to `/`; an external or `//host` return goes to `/`.
  - The rendered `/sites` page stays inside the QS‑2.3 budget, and `app.css` inside the static
    budget.
  - Every existing list, sign-in and CSP test stays green, unchanged in meaning.
- **Contrast** (`internal/web`): reads the `--hue-*` tokens, the light and dark values of
  `--surface`, `--text` and `--muted`, and the two wash percentages from the embedded `app.css`.
  It computes each wash as CSS `color-mix(in srgb, …)` does, and WCAG contrast ratios, and fails
  below 4.5:1 for:
  - white on every band;
  - `--text` and `--muted` on every light wash;
  - dark `--text` and dark `--muted` on every dark wash.

  When a colour fails, that appearance's wash percentage is lowered. A percentage may also be raised
  where a wash is too faint to see (the dark wash of the darker hues, plum and umber, will be), as
  long as the test still passes. The brand colours themselves are never altered.

`make check` green at the end.

## 9. Delivery

- Branch `feat/per-site-tiles` off `main` at `1c8eb70`, where the stateless reset was
  fast-forwarded (`main` and `reset/stateless` point at the same commit; not yet pushed to
  `origin/main`).
- An implementation plan follows from this spec (`superpowers:writing-plans`). Nothing is pushed.

## 10. Out of scope, deliberately

Filters on the Sites view, expanding a tile in place, a per-site detail page, colours written as
hex values in YAML, sites grouping several repositories (other than Other), and the family's other
properties (canvas, status, pdfminion) until their repositories are watched.
