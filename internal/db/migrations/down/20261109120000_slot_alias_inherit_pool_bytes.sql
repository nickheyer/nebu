ALTER TABLE `slot_aliases` DROP COLUMN `inherit_slot_id`;
ALTER TABLE `slot_aliases` ADD COLUMN `max_in_flight` integer NOT NULL DEFAULT 0;
ALTER TABLE `slot_aliases` ADD COLUMN `requests_per_second` real NOT NULL DEFAULT 0;
ALTER TABLE `slot_aliases` ADD COLUMN `burst` integer NOT NULL DEFAULT 0;
ALTER TABLE `slot_aliases` ADD COLUMN `request_timeout_ms` integer NOT NULL DEFAULT 0;
ALTER TABLE `slot_aliases` ADD COLUMN `upstream_timeout_ms` integer NOT NULL DEFAULT 0;
ALTER TABLE `slot_aliases` ADD COLUMN `profile` text NOT NULL DEFAULT '';
ALTER TABLE `slots` DROP COLUMN `pool_bytes`;
