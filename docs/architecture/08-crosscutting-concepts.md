# 8. Cross‑cutting concepts

## 8.1 Domain model

```mermaid
classDiagram
    class Item {
      ID ItemID
      Kind Kind
      Title string
      URL string
      Author string
      CreatedAt time
      UpdatedAt time
      LastActivityBy string
      LastActivityAt time
      Labels []string
      Payload any
      FirstSeen time
    }
    class ItemID { SourceID string; ExternalID string }
    class Kind { <<enum>> Issue PR WorkflowRun MetricSeries Mention Credential TLSCertificate }
    class Snapshot { SourceID; Date; TakenAt; IDs set }
    class Dismissal { ItemID; UpdatedAt; DismissedAt }
    class FetchStatus { SourceID; Kind; LastSuccess; LastError; ErrorMsg; NextRun; ItemCount; Duration; InFlight; AuthFailed }
    class Rules { Grace; StaleAfter; Me; Collaborators; BotSuffix }
    class AttentionLevel { <<enum>> None Aged Stale Expiring Unanswered New BuildFailed Expired Down AuthFailed }
    Item --> ItemID
    Item --> Kind
    Rules ..> Item : Evaluate(item, prevSnapshot, dismissal, now)
    Rules ..> Snapshot
    Rules ..> Dismissal
    Rules ..> AttentionLevel
```

Payload types per kind: `IssuePayload{Comments}`, `PRPayload{Draft, ReviewDecision, Comments}`,
`WorkflowRunPayload{RunID, WorkflowName, Conclusion, Status, Branch}`,
`MentionPayload{Reason, Repo, SubjectType}`,
`MetricSeriesPayload{Visitors7d, Visitors30d, Pageviews30d, DeltaVisitors7d, DeltaVisitors30d, Daily []int, TopPages []MetricPage}`,
`CredentialPayload{Expires *time, WarnDays, UsedBy, URL, AutoDetected, AuthFailed}`,
`TLSCertificatePayload{NotAfter, Issuer, HostnameValid, LastChecked}`.

`AttentionLevel` is severity‑ordered, not alphabetical or insertion‑ordered: the listing above is the real
`iota` order, it is also the sort order for the Attention tile, and `NeedsAttention()` is true from
`Expiring` upward. The order is therefore load‑bearing and must not be reordered casually.

## 8.2 Attention rules (the heart of QG‑1)

```text
Evaluate(item, prev, dismissal, now):
  if item.Kind == WorkflowRun and Conclusion == failure  → BuildFailed (unless dismissed for this RunID)
  if item.Kind == Credential:  Expires < now → Expired; Expires ≤ now+WarnDays → Expiring; else None   (dismissal keyed to Expires)
  if item.Kind == TLSCertificate: invalid/expired → Expired; NotAfter ≤ now+WarnDays → Expiring; else None
  (source status auth_failed → synthetic item Kind=Credential, Title "AUTH FAILED: <source>", level AuthFailed; created by app layer from FetchStatus)
  prev      := latest snapshot of the item's source with date < SnapshotDay(now)   (see §6.4)
  new       := prev == nil ? item.CreatedAt ≥ now-24h : !prev.Contains(item.ID)
  unanswered:= item.Kind ∈ {Issue, PR}
               and item.CreatedAt ≤ now-Grace
               and (item.LastActivityBy == ""
                    or (item.LastActivityBy == item.Author and item.LastActivityBy != Me)
                    or isBot(item.LastActivityBy))
  covered   := dismissal != nil and dismissal.UpdatedAt == item.UpdatedAt
  if covered → level = None (age bucket still shown)
  else if new → New; else if unanswered → Unanswered
  else if item.Kind ∈ {Issue,PR} and item.LastActivityAt ≤ now-StaleAfter → Stale
  else → Aged (i.e. only bucket colouring)
Bucket(item.CreatedAt, now) ∈ {<24h, <7d, <30d, ≥30d}
```

Rules are configurable (`github.grace_period`, `github.stale_after`, `github.me`, `github.bots`).
Collaborator answers: the GitHub adapter sets `LastActivityBy` from the last comment/review author; the
domain treats any author ≠ item author as an answer (bots excluded), and additionally treats `Me` replying
last on an item `Me` authored as an answer (D‑11, `docs/plans/README.md`): if I spoke last on my own item,
there is nothing on it waiting for me. With `Me` empty this reduces to the previous rule — the opener's own
bump never counts as an answer. Collaborator write‑access lookup is a later refinement (O‑3‑adjacent), not
needed for correctness of "someone reacted".

## 8.3 Configuration

The authoritative editable model is revisioned runtime YAML on the Fly volume, managed through the
authenticated `/api/v1/config` family (FR-8.x, ADR-0014). It contains GitHub repositories and attention
rules, Plausible sites, snapshot policy, credential/API-key metadata, TLS endpoints and presentation
hints. Strict validation applies before an atomic mode-0600 write; `If-Match` prevents lost updates.
Accepted changes rebuild the complete source/runtime generation without restarting the process.

Secret values are write-only. API reads return presence/source status only for GitHub and Plausible,
while AES-256-GCM protects their overrides in `/data/zorgscope.secrets` under deployment secret
`ZORGSCOPE_CONFIG_KEY`. Listen/storage/public-URL/auth/logging values are read-only deployment metadata;
deployment secret values and their presence are never returned.

The editable document has this complete shape (Fly first boot uses `config/fly.bootstrap.yaml` with
GitHub and Plausible disabled):

```yaml
server:
  timezone: Europe/Berlin
ui:
  tile_poll_seconds: 60
  attention_cap: 30
  refresh_min_gap_seconds: 30   # manual refresh never re-fetches a source more often than this
  tiles: [attention, repos, sites, watch] # order; omit to hide
snapshot:
  time: "03:00"
  retention_days: 30
github:
  enabled: false
  me: gernotstarke
  poll_interval: 10m
  grace_period: 4h
  stale_after: 720h            # 30 days
  bots: ["[bot]", "dependabot", "renovate"]
  mentions: true               # notifications for @me outside monitored repos
  base_url: https://api.github.com
  repos:
    - arc42/arc42.org-site
    - arc42/arc42.de-site
    - arc42/arc42-template
    - arc42/docs.arc42.org-site
    - arc42/quality.arc42.org-site
    - arc42/faq.arc42.org-site
    - gernotstarke/esabuch.de-site
    - gernotstarke/gernotstarke.de-site
    # per-repo override example:
    # - name: arc42/arc42-template
    #   poll_interval: 5m
plausible:
  enabled: false
  poll_interval: 30m
  sites: [arc42.org, arc42.de, docs.arc42.org, quality.arc42.org, faq.arc42.org, esabuch.de, gernotstarke.de]
  order: visitors             # visitors | config
  base_url: https://plausible.io
watch:
  enabled: true
  poll_interval: 15m           # refresh configured/auto-detected credential metadata
  warn_days: 14                # default for credentials and TLS certificates
  credentials:                 # manual registry; zorgscope's own GitHub token is added automatically (FR-11.2)
    - name: status.arc42.org GitHub token
      expires: 2026-12-31
      used_by: status.arc42.org
      url: https://github.com/settings/tokens
  urls:
    - name: status.arc42.org
      url: https://status.arc42.org
      expect_status: 200
      poll_interval: 15m
```

`ZORGSCOPE_API_TOKEN` and `ZORGSCOPE_CONFIG_KEY` are required deployment secrets. Set provider secrets
through their PUT routes, fetch the new revision, then enable each source with a complete config PUT.
Optional `GITHUB_TOKEN`/`PLAUSIBLE_API_KEY` environment values remain fallbacks until explicitly replaced
or cleared. Validation rejects bad identifiers/URLs, duplicates and unsafe intervals before persistence.

## 8.4 Persistence (SQLite schema, migration 0001)

```sql
items      (source_id TEXT, external_id TEXT, kind TEXT, title TEXT, url TEXT, author TEXT,
            created_at INT, updated_at INT, last_activity_by TEXT, last_activity_at INT,
            labels TEXT/*json*/, payload TEXT/*json*/, first_seen INT, PRIMARY KEY(source_id, external_id));
snapshots  (source_id TEXT, date TEXT/*YYYY-MM-DD*/, taken_at INT, ids TEXT/*json array*/, PRIMARY KEY(source_id, date));
dismissals (source_id TEXT, external_id TEXT, updated_at INT, dismissed_at INT, PRIMARY KEY(source_id, external_id));
fetch_status (source_id TEXT PRIMARY KEY, kind TEXT, last_success INT, last_error INT, error_msg TEXT,
            next_run INT, item_count INT, duration_ms INT, in_flight INT, auth_failed INT);
```

Pragmas: `journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000`, `foreign_keys=ON`. Migrations are
embedded SQL files applied at start (`schema_version` table). Times are Unix seconds UTC.

Runtime configuration is intentionally outside SQLite: `/data/zorgscope.yaml` contains the complete
non-secret DTO using duration strings; `/data/zorgscope.secrets` contains a versioned AES-GCM envelope.
Both use atomic temp-file + fsync + rename writes and mode 0600.

## 8.5 Client presentation

The backend does not own a dashboard layout. It returns presentation-ready, client-neutral JSON with
stable IDs, semantic attention levels and explicit content/empty/stale/error states. A Wails macOS client
and same-origin browser client may coexist. Each owns its design system, accessibility implementation,
keyboard/touch behaviour and visual regression tests; neither reimplements attention rules.

## 8.6 Security

Bootstrap requests use constant-time bearer validation of `ZORGSCOPE_API_TOKEN`; passkey-backed device
authentication (ADR-0006 adapted by ADR-0014) is the target. Invalid attempts are safely logged and
globally limited while valid tokens remain accepted. Runtime upstream secrets are write-only and
sealed with authenticated encryption under `ZORGSCOPE_CONFIG_KEY`; the versioned file header is bound as
AES-GCM associated data. Decryption fails closed. The legacy browser path uses CSRF (double-submit token in
`hx-headers`, plus a `Sec-Fetch-Site` check), security headers (CSP `default-src 'self'; script-src
'self'; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'`, HSTS in prod, `X-Content-Type-Options`,
`Referrer-Policy: no-referrer`), constant‑time token comparison, live secret redaction in `slog`, a non-root
distroless image and least-privilege upstream tokens (read-only).

## 8.7 Error handling

* Adapters return typed errors: `RateLimitedError{ResetAt}` (unwrapped via `ports.AsRateLimited`), plus
  sentinels `ErrAuth`, `ErrTransient`, `ErrPermanent` (`errors.Is`/`errors.As`); never panic on upstream data.
* `ErrAuth` additionally flags the source `auth_failed`, which the app layer turns into an `AUTH FAILED` attention item (FR‑11.3).
* Scheduler translates errors into `FetchStatus` + backoff; the UI shows "⚠ data from 14:02 · GitHub: 403 rate limited, retry 14:35".
* HTTP handlers: domain/store errors → 500 with generic page + logged; template errors are impossible at
  runtime because templates are parsed at start (`template.Must`) and golden‑tested.
* Config errors → exit code 2 with message; missing optional source secret with `enabled: false` is fine.

## 8.8 Logging & observability

`log/slog` JSON to stdout; fields `component`, `source_id`, `duration_ms`, `status`; request log middleware;
`/status` page for humans; `/healthz` liveness, `/readyz` readiness. No metrics endpoint in v1 (fly gives
machine metrics).

## 8.9 Testing (see ADR‑0010)

| Layer | Kind | Tooling | Location |
|-------|------|---------|----------|
| domain | unit, table‑driven, property‑style (`testing/quick` or `pgregory.net/rapid`) | `go test` | `internal/domain/*_test.go` |
| adapters | contract tests against `httptest` servers replaying fixtures; error‑path tests | `go test` | `internal/adapters/*/*_test.go`, fixtures in `test/fixtures/<adapter>/` |
| sqlite | store tests on temp DB; migration test | `go test` | `internal/adapters/sqlite/*_test.go` |
| app | scheduler with fake clock/fetchers; snapshotter; dashboard query | `go test -race` | `internal/app/*_test.go` |
| server | handler tests, auth tests, golden HTML for each tile state, security header tests, secret canary | `go test` | `internal/server/*_test.go`, goldens in `internal/server/testdata/` |
| e2e | Current Chromium HTML smoke: seeded dashboard, refresh/injected issue, dismissal/reload and phone-width overflow | Docker Compose | `test/e2e/` |
| architecture | import rules | `depguard` via golangci‑lint | `.golangci.yml` |

## 8.10 Time

All logic receives `now` from the `Clock` port; timezone from config for snapshot time and day grouping;
storage in UTC.
