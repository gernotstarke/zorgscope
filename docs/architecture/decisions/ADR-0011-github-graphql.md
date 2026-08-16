# ADR-0011: GitHub GraphQL API for repository data, REST for notifications

* Status: accepted
* Date: 2026-08-16
* Deciders: Gernot Starke
* Related: FR-2.x, FR-3.x, C-12, R-3

## Context and problem statement

Per repo we need open issues and PRs including the last comment/review author (for "unanswered"), labels,
timestamps, review decision, plus the latest workflow run on the default branch — for ~8 (growing) repos
every ~10 minutes, well within rate limits.

## Considered options

1. GraphQL v4: one query per repo (paginated) returning issues + PRs with `comments(last:1)`, `reviews(last:1)`, `timelineItems` as needed, plus `defaultBranchRef … checkSuites/workflow runs`; REST `/notifications` for mentions/review requests (not available in GraphQL).
2. REST v3 only: `/issues` (issues + PRs), then per‑item `/comments?per_page=1&direction=desc` — N+1 requests.
3. GitHub Search API only.

## Decision outcome

**Chosen option: 1** with a hand‑written query string and a small typed response struct (no codegen). Use
`rateLimit { cost remaining resetAt }` in every query to feed backoff. Notifications via REST with
`If-Modified-Since`/`Last-Modified` polling.

### Consequences

* Good: ~1–2 requests per repo per poll; all data for the attention rules in one shot; cost visibility.
* Bad: GraphQL error handling is more nuanced (partial data + errors); fixtures are larger. REST N+1 rejected
  for rate‑limit reasons; Search API rejected (rate limit 30/min, eventual consistency).
