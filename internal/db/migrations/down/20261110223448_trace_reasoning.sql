ALTER TABLE `traces` DROP COLUMN `reasoning`;
CREATE TABLE `chat_files_legacy` (
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
