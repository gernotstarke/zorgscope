# 5. Quality requirements

Written as [Q42](https://quality.arc42.org) scenarios: **Context → Stimulus → Response → Measure**.
The measure is a number or a named check, never an adjective. Ids `QS‑<quality goal>.<n>` refer to
the quality goals in [chapter 1](01-goals.md).

Every scenario below was verified, on 2026-09-15, to still have a test or a lint rule behind it
after the stateless reset; a scenario that did not — because it measured a database, a refresh run,
an image build, or a cron-warmed cold start that no longer holds — was retired rather than rewritten
to fit. A first review of that cut (2026-09-16, "fix round 1") found four ids — QS‑2.3, QS‑2.5,
QS‑4.1, QS‑5.2 — that were dropped even though the code still cites them against real, passing
tests; they are restored below, two of them (QS‑2.3, QS‑2.5) with their scenario text narrowed to
what is actually tested now rather than the cron/refresh-run framing they were written against
originally. See [ADR‑0010](../decisions/0010-stateless-no-database.md). Retired ids are not reused.

## 5.1 Quality tree

```text
zorgscope
├── QG‑1 Completeness           per-repository resilience, pagination completeness
├── QG‑2 Speed                  page weight, pagination that cannot hang, a page that never waits for GitHub
├── QG‑3 Frugality              GitHub request budget
├── QG‑4 Confidentiality        secret handling, authentication, transport, route-table coverage
└── QG‑5 Maintainability        isolation of the domain, per-adapter isolation, CI turnaround
```

## 5.2 QG‑1 Completeness

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑1.4 | GitHub returns 500, a rate-limit response or times out for one configured repository | A page view is stale and pays a fetch | The other repositories are still returned; the fetch returns no items for the failing one but reports the error rather than failing outright; the cache fills that repository in from the previous snapshot without advancing its fetched-at time, and the page shows the notice | `TestFetchReportsFailureButKeepsGoodRepos` (`internal/adapters/github`) asserts the good repositories' items come back alongside the named failure; `TestAFailingSourceShowsTheNoticeAndKeepsThePreviousList`, `TestPartialResultIsKeptTogetherWithItsError`, `TestAPartialFetchKeepsTheFailingRepositorysItemsAndTheOldFetchedAt` and `TestAPartialFetchKeepsTheFailingRepositoryAndTheSeenAtOfTheGoodFetch` (`internal/snapshot`, `internal/web`) assert the cache and the page never discard what they already had. |
| QS‑1.5 | A repository has more than one page of open issues or pull requests | A fetch runs | Every open item is fetched, page after page, until GitHub reports no more | `TestFetchFollowsPagination` (`internal/adapters/github`), a contract test against a paginated fixture asserting the stored count equals the fixture count. |

## 5.3 QG‑2 Speed

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑2.3 | The representative configuration (10 repositories, ~150 open items between them) | The dashboard or the Sites view is rendered, unfiltered | The response is small enough for a slow connection | ≤ 150 kB uncompressed HTML plus ≤ 50 kB of static assets on the wire (gzip-negotiated), including the vendored htmx and `search.js`. `TestRenderedPageStaysInsideItsBudget` and `TestStaticAssetsFitTheirBudgetOnTheWire` (`internal/web/dashboard_test.go`) assert both halves of the budget against the rendered fixture, and `TestSitesPageStaysInsideItsBudget` (`internal/web/sites_test.go`) holds the Sites view to the same 150 kB. The wait page (FR‑1.9) has its own measure: at most 20 kB of HTML and at most 100 kB of static assets on the wire, asserted by `TestWaitPageStaysInsideItsBudget` (`internal/web/waiting_test.go`). |
| QS‑2.5 | GitHub's pagination cursor stalls — repeated or empty — or a connection never reports `hasNextPage: false` | A fetch runs against such an upstream | The fetch stops with a named error rather than looping forever: a forward-progress check catches a cursor that does not move, and a hard page cap (`maxPages = 100`) backstops a cursor that genuinely advances but never terminates | `TestFetchStopsWhenCursorNeverAdvances` and `TestFetchStopsAtPageCap` (`internal/adapters/github`) each assert `Fetch` returns within a bounded time — rather than hanging the caller — when driven against a handler built to misbehave exactly one of those two ways. |
| QS‑2.6 | A list that is empty or stale | A page view while GitHub has not answered | The page answers without waiting for GitHub: the wait page, and the fetch runs on | With a source that blocks until the test releases it, `GET /` answers within 200 ms, asserted by `TestPageNeverWaitsForGitHub` (`internal/web/waiting_test.go`); `Get` of the cache returns at once with `Fetching` set, asserted by `TestFirstGetReturnsAtOnceWithNothingWhileFetching` (`internal/snapshot`). |
| QS‑2.7 | The representative configuration (10 repositories) | A fetch runs against a source that answers each request after 200 ms | The twenty requests run side by side | The fetch completes within 1 s, asserted by `TestFetchRunsRepositoriesSideBySide` (`internal/adapters/github`); the result keeps configuration order, asserted by `TestFetchKeepsConfigurationOrder`. |

## 5.4 QG‑3 Frugality

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑3.5 | The representative configuration (10 repositories) | A page view is stale and pays a fetch | GitHub's GraphQL endpoint is queried at most 20 times — one request read as one point-equivalent, the reading the requirement's own assertion clause prescribes — so that GitHub's rate limit is never a constraint. The measure counts requests; a connection nested in a node — the labels of an item (FR‑1.10) — raises GitHub's point cost of a query without raising the count, and is kept small (`first: 5`) for that reason. | `TestGraphQLRequestBudget` (`internal/adapters/github/cost_test.go`) counts requests against a fake server and asserts the exact count: 10 repositories × 2 queries (issues and pull requests paginate independently) = 20, met with no headroom. |

## 5.5 QG‑4 Confidentiality

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑4.1 | Production | An anonymous request to the dashboard, the list fragment `GET /items`, or any session-only `POST` (`/refresh`, `/logout`) | No data is returned: a browser navigation is redirected to sign-in, anything else gets 401 | `TestEveryProtectedRouteRefusesAnonymousAccess` and `TestEveryRouteIsEitherDeliberatelyPublicOrRefusesAnonymousAccess` (`internal/web`) assert the behaviour for every route in the router's route table, so a new route cannot be forgotten — the table's own type makes a forgotten auth field fail closed rather than open. |
| QS‑4.2 | Production | Repeated refused sign-in callbacks — a mismatched state, a cancelled or failed code exchange, a visitor without push access | The OAuth state is compared in constant time, refused callbacks are rate-limited by an in-memory bucket keyed by client IP, and neither the authorization code, the state nor the visitor's GitHub token ever reaches a log | `subtle.ConstantTimeCompare` in the callback path (`internal/web/signin.go`); `TestRefusedCallbacksAreRateLimited`, `TestASignInLockoutRecoversAsTheClockAdvances`, `TestASignInBeyondTheBudgetNeverReachesGitHub` and `TestTheLastAttemptInTheBudgetCanStillSignIn` cover the budget and its recovery; `TestNoSignInResponseOrLogLineCarriesACodeStateTokenOrSecret` and `TestNoAcceptedSignInResponseOrLogLineCarriesACodeStateTokenOrSecret` cover the log. |
| QS‑4.3 | Any operating state | Logs, HTTP responses and error pages are inspected | No client secret, derived session key, authorization code, visitor token or other configured secret appears | `TestNoResponseEverContainsASecret` and `TestRedactCoversEveryFieldOfSecrets` (`internal/web`) configure a canary value per secret, exercise the whole surface including its error paths, and search the output for them; `TestErrorNeverContainsSecretValues` (`internal/config`) does the same for configuration errors; `TestErrorNoticeNeverContainsASecret` (`internal/web`) covers the upstream-failure notice specifically. |
| QS‑4.4 | Production responses | Any request | Transport and browser hardening are explicit | `Content-Security-Policy` without `unsafe-inline`, `Strict-Transport-Security`, `X-Content-Type-Options: nosniff` and `Referrer-Policy: strict-origin-when-cross-origin` are set on every response, including 404s and 405s; no directive names an external host — every image comes from this origin or a `data:` URI. `TestSecurityHeaders`, `TestSecurityHeadersAreOnEveryResponse`, `TestNoRenderedHTMLNeedsUnsafeInline` and `TestTheContentSecurityPolicyNamesNoExternalHost` (`internal/web`) assert it. |
| QS‑4.5 | The dependency set | A new Go vulnerability is published | CI fails until it is resolved | `govulncheck` runs as its own step in `.github/workflows/ci.yml` and is a required check. |

## 5.6 QG‑5 Maintainability

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑5.1 | `internal/domain` | The package is compiled | It depends on nothing but the standard library | The `domain` `depguard` rule in `.golangci.yml`; statement coverage ≥ 90 %, gated in both `make check` and CI (`ci.yml`'s "Domain coverage gate" step) by running `go test -coverprofile` against `internal/domain/...` alone and failing under the threshold. |
| QS‑5.2 | GitHub's GraphQL API changes its response shape | The change is implemented | Only `internal/adapters/github` and its fixtures change | The `github-client` `depguard` rule in `.golangci.yml` denies `github.com/shurcooL/githubv4` to every package outside `internal/adapters/github`, so a client-library import anywhere else is a lint failure, not a review comment; `internal/adapters/github/issues.go`'s own package comment names it the one package allowed to import it. |
| QS‑5.3 | Any change | A pull request is opened | Vet, lint, race tests, the domain coverage gate and `govulncheck` run as one job | CI completes in ≤ 3 minutes; `.github/workflows/ci.yml` runs the toolchain directly rather than through the Docker-only `make` targets specifically to stay inside that budget (its own comment says so), with `timeout-minutes: 10` as the hard stop. `make check` reproduces the same checks locally — plus `markdownlint-cli2` and `fly config validate --strict`, which are local-only concerns CI does not repeat. |
