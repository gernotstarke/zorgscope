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
├── Compatibility          (QG‑5)  client-neutral API, native and browser clients
├── Usability & aesthetics (QG‑6)  scanability, clarity of highlights, calm visual design
└── Maintainability        (cross‑cutting, G‑4)  testability, agent‑friendliness, analysability
```

## 5.2 Scenarios

### QG‑1 Reliability of detection

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑1.1 | Normal operation, daily snapshot exists | A contributor opens an issue in any monitored repo at time *t* | The item appears in Attention API data with `NEW` at the latest after the next successful GitHub poll | ≤ poll interval + 30 s after *t*; verified by an e2e test with the fake GitHub server injecting a new issue. |
| QS‑1.2 | Any set of items and any sequence of snapshots | Items are added, removed, updated in arbitrary order between snapshots | The `new` predicate returns true exactly for items absent from the last completed earlier snapshot | Property‑based test over random sequences; 100 % of cases; domain coverage ≥ 90 %. |
| QS‑1.3 | The app was down over the snapshot time | App restarts | A catch‑up snapshot is taken once, "new" continues to be computed against the last snapshot before downtime | Unit test with fake clock; no item is ever wrongly marked seen. |
| QS‑1.4 | GitHub API returns an error / rate limit / timeout for one repo | Poll runs | Other repos are refreshed; the failing repo keeps its last data and exposes warning/freshness metadata; retry uses exponential backoff (max 30 min) | Integration test with fake server returning 5xx/403; API retains data and status; log contains structured error. |
| QS‑1.5 | Repo has > 100 open issues | Poll runs | All open items are fetched (pagination) | Contract test with paginated fixture; count matches. |
| QS‑1.6 | Item was dismissed | Someone comments on it | The item's `updated_at` changes → dismissal expires → item reappears as `UNANSWERED` if applicable | Unit + e2e test. |
| QS‑1.7 | Two polls overlap (slow upstream) | Scheduler ticks | No concurrent fetch of the same source; no lost updates | Unit test with blocking fake fetcher; `-race` clean. |
| QS‑1.9 | A registered credential/API key or TLS certificate expires in 14 days | Daily evaluation | `EXPIRING` attention item appears with remaining days; disappears only when the expiry changes or it is dismissed for that date | Unit test with fake clock; API integration test. |
| QS‑1.10 | A source token is revoked | Next poll returns 401 | Within one poll interval the source shows `AUTH FAILED` and an attention item exists; other sources unaffected | Integration test with fake server switching to 401. |
| QS‑1.8 | Any upstream failure | — | The API never represents cached content as empty without saying why and how old the data is | API integration assertion on retained content and freshness/error metadata. |

### QG‑2 Performance efficiency

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑2.1 | Warm cache, hosted on fly.io | A client requests the dashboard | Complete cached JSON is delivered | Server response time p95 <= 150 ms and payload <= 250 kB for the initial configured set; measured by API benchmark/integration test. |
| QS‑2.2 | Client has the latest revision | Client revalidates dashboard | Backend avoids retransmitting unchanged data | `If-None-Match` receives 304 with no response body. |
| QS‑2.3 | Steady state | Background polling for ~15 sources | Memory RSS ≤ 128 MB, CPU idle ≤ 5 % on shared‑cpu‑1x | fly metrics after 24 h; unit tests use bounded caches. |
| QS‑2.4 | Client polls at the configured hint | Dashboard unchanged | Response is `304 Not Modified` | Handler test with ETag. |

### QG‑3 Security and low operating cost

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑3.1 | Production | Anonymous request to any `/api/v1/*` route | 401, no status or configuration data leaked | Handler tests for every route. |
| QS‑3.2 | Production bootstrap authentication | Repeated invalid bearer tokens | Requests are rejected in constant time and rate limited; tokens are never logged | Handler and log-capture tests. |
| QS‑3.3 | Any | Logs, API responses, volume-file inspection or errors | Contain no plaintext token, API key, master key or client credential | Secret-canary tests; the encrypted provider-secret envelope differs from plaintext and fails closed with the wrong key. |
| QS‑3.4 | Production response | — | HTTPS required; content type and cache policy are explicit; optional browser client receives strict CSP, HSTS, `X-Content-Type-Options` and `Referrer-Policy` | Handler tests. |
| QS‑3.5 | Configuration mutation | Stale revision, malformed value or failed encryption/activation | Request is rejected atomically; old revision and scheduler remain active | Transaction/integration tests. |
| QS‑3.6 | Dependencies | New Go module vulnerability published | CI fails on `govulncheck` findings; Dependabot opens PR | CI config review. |
| QS‑3.7 | Operations | A month passes | Zero manual interventions required; hosting cost ≤ 5 €/month | fly invoice; runbook contains no periodic tasks. |
| QS‑3.8 | Container | Image built | Runs as non‑root, distroless/scratch base, no shell, ≤ 30 MB | Dockerfile review; `docker inspect`. |

### QG‑4 Flexibility of sources and clients

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑4.1 | Running system | User adds a repo, Plausible site, credential/API key or TLS endpoint through `/api/v1/config` | It is validated, persisted, scheduled and visible without restart/deploy | <= 10 s after successful response; zero code changes; integration test verifies restart persistence. |
| QS‑4.2 | Codebase | Developer adds a new source kind | Requires one adapter implementing `SourceFetcher`, one schema fragment, one response mapping, one fake and one registry entry | Architecture review and guide; clients consume generic source/freshness structures where practical. |
| QS‑4.3 | Config | Invalid/unknown entry or concurrent edit | 422 names JSON paths or 409 reports revision conflict; no partial update | Unit and API integration tests per validation/concurrency rule. |
| QS‑4.4 | Same backend | A Wails client and same-origin browser client request the same revision | Both receive equivalent domain data and can perform all configuration operations | Contract suite runs against two independent test clients. |

### QG‑5 Client compatibility

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑5.1 | Published API schema | Wails or browser client reads status and mutates config | Stable versioned JSON semantics work without HTML scraping or client-specific endpoints | Contract tests against generated/recorded schemas. |
| QS‑5.2 | API evolves within v1 | Backend adds an optional property | Existing clients continue to work | Backward-compatibility test corpus; breaking changes require `/api/v2`. |
| QS‑5.3 | Native client temporarily offline | App opens | Last locally cached dashboard can be shown clearly marked with its server timestamp; mutations remain pending/disabled rather than claimed successful | Wails acceptance test when that client is implemented. |
| QS‑5.4 | Reduced motion / high contrast OS settings | Open any visual client | Animations off, contrast >= WCAG AA | Client-specific accessibility review/tests. |

### QG‑6 Usability and aesthetics

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑6.1 | First‑time look at the page | User glances for 3 s | Can tell how many items need attention and whether any build is broken | Attention count in tile title & browser tab title (`(3) zorgscope`); UI review by owner. |
| QS‑6.2 | Any tile | — | Colour is never the only carrier of meaning (badges have text; icons have labels) | axe + review. |
| QS‑6.3 | Any state | Empty, loading, error, stale | Each visual client has a designed representation, no raw errors or ambiguous blank state | Visual regression/accessibility tests per client. |
| QS‑6.4 | Typography & layout | — | Each visual client documents and consistently applies typography, spacing and semantic-colour tokens | Design review; token source exists per client. |

### Maintainability (cross‑cutting)

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑7.1 | Repository | An unfamiliar agent picks a plan task | Task lists files, tests to write first, acceptance command; no task > ~2 h for a competent developer | Plan review. |
| QS‑7.2 | Codebase | `make check` | Passes: gofmt, vet, golangci‑lint (errcheck, staticcheck, gosec, revive), tests, docs lint | CI green. |
| QS‑7.3 | Domain package | — | Zero imports outside std lib; ≥ 90 % coverage | `depguard` rule + coverage gate. |
| QS‑7.4 | Any adapter | Upstream API change | Only that adapter and its fixtures change | Architecture rule: adapters are the only packages importing API clients (`depguard`). |
| QS‑7.5 | Any change | PR opened | CI runs lint, unit, integration, e2e in ≤ 10 min | Workflow timing. |
