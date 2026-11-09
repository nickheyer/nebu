-- Add column "snapshot" to table: "instance_requests"
ALTER TABLE `instance_requests` ADD COLUMN `snapshot` integer NOT NULL DEFAULT 0;
-- Add column "snapshot" to table: "slot_requests"
ALTER TABLE `slot_requests` ADD COLUMN `snapshot` integer NOT NULL DEFAULT 0;
