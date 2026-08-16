# ADR-0005: fly.io single always‑on machine with in‑process scheduler

* Status: accepted
* Date: 2026-08-16
* Deciders: Gernot Starke
* Related: FR-1.3, FR-1.4, FR-7.1, QG-2, QG-3, C-3

## Context and problem statement

The dashboard must be reachable from any device (hosted), data must be pre‑fetched continuously, and there
was an idea of "cron‑triggered backend jobs on fly.io".

## Considered options

1. One always‑on fly Machine running the web app with an internal scheduler (per‑source tickers) and a volume.
2. Web app with `auto_stop` (scale to zero) + separate fly scheduled Machines / cron worker writing to a shared store.
3. Local Docker only; fly only as poller/cache API.

## Decision outcome

**Chosen option: 1.** `min_machines_running = 1`, `auto_stop_machines = "off"`; scheduler goroutines with
jitter, single‑flight per source, exponential backoff; snapshotter runs in the same process (daily + catch‑up).
CI deploys on push to `main`.

### Consequences

* Good: simplest possible topology; SQLite works because there is exactly one writer; instant page loads from
  warm cache; ≈ 3–4 €/month.
* Bad: brief downtime on deploy; a crash loop would stop polling (mitigated by health checks/restarts and
  visible staleness). Two‑process design (option 2) would need a shared DB and more moving parts for no user
  benefit; option 3 fails the "works from any device" requirement.
