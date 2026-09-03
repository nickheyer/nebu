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

-- Installs produced by a build point back at it
ALTER TABLE installs ADD COLUMN build_id TEXT NOT NULL DEFAULT '';
