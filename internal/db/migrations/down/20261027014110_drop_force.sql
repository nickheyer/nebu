ALTER TABLE `instance_requests` ADD COLUMN `force` integer NOT NULL DEFAULT 0;
ALTER TABLE `slot_requests` ADD COLUMN `force` integer NOT NULL DEFAULT 0;
