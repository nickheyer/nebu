-- Add column "system_messages" to table: "slots"
ALTER TABLE `slots` ADD COLUMN `system_messages` text NOT NULL DEFAULT '';
-- Add column "system_messages" to table: "routes"
ALTER TABLE `routes` ADD COLUMN `system_messages` text NOT NULL DEFAULT '';
-- Create "instance_templates" table
CREATE TABLE `instance_templates` (
  `instance_id` text NULL,
  `late_system` integer NOT NULL DEFAULT 0,
  `refusal` text NOT NULL DEFAULT '',
  `error` text NOT NULL DEFAULT '',
  PRIMARY KEY (`instance_id`),
  CONSTRAINT `0` FOREIGN KEY (`instance_id`) REFERENCES `instances` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
