# ADR-0013: Documentation formats — req42, arc42, MADR in Markdown

* Status: accepted
* Date: 2026-08-16
* Deciders: Gernot Starke
* Related: C-8, G-4

## Context and problem statement

Requirements, architecture and decisions must be part of the repository, exemplary, and directly readable on
GitHub by humans and agents.

## Considered options

1. Markdown: `docs/requirements` after req42 (pragmatic subset), `docs/architecture` after arc42 (one file per chapter), ADRs as MADR, Mermaid diagrams, stable ids.
2. AsciiDoc with generated HTML/PDF.
3. Single README.

## Decision outcome

**Chosen option: 1.** GitHub renders Markdown and Mermaid natively; no docs toolchain needed beyond
`markdownlint` and `lychee` in `make docs-check`. AsciiDoc would be the owner's usual arc42 format but adds a
build step for no reader benefit here.

### Consequences

* Good: zero build, diffs readable, ids traceable from plans/tests/commits.
* Bad: Markdown tables are less comfortable than AsciiDoc for wide content; kept narrow.
