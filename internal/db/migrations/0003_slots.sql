-- Reservations of devices and memory with one public name each
CREATE TABLE slots (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  description TEXT NOT NULL DEFAULT '',
  runtime_id TEXT NOT NULL DEFAULT '',
  memory_bytes INTEGER NOT NULL DEFAULT 0,
  instance_id TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  task_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE slot_devices (
  slot_id TEXT NOT NULL REFERENCES slots (id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  device_id TEXT NOT NULL,
  PRIMARY KEY (slot_id, position)
);

CREATE TABLE slot_params (
  slot_id TEXT NOT NULL REFERENCES slots (id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  value TEXT NOT NULL,
  PRIMARY KEY (slot_id, name)
);

-- The last run request, replayed on rollback and relaunch
CREATE TABLE slot_requests (
  slot_id TEXT PRIMARY KEY REFERENCES slots (id) ON DELETE CASCADE,
  source_id TEXT NOT NULL,
  repo TEXT NOT NULL,
  weight_group TEXT NOT NULL,
  runtime_id TEXT NOT NULL,
  install_id TEXT NOT NULL,
  name TEXT NOT NULL
);

CREATE TABLE slot_request_params (
  slot_id TEXT NOT NULL REFERENCES slots (id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  value TEXT NOT NULL,
  PRIMARY KEY (slot_id, name)
);

-- Public model names the gateway answers for
CREATE TABLE routes (
  name TEXT PRIMARY KEY,
  instance_id TEXT NOT NULL DEFAULT '',
  slot_id TEXT NOT NULL DEFAULT '',
  endpoint TEXT NOT NULL DEFAULT '',
  api TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL,
  model TEXT NOT NULL DEFAULT '',
  requests INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL
);

ALTER TABLE instances ADD COLUMN slot_id TEXT NOT NULL DEFAULT '';
ALTER TABLE instance_requests ADD COLUMN slot_id TEXT NOT NULL DEFAULT '';

-- Repositories watched for new revisions and weight groups
CREATE TABLE watches (
  id TEXT PRIMARY KEY,
  source_id TEXT NOT NULL,
  repo TEXT NOT NULL,
  revision TEXT NOT NULL DEFAULT '',
  group_match TEXT NOT NULL DEFAULT '',
  auto_pull INTEGER NOT NULL DEFAULT 0,
  slot_id TEXT NOT NULL DEFAULT '',
  runtime_id TEXT NOT NULL DEFAULT '',
  last_commit TEXT NOT NULL DEFAULT '',
  checked_at TEXT,
  created_at TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX watches_by_repo ON watches (source_id, repo, revision);

CREATE TABLE watch_params (
  watch_id TEXT NOT NULL REFERENCES watches (id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  value TEXT NOT NULL,
  PRIMARY KEY (watch_id, name)
);

CREATE TABLE watch_groups (
  watch_id TEXT NOT NULL REFERENCES watches (id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  group_name TEXT NOT NULL,
  PRIMARY KEY (watch_id, position)
);

CREATE TABLE findings (
  id TEXT PRIMARY KEY,
  watch_id TEXT NOT NULL REFERENCES watches (id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  repo TEXT NOT NULL,
  commit_id TEXT NOT NULL DEFAULT '',
  group_name TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT '',
  task_id TEXT NOT NULL DEFAULT '',
  acknowledged INTEGER NOT NULL DEFAULT 0,
  found_at TEXT NOT NULL
);
CREATE INDEX findings_by_watch ON findings (watch_id, found_at);
