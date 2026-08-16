# 5. Building block view

## 5.1 Level 1 – the zorgscope binary

```mermaid
flowchart TB
    subgraph zorgscope["zorgscope (single Go binary)"]
        HTTP[internal/http<br/>router, handlers, auth, middleware, view models]
        APP[internal/app<br/>scheduler, refresh, snapshotter, dismiss, dashboard query]
        DOM[internal/domain<br/>Item, Snapshot, Attention, Dismissal — pure]
        PORTS[internal/ports<br/>SourceFetcher, ItemStore, SnapshotStore, DismissalStore,<br/>StatusStore, AuthStore, Clock, Notifier]
        CFG[internal/config<br/>YAML schema, validation, env secrets]
        subgraph adapters["internal/adapters"]
            GH[github]
            PL[plausible]
            TD[todoist]
            FD[feed]
            SQ[sqlite]
            CLK[clock]
        end
        WEB[web/<br/>templates, static css, htmx]
    end
    HTTP --> APP --> DOM
    APP --> PORTS
    HTTP --> WEB
    GH & PL & TD & FD --> PORTS
    SQ --> PORTS
    CFG --> APP
    MAIN[cmd/zorgscope/main.go<br/>wiring only] --> HTTP & APP & CFG & adapters
    FAKE[cmd/fakesources<br/>fake GitHub/Plausible/Todoist/feeds HTTP server] -.-> GH & PL & TD & FD
```

Import rules (enforced by `depguard`):

| Package | May import |
|---------|-----------|
| `internal/domain` | std lib only |
| `internal/ports` | `domain`, std lib |
| `internal/app` | `domain`, `ports`, `config`, std lib |
| `internal/adapters/*` | `domain`, `ports`, its own third‑party client libs, std lib |
| `internal/http` | `app`, `domain`, `config`, `ports` (auth store), `web` (embedded FS), webauthn lib, std lib |
| `cmd/*` | everything (wiring) |

### Responsibilities

| Block | Responsibility | Key types / functions |
|-------|----------------|-----------------------|
| `internal/domain` | Data model and rules: age buckets, `IsNew(item, prevSnapshot)`, `IsUnanswered(item, grace, me, collaborators)`, `Attention(item, ctx) Level`, dismissal expiry, snapshot diff, item identity (`SourceID` + `ExternalID`), sorting/capping. | `Item`, `Kind`, `Snapshot`, `Dismissal`, `AttentionLevel`, `Bucket`, `Rules` |
| `internal/ports` | Interfaces the app depends on; in‑memory fakes for tests live in `ports/fake`. | `SourceFetcher{ID(); Kind(); Fetch(ctx) ([]Item, error)}`, `ItemStore`, `SnapshotStore`, `DismissalStore`, `StatusStore`, `AuthStore`, `Clock`, `Notifier` |
| `internal/config` | Parse `zorgscope.yaml`, merge env secrets, validate, expose typed config; hot reload (SIGHUP/fsnotify, local). | `Load(path, env) (Config, error)`, `Validate` |
| `internal/app` | Use cases: `Scheduler` (ticker per source, jitter, backoff, single‑flight), `RefreshAll`, `Snapshotter` (daily + catch‑up + prune), `Dismiss`, `DashboardQuery` (assemble tile view models from stores + rules), `SourceRegistry` (kind → adapter constructor). | `Scheduler`, `Snapshotter`, `Dashboard`, `Registry` |
| `internal/adapters/github` | GraphQL client, pagination, mapping to `Item` (issue/pr/workflow‑run), notifications REST for mentions, rate‑limit awareness, ETag. | `RepoFetcher`, `MentionsFetcher` |
| `internal/adapters/plausible` | Stats API v2 queries: aggregates 7 d/30 d with comparison, timeseries, top pages → `Item{Kind: MetricSeries}`. | `SiteFetcher` |
| `internal/adapters/todoist` | Tasks due ≤ +7 d and overdue, projects; mapping to `Item{Kind: Task}`. | `TasksFetcher` |
| `internal/adapters/feed` | `gofeed` parsing, conditional GET, dedup by canonical URL, keyword filters → `Item{Kind: Article}`. | `FeedFetcher` |
| `internal/adapters/sqlite` | Schema/migrations (embedded SQL), implementations of all store ports, WAL, pragmas. | `Store` implementing all `*Store` ports |
| `internal/adapters/clock` | Real clock; fake clock in tests. | `Clock` |
| `internal/http` | Router (`net/http` mux), handlers `/`, `/tiles/{name}`, `/dismiss`, `/refresh`, `/status`, `/login`, `/enroll`, `/account`, `/logout`, `/healthz`, `/readyz`, `/static/*`; middleware (session, CSRF, security headers, request log, gzip); WebAuthn ceremonies; template rendering with view models. | `Server`, `Handlers`, `Auth` |
| `web/` | `templates/` (layout, page, one partial per tile and per state), `static/` (`tokens.css`, `app.css`, `htmx.min.js`, icons). Embedded via `embed.FS`. | — |
| `cmd/zorgscope` | Wiring, flags, graceful shutdown. | `main` |
| `cmd/fakesources` | Deterministic HTTP fakes of all upstreams with a small control API (`POST /__control/github/issues` to inject events) for e2e. | `main` |

## 5.2 Level 2 – `internal/domain`

```text
domain/
  item.go          Item, Kind, ItemID, Author, Labels; ItemID = SourceID + "/" + ExternalID
  snapshot.go      Snapshot{SourceID, TakenAt, IDs}; Diff(prev, cur) (added, removed)
  attention.go     Level (None, Stale, Aged, Unanswered, New, BuildFailed), Rules struct (Grace, StaleAfter, Me, Bots), Evaluate(item, prevSnapshot, dismissal, now)
  buckets.go       Bucket(age) → LT24h | LT7d | LT30d | GE30d
  dismissal.go     Dismissal{ItemID, UpdatedAt, DismissedAt}; Covers(item) bool
  metrics.go       MetricSeries payload for Plausible items (typed, not raw JSON)
  task.go          Task payload (project, priority, due, recurring)
  sort.go          attention ordering: level desc, created desc; cap with overflow count
```

No I/O, no time.Now(), no logging in this package.
