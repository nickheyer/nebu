-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
DROP TABLE `bot_activity`;
-- The legacy table holds the bytes until the daemon moves them, an empty one stands in once it has
CREATE TABLE IF NOT EXISTS `chat_files_legacy` (
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
-- Files added since the rename come back without their bytes, which live in the content store
INSERT INTO `chat_files_legacy` (`id`, `provider`, `subject`, `media_type`, `name`, `width`, `height`, `data`, `created_at`) SELECT `id`, `provider`, `subject`, `media_type`, `name`, `width`, `height`, X'', `created_at` FROM `chat_files` WHERE `id` NOT IN (SELECT `id` FROM `chat_files_legacy`);
DROP TABLE `chat_files`;
ALTER TABLE `chat_files_legacy` RENAME TO `chat_files`;
CREATE INDEX `chat_files_owner` ON `chat_files` (`provider`, `subject`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
