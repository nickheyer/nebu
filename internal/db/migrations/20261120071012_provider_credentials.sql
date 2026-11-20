-- Create "provider_credentials" table
CREATE TABLE `provider_credentials` (
  `id` text NULL,
  `owner` text NOT NULL,
  `source_id` text NOT NULL,
  `name` text NOT NULL DEFAULT '',
  `kind` text NOT NULL,
  `secret` text NOT NULL,
  `expires_at` text NULL,
  `position` integer NOT NULL DEFAULT 0,
  `created_at` text NOT NULL,
  `updated_at` text NOT NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`source_id`) REFERENCES `sources` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "provider_credentials_owner" to table: "provider_credentials"
CREATE INDEX `provider_credentials_owner` ON `provider_credentials` (`owner`, `source_id`, `position`);
