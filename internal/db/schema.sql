-- The whole schema, what atlas diffs the migration directory against

-- Runtime installs adopted, downloaded, or built on this host
CREATE TABLE installs (
  id TEXT PRIMARY KEY,
  runtime_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  path TEXT NOT NULL,
  dir TEXT NOT NULL,
  version TEXT NOT NULL DEFAULT '',
  origin TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  build_id TEXT NOT NULL DEFAULT ''
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
  stopped_at TEXT,
  slot_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX instances_by_created ON instances (created_at);

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
  name TEXT NOT NULL,
  slot_id TEXT NOT NULL DEFAULT '',
  force INTEGER NOT NULL DEFAULT 0
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
  detail TEXT NOT NULL DEFAULT '',
  overhead_delta REAL NOT NULL DEFAULT 0
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

-- What the runtime's chat template accepted, one row once the instance was probed
CREATE TABLE instance_templates (
  instance_id TEXT PRIMARY KEY REFERENCES instances (id) ON DELETE CASCADE,
  late_system INTEGER NOT NULL DEFAULT 0,
  refusal TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT ''
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

-- Builds of recipes on this host
CREATE TABLE builds (
  id TEXT PRIMARY KEY,
  recipe_id TEXT NOT NULL,
  runtime_id TEXT NOT NULL,
  variant TEXT NOT NULL,
  ref TEXT NOT NULL DEFAULT '',
  commit_id TEXT NOT NULL DEFAULT '',
  sandbox TEXT NOT NULL,
  image TEXT NOT NULL DEFAULT '',
  dir TEXT NOT NULL,
  binary TEXT NOT NULL DEFAULT '',
  install_id TEXT NOT NULL DEFAULT '',
  task_id TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  finished_at TEXT
);
CREATE INDEX builds_by_runtime ON builds (runtime_id, created_at);

CREATE TABLE build_vars (
  build_id TEXT NOT NULL REFERENCES builds (id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  value TEXT NOT NULL,
  PRIMARY KEY (build_id, name)
);

CREATE TABLE build_facts (
  build_id TEXT NOT NULL REFERENCES builds (id) ON DELETE CASCADE,
  key TEXT NOT NULL,
  value TEXT NOT NULL,
  PRIMARY KEY (build_id, key)
);

CREATE TABLE build_patches (
  build_id TEXT NOT NULL REFERENCES builds (id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  patch_id TEXT NOT NULL,
  PRIMARY KEY (build_id, position)
);

-- Reservations of devices and memory with one public name each, and the limits their route enforces
CREATE TABLE slots (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  position INTEGER NOT NULL DEFAULT 0,
  description TEXT NOT NULL DEFAULT '',
  runtime_id TEXT NOT NULL DEFAULT '',
  memory_bytes INTEGER NOT NULL DEFAULT 0,
  placement TEXT NOT NULL DEFAULT '',
  instance_id TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  task_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  max_in_flight INTEGER NOT NULL DEFAULT 0,
  requests_per_second REAL NOT NULL DEFAULT 0,
  burst INTEGER NOT NULL DEFAULT 0,
  request_timeout_ms INTEGER NOT NULL DEFAULT 0,
  upstream_timeout_ms INTEGER NOT NULL DEFAULT 0,
  system_messages TEXT NOT NULL DEFAULT ''
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
  name TEXT NOT NULL,
  force INTEGER NOT NULL DEFAULT 0
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
  updated_at TEXT NOT NULL,
  served TEXT NOT NULL DEFAULT '',
  system_messages TEXT NOT NULL DEFAULT ''
);

-- Configured places models come from, one row per source, seeded defaults included
CREATE TABLE sources (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  seeded INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT ''
);

-- Every setting of a source is one row, the provider says which names it accepts
CREATE TABLE source_config (
  source_id TEXT NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  value TEXT NOT NULL,
  PRIMARY KEY (source_id, name)
);

-- Host wide preferences, one row per field of the Settings message, the value as JSON
CREATE TABLE settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
