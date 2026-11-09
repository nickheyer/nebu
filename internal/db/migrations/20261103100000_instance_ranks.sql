-- Add column "ranks" to table: "instances"
ALTER TABLE `instances` ADD COLUMN `ranks` text NOT NULL DEFAULT '';
-- Add column "ranks" to table: "formation_seats"
ALTER TABLE `formation_seats` ADD COLUMN `ranks` text NOT NULL DEFAULT '';
