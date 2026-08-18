# 0001. Go modular monolith with a hexagonal core

* Status: accepted
* Date: 2026-08-17
* Requirements: C‑1, C‑2, C‑8, QG‑5, QS‑5.1, QS‑5.2

## Context and problem statement

zorgscope is one Go binary (C‑1) built and extended largely by LLM agents working one task at a
time (QG‑5, and QS‑5.4: "an unfamiliar agent picks a task ... and takes under about two hours").
Four upstream services (GitHub, Plausible, Todoist, Slack) and one persistence layer (libSQL) each
change on their own schedule and each pull in their own client code. The question is how much
internal structure a single-binary, single-user tool should carry: enough that an upstream change
stays contained in one file, not so much that a two-hour task needs to touch five packages to add
a source.

## Considered options

* A flat package layout: `internal/{github,plausible,todoist,libsql}` calling each other directly,
  with shared types wherever it was convenient to put them.
* A modular monolith with a hexagonal core: `internal/domain` importing nothing but the standard
  library, `internal/ports` declaring the interfaces, one `internal/adapters/*` package per
  upstream, `internal/refresh` orchestrating.
* Independently deployed microservices, one per source, behind an API gateway.

## Decision outcome

Chosen: **a modular monolith with a hexagonal core**, because it is the only option that lets a
lint rule — not a review comment — enforce the isolation QG‑5 asks for, while staying one binary
on one Fly Machine as C‑3 and C‑8 require.

The enforcement is `.golangci.yml`'s `depguard` settings, not a convention documented and hoped
for:

```yaml
domain:
  files:
    - "**/internal/domain/**"
  allow:
    - "$gostd"
    - "github.com/gernotstarke/zorgscope/internal/domain"
github-client:
  files:
    - "!**/internal/adapters/github/**"
  deny:
    - pkg: "github.com/shurcooL/githubv4"
      desc: "the GitHub client belongs in internal/adapters/github (QS-5.2)"
db-driver:
  files:
    - "!**/internal/adapters/libsql/**"
  deny:
    - pkg: "github.com/tursodatabase/libsql-client-go"
      desc: "the libSQL driver belongs in internal/adapters/libsql (QS-5.2)"
```

`internal/domain` cannot import `github.com/shurcooL/githubv4` or the libSQL driver even by
accident; the build fails, not the code review. The package layout on disk
(`internal/domain`, `internal/ports`, `internal/adapters/github|plausible|todoist|slack|libsql`,
`internal/refresh`, `internal/config`, `internal/web`) is the same shape §4 ("Structure") of the
[design](../superpowers/specs/2026-08-17-zorgscope-reset-design.md) describes.

### Consequences

* Good: an upstream API change is contained to its adapter package and its fixtures (QS‑5.2); the
  domain's new-detection rule (`internal/domain/item.go`) is tested with the standard library only
  and needs no fake server, container or network.
* Good: adding a fifth source (GitHub mentions, FR‑2.4, v2) is "one adapter satisfying
  `ports.SourceFetcher`, one configuration fragment, one fixture set and one registry line" per
  QS‑5.5 — a shape an agent can hold in one task.
* Bad: more packages and more indirection than the problem strictly needs for a single user. A
  change that touches both a data shape and its two adapters still means editing three files for
  what could, in a smaller tool, be one.
* Neutral: the ports package (`SourceFetcher`, `Store`, `Notifier`, `Clock`) exists purely to be
  implemented by adapters and consumed by `internal/refresh` and `internal/web`; it has no logic of
  its own.

## Pros and cons of the options

### Flat package layout

* Good: fewer files, fewer indirections, faster to read start to finish for a project this small.
* Bad: nothing stops a domain type from picking up a field only `githubv4` knows how to fill, or a
  handler from importing the libSQL driver directly — the isolation QG‑5 wants would depend on
  everyone remembering, not on the linter catching it. For a project meant to be extended by agents
  working from documentation rather than tribal knowledge, that is the wrong place to economise.
  This was close: for a project this size, a flat layout would work functionally, and the domain's
  tests would still pass. It loses on QS‑5.2's enforceability, not on capability.

### Modular monolith with a hexagonal core

* Good: see Decision outcome above.
* Bad: see Consequences above.

### Independently deployed microservices

* Bad: contradicts C‑3 (one Fly Machine, scaled to zero) and C‑8 (≤ 1 €/month): four services would
  mean four machines, four cold starts, and inter-service calls that a single in-process function
  call currently is. Splitting a personal, single-user tool into services trades one deployable for
  several without a scaling reason to justify it.
* Good: none that apply here — the isolation benefit microservices normally buy is already bought
  by `depguard` at zero infrastructure cost.
