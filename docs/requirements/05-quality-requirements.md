# 5. Quality requirements

Written as [Q42](https://quality.arc42.org) scenarios: **Context → Stimulus → Response → Measure**.
The measure is a number or a named check, never an adjective. Ids `QS‑<quality goal>.<n>` refer to
the quality goals in [chapter 1](01-goals.md).

Every scenario below was verified, on 2026-09-15, to still have a test or a lint rule behind it
after the stateless reset; a scenario that did not — because it measured a database, a refresh run,
an image build, or a warm-cache assumption that no longer holds — was retired rather than rewritten
to fit. See [ADR‑0010](../decisions/0010-stateless-no-database.md). Retired ids are not reused.

## 5.1 Quality tree

```text
zorgscope
├── QG‑1 Correctness of "new"   per-repository resilience, pagination completeness
├── QG‑3 Frugality              GitHub request budget
├── QG‑4 Confidentiality        secret handling, authentication, transport
└── QG‑5 Maintainability        isolation of the domain, CI turnaround
```

## 5.2 QG‑1 Correctness of "new"

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑1.4 | GitHub returns 500, a rate-limit response or times out for one configured repository | A page view is stale and pays a fetch | The other repositories are still returned; the failing one keeps no items of its own but the fetch as a whole reports the error rather than failing outright, and the page keeps the previous snapshot and shows the notice | `TestFetchReportsFailureButKeepsGoodRepos` (`internal/adapters/github`) asserts the good repositories' items come back alongside the named failure; `TestAFailingSourceShowsTheNoticeAndKeepsThePreviousList` and `TestPartialResultIsKeptTogetherWithItsError` (`internal/snapshot`, `internal/web`) assert the cache and the page never discard what they already had. |
| QS‑1.5 | A repository has more than one page of open issues or pull requests | A fetch runs | Every open item is fetched, page after page, until GitHub reports no more | `TestFetchFollowsPagination` (`internal/adapters/github`), a contract test against a paginated fixture asserting the stored count equals the fixture count. |

## 5.3 QG‑3 Frugality

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑3.5 | The representative configuration (10 repositories) | A page view is stale and pays a fetch | GitHub's GraphQL endpoint is queried at most 20 times — one request read as one point-equivalent, the reading the requirement's own assertion clause prescribes — so that GitHub's rate limit is never a constraint | `TestGraphQLRequestBudget` (`internal/adapters/github/cost_test.go`) counts requests against a fake server and asserts the exact count: 10 repositories × 2 queries (issues and pull requests paginate independently) = 20, met with no headroom. |

## 5.4 QG‑4 Confidentiality

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑4.2 | Production | Repeated refused sign-in callbacks — a mismatched state, a cancelled or failed code exchange, a visitor without push access | The OAuth state is compared in constant time, refused callbacks are rate-limited by an in-memory bucket keyed by client IP, and neither the authorization code, the state nor the visitor's GitHub token ever reaches a log | `subtle.ConstantTimeCompare` in the callback path (`internal/web/signin.go`); `TestRefusedCallbacksAreRateLimited`, `TestASignInLockoutRecoversAsTheClockAdvances`, `TestASignInBeyondTheBudgetNeverReachesGitHub` and `TestTheLastAttemptInTheBudgetCanStillSignIn` cover the budget and its recovery; `TestNoSignInResponseOrLogLineCarriesACodeStateTokenOrSecret` and `TestNoAcceptedSignInResponseOrLogLineCarriesACodeStateTokenOrSecret` cover the log. |
| QS‑4.3 | Any operating state | Logs, HTTP responses and error pages are inspected | No client secret, derived session key, authorization code, visitor token or other configured secret appears | `TestNoResponseEverContainsASecret` and `TestRedactCoversEveryFieldOfSecrets` (`internal/web`) configure a canary value per secret, exercise the whole surface including its error paths, and search the output for them; `TestErrorNeverContainsSecretValues` (`internal/config`) does the same for configuration errors; `TestErrorNoticeNeverContainsASecret` (`internal/web`) covers the upstream-failure notice specifically. |
| QS‑4.4 | Production responses | Any request | Transport and browser hardening are explicit | `Content-Security-Policy` without `unsafe-inline`, `Strict-Transport-Security`, `X-Content-Type-Options: nosniff` and `Referrer-Policy: strict-origin-when-cross-origin` are set on every response, including 404s and 405s; no directive names an external host — every image comes from this origin or a `data:` URI. `TestSecurityHeaders`, `TestSecurityHeadersAreOnEveryResponse`, `TestNoRenderedHTMLNeedsUnsafeInline` and `TestTheContentSecurityPolicyNamesNoExternalHost` (`internal/web`) assert it. |
| QS‑4.5 | The dependency set | A new Go vulnerability is published | CI fails until it is resolved | `govulncheck` runs as its own step in `.github/workflows/ci.yml` and is a required check. |

## 5.5 QG‑5 Maintainability

| Id | Context | Stimulus | Response | Measure |
|----|---------|----------|----------|---------|
| QS‑5.1 | `internal/domain` | The package is compiled | It depends on nothing but the standard library | The `domain` `depguard` rule in `.golangci.yml`; statement coverage ≥ 90 %, gated in both `make check` and CI (`ci.yml`'s "Domain coverage gate" step) by running `go test -coverprofile` against `internal/domain/...` alone and failing under the threshold. |
| QS‑5.3 | Any change | A pull request is opened | Vet, lint, race tests, the domain coverage gate and `govulncheck` run as one job | CI completes in ≤ 3 minutes; `.github/workflows/ci.yml` runs the toolchain directly rather than through the Docker-only `make` targets specifically to stay inside that budget (its own comment says so), with `timeout-minutes: 10` as the hard stop. `make check` reproduces the same checks locally — plus `markdownlint-cli2` and `fly config validate --strict`, which are local-only concerns CI does not repeat. |
