-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_instance_templates" table
CREATE TABLE `new_instance_templates` (
  `instance_id` text NULL,
  `source` text NOT NULL DEFAULT '',
  `tools` text NOT NULL DEFAULT '',
  `error` text NOT NULL DEFAULT '',
  PRIMARY KEY (`instance_id`),
  CONSTRAINT `0` FOREIGN KEY (`instance_id`) REFERENCES `instances` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Drop "instance_templates" table, its probe rows left behind so recovery reads each template from the model's files
DROP TABLE `instance_templates`;
-- Rename temporary table "new_instance_templates" to "instance_templates"
ALTER TABLE `new_instance_templates` RENAME TO `instance_templates`;
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
-- Drop the system message mode from every profile, one left with nothing set inheriting
UPDATE `slots` SET `profile` = CASE WHEN json_remove(`profile`, '$.systemMessages') = '{}' THEN '' ELSE json_remove(`profile`, '$.systemMessages') END WHERE `profile` != '';
UPDATE `slot_aliases` SET `profile` = CASE WHEN json_remove(`profile`, '$.systemMessages') = '{}' THEN '' ELSE json_remove(`profile`, '$.systemMessages') END WHERE `profile` != '';
UPDATE `routes` SET `profile` = CASE WHEN json_remove(`profile`, '$.systemMessages') = '{}' THEN '' ELSE json_remove(`profile`, '$.systemMessages') END WHERE `profile` != '';
