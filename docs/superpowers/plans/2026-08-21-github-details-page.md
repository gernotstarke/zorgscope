# GitHub details page — implementation plan

**Goal:** A page behind the GitHub tile that shows *every* open issue and pull request, grouped by
repository, with a strip of small per-repository counters at the top — so that "eight repositories,
where is the work" is one glance rather than eight browser tabs.

**Status:** plan only. Nothing below is implemented.

**Spec:** [the v1 design](../specs/2026-08-17-zorgscope-reset-design.md) ·
**Requirements:** [functional](../../requirements/04-functional-requirements.md),
[quality](../../requirements/05-quality-requirements.md) ·
**Precedent:** the builds details page (`/builds`), which this follows in shape and in the reasons
for that shape.

---

## Why this page exists

The front page is deliberately not a report. The GitHub tile shows five items — newest first,
across every repository — and says "and 37 more open" underneath (`githubTileLimit`). That is the
right answer to *does anything need me right now*, and the wrong answer to two other questions the
count itself provokes:

- **Which repository is the work in?** The tile orders by newness, so five items from five
  repositories look the same as five from one. The distribution is invisible exactly when it
  matters.
- **What is the other 37?** There is currently nowhere to see it. The number is a dead end.

`/builds` already established the pattern for this: the front page carries the indicator, the page
behind it carries the rows. This does the same thing for issues and pull requests.

## The decisions this plan makes

**D1 — Grouped by repository, not sorted by it.** A flat table with a repository column that
happens to be sorted still makes the reader do the grouping. One section per repository, each with
its own heading and its own count, means the answer to "how many in this one" is the heading rather
than a subtraction.

**D2 — The overview strip is counts only, and links.** Very small tiles, small type: repository
name, issue count, pull-request count, and how many of them are NEW. No titles, no dates, no
badges. Its job is to be read in one sweep and to get you to the right section — each tile is an
anchor link to its own section below. Anything else on it makes it a second copy of the page.

**D3 — Repository order is configuration order, not count order.** The list in
`config/zorgscope.yaml` is the order the operator chose, and it is stable between visits;
count order re-arranges the page every time an issue is opened, which makes the strip unreadable
as a habit. The sites tile already relies on configuration order for the same reason (FR-3.1 AC3).
A repository with nothing open still gets a tile, showing zero — an absent tile and a zero tile say
different things, and the second one is true.

**D4 — Inside a repository, the existing item order.** `SortItems` puts NEW first and then
most-recently-updated, and that is already the order the tile uses. Two orders for the same items
on two pages of the same dashboard would be a bug report waiting to happen.

**D5 — Everything open, with no cap.** The page's whole purpose is the part the tile cuts off.
There is a payload question behind this and it is answered in the risks below, not by a limit that
would reintroduce the dead end.

**D6 — Issues and pull requests together, told apart.** They live in one list per repository,
ordered as D4 says, with the kind shown per row. Splitting them into two lists doubles the
headings and buries a two-item repository under four of them. The overview counts them separately,
because *3 issues and 9 PRs* is a different day from *9 issues and 3 PRs*.

## Scope

**In:** `GET /github`; the grouped list; the overview strip; the GitHub tile's "and N more open"
becoming the link to it; NEW badges consistent with FR-1.2.

**Out, deliberately:** filtering and search (a page nobody can read yet does not need controls);
per-repository collapse (state that has to be remembered somewhere, for a page that is a glance);
any new upstream data — every field this page shows is already stored.

## Global constraints

These bind every task and are the ones the v1 plan sets out:

- `internal/domain` imports only the standard library (QS‑5.1); `depguard` enforces it.
- The page renders from stored data only and contacts no upstream service (FR‑1.1 AC2).
- Every route is declared in `routes()` with the credential it needs (QS‑4.1). This one is
  `authSessionPage`, like `/problems` and `/builds`.
- No secret value may be logged, rendered or written to the repository (QS‑4.3).
- Colour is never the only carrier of meaning (FR‑1.5 AC2).
- The page works fully with JavaScript disabled (FR‑1.3 AC3).
- Every task ends with `make check` green.

---

## Task 1: The domain assembles the grouping

**Files:** `internal/domain/github.go`, `internal/domain/github_test.go`,
`internal/domain/dashboard.go`

```go
// RepoItems is one repository's open items, and the counts a reader needs before reading them.
type RepoItems struct {
    Repo                 string
    Items                []Item
    Issues, PRs, NewCount int
}

// GitHubDetail is the whole page: one entry per configured repository, in configuration order,
// plus the totals the strip's last tile shows.
type GitHubDetail struct {
    Repos                        []RepoItems
    Issues, PRs, NewCount, Total int
}

func githubDetail(in DashboardInput, disabled map[string]bool) GitHubDetail
```

`Dashboard` gains `GitHub GitHubDetail`. The grouping walks `in.Repos` first (D3), then appends any
repository that has stored items but is no longer configured — the same complement the builds page
does, and for the same reason: a row that is there says something, a row that vanished says
nothing.

**Watch for:** `Item.Repo` is the *container*, and for a Todoist task it is the project name, not a
repository (see its doc comment). The grouping must filter by source first — `itemsBySource(in.Items,
"github")` — or a Todoist project called `arc42/faq.arc42.org-site` would land in a repository
section. Filtering by source is what `buildTile` already does.

**Acceptance:** a repository with no open items appears with zero counts; an unconfigured
repository holding stored items appears last; counts add up to the tile's `Total` for the same
input; a Todoist task never appears; item order inside a repository matches `SortItems`.

## Task 2: The page

**Files:** `internal/web/github.go`, `internal/web/templates/github.html`,
`internal/web/server.go`, `internal/web/github_test.go`

`GET /github`, `authSessionPage`, `handleGitHubDetail`. View types mirror the builds page:
`githubDetailView`, `repoItemsView`, reusing the existing `itemView` so a row on this page and a
row in the tile cannot drift apart.

The source's own health goes above the strip, exactly as `/builds` carries it: a failed fetch means
every count below is as old as the last one that worked, which is a different question from any
repository's count.

Page order: `<h1>`, the source's health, the overview strip, then the sections.

**Acceptance:** anonymous `GET /github` redirects to `/login`; every configured repository has a
section; an item appears exactly once; the page carries no secret (the canary that sweeps every
route still passes).

## Task 3: The overview strip

**Files:** `internal/web/templates/github.html`, `internal/web/static/app.css`

Very small tiles, small type (D2). One per repository plus a total:

```text
┌──────────────────┐ ┌──────────────────┐ ┌──────────────────┐
│ faq.arc42.org…   │ │ arc42-template   │ │ docs.arc42.org…  │
│ 3 issues · 1 PR  │ │ 0 issues · 2 PR  │ │ 7 issues · 0 PR  │
│ 2 new            │ │                  │ │                  │
└──────────────────┘ └──────────────────┘ └──────────────────┘
```

- A CSS grid with `repeat(auto-fill, minmax(11rem, 1fr))`, so eight tiles wrap to the width rather
  than needing a breakpoint per count.
- Each tile is an `<a href="#repo-<slug>">` to its own section. The anchor is a slug of the
  repository name, computed in Go and used for both the `id` and the `href` so the two cannot
  disagree; `owner/name` contains a `/`, which is legal in a fragment but reads badly in a URL bar.
- The "N new" line is present only when N > 0, so a quiet repository's tile is shorter rather than
  carrying a zero.
- The repository name is shown without its owner when every configured repository shares one
  (they all say `arc42/` today, which is eight repetitions of nothing). The owner returns as soon
  as two owners are configured. Compute this once, in Go, from the configured list.

**Acceptance:** eight repositories render eight tiles plus a total; a tile's link reaches its
section; the owner is dropped only when it is common to all; a repository with nothing open shows
zeros and no "new" line.

## Task 4: The tile points at it

**Files:** `internal/web/templates/tiles/github.html`, `internal/web/dashboard.go`

"and 37 more open" becomes a link to `/github`, and the tile's title gains one too — the same
move `/builds` made with "Build details". This is the change that makes the count stop being a dead
end, and it is one line of markup and one of CSS.

**Acceptance:** the tile links `/github`; the fragment served by `/tile/github` links it too (a
polled tile must not lose the link); the link is absent when the source is disabled.

## Task 5: Documentation

**Files:** `docs/requirements/04-functional-requirements.md`

FR‑2.2 gains an acceptance criterion for the details page and the overview strip, in the shape
FR‑2.3 AC4 uses for builds — the front page carries the summary, the detail is one click away.

---

## Risks and what to do about them

**Payload (QS‑2.3).** This is the real one. The dashboard's budget is 150 kB and the representative
configuration is ten repositories; the tile shows five items, this page shows all of them. At forty
items with a title, an 80-character summary, a repository, an author and two timestamps each, the
page is on the order of 40–60 kB — inside the budget, but it is the first page in this application
whose size scales with the data rather than with the design.

Do not add a cap to fix this (D5). Do add the same measurement `TestDashboardFitsItsBudget` makes,
against the representative store, so the day it stops being true is the day a test says so rather
than the day someone waits on a phone. If it ever does exceed: drop the summary line first, since
it is the largest per-item field and the least load-bearing on a page that is about distribution.

**An eleventh repository.** QS‑3.5 caps the watch list at ten. The strip is built to wrap, so this
is a layout non-event, but the plan should not quietly assume ten either.

**A repository named like a Todoist project.** Covered by Task 1's source filter; it is called out
here because the field name (`Repo`) actively invites the mistake, and the fix is one function call
that is easy to leave out.
