-- Create "bot_activity" table
CREATE TABLE `bot_activity` (
  `seq` integer NULL PRIMARY KEY AUTOINCREMENT,
  `bot_id` text NOT NULL,
  `at` text NOT NULL,
  `level` text NOT NULL,
  `kind` text NOT NULL,
  `message` text NOT NULL,
  `guild_id` text NOT NULL DEFAULT '',
  `channel_id` text NOT NULL DEFAULT '',
  `persona` text NOT NULL DEFAULT '',
  `trace` text NOT NULL DEFAULT '',
  CONSTRAINT `0` FOREIGN KEY (`bot_id`) REFERENCES `bots` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "bot_activity_by_bot" to table: "bot_activity"
CREATE INDEX `bot_activity_by_bot` ON `bot_activity` (`bot_id`, `seq`);
-- Keep the stored image bytes aside until the daemon has moved them into the content store
ALTER TABLE `chat_files` RENAME TO `chat_files_legacy`;
-- Drop index "chat_files_owner", which followed the rename
DROP INDEX `chat_files_owner`;
-- Create "chat_files" table
CREATE TABLE `chat_files` (
  `id` text NULL,
  `provider` text NOT NULL,
  `subject` text NOT NULL,
  `media_type` text NOT NULL,
  `name` text NOT NULL DEFAULT '',
  `width` integer NOT NULL DEFAULT 0,
  `height` integer NOT NULL DEFAULT 0,
  `size_bytes` integer NOT NULL DEFAULT 0,
  `digest` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL,
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "chat_files_legacy" to new table "chat_files", the digest filled once the bytes are in the store
INSERT INTO `chat_files` (`id`, `provider`, `subject`, `media_type`, `name`, `width`, `height`, `size_bytes`, `digest`, `created_at`) SELECT `id`, `provider`, `subject`, `media_type`, `name`, `width`, `height`, length(`data`), '', `created_at` FROM `chat_files_legacy`;
-- Create index "chat_files_owner" to table: "chat_files"
CREATE INDEX `chat_files_owner` ON `chat_files` (`provider`, `subject`);
-- Create index "chat_files_digest" to table: "chat_files"
CREATE INDEX `chat_files_digest` ON `chat_files` (`digest`);
