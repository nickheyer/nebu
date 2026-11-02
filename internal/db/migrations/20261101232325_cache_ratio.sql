-- Add column "cache_ratio" to table: "calibrations"
ALTER TABLE `calibrations` ADD COLUMN `cache_ratio` real NOT NULL DEFAULT 0;
-- Add column "cache_samples" to table: "calibrations"
ALTER TABLE `calibrations` ADD COLUMN `cache_samples` integer NOT NULL DEFAULT 0;
