-- Add column "budgets" to table: "provider_credentials"
ALTER TABLE `provider_credentials` ADD COLUMN `budgets` text NOT NULL DEFAULT '';
-- Create "credential_usage" table
CREATE TABLE `credential_usage` (
  `credential_id` text NOT NULL,
  `model` text NOT NULL,
  `bucket` integer NOT NULL,
  `requests` integer NOT NULL DEFAULT 0,
  `input_tokens` integer NOT NULL DEFAULT 0,
  `output_tokens` integer NOT NULL DEFAULT 0,
  `cached_tokens` integer NOT NULL DEFAULT 0,
  PRIMARY KEY (`credential_id`, `model`, `bucket`),
  CONSTRAINT `0` FOREIGN KEY (`credential_id`) REFERENCES `provider_credentials` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
