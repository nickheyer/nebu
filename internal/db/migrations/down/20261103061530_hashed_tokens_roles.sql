-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "old_api_tokens" table, the hash standing as the secret since the plaintext is gone
CREATE TABLE `old_api_tokens` (
  `id` text NULL,
  `provider` text NOT NULL,
  `subject` text NOT NULL,
  `owner` text NOT NULL,
  `email` text NOT NULL,
  `name` text NOT NULL,
  `secret` text NOT NULL,
  `created_at` text NOT NULL,
  `last_used_at` text NULL,
  PRIMARY KEY (`id`)
);
INSERT INTO `old_api_tokens` (`id`, `provider`, `subject`, `owner`, `email`, `name`, `secret`, `created_at`, `last_used_at`) SELECT `id`, `provider`, `subject`, `owner`, `email`, `name`, `hash`, `created_at`, `last_used_at` FROM `api_tokens`;
DROP TABLE `api_tokens`;
ALTER TABLE `old_api_tokens` RENAME TO `api_tokens`;
CREATE UNIQUE INDEX `api_tokens_secret` ON `api_tokens` (`secret`);
CREATE INDEX `api_tokens_owner` ON `api_tokens` (`provider`, `subject`);
ALTER TABLE `users` DROP COLUMN `role`;
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
