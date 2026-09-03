-- Runtime installs adopted, downloaded, or built on this host
CREATE TABLE installs (
  id TEXT PRIMARY KEY,
  runtime_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  path TEXT NOT NULL,
  dir TEXT NOT NULL,
  version TEXT NOT NULL DEFAULT '',
  origin TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX installs_by_runtime ON installs (runtime_id, created_at);

CREATE TABLE install_facts (
  install_id TEXT NOT NULL REFERENCES installs (id) ON DELETE CASCADE,
  key TEXT NOT NULL,
  value TEXT NOT NULL,
  PRIMARY KEY (install_id, key)
);

-- Learned estimator corrections per runtime and architecture
CREATE TABLE calibrations (
  runtime_id TEXT NOT NULL,
  architecture TEXT NOT NULL,
  overhead_delta REAL NOT NULL,
  samples INTEGER NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (runtime_id, architecture)
);

-- Runtime processes serving stored models, live and historical
CREATE TABLE instances (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  source_id TEXT NOT NULL,
  repo TEXT NOT NULL,
  weight_group TEXT NOT NULL,
  runtime_id TEXT NOT NULL,
  install_id TEXT NOT NULL,
  endpoint TEXT NOT NULL,
  state TEXT NOT NULL,
  pid INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  task_id TEXT NOT NULL DEFAULT '',
  desired_running INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  ready_at TEXT,
  stopped_at TEXT
);
CREATE INDEX instances_by_created ON instances (created_at);
CREATE INDEX instances_by_name ON instances (name);

CREATE TABLE instance_params (
  instance_id TEXT NOT NULL REFERENCES instances (id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  value TEXT NOT NULL,
  PRIMARY KEY (instance_id, name)
);

CREATE TABLE instance_command (
  instance_id TEXT NOT NULL REFERENCES instances (id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  arg TEXT NOT NULL,
  PRIMARY KEY (instance_id, position)
);

-- The caller's run request, replayed on relaunch
CREATE TABLE instance_requests (
  instance_id TEXT PRIMARY KEY REFERENCES instances (id) ON DELETE CASCADE,
  source_id TEXT NOT NULL,
  repo TEXT NOT NULL,
  weight_group TEXT NOT NULL,
  runtime_id TEXT NOT NULL,
  install_id TEXT NOT NULL,
  name TEXT NOT NULL
);

CREATE TABLE instance_request_params (
  instance_id TEXT NOT NULL REFERENCES instances (id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  value TEXT NOT NULL,
  PRIMARY KEY (instance_id, name)
);

CREATE TABLE instance_plans (
  instance_id TEXT PRIMARY KEY REFERENCES instances (id) ON DELETE CASCADE,
  verdict TEXT NOT NULL,
  weights_bytes INTEGER NOT NULL,
  cache_bytes INTEGER NOT NULL,
  overhead_bytes INTEGER NOT NULL,
  detail TEXT NOT NULL DEFAULT ''
);

CREATE TABLE instance_plan_pools (
  instance_id TEXT NOT NULL REFERENCES instances (id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  pool_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  used_bytes INTEGER NOT NULL,
  capacity_bytes INTEGER NOT NULL,
  PRIMARY KEY (instance_id, position)
);

CREATE TABLE instance_plan_placements (
  instance_id TEXT NOT NULL REFERENCES instances (id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  kind TEXT NOT NULL,
  pool_id TEXT NOT NULL,
  bytes INTEGER NOT NULL,
  count INTEGER NOT NULL,
  PRIMARY KEY (instance_id, position)
);

CREATE TABLE instance_plan_params (
  instance_id TEXT NOT NULL REFERENCES instances (id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  value TEXT NOT NULL,
  PRIMARY KEY (instance_id, name)
);

CREATE TABLE instance_measurements (
  instance_id TEXT NOT NULL REFERENCES instances (id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  key TEXT NOT NULL,
  bytes INTEGER NOT NULL,
  line TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (instance_id, position)
);

CREATE TABLE instance_triage (
  instance_id TEXT NOT NULL REFERENCES instances (id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  rule_id TEXT NOT NULL,
  summary TEXT NOT NULL,
  hint TEXT NOT NULL,
  line TEXT NOT NULL,
  PRIMARY KEY (instance_id, position)
);

CREATE TABLE instance_triage_fixes (
  instance_id TEXT NOT NULL REFERENCES instances (id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  name TEXT NOT NULL,
  value TEXT NOT NULL,
  PRIMARY KEY (instance_id, position, name)
);

-- Long running operations and their logs
CREATE TABLE tasks (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  title TEXT NOT NULL,
  state TEXT NOT NULL,
  progress_done INTEGER NOT NULL DEFAULT 0,
  progress_total INTEGER NOT NULL DEFAULT 0,
  progress_message TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  started_at TEXT,
  finished_at TEXT
);
CREATE INDEX tasks_by_created ON tasks (created_at);

CREATE TABLE task_labels (
  task_id TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
  key TEXT NOT NULL,
  value TEXT NOT NULL,
  PRIMARY KEY (task_id, key)
);

CREATE TABLE task_logs (
  task_id TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  line TEXT NOT NULL,
  PRIMARY KEY (task_id, position)
);
