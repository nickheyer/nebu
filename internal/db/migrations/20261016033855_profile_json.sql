-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_slots" table
CREATE TABLE `new_slots` (
  `id` text NULL,
  `name` text NOT NULL,
  `position` integer NOT NULL DEFAULT 0,
  `description` text NOT NULL DEFAULT '',
  `runtime_id` text NOT NULL DEFAULT '',
  `memory_bytes` integer NOT NULL DEFAULT 0,
  `placement` text NOT NULL DEFAULT '',
  `instance_id` text NOT NULL DEFAULT '',
  `state` text NOT NULL,
  `error` text NOT NULL DEFAULT '',
  `task_id` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL,
  `updated_at` text NOT NULL,
  `max_in_flight` integer NOT NULL DEFAULT 0,
  `requests_per_second` real NOT NULL DEFAULT 0,
  `burst` integer NOT NULL DEFAULT 0,
  `request_timeout_ms` integer NOT NULL DEFAULT 0,
  `upstream_timeout_ms` integer NOT NULL DEFAULT 0,
  `formation_id` text NOT NULL DEFAULT '',
  `profile` text NOT NULL DEFAULT '',
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "slots" to new temporary table "new_slots", the shaping columns folded into profile JSON
INSERT INTO `new_slots` (`id`, `name`, `position`, `description`, `runtime_id`, `memory_bytes`, `placement`, `instance_id`, `state`, `error`, `task_id`, `created_at`, `updated_at`, `max_in_flight`, `requests_per_second`, `burst`, `request_timeout_ms`, `upstream_timeout_ms`, `formation_id`, `profile`) SELECT `id`, `name`, `position`, `description`, `runtime_id`, `memory_bytes`, `placement`, `instance_id`, `state`, `error`, `task_id`, `created_at`, `updated_at`, `max_in_flight`, `requests_per_second`, `burst`, `request_timeout_ms`, `upstream_timeout_ms`, `formation_id`, CASE WHEN `system_messages` = '' AND `allow_all` = 0 THEN '' WHEN `allow_all` = 1 THEN json_object('systemMessages', CASE `system_messages` WHEN 'keep' THEN 'SYSTEM_MESSAGES_KEEP' WHEN 'merge' THEN 'SYSTEM_MESSAGES_MERGE' WHEN 'user' THEN 'SYSTEM_MESSAGES_USER' ELSE 'SYSTEM_MESSAGES_UNSPECIFIED' END, 'allowAll', json('true')) ELSE json_object('systemMessages', CASE `system_messages` WHEN 'keep' THEN 'SYSTEM_MESSAGES_KEEP' WHEN 'merge' THEN 'SYSTEM_MESSAGES_MERGE' WHEN 'user' THEN 'SYSTEM_MESSAGES_USER' ELSE 'SYSTEM_MESSAGES_UNSPECIFIED' END) END FROM `slots`;
-- Drop "slots" table after copying rows
DROP TABLE `slots`;
-- Rename temporary table "new_slots" to "slots"
ALTER TABLE `new_slots` RENAME TO `slots`;
-- Create index "slots_name" to table: "slots"
CREATE UNIQUE INDEX `slots_name` ON `slots` (`name`);
-- Create "new_routes" table
CREATE TABLE `new_routes` (
  `name` text NULL,
  `instance_id` text NOT NULL DEFAULT '',
  `slot_id` text NOT NULL DEFAULT '',
  `endpoint` text NOT NULL DEFAULT '',
  `api` text NOT NULL DEFAULT '',
  `state` text NOT NULL,
  `model` text NOT NULL DEFAULT '',
  `requests` integer NOT NULL DEFAULT 0,
  `updated_at` text NOT NULL,
  `served` text NOT NULL DEFAULT '',
  `profile` text NOT NULL DEFAULT '',
  PRIMARY KEY (`name`)
);
-- Copy rows from old table "routes" to new temporary table "new_routes"
INSERT INTO `new_routes` (`name`, `instance_id`, `slot_id`, `endpoint`, `api`, `state`, `model`, `requests`, `updated_at`, `served`, `profile`) SELECT `name`, `instance_id`, `slot_id`, `endpoint`, `api`, `state`, `model`, `requests`, `updated_at`, `served`, CASE WHEN `system_messages` = '' AND `allow_all` = 0 THEN '' WHEN `allow_all` = 1 THEN json_object('systemMessages', CASE `system_messages` WHEN 'keep' THEN 'SYSTEM_MESSAGES_KEEP' WHEN 'merge' THEN 'SYSTEM_MESSAGES_MERGE' WHEN 'user' THEN 'SYSTEM_MESSAGES_USER' ELSE 'SYSTEM_MESSAGES_UNSPECIFIED' END, 'allowAll', json('true')) ELSE json_object('systemMessages', CASE `system_messages` WHEN 'keep' THEN 'SYSTEM_MESSAGES_KEEP' WHEN 'merge' THEN 'SYSTEM_MESSAGES_MERGE' WHEN 'user' THEN 'SYSTEM_MESSAGES_USER' ELSE 'SYSTEM_MESSAGES_UNSPECIFIED' END) END FROM `routes`;
-- Drop "routes" table after copying rows
DROP TABLE `routes`;
-- Rename temporary table "new_routes" to "routes"
ALTER TABLE `new_routes` RENAME TO `routes`;
-- Create "new_slot_aliases" table
CREATE TABLE `new_slot_aliases` (
  `slot_id` text NOT NULL,
  `position` integer NOT NULL,
  `name` text NOT NULL,
  `max_in_flight` integer NOT NULL DEFAULT 0,
  `requests_per_second` real NOT NULL DEFAULT 0,
  `burst` integer NOT NULL DEFAULT 0,
  `request_timeout_ms` integer NOT NULL DEFAULT 0,
  `upstream_timeout_ms` integer NOT NULL DEFAULT 0,
  `profile` text NOT NULL DEFAULT '',
  PRIMARY KEY (`slot_id`, `position`),
  CONSTRAINT `0` FOREIGN KEY (`slot_id`) REFERENCES `slots` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "slot_aliases" to new temporary table "new_slot_aliases"
INSERT INTO `new_slot_aliases` (`slot_id`, `position`, `name`, `max_in_flight`, `requests_per_second`, `burst`, `request_timeout_ms`, `upstream_timeout_ms`, `profile`) SELECT `slot_id`, `position`, `name`, `max_in_flight`, `requests_per_second`, `burst`, `request_timeout_ms`, `upstream_timeout_ms`, CASE WHEN `system_messages` = '' AND `allow_all` = 0 THEN '' WHEN `allow_all` = 1 THEN json_object('systemMessages', CASE `system_messages` WHEN 'keep' THEN 'SYSTEM_MESSAGES_KEEP' WHEN 'merge' THEN 'SYSTEM_MESSAGES_MERGE' WHEN 'user' THEN 'SYSTEM_MESSAGES_USER' ELSE 'SYSTEM_MESSAGES_UNSPECIFIED' END, 'allowAll', json('true')) ELSE json_object('systemMessages', CASE `system_messages` WHEN 'keep' THEN 'SYSTEM_MESSAGES_KEEP' WHEN 'merge' THEN 'SYSTEM_MESSAGES_MERGE' WHEN 'user' THEN 'SYSTEM_MESSAGES_USER' ELSE 'SYSTEM_MESSAGES_UNSPECIFIED' END) END FROM `slot_aliases`;
-- Drop "slot_aliases" table after copying rows
DROP TABLE `slot_aliases`;
-- Rename temporary table "new_slot_aliases" to "slot_aliases"
ALTER TABLE `new_slot_aliases` RENAME TO `slot_aliases`;
-- Create index "slot_aliases_name" to table: "slot_aliases"
CREATE UNIQUE INDEX `slot_aliases_name` ON `slot_aliases` (`name`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
