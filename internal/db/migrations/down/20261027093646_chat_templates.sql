-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "old_instance_templates" table, its probe rows gone with the forward migration
CREATE TABLE `old_instance_templates` (
  `instance_id` text NULL,
  `late_system` integer NOT NULL DEFAULT 0,
  `refusal` text NOT NULL DEFAULT '',
  `error` text NOT NULL DEFAULT '',
  PRIMARY KEY (`instance_id`),
  CONSTRAINT `0` FOREIGN KEY (`instance_id`) REFERENCES `instances` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
DROP TABLE `instance_templates`;
ALTER TABLE `old_instance_templates` RENAME TO `instance_templates`;
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
