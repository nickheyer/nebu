-- Drop "chat_files_legacy" table
DROP TABLE IF EXISTS `chat_files_legacy`;
-- Add column "reasoning" to table: "traces"
ALTER TABLE `traces` ADD COLUMN `reasoning` text NOT NULL DEFAULT '';
