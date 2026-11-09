-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "old_slots" table with the shaping columns the profile JSON folded
CREATE TABLE `old_slots` (
  `id` text NULL,
  `name` text NOT NULL,
  `position` integer NOT NULL DEFAULT 0,
  `description` text NOT NULL DEFAULT '',
  `runtime_id` text NOT NULL DEFAULT '',
  `memory_bytes` integer NOT NULL DEFAULT 0,
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
  `system_messages` text NOT NULL DEFAULT '',
  `placement` text NOT NULL DEFAULT '',
  `formation_id` text NOT NULL DEFAULT '',
  `allow_all` integer NOT NULL DEFAULT 0,
  PRIMARY KEY (`id`)
);
INSERT INTO `old_slots` (`id`, `name`, `position`, `description`, `runtime_id`, `memory_bytes`, `instance_id`, `state`, `error`, `task_id`, `created_at`, `updated_at`, `max_in_flight`, `requests_per_second`, `burst`, `request_timeout_ms`, `upstream_timeout_ms`, `system_messages`, `placement`, `formation_id`, `allow_all`) SELECT `id`, `name`, `position`, `description`, `runtime_id`, `memory_bytes`, `instance_id`, `state`, `error`, `task_id`, `created_at`, `updated_at`, `max_in_flight`, `requests_per_second`, `burst`, `request_timeout_ms`, `upstream_timeout_ms`, CASE json_extract(`profile`, '$.systemMessages') WHEN 'SYSTEM_MESSAGES_KEEP' THEN 'keep' WHEN 'SYSTEM_MESSAGES_MERGE' THEN 'merge' WHEN 'SYSTEM_MESSAGES_USER' THEN 'user' ELSE '' END, `placement`, `formation_id`, CASE WHEN `profile` != '' AND json_extract(`profile`, '$.allowAll') = 1 THEN 1 ELSE 0 END FROM `slots`;
DROP TABLE `slots`;
ALTER TABLE `old_slots` RENAME TO `slots`;
CREATE UNIQUE INDEX `slots_name` ON `slots` (`name`);
-- Create "old_routes" table with the shaping columns the profile JSON folded
CREATE TABLE `old_routes` (
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
  `system_messages` text NOT NULL DEFAULT '',
  `allow_all` integer NOT NULL DEFAULT 0,
  PRIMARY KEY (`name`)
);
INSERT INTO `old_routes` (`name`, `instance_id`, `slot_id`, `endpoint`, `api`, `state`, `model`, `requests`, `updated_at`, `served`, `system_messages`, `allow_all`) SELECT `name`, `instance_id`, `slot_id`, `endpoint`, `api`, `state`, `model`, `requests`, `updated_at`, `served`, CASE json_extract(`profile`, '$.systemMessages') WHEN 'SYSTEM_MESSAGES_KEEP' THEN 'keep' WHEN 'SYSTEM_MESSAGES_MERGE' THEN 'merge' WHEN 'SYSTEM_MESSAGES_USER' THEN 'user' ELSE '' END, CASE WHEN `profile` != '' AND json_extract(`profile`, '$.allowAll') = 1 THEN 1 ELSE 0 END FROM `routes`;
DROP TABLE `routes`;
ALTER TABLE `old_routes` RENAME TO `routes`;
-- Create "old_slot_aliases" table with the shaping columns the profile JSON folded
CREATE TABLE `old_slot_aliases` (
  `slot_id` text NOT NULL,
  `position` integer NOT NULL,
  `name` text NOT NULL,
  `max_in_flight` integer NOT NULL DEFAULT 0,
  `requests_per_second` real NOT NULL DEFAULT 0,
  `burst` integer NOT NULL DEFAULT 0,
  `request_timeout_ms` integer NOT NULL DEFAULT 0,
  `upstream_timeout_ms` integer NOT NULL DEFAULT 0,
  `system_messages` text NOT NULL DEFAULT '',
  `allow_all` integer NOT NULL DEFAULT 0,
  PRIMARY KEY (`slot_id`, `position`),
  CONSTRAINT `0` FOREIGN KEY (`slot_id`) REFERENCES `slots` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
INSERT INTO `old_slot_aliases` (`slot_id`, `position`, `name`, `max_in_flight`, `requests_per_second`, `burst`, `request_timeout_ms`, `upstream_timeout_ms`, `system_messages`, `allow_all`) SELECT `slot_id`, `position`, `name`, `max_in_flight`, `requests_per_second`, `burst`, `request_timeout_ms`, `upstream_timeout_ms`, CASE json_extract(`profile`, '$.systemMessages') WHEN 'SYSTEM_MESSAGES_KEEP' THEN 'keep' WHEN 'SYSTEM_MESSAGES_MERGE' THEN 'merge' WHEN 'SYSTEM_MESSAGES_USER' THEN 'user' ELSE '' END, CASE WHEN `profile` != '' AND json_extract(`profile`, '$.allowAll') = 1 THEN 1 ELSE 0 END FROM `slot_aliases`;
DROP TABLE `slot_aliases`;
ALTER TABLE `old_slot_aliases` RENAME TO `slot_aliases`;
CREATE UNIQUE INDEX `slot_aliases_name` ON `slot_aliases` (`name`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
