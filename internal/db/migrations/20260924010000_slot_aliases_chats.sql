-- Create "slot_aliases" table
CREATE TABLE `slot_aliases` (
  `slot_id` text NOT NULL,
  `position` integer NOT NULL,
  `name` text NOT NULL,
  `max_in_flight` integer NOT NULL DEFAULT 0,
  `requests_per_second` real NOT NULL DEFAULT 0,
  `burst` integer NOT NULL DEFAULT 0,
  `request_timeout_ms` integer NOT NULL DEFAULT 0,
  `upstream_timeout_ms` integer NOT NULL DEFAULT 0,
  `system_messages` text NOT NULL DEFAULT '',
  PRIMARY KEY (`slot_id`, `position`),
  CONSTRAINT `0` FOREIGN KEY (`slot_id`) REFERENCES `slots` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "slot_aliases_name" to table: "slot_aliases"
CREATE UNIQUE INDEX `slot_aliases_name` ON `slot_aliases` (`name`);
-- Create "conversations" table
CREATE TABLE `conversations` (
  `id` text NULL,
  `provider` text NOT NULL,
  `subject` text NOT NULL,
  `title` text NOT NULL DEFAULT '',
  `model` text NOT NULL DEFAULT '',
  `turn_count` integer NOT NULL DEFAULT 0,
  `body` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL,
  `updated_at` text NOT NULL,
  PRIMARY KEY (`id`)
);
-- Create index "conversations_owner" to table: "conversations"
CREATE INDEX `conversations_owner` ON `conversations` (`provider`, `subject`, `updated_at`);
-- Create "chat_files" table
CREATE TABLE `chat_files` (
  `id` text NULL,
  `provider` text NOT NULL,
  `subject` text NOT NULL,
  `media_type` text NOT NULL,
  `name` text NOT NULL DEFAULT '',
  `width` integer NOT NULL DEFAULT 0,
  `height` integer NOT NULL DEFAULT 0,
  `data` blob NOT NULL,
  `created_at` text NOT NULL,
  PRIMARY KEY (`id`)
);
-- Create index "chat_files_owner" to table: "chat_files"
CREATE INDEX `chat_files_owner` ON `chat_files` (`provider`, `subject`);
