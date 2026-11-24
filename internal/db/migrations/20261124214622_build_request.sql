-- Add column "request" to table: "builds"
ALTER TABLE `builds` ADD COLUMN `request` text NOT NULL DEFAULT '';
