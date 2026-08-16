# ADR-0001: Web application instead of a Wails desktop app

* Status: accepted
* Date: 2026-08-16
* Deciders: Gernot Starke
* Related: FR-1.1, QG-5, C-2, C-3

## Context and problem statement

The initial idea mentioned Wails (Go + webview desktop shell). At the same time the dashboard must (a) be
the browser's new‑tab page, (b) run locally purely under Docker, and (c) optionally run on fly.io. A Wails
window is not a browser tab, cannot run headless in a container, and would require a local Go/Wails/Xcode
toolchain.

## Decision drivers

* New‑tab page = a URL → needs an HTTP server.
* Docker‑only local toolchain (C‑2); same artefact locally and hosted (C‑3).
* Reachable from several devices/browsers (QG‑5).

## Considered options

1. Go HTTP server + web UI (browser is the shell).
2. Wails desktop app.
3. Both: shared core, web server primary, optional Wails wrapper.

## Decision outcome

**Chosen option: 1** — the owner dropped the Wails requirement once the conflict was explicit. Backend stays
100 % Go. Option 3 remains possible later because the core is UI‑agnostic (ADR‑0002), but is YAGNI now.

### Consequences

* Good: one deployable, works in every browser and on the phone, trivial to host, testable with standard web tooling.
* Bad: no native OS integration (notifications, menu bar) — not required.
