-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Add column "role" to table: "users"
ALTER TABLE `users` ADD COLUMN `role` text NOT NULL DEFAULT '';
-- Create "new_api_tokens" table
CREATE TABLE `new_api_tokens` (
  `id` text NULL,
  `provider` text NOT NULL,
  `subject` text NOT NULL,
  `owner` text NOT NULL,
  `email` text NOT NULL,
  `name` text NOT NULL,
  `prefix` text NOT NULL,
  `hash` text NOT NULL,
  `scopes` text NOT NULL DEFAULT '',
  `role` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL,
  `last_used_at` text NULL,
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "api_tokens" to new temporary table "new_api_tokens"
-- Plaintext secrets land in hash for the upgrade to hash in place, prefix taking their first 12 characters
INSERT INTO `new_api_tokens` (`id`, `provider`, `subject`, `owner`, `email`, `name`, `prefix`, `hash`, `created_at`, `last_used_at`) SELECT `id`, `provider`, `subject`, `owner`, `email`, `name`, substr(`secret`, 1, 12), `secret`, `created_at`, `last_used_at` FROM `api_tokens`;
-- Drop "api_tokens" table after copying rows
DROP TABLE `api_tokens`;
-- Rename temporary table "new_api_tokens" to "api_tokens"
ALTER TABLE `new_api_tokens` RENAME TO `api_tokens`;
-- Create index "api_tokens_prefix" to table: "api_tokens"
CREATE UNIQUE INDEX `api_tokens_prefix` ON `api_tokens` (`prefix`);
-- Create index "api_tokens_owner" to table: "api_tokens"
CREATE INDEX `api_tokens_owner` ON `api_tokens` (`provider`, `subject`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
