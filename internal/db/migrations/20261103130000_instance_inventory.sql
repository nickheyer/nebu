-- Add column "inventory" to table: "instances"
ALTER TABLE `instances` ADD COLUMN `inventory` text NOT NULL DEFAULT '';
