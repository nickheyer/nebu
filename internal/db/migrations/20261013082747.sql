-- Add column "allow_all" to table: "slots"
ALTER TABLE `slots` ADD COLUMN `allow_all` integer NOT NULL DEFAULT 0;
-- Add column "allow_all" to table: "routes"
ALTER TABLE `routes` ADD COLUMN `allow_all` integer NOT NULL DEFAULT 0;
-- Add column "allow_all" to table: "slot_aliases"
ALTER TABLE `slot_aliases` ADD COLUMN `allow_all` integer NOT NULL DEFAULT 0;
