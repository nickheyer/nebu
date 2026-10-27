-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_instance_requests" table
CREATE TABLE `new_instance_requests` (
  `instance_id` text NULL,
  `source_id` text NOT NULL,
  `repo` text NOT NULL,
  `weight_group` text NOT NULL,
  `runtime_id` text NOT NULL,
  `install_id` text NOT NULL,
  `name` text NOT NULL,
  `slot_id` text NOT NULL DEFAULT '',
  `span` text NOT NULL DEFAULT '',
  `shape` text NOT NULL DEFAULT '',
  `seat` text NOT NULL DEFAULT '',
  `profile` text NOT NULL DEFAULT '',
  PRIMARY KEY (`instance_id`),
  CONSTRAINT `0` FOREIGN KEY (`instance_id`) REFERENCES `instances` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "instance_requests" to new temporary table "new_instance_requests"
INSERT INTO `new_instance_requests` (`instance_id`, `source_id`, `repo`, `weight_group`, `runtime_id`, `install_id`, `name`, `slot_id`, `span`, `shape`, `seat`, `profile`) SELECT `instance_id`, `source_id`, `repo`, `weight_group`, `runtime_id`, `install_id`, `name`, `slot_id`, `span`, `shape`, `seat`, `profile` FROM `instance_requests`;
-- Drop "instance_requests" table after copying rows
DROP TABLE `instance_requests`;
-- Rename temporary table "new_instance_requests" to "instance_requests"
ALTER TABLE `new_instance_requests` RENAME TO `instance_requests`;
-- Create "new_slot_requests" table
CREATE TABLE `new_slot_requests` (
  `slot_id` text NULL,
  `source_id` text NOT NULL,
  `repo` text NOT NULL,
  `weight_group` text NOT NULL,
  `runtime_id` text NOT NULL,
  `install_id` text NOT NULL,
  `name` text NOT NULL,
  `span` text NOT NULL DEFAULT '',
  `shape` text NOT NULL DEFAULT '',
  `profile` text NOT NULL DEFAULT '',
  PRIMARY KEY (`slot_id`),
  CONSTRAINT `0` FOREIGN KEY (`slot_id`) REFERENCES `slots` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "slot_requests" to new temporary table "new_slot_requests"
INSERT INTO `new_slot_requests` (`slot_id`, `source_id`, `repo`, `weight_group`, `runtime_id`, `install_id`, `name`, `span`, `shape`, `profile`) SELECT `slot_id`, `source_id`, `repo`, `weight_group`, `runtime_id`, `install_id`, `name`, `span`, `shape`, `profile` FROM `slot_requests`;
-- Drop "slot_requests" table after copying rows
DROP TABLE `slot_requests`;
-- Rename temporary table "new_slot_requests" to "slot_requests"
ALTER TABLE `new_slot_requests` RENAME TO `slot_requests`;
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
