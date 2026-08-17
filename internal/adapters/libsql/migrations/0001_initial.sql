-- 0001_initial: the schema of spec §5.
--
-- Times are RFC 3339 UTC strings ("2026-08-17T10:00:00Z"); the zero time is the empty string.
-- That format sorts and compares lexicographically, which is what the refresh lease relies on.

CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);

-- first_seen_at is written when a row is inserted and never updated again: that is the whole
-- point of the table (FR-5.3 AC2, QS-1.2). See upsertItemSQL in store.go.
CREATE TABLE IF NOT EXISTS items (
  source          TEXT,
  external_id     TEXT,
  kind            TEXT,
  repo            TEXT,
  number          INTEGER,
  title           TEXT,
  url             TEXT,
  author          TEXT,
  state           TEXT,
  created_at      TEXT,
  updated_at      TEXT,
  due_at          TEXT,
  priority        INTEGER,
  first_seen_at   TEXT NOT NULL,
  last_fetched_at TEXT NOT NULL,
  payload         TEXT,
  PRIMARY KEY (source, external_id)
);

CREATE TABLE IF NOT EXISTS builds (
  repo        TEXT PRIMARY KEY,
  workflow    TEXT,
  conclusion  TEXT,
  status      TEXT,
  run_url     TEXT,
  finished_at TEXT,
  fetched_at  TEXT
);

CREATE TABLE IF NOT EXISTS metrics (
  site           TEXT,
  window_days    INTEGER,
  visitors       INTEGER,
  pageviews      INTEGER,
  prev_visitors  INTEGER,
  prev_pageviews INTEGER,
  fetched_at     TEXT,
  PRIMARY KEY (site, window_days)
);

-- "trigger" is a SQL keyword, so it is quoted here and in every statement that touches it.
CREATE TABLE IF NOT EXISTS refresh_run (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  started_at  TEXT,
  finished_at TEXT,
  "trigger"   TEXT,
  ok          INTEGER,
  detail      TEXT
);

CREATE TABLE IF NOT EXISTS source_state (
  source          TEXT PRIMARY KEY,
  last_success_at TEXT,
  last_error      TEXT,
  last_error_at   TEXT,
  item_count      INTEGER
);

-- app_state holds last_visit_at and the refresh lease (holder|expiry under key refresh_lease).
CREATE TABLE IF NOT EXISTS app_state (
  key   TEXT PRIMARY KEY,
  value TEXT
);

-- One row per item already announced, so a notification is sent at most once (FR-6.1 AC2).
CREATE TABLE IF NOT EXISTS notified (
  key     TEXT PRIMARY KEY,
  sent_at TEXT
);

CREATE INDEX IF NOT EXISTS items_source_idx ON items(source);
CREATE INDEX IF NOT EXISTS items_first_seen_idx ON items(first_seen_at);
