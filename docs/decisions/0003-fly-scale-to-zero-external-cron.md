# 0003. Fly.io scaled to zero with an external cron trigger

* Status: partially superseded by [0010](0010-stateless-no-database.md) — the cron-triggered
  refresh half only; scale to zero itself stays, unchanged from the decision below.
* Date: 2026-08-17
* Requirements: C‑3, C‑5, C‑8, QS‑2.1, QS‑2.4, QS‑3.1

## Context and problem statement

The previous design assumed "an always-on Fly Machine with a persistent volume [and] an in-process
scheduler" (the 2026-08-17 reset design, "Why the reset" — removed in the stateless reset, see git
history). C‑3 now fixes `min_machines_running = 0`: no process survives between requests. A process
that survives between requests is exactly what an in-process scheduler needs to exist — a
`time.Ticker` running in a goroutine does nothing once the machine that hosts it is stopped. The
question is how a refresh happens at all when nothing is running to notice that fifteen minutes
have passed.

## Considered options

* An always-on Fly Machine with an in-process scheduler (`time.Ticker` or similar) driving the
  refresh loop — the previous design.
* Scale to zero, with `POST /api/refresh` triggered by an external scheduler (cron-job.org).
* `min_machines_running = 1` (an always-on machine) but without an in-process scheduler — the same
  external trigger as the chosen option, just against a machine that never stops.

## Decision outcome

Chosen: **scale to zero, refreshed by an external cron trigger**, because it is the only option
that satisfies C‑3 as written and keeps the machine's running time, and therefore its cost,
proportional to actual use rather than to wall-clock time (C‑8).

`deploy/fly.toml` states the shape directly:

```toml
[http_service]
  auto_stop_machines = "stop"
  auto_start_machines = true
  min_machines_running = 0
```

cron-job.org calls `POST /api/refresh` with the `REFRESH_SECRET` bearer at the configured interval,
the same pattern `status.arc42.org-site` already uses for its availability prober (design §1,
§3). `cmd/zorgscope/main.go`'s package comment states the consequence plainly: "The process is
deliberately stateless and short-lived ... there is no background scheduler here. Upstream data is
refreshed by an external cron service calling `POST /api/refresh` (ADR-0003)." The handler itself
is Task 12's work and does not exist yet at the time of writing; the design commits to it, and this
record documents why, not the handler's code.

Two things wake the machine, and the design leans on both: cron-job.org's ping, and an ordinary
browser visit. Because the ping runs at the refresh interval, a user opening the dashboard usually
meets a warm machine — the ping is a side-effect free warmer, which is what makes QS‑2.4 ("the
machine is already awake ... between two pings") plausible rather than wishful.

### Consequences

* Good: hosting cost tracks actual traffic. A machine that is asked to do nothing between refreshes
  runs for seconds per cycle, not continuously, which is what keeps QS‑3.1's ≤ 1 €/month figure
  realistic.
* Good: the refresh interval becomes configuration external to the binary (cron-job.org's own
  schedule) rather than code, matching C‑5's stated consequence directly.
* Bad: the constraint this decision exists to satisfy is also its cost. Scale-to-zero cannot poll —
  there is no process to hold a ticker between requests — so *every* refresh must be triggered from
  outside, with no fallback to "just start a background loop if the external trigger is unreliable"
  without breaking C‑3. If cron-job.org stops calling, nothing else notices until a human does; the
  design's mitigation (design §10) is that the dashboard shows the age of the last successful run
  and a user-triggered refresh is always available, not that the system self-heals.
* Bad: the very first request after an idle period pays a real cold-start cost — QS‑2.1 budgets
  2.5 s at the 95th percentile — that an always-on machine would never pay. This is the price
  scale-to-zero explicitly charges in exchange for its cost profile.
* Neutral: the browser handler never contacts an upstream service (design §3); it only reads what
  the last refresh already stored, so a slow refresh never turns into a slow page load.

## Pros and cons of the options

### Always-on machine with an in-process scheduler

* Good: no external dependency on cron-job.org's availability; the refresh loop runs as long as the
  process does, and the machine never pays a cold-start penalty for an ordinary visit.
* Bad: contradicts C‑3 directly — "nothing may rely on an in-process scheduler" is stated as the
  constraint's consequence, not inferred from it. It also runs the machine continuously, which
  moves cost from "proportional to a few daily visits and periodic refreshes" to "proportional to
  the month", working against C‑8.

### Scale to zero with an external cron trigger

* Good: see Decision outcome above.
* Bad: see Consequences above.

### Always-on machine (`min_machines_running = 1`) with the same external trigger

* Good: this is the closest alternative, and the design's own risk table (§10) names it explicitly
  as the fallback if the cold-start budget turns out not to hold in practice: "shorten the interval
  or accept `min_machines_running = 1` at a few euros a month." It removes QS‑2.1's cold-start risk
  entirely while keeping the external-trigger pattern C‑5 asks for.
* Bad: "a few euros a month" is not free, and C‑8's ≤ 1 €/month budget is written to be met by free
  tiers, not stretched by a standing machine. This option is held in reserve rather than chosen
  because QS‑2.1 has not yet been shown to fail against the cheaper option; adopting it now would
  be paying for a problem not yet observed.
