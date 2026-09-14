-- 0005_github_only: Plausible and Todoist are gone (design 2026-09-14 §3). The metrics table held
-- Plausible's numbers and nothing else; Todoist's items and both sources' health rows go with
-- them; the two columns only a task ever filled leave the items table. GitHub rows are untouched,
-- first_seen_at included — that column is the product (ADR-0006) and no clean-up may rewrite it.
DROP TABLE IF EXISTS metrics;
DELETE FROM items WHERE source = 'todoist';
DELETE FROM source_state WHERE source IN ('plausible', 'todoist');
DELETE FROM notified WHERE key LIKE 'todoist|%';
ALTER TABLE items DROP COLUMN due_at;
ALTER TABLE items DROP COLUMN priority;
