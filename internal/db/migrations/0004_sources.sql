-- Configured places models come from, one row per source, seeded defaults included
CREATE TABLE sources (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  endpoint TEXT NOT NULL DEFAULT '',
  token_env TEXT NOT NULL DEFAULT '',
  path TEXT NOT NULL DEFAULT '',
  seeded INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

-- Kind specific settings such as namespace, username_env, or nsfw
CREATE TABLE source_options (
  source_id TEXT NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  value TEXT NOT NULL,
  PRIMARY KEY (source_id, name)
);
