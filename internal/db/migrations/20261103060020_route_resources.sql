-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_routes" table
CREATE TABLE `new_routes` (
  `name` text NULL,
  `owner_kind` text NOT NULL DEFAULT '',
  `owner_id` text NOT NULL DEFAULT '',
  `node_id` text NOT NULL DEFAULT '',
  `generation` integer NOT NULL DEFAULT 0,
  `spec` text NOT NULL DEFAULT '',
  `status` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL,
  `updated_at` text NOT NULL,
  PRIMARY KEY (`name`)
);
-- Copy rows from old table "routes" to new temporary table "new_routes", the slot or instance that held each becoming its owner and its profile its spec, rows no owner held dropped
INSERT INTO `new_routes` (`name`, `owner_kind`, `owner_id`, `spec`, `created_at`, `updated_at`) SELECT `name`, CASE WHEN `slot_id` != '' THEN 'slot' ELSE 'instance' END, CASE WHEN `slot_id` != '' THEN `slot_id` ELSE `instance_id` END, CASE WHEN `profile` != '' THEN json_object('profile', json(`profile`)) ELSE '' END, `updated_at`, `updated_at` FROM `routes` WHERE `slot_id` != '' OR `instance_id` != '';
-- Drop "routes" table after copying rows
DROP TABLE `routes`;
-- Rename temporary table "new_routes" to "routes"
ALTER TABLE `new_routes` RENAME TO `routes`;
-- Create "pools" table
CREATE TABLE `pools` (
  `id` text NULL,
  `owner_kind` text NOT NULL DEFAULT '',
  `owner_id` text NOT NULL DEFAULT '',
  `generation` integer NOT NULL DEFAULT 0,
  `spec` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL,
  `updated_at` text NOT NULL,
  PRIMARY KEY (`id`)
);
-- Create "objectives" table
CREATE TABLE `objectives` (
  `name` text NULL,
  `priority` integer NOT NULL DEFAULT 0,
  `ttft_ms` integer NOT NULL DEFAULT 0,
  `tpot_ms` integer NOT NULL DEFAULT 0,
  `created_at` text NOT NULL,
  `updated_at` text NOT NULL,
  PRIMARY KEY (`name`)
);
-- Create "scheduler_configs" table
CREATE TABLE `scheduler_configs` (
  `name` text NULL,
  `yaml` text NOT NULL,
  `generation` integer NOT NULL DEFAULT 0,
  `created_at` text NOT NULL,
  `updated_at` text NOT NULL,
  PRIMARY KEY (`name`)
);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
