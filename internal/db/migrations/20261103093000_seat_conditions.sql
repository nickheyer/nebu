-- Add column "conditions" to table: "formation_seats"
ALTER TABLE `formation_seats` ADD COLUMN `conditions` text NOT NULL DEFAULT '';
