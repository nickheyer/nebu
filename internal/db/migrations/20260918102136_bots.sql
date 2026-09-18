-- Create "bots" table
CREATE TABLE `bots` (
  `id` text NULL,
  `name` text NOT NULL,
  `token` text NOT NULL,
  `enabled` integer NOT NULL DEFAULT 0,
  `spec` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL,
  `updated_at` text NOT NULL,
  PRIMARY KEY (`id`)
);
-- Create index "bots_name" to table: "bots"
CREATE UNIQUE INDEX `bots_name` ON `bots` (`name`);
-- Create "bot_channels" table
CREATE TABLE `bot_channels` (
  `bot_id` text NOT NULL,
  `channel_id` text NOT NULL,
  `persona_id` text NOT NULL DEFAULT '',
  `cutoff_message_id` text NOT NULL DEFAULT '',
  `webhook_id` text NOT NULL DEFAULT '',
  `webhook_token` text NOT NULL DEFAULT '',
  PRIMARY KEY (`bot_id`, `channel_id`),
  CONSTRAINT `0` FOREIGN KEY (`bot_id`) REFERENCES `bots` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "bot_schedules" table
CREATE TABLE `bot_schedules` (
  `bot_id` text NOT NULL,
  `automation_id` text NOT NULL,
  `last_run_at` text NOT NULL,
  PRIMARY KEY (`bot_id`, `automation_id`),
  CONSTRAINT `0` FOREIGN KEY (`bot_id`) REFERENCES `bots` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
