-- Add column "args" to table: "instance_requests"
ALTER TABLE `instance_requests` ADD COLUMN `args` text NOT NULL DEFAULT '';
-- Add column "args" to table: "slot_requests"
ALTER TABLE `slot_requests` ADD COLUMN `args` text NOT NULL DEFAULT '';
