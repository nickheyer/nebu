-- Add column "progress_unit" to table: "tasks"
ALTER TABLE `tasks` ADD COLUMN `progress_unit` text NOT NULL DEFAULT '';
