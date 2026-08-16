# 5. Quality requirements

Structured after [quality.arc42.org](https://quality.arc42.org) (Q42): a quality tree naming the relevant
qualities, then concrete scenarios. A scenario is **Context → Stimulus → Response → Measure**; the measure is
what a test or review checks. Ids `QS‑<goal>.<n>` map to the quality goals of [chapter 1](01-goals.md).

## 5.1 Quality tree

```text
zorgscope quality
├── Reliability            (QG‑1)  correctness of detection, completeness, fault tolerance, visible failure
├── Performance efficiency (QG‑2)  time to first meaningful paint, resource frugality
├── Security & operability (QG‑3)  confidentiality of secrets, authentication, low ops effort, low cost
├── Flexibility            (QG‑4)  configurability, extensibility of source kinds
├── Compatibility          (QG‑5)  browsers, devices, screen sizes, no extensions
├── Usability & aesthetics (QG‑6)  scanability, clarity of highlights, calm visual design
└── Maintainability        (cross‑cutting, G‑4)  testability, agent‑friendliness, analysability
```

## 5.2 Scenarios

### QG‑1 Reliability of detection

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑1.1 | Normal operation, daily snapshot exists | A contributor opens an issue in any monitored repo at time *t* | The item appears in the Attention tile with `NEW` badge at the latest after the next successful GitHub poll | ≤ poll interval + 30 s after *t*; verified by an e2e test with the fake GitHub server injecting a new issue. |
| QS‑1.2 | Any set of items and any sequence of snapshots | Items are added, removed, updated in arbitrary order between snapshots | The `new` predicate returns true exactly for items absent from the last completed earlier snapshot | Property‑based test over random sequences; 100 % of cases; domain coverage ≥ 90 %. |
| QS‑1.3 | The app was down over the snapshot time | App restarts | A catch‑up snapshot is taken once, "new" continues to be computed against the last snapshot before downtime | Unit test with fake clock; no item is ever wrongly marked seen. |
| QS‑1.4 | GitHub API returns an error / rate limit / timeout for one repo | Poll runs | Other repos are still refreshed; the failing repo keeps its last data, tile shows a warning badge with the error age; a retry with exponential backoff (max 30 min) is scheduled | Integration test with fake server returning 5xx/403; UI shows badge; log contains structured error. |
| QS‑1.5 | Repo has > 100 open issues | Poll runs | All open items are fetched (pagination) | Contract test with paginated fixture; count matches. |
| QS‑1.6 | Item was dismissed | Someone comments on it | The item's `updated_at` changes → dismissal expires → item reappears as `UNANSWERED` if applicable | Unit + e2e test. |
| QS‑1.7 | Two polls overlap (slow upstream) | Scheduler ticks | No concurrent fetch of the same source; no lost updates | Unit test with blocking fake fetcher; `-race` clean. |
| QS‑1.9 | A registered credential expires in 14 days | Daily evaluation | `EXPIRING` attention item appears with remaining days; disappears only when the config date is renewed or dismissed for that date | Unit test with fake clock; e2e with fake config. |
| QS‑1.10 | A source token is revoked | Next poll returns 401 | Within one poll interval the source shows `AUTH FAILED` and an attention item exists; other sources unaffected | Integration test with fake server switching to 401. |
| QS‑1.8 | Any upstream failure | — | The dashboard never shows an empty tile without saying why and how old the data is | UI review + e2e assertion on staleness badge text. |

### QG‑2 Performance efficiency

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑2.1 | Warm cache, hosted on fly.io (fra region), desktop browser | User opens a new tab | HTML for the full dashboard is delivered and painted | Server response time (TTFB) p95 ≤ 150 ms; first contentful paint ≤ 1 s on a normal home connection; measured by Playwright trace in CI (fake sources) and documented Lighthouse run. |
| QS‑2.2 | Same | Page renders | No render‑blocking external resources; total transfer of `/` incl. CSS/JS/fonts ≤ 150 kB (uncompressed) | Playwright network assertion. |
| QS‑2.3 | Steady state | Background polling for ~15 sources | Memory RSS ≤ 128 MB, CPU idle ≤ 5 % on shared‑cpu‑1x | fly metrics after 24 h; unit tests use bounded caches. |
| QS‑2.4 | Tile poll every 60 s | Tile content unchanged | Response is `304 Not Modified` or ≤ 5 kB fragment | Handler test with ETag. |

### QG‑3 Security and low operating cost

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑3.1 | Production | Anonymous request to `/`, `/tiles/*`, `/status`, `/dismiss`, `/refresh` | Redirect to login / 401, no data leaked | Handler tests for every route; e2e. |
| QS‑3.2 | Production, no credential enrolled | `GET /enroll` with wrong or missing token | 404 (not 403 — do not reveal endpoint), rate limited 5/min/IP | Handler test. |
| QS‑3.3 | Any | Logs, HTML, DB, error pages | Contain no token, key, session secret or cookie value | Redaction unit tests; a "secret canary" test starts the app with known dummy secrets and greps all outputs. |
| QS‑3.4 | Any response | — | Security headers: CSP `default-src 'self'` (no inline scripts; htmx via self‑hosted file and nonce for its inline attributes if needed), `X-Content-Type-Options`, `Referrer-Policy`, `Strict-Transport-Security` (prod), cookies `Secure` `HttpOnly` `SameSite=Lax` | Handler tests. |
| QS‑3.5 | State‑changing routes | Cross‑site POST | Rejected (CSRF token / origin check) | Handler test. |
| QS‑3.6 | Dependencies | New Go module vulnerability published | CI fails on `govulncheck` findings; Dependabot opens PR | CI config review. |
| QS‑3.7 | Operations | A month passes | Zero manual interventions required; hosting cost ≤ 5 €/month | fly invoice; runbook contains no periodic tasks. |
| QS‑3.8 | Container | Image built | Runs as non‑root, distroless/scratch base, no shell, ≤ 30 MB | Dockerfile review; `docker inspect`. |

### QG‑4 Flexibility of sources

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑4.1 | Running system | Operator adds a repo/site/feed line to `config/zorgscope.yaml` and pushes | It is monitored and shown after CI deploy | ≤ 10 min wall clock, zero code changes; e2e test adds a repo to the fake config and asserts a new card. |
| QS‑4.2 | Codebase | Developer adds a new source *kind* (e.g. Mastodon) | Requires exactly: one adapter package implementing `SourceFetcher`, one tile template, one fake, one config section, registration in one place | Guide `adding-a-source.md`; reviewed by adding the `feed` kind last in the plan following the guide. |
| QS‑4.3 | Config | Invalid entry (typo in key, bad URL, negative interval) | Startup fails fast naming the key and line | Unit tests per validation rule. |

### QG‑5 Compatibility

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑5.1 | Arc, Vivaldi, Firefox, Safari (current versions) | Open dashboard, log in with passkey, dismiss an item | Works identically without extensions | Manual matrix once per release; Playwright runs Chromium, Firefox, WebKit in CI. |
| QS‑5.2 | Phone (375 px), tablet (768 px), desktop (1440 px), ultrawide (2560 px) | Open dashboard | Single column → 4 columns; no horizontal scroll; tap targets ≥ 44 px | Playwright viewport tests + screenshots. |
| QS‑5.3 | Browser configured with the dashboard as new‑tab/homepage | New tab | Loads without extension in Vivaldi/Firefox/Safari; Arc via pinned tab or "open on start" — documented | `docs/guides/browser-new-tab.md`. |
| QS‑5.4 | Reduced motion / high contrast OS settings | Open dashboard | Animations off, contrast ≥ WCAG AA | axe check in e2e, `prefers-reduced-motion` respected. |

### QG‑6 Usability and aesthetics

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑6.1 | First‑time look at the page | User glances for 3 s | Can tell how many items need attention and whether any build is broken | Attention count in tile title & browser tab title (`(3) zorgscope`); UI review by owner. |
| QS‑6.2 | Any tile | — | Colour is never the only carrier of meaning (badges have text; icons have labels) | axe + review. |
| QS‑6.3 | Any state | Empty, loading, error, stale | Each state has a designed representation, no raw errors, no layout jumps | Golden template tests for each state. |
| QS‑6.4 | Typography & layout | — | Consistent spacing scale, ≤ 2 type families, ≤ 6 semantic colours (+ age scale) defined as CSS custom properties | Stylelint/ review; design tokens file exists. |

### Maintainability (cross‑cutting)

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑7.1 | Repository | An unfamiliar agent picks a plan task | Task lists files, tests to write first, acceptance command; no task > ~2 h for a competent developer | Plan review. |
| QS‑7.2 | Codebase | `make check` | Passes: gofmt, vet, golangci‑lint (errcheck, staticcheck, gosec, revive), tests, docs lint | CI green. |
| QS‑7.3 | Domain package | — | Zero imports outside std lib; ≥ 90 % coverage | `depguard` rule + coverage gate. |
| QS‑7.4 | Any adapter | Upstream API change | Only that adapter and its fixtures change | Architecture rule: adapters are the only packages importing API clients (`depguard`). |
| QS‑7.5 | Any change | PR opened | CI runs lint, unit, integration, e2e in ≤ 10 min | Workflow timing. |
