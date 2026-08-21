# 5. Quality requirements

Written as [Q42](https://quality.arc42.org) scenarios: **Context → Stimulus → Response → Measure**.
The measure is a number or a named check, never an adjective. Ids `QS‑<quality goal>.<n>` refer to the
quality goals in [chapter 1](01-goals.md).

## 5.1 Quality tree

```text
zorgscope
├── QG‑1 Correctness of "new"   first-seen semantics, completeness, honest freshness
├── QG‑2 Speed                  cold start, warm render, payload size
├── QG‑3 Frugality              hosting cost, storage volume, request budget
├── QG‑4 Confidentiality        secret handling, authentication, transport
└── QG‑5 Maintainability        testability, isolation of the domain, analysability
```

## 5.2 QG‑1 Correctness of "new"

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑1.1 | Steady state, refresh interval *i* configured | Someone opens an issue in a configured repository at time *t* | The item appears with `NEW` on the dashboard | Visible at the latest at *t + i +* 60 s; measured by a test that injects an issue into the fake GitHub source and runs one refresh. |
| QS‑1.2 | Arbitrary sequences of refresh runs, with items appearing, changing and disappearing | The new-detection rule is evaluated | Exactly the items whose first-seen time is later than the last-visit time are new | Table-driven and property-style tests over generated sequences; the rule lives in one function in `internal/domain` with ≥ 95 % statement coverage. |
| QS‑1.3 | An item has been marked new and the user presses "mark all seen" | The dashboard is reloaded | No item is new; an item first seen after the click is new again | Store-level integration test against the libsql container. |
| QS‑1.4 | GitHub returns 500, a rate-limit response or times out for one repository | A refresh runs | The other repositories and the other sources are stored; the failing repository keeps its previous items and shows the error and its last success | Integration test against a fake server returning 500/403/timeouts; asserts stored item count is unchanged and the run record names the failure. |
| QS‑1.5 | A repository has more than 100 open issues and PRs | A refresh runs | Every open item is stored | Contract test against a paginated fixture; stored count equals fixture count. |
| QS‑1.6 | The Fly machine is stopped while a refresh is running | The next refresh runs | Data is consistent: sources fetched before the stop keep their items, the rest are refetched | Test that aborts the run context between sources and asserts per-source transactional commit. |
| QS‑1.7 | Two refreshes are triggered at once (cron and user) | Both arrive | Only one runs; the second is rejected with 409 | Handler test with a blocking fake fetcher; `go test -race` clean. |

## 5.3 QG‑2 Speed

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑2.1 | The Fly machine is stopped (scaled to zero) | The user opens the dashboard | The complete page is delivered | ≤ 2.5 s to last byte at the 95th percentile, measured over 20 cold opens with `curl -w` against the deployed app. |
| QS‑2.2 | The machine is running, data is in Turso | The user opens the dashboard | The complete page is delivered | ≤ 200 ms server time at the 95th percentile over 100 requests; asserted in a benchmark test against the local libsql container with a representative fixture. |
| QS‑2.3 | A representative configuration (10 repositories, 4 sites, 30 tasks) | The dashboard is rendered | The response is small enough for a slow connection | ≤ 150 kB uncompressed HTML plus ≤ 50 kB of static assets, including the vendored htmx; asserted by a test on the rendered fixture. |
| QS‑2.4 | cron-job.org pings at the configured interval | The user opens the dashboard between two pings | The machine is already awake, so QS‑2.2 applies rather than QS‑2.1 | Manual verification after deployment; the refresh interval is chosen so that the machine's idle-stop timeout is exceeded no more than once per interval. |
| QS‑2.5 | A refresh run with the representative configuration | `POST /api/refresh` | The run completes inside the cron trigger's timeout | ≤ 30 s wall clock, measured in the run record; the record's duration is asserted in an integration test against the fake sources. |

## 5.4 QG‑3 Frugality

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑3.1 | Production, one month of operation | The bill arrives | Hosting, database and cron cost stay inside the free or near-free tiers | ≤ 1 €/month across Fly and Turso, read from the invoices. |
| QS‑3.2 | The representative configuration, refreshed every 15 minutes for a month | Storage grows | The database stays far inside the Turso free tier | ≤ 50 MB total and ≤ 1 million row reads per month; checked with `db-size` against production. |
| QS‑3.3 | Steady state | The machine runs a refresh | Resource use fits the smallest Fly Machine | Peak RSS ≤ 128 MB on `shared-cpu-1x` with 256 MB; read from Fly metrics after 24 h. |
| QS‑3.4 | The image is built | It is pushed and started | The image is small enough to start quickly from cold | ≤ 25 MB, scratch base, non-root; asserted by `docker image inspect` in CI. |
| QS‑3.5 | A refresh run | GitHub is queried | The GitHub rate limit is never a constraint | ≤ 20 GraphQL point-equivalents per run, so that a 15-minute interval uses under 2 % of the hourly limit; asserted by counting requests against the fake server. |

## 5.5 QG‑4 Confidentiality

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑4.1 | Production | An anonymous request to the dashboard, a tile fragment or `/api/refresh` | No data is returned: a browser navigation is redirected to sign-in, anything else gets 401 | Handler test asserting the behaviour for every route in the router's route table, so a new route cannot be forgotten. |
| QS‑4.2 | Production | Repeated wrong tokens at the sign-in page or the refresh endpoint | Comparison is constant-time, attempts are rate-limited, and the submitted value never reaches a log | `subtle.ConstantTimeCompare` in the code path; log-capture test; rate-limit test asserting rejection after the configured count and acceptance of a valid credential afterwards. |
| QS‑4.3 | Any operating state | Logs, HTTP responses, error pages and the rendered configuration page are inspected | No upstream token, session key or webhook URL appears | Canary test: fake secrets with recognisable values are configured, the whole surface is exercised, and the output is searched for them. |
| QS‑4.4 | Production responses | Any request | Transport and browser hardening are explicit | HTTPS enforced by Fly; responses carry `Content-Security-Policy` without `unsafe-inline`, `Strict-Transport-Security`, `X-Content-Type-Options: nosniff` and `Referrer-Policy: strict-origin-when-cross-origin`; asserted by a handler test. `img-src` names exactly one external host, `https://img.shields.io`, for the build badges (FR‑2.3 AC5) — no wildcard, and every other directive stays `self`. |
| QS‑4.5 | The dependency set | A new Go vulnerability is published | CI fails until it is resolved | `govulncheck` runs in CI and is a required check. |

## 5.6 QG‑5 Maintainability

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑5.1 | `internal/domain` | The package is compiled | It depends on nothing but the standard library | `depguard` rule in `.golangci.yml`; statement coverage ≥ 90 %. |
| QS‑5.2 | An upstream API changes its response shape | The change is implemented | Only that adapter package and its fixtures change | `depguard` confines upstream client libraries to `internal/adapters/*`; verified by review of the resulting diff. |
| QS‑5.3 | Any change | A pull request is opened | Lint, unit tests, integration tests and documentation lint run | CI completes in ≤ 3 minutes; `make check` reproduces it locally. |
| QS‑5.4 | An unfamiliar agent picks a task from the implementation plan | It reads the task | The task names its files, its tests and the command that proves it done, and takes under about two hours | Plan review before execution. |
| QS‑5.5 | A new kind of source is added | The developer implements it | One adapter satisfying `ports.SourceFetcher`, one configuration fragment, one fixture set and one registry line suffice | Verified when FR‑2.4 (mentions) is implemented in v2. |
