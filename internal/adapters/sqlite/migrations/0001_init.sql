CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);

CREATE TABLE items (
  source_id        TEXT NOT NULL,
  external_id      TEXT NOT NULL,
  kind             TEXT NOT NULL,
  title            TEXT NOT NULL DEFAULT '',
  url              TEXT NOT NULL DEFAULT '',
  author           TEXT NOT NULL DEFAULT '',
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  last_activity_by TEXT NOT NULL DEFAULT '',
  last_activity_at INTEGER NOT NULL DEFAULT 0,
  labels           TEXT NOT NULL DEFAULT '[]',
  payload          TEXT NOT NULL DEFAULT '',
  first_seen       INTEGER NOT NULL,
  PRIMARY KEY (source_id, external_id)
);
CREATE INDEX items_source_created ON items (source_id, created_at DESC);

CREATE TABLE snapshots (
  source_id TEXT NOT NULL,
  date      TEXT NOT NULL,
  taken_at  INTEGER NOT NULL,
  ids       TEXT NOT NULL,
  PRIMARY KEY (source_id, date)
);

CREATE TABLE dismissals (
  source_id    TEXT NOT NULL,
  external_id  TEXT NOT NULL,
  updated_at   INTEGER NOT NULL,
  dismissed_at INTEGER NOT NULL,
  PRIMARY KEY (source_id, external_id)
);

CREATE TABLE fetch_status (
  source_id    TEXT PRIMARY KEY,
  kind         TEXT NOT NULL DEFAULT '',
  last_success INTEGER NOT NULL DEFAULT 0,
  last_error   INTEGER NOT NULL DEFAULT 0,
  error_msg    TEXT NOT NULL DEFAULT '',
  next_run     INTEGER NOT NULL DEFAULT 0,
  item_count   INTEGER NOT NULL DEFAULT 0,
  duration_ms  INTEGER NOT NULL DEFAULT 0,
  in_flight    INTEGER NOT NULL DEFAULT 0,
  auth_failed  INTEGER NOT NULL DEFAULT 0
);
