# ADR-0008: Daily snapshots define "new"; dismissals bound to updated_at

* Status: accepted
* Date: 2026-08-16
* Deciders: Gernot Starke
* Related: FR-2.2, FR-2.7, FR-7.x, QS-1.x, R-1

## Context and problem statement

"Don't miss anything" needs a precise, testable definition of *new* that survives restarts and applies to
all item kinds, plus a way to acknowledge items early. The owner explicitly asked for a store of
"yesterday's" ids.

## Considered options

1. Daily snapshot of ids per source; new = not in previous snapshot; optional manual dismissal that expires when the item's `updated_at` changes.
2. Manual acknowledgement only ("mark seen").
3. "New since last page view" only (no persistence beyond a timestamp).
4. Persist per‑item `first_seen` and define new = first_seen within last 24 h.

## Decision outcome

**Chosen option: 1**, complemented by a lightweight "since last visit" marker (FR‑7.4, based on
`first_seen`, which we store anyway). Snapshot at a configurable time (default 03:00 Europe/Berlin), catch‑up
at start if missed, retention 30 d. First‑day fallback: new = created within 24 h.

### Consequences

* Good: deterministic and explainable ("new since yesterday 03:00"); identical rule for issues, PRs,
  mentions, articles, workflow runs; property‑testable; zero clicks needed but early dismissal possible;
  dismissals cannot hide subsequent activity because they are keyed to `updated_at`.
* Bad: an item created and answered between two snapshots still shows as new for a day (correct — you should
  see it once); requires persistence (SQLite, ADR‑0004).
* Detail: "previous snapshot" is the latest one older than the current *snapshot day* (the date of the last
  scheduled snapshot time ≤ now), so highlights last between 24 h and 48 h — never less than a full day.
