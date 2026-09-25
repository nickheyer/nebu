-- Add column "seat" to table: "instances"
ALTER TABLE `instances` ADD COLUMN `seat` text NOT NULL DEFAULT '';
-- Add column "transport" to table: "instances"
ALTER TABLE `instances` ADD COLUMN `transport` text NOT NULL DEFAULT '';
-- Add column "span" to table: "instance_requests"
ALTER TABLE `instance_requests` ADD COLUMN `span` text NOT NULL DEFAULT '';
-- Add column "shape" to table: "instance_requests"
ALTER TABLE `instance_requests` ADD COLUMN `shape` text NOT NULL DEFAULT '';
-- Add column "seat" to table: "instance_requests"
ALTER TABLE `instance_requests` ADD COLUMN `seat` text NOT NULL DEFAULT '';
-- Add column "profile" to table: "instance_requests"
ALTER TABLE `instance_requests` ADD COLUMN `profile` text NOT NULL DEFAULT '';
-- Add column "formation_id" to table: "slots"
ALTER TABLE `slots` ADD COLUMN `formation_id` text NOT NULL DEFAULT '';
-- Add column "span" to table: "slot_requests"
ALTER TABLE `slot_requests` ADD COLUMN `span` text NOT NULL DEFAULT '';
-- Add column "shape" to table: "slot_requests"
ALTER TABLE `slot_requests` ADD COLUMN `shape` text NOT NULL DEFAULT '';
-- Add column "profile" to table: "slot_requests"
ALTER TABLE `slot_requests` ADD COLUMN `profile` text NOT NULL DEFAULT '';
-- Create "mesh_identity" table
CREATE TABLE `mesh_identity` (
  `id` text NULL,
  `public_key` blob NOT NULL,
  `private_key` blob NOT NULL,
  `created_at` text NOT NULL,
  PRIMARY KEY (`id`)
);
-- Create "mesh" table
CREATE TABLE `mesh` (
  `id` text NULL,
  `name` text NOT NULL,
  `secret` blob NOT NULL,
  `tls` integer NOT NULL DEFAULT 0,
  `ca_certificate` blob NOT NULL DEFAULT (X''),
  `ca_key` blob NOT NULL DEFAULT (X''),
  `certificate` blob NOT NULL DEFAULT (X''),
  `created_at` text NOT NULL,
  `joined_at` text NOT NULL,
  PRIMARY KEY (`id`)
);
-- Create "mesh_members" table
CREATE TABLE `mesh_members` (
  `id` text NULL,
  `state` text NOT NULL,
  `seen_at` text NULL,
  `record` text NOT NULL DEFAULT '',
  PRIMARY KEY (`id`)
);
-- Create "mesh_admissions" table
CREATE TABLE `mesh_admissions` (
  `id` text NULL,
  `side` text NOT NULL,
  `state` text NOT NULL,
  `node_id` text NOT NULL DEFAULT '',
  `mesh_hash` text NOT NULL DEFAULT '',
  `record` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL,
  `updated_at` text NOT NULL,
  `expires_at` text NULL,
  PRIMARY KEY (`id`)
);
-- Create index "mesh_admissions_node" to table: "mesh_admissions"
CREATE INDEX `mesh_admissions_node` ON `mesh_admissions` (`node_id`);
-- Create "mesh_sessions" table
CREATE TABLE `mesh_sessions` (
  `peer_id` text NULL,
  `accept` text NOT NULL,
  `send` text NOT NULL,
  `expires_at` text NOT NULL,
  `granted_at` text NOT NULL,
  PRIMARY KEY (`peer_id`)
);
-- Create index "mesh_sessions_accept" to table: "mesh_sessions"
CREATE UNIQUE INDEX `mesh_sessions_accept` ON `mesh_sessions` (`accept`);
-- Create "mesh_links" table
CREATE TABLE `mesh_links` (
  `from_id` text NOT NULL,
  `to_id` text NOT NULL,
  `rtt_us` integer NOT NULL DEFAULT 0,
  `rtt_p95_us` integer NOT NULL DEFAULT 0,
  `stream_bps` integer NOT NULL DEFAULT 0,
  `aggregate_bps` integer NOT NULL DEFAULT 0,
  `interface` text NOT NULL DEFAULT '',
  `interface_bps` integer NOT NULL DEFAULT 0,
  `rdma_device` text NOT NULL DEFAULT '',
  `class` text NOT NULL DEFAULT '',
  `measured_at` text NULL,
  `mtu` integer NOT NULL DEFAULT 0,
  `subnet` integer NOT NULL DEFAULT 0,
  `detail` text NOT NULL DEFAULT '',
  `bandwidth_held` integer NOT NULL DEFAULT 0,
  PRIMARY KEY (`from_id`, `to_id`)
);
-- Create "throughput" table
CREATE TABLE `throughput` (
  `device_id` text NULL,
  `stream_bps` real NOT NULL DEFAULT 0,
  `compute_flops` real NOT NULL DEFAULT 0,
  `fixed_seconds` real NOT NULL DEFAULT 0,
  `acceptance` real NOT NULL DEFAULT 0,
  `samples` integer NOT NULL DEFAULT 0,
  `acceptance_samples` integer NOT NULL DEFAULT 0,
  `sum_x` real NOT NULL DEFAULT 0,
  `sum_y` real NOT NULL DEFAULT 0,
  `sum_xy` real NOT NULL DEFAULT 0,
  `sum_xx` real NOT NULL DEFAULT 0,
  `points` integer NOT NULL DEFAULT 0,
  `updated_at` text NOT NULL,
  `compute_samples` integer NOT NULL DEFAULT 0,
  PRIMARY KEY (`device_id`)
);
-- Create "formation_ratios" table
CREATE TABLE `formation_ratios` (
  `shape` text NOT NULL,
  `runtime_id` text NOT NULL,
  `link_class` text NOT NULL,
  `ttft_ratio` real NOT NULL DEFAULT 1,
  `tpt_ratio` real NOT NULL DEFAULT 1,
  `samples` integer NOT NULL DEFAULT 0,
  `updated_at` text NOT NULL,
  PRIMARY KEY (`shape`, `runtime_id`, `link_class`)
);
-- Create "device_profiles" table
CREATE TABLE `device_profiles` (
  `pattern` text NULL,
  `stream_bps` real NOT NULL,
  `compute_flops` real NOT NULL,
  `fixed_seconds` real NOT NULL DEFAULT 0,
  `updated_at` text NOT NULL,
  PRIMARY KEY (`pattern`)
);
-- Create "formations" table
CREATE TABLE `formations` (
  `id` text NULL,
  `name` text NOT NULL,
  `conductor` text NOT NULL,
  `conductor_name` text NOT NULL DEFAULT '',
  `shape` text NOT NULL,
  `state` text NOT NULL,
  `error` text NOT NULL DEFAULT '',
  `task_id` text NOT NULL DEFAULT '',
  `slot_id` text NOT NULL DEFAULT '',
  `desired_running` integer NOT NULL DEFAULT 0,
  `runtime_id` text NOT NULL DEFAULT '',
  `endpoint` text NOT NULL DEFAULT '',
  `bytes_moved` integer NOT NULL DEFAULT 0,
  `sequence` integer NOT NULL DEFAULT 0,
  `source_id` text NOT NULL DEFAULT '',
  `repo` text NOT NULL DEFAULT '',
  `weight_group` text NOT NULL DEFAULT '',
  `request` text NOT NULL DEFAULT '{}',
  `plan` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL,
  `ready_at` text NULL,
  `stopped_at` text NULL,
  `updated_at` text NOT NULL,
  `rendezvous` text NOT NULL DEFAULT '',
  `cache_key` text NOT NULL DEFAULT '',
  PRIMARY KEY (`id`)
);
-- Create index "formations_by_created" to table: "formations"
CREATE INDEX `formations_by_created` ON `formations` (`created_at`);
-- Create "formation_seats" table
CREATE TABLE `formation_seats` (
  `formation_id` text NOT NULL,
  `position` integer NOT NULL,
  `node_id` text NOT NULL,
  `node_name` text NOT NULL DEFAULT '',
  `role` text NOT NULL,
  `rank` integer NOT NULL DEFAULT 0,
  `instance_id` text NOT NULL DEFAULT '',
  `state` text NOT NULL,
  `error` text NOT NULL DEFAULT '',
  `endpoint` text NOT NULL DEFAULT '',
  `transport` text NOT NULL DEFAULT '',
  `layer_from` integer NOT NULL DEFAULT 0,
  `layer_to` integer NOT NULL DEFAULT 0,
  `read_bytes` integer NOT NULL DEFAULT 0,
  `cache_bytes` integer NOT NULL DEFAULT 0,
  `weight_bytes` integer NOT NULL DEFAULT 0,
  `install_id` text NOT NULL DEFAULT '',
  `exposed` integer NOT NULL DEFAULT 0,
  `phase` integer NOT NULL DEFAULT 0,
  `placements` text NOT NULL DEFAULT '[]',
  `memory` text NOT NULL DEFAULT '',
  `triage` text NOT NULL DEFAULT '[]',
  `measurements` text NOT NULL DEFAULT '[]',
  PRIMARY KEY (`formation_id`, `position`),
  CONSTRAINT `0` FOREIGN KEY (`formation_id`) REFERENCES `formations` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "formation_candidates" table
CREATE TABLE `formation_candidates` (
  `formation_id` text NOT NULL,
  `position` integer NOT NULL,
  `shape` text NOT NULL,
  `node_ids` text NOT NULL DEFAULT '',
  `verdict` text NOT NULL,
  `score` real NOT NULL DEFAULT 0,
  `prefill_seconds` real NOT NULL DEFAULT 0,
  `decode_seconds_per_token` real NOT NULL DEFAULT 0,
  `reason` text NOT NULL DEFAULT '',
  `head` text NOT NULL DEFAULT '',
  `requests_per_second` real NOT NULL DEFAULT 0,
  `tokens_per_second` real NOT NULL DEFAULT 0,
  `speedup` real NOT NULL DEFAULT 0,
  PRIMARY KEY (`formation_id`, `position`),
  CONSTRAINT `0` FOREIGN KEY (`formation_id`) REFERENCES `formations` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
