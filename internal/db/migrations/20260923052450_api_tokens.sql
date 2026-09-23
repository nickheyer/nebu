-- Create "api_tokens" table
CREATE TABLE `api_tokens` (
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
-- Create index "api_tokens_secret" to table: "api_tokens"
CREATE UNIQUE INDEX `api_tokens_secret` ON `api_tokens` (`secret`);
-- Create index "api_tokens_owner" to table: "api_tokens"
CREATE INDEX `api_tokens_owner` ON `api_tokens` (`provider`, `subject`);
