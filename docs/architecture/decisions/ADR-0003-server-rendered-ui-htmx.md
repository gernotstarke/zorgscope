# ADR-0003: Server‑rendered UI with html/template, htmx and hand‑written CSS

* Status: superseded by ADR-0014; implementation retained as a local-development client
* Date: 2026-08-16
* Deciders: Gernot Starke
* Related: QG-2, QG-5, QG-6, C-2, C-7, FR-1.x

## Context and problem statement

The UI is a grid of mostly read‑only tiles with a few actions (dismiss, refresh, login). It must load in
< 1 s, work in every browser incl. phones, look polished, and be maintainable by agents. The owner has no
frontend framework preference and wants no local toolchain beyond Docker.

## Decision drivers

* No JS/CSS build pipeline in Docker/CI if avoidable (C‑2, simplicity).
* Performance: minimal bytes, no client‑side rendering (QG‑2).
* Compatibility & progressive enhancement (QG‑5).
* Testability: HTML rendering testable in Go; e2e with Playwright.
* Single language for agents (Go) wherever possible.

## Considered options

1. Go `html/template` + htmx (vendored) + hand‑written CSS with design tokens.
2. `templ` (typed Go templates) + htmx + Tailwind (standalone CLI).
3. TypeScript SPA (Svelte/React) + Go JSON API.

## Decision outcome

**Chosen option: 1.** Templates parsed at start (`template.Must`), embedded via `embed.FS`; one partial per
tile; htmx (~15 kB, self‑hosted) provides periodic tile refresh and `hx-post` actions; a tiny self‑hosted
`auth.js` handles WebAuthn. CSS is written by hand against a small token set (`tokens.css`), no framework,
no preprocessing. Sparklines are inline SVG rendered server‑side.

### Consequences

* Good: zero Node in the toolchain, ~< 100 kB page, works without JS (except auto‑refresh/passkeys), golden
  tests for templates, easy for agents, strict CSP possible.
* Bad: no compile‑time template type checks (mitigated by view‑model structs + golden tests + `go vet`'s
  template checks being absent → we add a template‑execution test for every partial); richer interactions
  (drag & drop tile ordering) would need more JS later.
* Options 2/3 rejected: extra toolchain (templ generate / Tailwind / Node) for little benefit at this UI complexity.

ADR-0014 later made the authenticated client-neutral JSON API the product boundary and left the Wails
versus browser client decision open. The M1 templates remain useful locally but do not define the target UI.
