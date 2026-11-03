-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Add column "owners" to table: "objectives"
ALTER TABLE `objectives` ADD COLUMN `owners` text NOT NULL DEFAULT '';
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
