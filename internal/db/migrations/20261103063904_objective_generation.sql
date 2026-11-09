-- Add column "generation" to table: "objectives"
ALTER TABLE `objectives` ADD COLUMN `generation` integer NOT NULL DEFAULT 0;
