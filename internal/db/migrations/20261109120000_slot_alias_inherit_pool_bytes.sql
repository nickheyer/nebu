-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Add column "pool_bytes" to table: "slots"
ALTER TABLE `slots` ADD COLUMN `pool_bytes` text NOT NULL DEFAULT '';
-- Create "new_slot_aliases" table
CREATE TABLE `new_slot_aliases` (
  `slot_id` text NOT NULL,
  `position` integer NOT NULL,
  `name` text NOT NULL,
  `inherit_slot_id` text NOT NULL DEFAULT '',
  PRIMARY KEY (`slot_id`, `position`),
  CONSTRAINT `0` FOREIGN KEY (`slot_id`) REFERENCES `slots` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "slot_aliases" to new temporary table "new_slot_aliases"
INSERT INTO `new_slot_aliases` (`slot_id`, `position`, `name`) SELECT `slot_id`, `position`, `name` FROM `slot_aliases`;
-- Drop "slot_aliases" table after copying rows
DROP TABLE `slot_aliases`;
-- Rename temporary table "new_slot_aliases" to "slot_aliases"
ALTER TABLE `new_slot_aliases` RENAME TO `slot_aliases`;
-- Create index "slot_aliases_name" to table: "slot_aliases"
CREATE UNIQUE INDEX `slot_aliases_name` ON `slot_aliases` (`name`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
