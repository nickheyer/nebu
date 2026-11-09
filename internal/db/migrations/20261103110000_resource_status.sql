-- Add column "status" to table: "installs"
ALTER TABLE `installs` ADD COLUMN `status` text NOT NULL DEFAULT '';
-- Add column "generation" to table: "installs"
ALTER TABLE `installs` ADD COLUMN `generation` integer NOT NULL DEFAULT 0;
-- Add column "status" to table: "objectives"
ALTER TABLE `objectives` ADD COLUMN `status` text NOT NULL DEFAULT '';
-- Add column "status" to table: "scheduler_configs"
ALTER TABLE `scheduler_configs` ADD COLUMN `status` text NOT NULL DEFAULT '';
