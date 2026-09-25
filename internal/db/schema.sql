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
  slot_id TEXT NOT NULL DEFAULT '',
  seat TEXT NOT NULL DEFAULT '',
  transport TEXT NOT NULL DEFAULT ''
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
  force INTEGER NOT NULL DEFAULT 0,
  span TEXT NOT NULL DEFAULT '',
  shape TEXT NOT NULL DEFAULT '',
  seat TEXT NOT NULL DEFAULT '',
  profile TEXT NOT NULL DEFAULT ''
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
  system_messages TEXT NOT NULL DEFAULT '',
  formation_id TEXT NOT NULL DEFAULT ''
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

-- Extra public names a slot answers to, in list order, each with limits and shaping that override the slot's when set
CREATE TABLE slot_aliases (
  slot_id TEXT NOT NULL REFERENCES slots (id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  name TEXT NOT NULL,
  max_in_flight INTEGER NOT NULL DEFAULT 0,
  requests_per_second REAL NOT NULL DEFAULT 0,
  burst INTEGER NOT NULL DEFAULT 0,
  request_timeout_ms INTEGER NOT NULL DEFAULT 0,
  upstream_timeout_ms INTEGER NOT NULL DEFAULT 0,
  system_messages TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (slot_id, position)
);
CREATE UNIQUE INDEX slot_aliases_name ON slot_aliases (name);

-- The last run request, replayed on rollback and relaunch
CREATE TABLE slot_requests (
  slot_id TEXT PRIMARY KEY REFERENCES slots (id) ON DELETE CASCADE,
  source_id TEXT NOT NULL,
  repo TEXT NOT NULL,
  weight_group TEXT NOT NULL,
  runtime_id TEXT NOT NULL,
  install_id TEXT NOT NULL,
  name TEXT NOT NULL,
  force INTEGER NOT NULL DEFAULT 0,
  span TEXT NOT NULL DEFAULT '',
  shape TEXT NOT NULL DEFAULT '',
  profile TEXT NOT NULL DEFAULT ''
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

-- Discord bots, their token and settings; the connection state lives in the daemon
CREATE TABLE bots (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  token TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 0,
  spec TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

-- What a bot keeps per channel: the persona chosen there, where its memory starts, and the webhook it speaks through
CREATE TABLE bot_channels (
  bot_id TEXT NOT NULL REFERENCES bots (id) ON DELETE CASCADE,
  channel_id TEXT NOT NULL,
  persona_id TEXT NOT NULL DEFAULT '',
  cutoff_message_id TEXT NOT NULL DEFAULT '',
  webhook_id TEXT NOT NULL DEFAULT '',
  webhook_token TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (bot_id, channel_id)
);

-- When each scheduled automation last ran, so intervals survive a restart
CREATE TABLE bot_schedules (
  bot_id TEXT NOT NULL REFERENCES bots (id) ON DELETE CASCADE,
  automation_id TEXT NOT NULL,
  last_run_at TEXT NOT NULL,
  PRIMARY KEY (bot_id, automation_id)
);

-- Local accounts for the web UI. Usernames are stored lowercase.
CREATE TABLE users (
  id TEXT PRIMARY KEY,
  username TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX users_username ON users (username);

-- API tokens users make in the web UI. The subject is the local account id or
-- the single sign-on subject, and the secret is what clients send as a bearer token.
CREATE TABLE api_tokens (
  id TEXT PRIMARY KEY,
  provider TEXT NOT NULL,
  subject TEXT NOT NULL,
  owner TEXT NOT NULL,
  email TEXT NOT NULL,
  name TEXT NOT NULL,
  secret TEXT NOT NULL,
  created_at TEXT NOT NULL,
  last_used_at TEXT
);
CREATE UNIQUE INDEX api_tokens_secret ON api_tokens (secret);
CREATE INDEX api_tokens_owner ON api_tokens (provider, subject);

-- Saved web chat conversations, owned by the account that made them. The turns
-- and settings are one JSON document, the columns what lists show.
CREATE TABLE conversations (
  id TEXT PRIMARY KEY,
  provider TEXT NOT NULL,
  subject TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  turn_count INTEGER NOT NULL DEFAULT 0,
  body TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX conversations_owner ON conversations (provider, subject, updated_at);

-- Images attached to or answered in saved conversations, bytes included, owned like the conversations
CREATE TABLE chat_files (
  id TEXT PRIMARY KEY,
  provider TEXT NOT NULL,
  subject TEXT NOT NULL,
  media_type TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  width INTEGER NOT NULL DEFAULT 0,
  height INTEGER NOT NULL DEFAULT 0,
  data BLOB NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX chat_files_owner ON chat_files (provider, subject);

-- This node's identity in any mesh: a random 128 bit id and an Ed25519 key pair made on first start
CREATE TABLE mesh_identity (
  id TEXT PRIMARY KEY,
  public_key BLOB NOT NULL,
  private_key BLOB NOT NULL,
  created_at TEXT NOT NULL
);

-- The mesh this node belongs to, one row: the shared secret and the certificate authority when it has one
CREATE TABLE mesh (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  secret BLOB NOT NULL,
  tls INTEGER NOT NULL DEFAULT 0,
  ca_certificate BLOB NOT NULL DEFAULT X'',
  ca_key BLOB NOT NULL DEFAULT X'',
  certificate BLOB NOT NULL DEFAULT X'',
  created_at TEXT NOT NULL,
  joined_at TEXT NOT NULL
);

-- Every other member's record as last synced, the node record kept as JSON beside its sequence
CREATE TABLE mesh_members (
  id TEXT PRIMARY KEY,
  state TEXT NOT NULL,
  seen_at TEXT,
  record TEXT NOT NULL DEFAULT ''
);

-- Admissions under way: on a member one per node outside asking or invited, on a node outside
-- one per mesh it asked or was invited to, the record kept as JSON beside its keys
CREATE TABLE mesh_admissions (
  id TEXT PRIMARY KEY,
  side TEXT NOT NULL,
  state TEXT NOT NULL,
  node_id TEXT NOT NULL DEFAULT '',
  mesh_hash TEXT NOT NULL DEFAULT '',
  record TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  expires_at TEXT
);
CREATE INDEX mesh_admissions_node ON mesh_admissions (node_id);

-- Session tokens minted at the handshake: what we accept from a peer and what we send it
CREATE TABLE mesh_sessions (
  peer_id TEXT PRIMARY KEY,
  accept TEXT NOT NULL,
  send TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  granted_at TEXT NOT NULL
);
CREATE UNIQUE INDEX mesh_sessions_accept ON mesh_sessions (accept);

-- What the link prober measured from this node to each member
CREATE TABLE mesh_links (
  from_id TEXT NOT NULL,
  to_id TEXT NOT NULL,
  rtt_us INTEGER NOT NULL DEFAULT 0,
  rtt_p95_us INTEGER NOT NULL DEFAULT 0,
  stream_bps INTEGER NOT NULL DEFAULT 0,
  aggregate_bps INTEGER NOT NULL DEFAULT 0,
  interface TEXT NOT NULL DEFAULT '',
  interface_bps INTEGER NOT NULL DEFAULT 0,
  rdma_device TEXT NOT NULL DEFAULT '',
  class TEXT NOT NULL DEFAULT '',
  measured_at TEXT,
  mtu INTEGER NOT NULL DEFAULT 0,
  subnet INTEGER NOT NULL DEFAULT 0,
  detail TEXT NOT NULL DEFAULT '',
  bandwidth_held INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (from_id, to_id)
);

-- Learned streaming bandwidth, compute, fixed cost, and draft acceptance per device, with the
-- regression sums the fixed cost intercept comes from
CREATE TABLE throughput (
  device_id TEXT PRIMARY KEY,
  stream_bps REAL NOT NULL DEFAULT 0,
  compute_flops REAL NOT NULL DEFAULT 0,
  fixed_seconds REAL NOT NULL DEFAULT 0,
  acceptance REAL NOT NULL DEFAULT 0,
  samples INTEGER NOT NULL DEFAULT 0,
  acceptance_samples INTEGER NOT NULL DEFAULT 0,
  sum_x REAL NOT NULL DEFAULT 0,
  sum_y REAL NOT NULL DEFAULT 0,
  sum_xy REAL NOT NULL DEFAULT 0,
  sum_xx REAL NOT NULL DEFAULT 0,
  points INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  compute_samples INTEGER NOT NULL DEFAULT 0
);

-- Ratio of predicted to measured time to first token and time per token, by shape, runtime, and link class
CREATE TABLE formation_ratios (
  shape TEXT NOT NULL,
  runtime_id TEXT NOT NULL,
  link_class TEXT NOT NULL,
  ttft_ratio REAL NOT NULL DEFAULT 1,
  tpt_ratio REAL NOT NULL DEFAULT 1,
  samples INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (shape, runtime_id, link_class)
);

-- Declared device numbers a person set, over the table nebu ships
CREATE TABLE device_profiles (
  pattern TEXT PRIMARY KEY,
  stream_bps REAL NOT NULL,
  compute_flops REAL NOT NULL,
  fixed_seconds REAL NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL
);

-- Formations conducted here and copies of every other conductor's, the request and the plan's
-- own numbers as JSON, the seats and candidates in their own tables
CREATE TABLE formations (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  conductor TEXT NOT NULL,
  conductor_name TEXT NOT NULL DEFAULT '',
  shape TEXT NOT NULL,
  state TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  task_id TEXT NOT NULL DEFAULT '',
  slot_id TEXT NOT NULL DEFAULT '',
  desired_running INTEGER NOT NULL DEFAULT 0,
  runtime_id TEXT NOT NULL DEFAULT '',
  endpoint TEXT NOT NULL DEFAULT '',
  bytes_moved INTEGER NOT NULL DEFAULT 0,
  sequence INTEGER NOT NULL DEFAULT 0,
  source_id TEXT NOT NULL DEFAULT '',
  repo TEXT NOT NULL DEFAULT '',
  weight_group TEXT NOT NULL DEFAULT '',
  request TEXT NOT NULL DEFAULT '{}',
  plan TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  ready_at TEXT,
  stopped_at TEXT,
  updated_at TEXT NOT NULL,
  rendezvous TEXT NOT NULL DEFAULT '',
  cache_key TEXT NOT NULL DEFAULT ''
);
CREATE INDEX formations_by_created ON formations (created_at);

CREATE TABLE formation_seats (
  formation_id TEXT NOT NULL REFERENCES formations (id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  node_id TEXT NOT NULL,
  node_name TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL,
  rank INTEGER NOT NULL DEFAULT 0,
  instance_id TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  endpoint TEXT NOT NULL DEFAULT '',
  transport TEXT NOT NULL DEFAULT '',
  layer_from INTEGER NOT NULL DEFAULT 0,
  layer_to INTEGER NOT NULL DEFAULT 0,
  read_bytes INTEGER NOT NULL DEFAULT 0,
  cache_bytes INTEGER NOT NULL DEFAULT 0,
  weight_bytes INTEGER NOT NULL DEFAULT 0,
  install_id TEXT NOT NULL DEFAULT '',
  exposed INTEGER NOT NULL DEFAULT 0,
  phase INTEGER NOT NULL DEFAULT 0,
  placements TEXT NOT NULL DEFAULT '[]',
  memory TEXT NOT NULL DEFAULT '',
  triage TEXT NOT NULL DEFAULT '[]',
  measurements TEXT NOT NULL DEFAULT '[]',
  PRIMARY KEY (formation_id, position)
);

CREATE TABLE formation_candidates (
  formation_id TEXT NOT NULL REFERENCES formations (id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  shape TEXT NOT NULL,
  node_ids TEXT NOT NULL DEFAULT '',
  verdict TEXT NOT NULL,
  score REAL NOT NULL DEFAULT 0,
  prefill_seconds REAL NOT NULL DEFAULT 0,
  decode_seconds_per_token REAL NOT NULL DEFAULT 0,
  reason TEXT NOT NULL DEFAULT '',
  head TEXT NOT NULL DEFAULT '',
  requests_per_second REAL NOT NULL DEFAULT 0,
  tokens_per_second REAL NOT NULL DEFAULT 0,
  speedup REAL NOT NULL DEFAULT 0,
  PRIMARY KEY (formation_id, position)
);
